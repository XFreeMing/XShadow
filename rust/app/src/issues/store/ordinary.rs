use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgConnection, PgPool, Row};

use super::{CallerActor, IssueReadError, resolve_caller_access_on};

mod channel_media;
mod recommendation;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct OrdinaryIssueEdit {
    expected_revision: Option<i64>,
    title: Option<String>,
    title_base: Option<String>,
    description: Option<String>,
    description_base: Option<String>,
    #[serde(default)]
    attachment_ids: Vec<String>,
    method_catalog: Option<recommendation::CatalogSeed>,
    priority: Option<String>,
    position: Option<f64>,
    start_date: Option<DateChange>,
    due_date: Option<DateChange>,
    source_task_id: Option<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct DateChange {
    /// A PostgreSQL calendar day measured from 2000-01-01. Null clears it.
    day: Option<i32>,
}

#[derive(Serialize)]
pub struct OrdinaryIssueWrite {
    before: Box<RawValue>,
    after: Box<RawValue>,
    attachments_changed: bool,
}

#[derive(Debug)]
pub enum OrdinaryIssueEditError {
    Access(IssueReadError),
    RevisionConflict(i64),
    FieldConflict,
    Database(sqlx::Error),
    Snapshot(serde_json::Error),
}

impl From<sqlx::Error> for OrdinaryIssueEditError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for OrdinaryIssueEditError {
    fn from(error: serde_json::Error) -> Self {
        Self::Snapshot(error)
    }
}

// PostgreSQL JSON spells BC dates differently from the public API. Use the
// same integer representation as the PostgreSQL date codec in the gateway.
const SNAPSHOT: &str = "(
    to_jsonb(i) || jsonb_build_object(
        'start_date', CASE i.start_date
            WHEN 'infinity'::date THEN 2147483647
            WHEN '-infinity'::date THEN -2147483648
            ELSE i.start_date - date '2000-01-01' END,
        'due_date', CASE i.due_date
            WHEN 'infinity'::date THEN 2147483647
            WHEN '-infinity'::date THEN -2147483648
            ELSE i.due_date - date '2000-01-01' END
    )
)::text";

pub async fn update_ordinary_issue(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    edit: OrdinaryIssueEdit,
) -> Result<OrdinaryIssueWrite, OrdinaryIssueEditError> {
    let mut tx = pool.begin().await?;
    if !edit.attachment_ids.is_empty() {
        // Ownerless uploads are locked before the issue, in the same order
        // as the other issue and comment binders.
        sqlx::query(
            "SELECT id FROM attachment
             WHERE workspace_id = $1::uuid AND issue_id IS NULL AND source_context_id IS NULL
               AND id = ANY($2::uuid[]) ORDER BY id FOR UPDATE",
        )
        .bind(workspace_id)
        .bind(&edit.attachment_ids)
        .execute(&mut *tx)
        .await?;
    }
    let locked = sqlx::query(&format!(
        "SELECT i.project_id::text, i.revision, i.title, i.description, i.status,
                i.work_method_run::text AS work_method, {SNAPSHOT} AS snapshot
         FROM issue i WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid FOR UPDATE"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(OrdinaryIssueEditError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(OrdinaryIssueEditError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(OrdinaryIssueEditError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    let old_title: &str = locked.try_get("title")?;
    if let (Some(title), Some(base)) = (&edit.title, &edit.title_base)
        && old_title != base
        && old_title != title
    {
        return Err(OrdinaryIssueEditError::FieldConflict);
    }
    let revision: i64 = locked.try_get("revision")?;
    if edit
        .expected_revision
        .is_some_and(|expected| expected != revision)
    {
        return Err(OrdinaryIssueEditError::RevisionConflict(revision));
    }
    let old_description = locked
        .try_get::<Option<&str>, _>("description")?
        .unwrap_or_default();
    let description = match edit.description {
        Some(content) => Some(
            merge_description(
                &mut tx,
                workspace_id,
                issue_id,
                old_description,
                content,
                edit.description_base.as_deref(),
            )
            .await?,
        ),
        None => None,
    };
    let next_title = edit.title.as_deref().unwrap_or(old_title);
    let next_description = description.as_deref().unwrap_or(old_description);
    let mut ready_method = None;
    if next_title != old_title || next_description != old_description {
        let method = locked.try_get::<Option<&str>, _>("work_method")?;
        if method
            .map(recommendation::is_ready)
            .transpose()?
            .unwrap_or(true)
        {
            ready_method = Some(
                recommend_method(
                    &mut tx,
                    workspace_id,
                    issue_id,
                    locked.try_get("status")?,
                    edit.method_catalog,
                    next_title,
                    next_description,
                )
                .await?,
            );
        }
    }

    set_write_actor(&mut tx, actor, edit.source_task_id.as_deref()).await?;

    // Keep PostgreSQL's activity and duplicate rules. A position-only change
    // orders the issue without marking it recently active. Assigning status to
    // itself preserves the legacy work-method closure constraint trigger.
    let saved = sqlx::query(&format!(
        "WITH next AS (
            SELECT COALESCE($3::text, i.priority) AS priority,
                   COALESCE($4::double precision, i.position) AS position,
                   CASE WHEN $5 THEN date '2000-01-01' + $6::int ELSE i.start_date END AS start_date,
                   CASE WHEN $7 THEN date '2000-01-01' + $8::int ELSE i.due_date END AS due_date,
                   CASE WHEN i.status = 'cancelled' THEN i.duplicate_of_issue_id END AS duplicate,
                   COALESCE($9::text, i.title) AS title,
                   COALESCE($10::text, i.description) AS description
            FROM issue i WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        ), changes AS (
            SELECT next.*,
                   ROW(next.priority, next.start_date, next.due_date, next.duplicate, next.title, next.description)
                       IS DISTINCT FROM ROW(i.priority, i.start_date, i.due_date, i.duplicate_of_issue_id, i.title, i.description)
                       AS activity,
                   next.position IS DISTINCT FROM i.position AS moved
            FROM issue i CROSS JOIN next
            WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        )
        UPDATE issue i
           SET priority = changes.priority, position = changes.position,
               start_date = changes.start_date, due_date = changes.due_date,
               duplicate_of_issue_id = changes.duplicate,
               title = changes.title, description = changes.description,
               work_method_run = CASE WHEN $11 THEN $12::jsonb ELSE i.work_method_run END,
               status = i.status,
               revision = i.revision + (changes.activity OR changes.moved)::int,
               updated_at = CASE WHEN changes.activity OR changes.moved THEN now() ELSE i.updated_at END,
               last_activity_at = CASE WHEN changes.activity
                   THEN GREATEST(COALESCE(i.last_activity_at, i.updated_at), now())
                   ELSE i.last_activity_at END
          FROM changes
         WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        RETURNING {SNAPSHOT}, i.revision"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .bind(edit.priority)
    .bind(edit.position)
    .bind(edit.start_date.is_some())
    .bind(edit.start_date.and_then(|change| change.day))
    .bind(edit.due_date.is_some())
    .bind(edit.due_date.and_then(|change| change.day))
    .bind(edit.title)
    .bind(description)
    .bind(ready_method.is_some())
    .bind(ready_method.flatten())
    .fetch_one(&mut *tx)
    .await?;
    let saved_revision: i64 = saved.try_get(1)?;
    let attachments_changed = if edit.attachment_ids.is_empty() {
        false
    } else {
        bind_attachments(
            &mut tx,
            workspace_id,
            issue_id,
            &edit.attachment_ids,
            saved_revision == revision,
        )
        .await?
    };
    let after = if attachments_changed && saved_revision == revision {
        sqlx::query_scalar(&format!(
            "SELECT {SNAPSHOT} FROM issue i WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid"
        ))
        .bind(issue_id)
        .bind(workspace_id)
        .fetch_one(&mut *tx)
        .await?
    } else {
        saved.try_get::<String, _>(0)?
    };
    let write = OrdinaryIssueWrite {
        before: serde_json::from_str(locked.try_get("snapshot")?)?,
        after: serde_json::from_str(&after)?,
        attachments_changed,
    };
    tx.commit().await?;
    Ok(write)
}

pub(super) async fn set_write_actor(
    conn: &mut PgConnection,
    actor: &CallerActor,
    source_task_id: Option<&str>,
) -> Result<(), sqlx::Error> {
    let (actor_type, actor_id) = match actor {
        CallerActor::Member { user_id } => ("member", user_id),
        CallerActor::Agent { agent_id, .. } => ("agent", agent_id),
    };
    sqlx::query(
        "SELECT set_config('multica.actor_type',$1,true),
                set_config('multica.actor_id',$2,true),
                set_config('multica.source_task_id',$3,true)",
    )
    .bind(actor_type)
    .bind(actor_id)
    .bind(source_task_id.unwrap_or(""))
    .execute(conn)
    .await?;
    Ok(())
}

async fn merge_description(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    current: &str,
    incoming: String,
    base: Option<&str>,
) -> Result<String, sqlx::Error> {
    let attachments = sqlx::query_as::<_, (String, String, String)>(
        "SELECT id::text, filename, content_type FROM attachment
         WHERE issue_id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_all(conn)
    .await?;
    let attachments = attachments
        .iter()
        .map(|(id, name, kind)| (id.as_str(), (name.as_str(), kind.as_str())))
        .collect::<HashMap<_, _>>();
    Ok(channel_media::merge(current, incoming, base, |id| {
        attachments.get(id).copied()
    }))
}

async fn recommend_method(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    status: &str,
    catalog: Option<recommendation::CatalogSeed>,
    title: &str,
    description: &str,
) -> Result<Option<String>, OrdinaryIssueEditError> {
    let active = match status {
        "cancelled" | "done" => false,
        "backlog" | "todo" | "in_progress" | "in_review" | "blocked" => true,
        _ => {
            let category: String = sqlx::query_scalar(
                "SELECT category FROM issue_status WHERE workspace_id = $1::uuid AND key = $2",
            )
            .bind(workspace_id)
            .bind(status)
            .fetch_one(&mut *conn)
            .await?;
            match category.as_str() {
                "done" | "closed" => false,
                "unstarted" | "started" => true,
                _ => {
                    return Err(
                        sqlx::Error::Protocol("invalid issue status category".into()).into(),
                    );
                }
            }
        }
    };
    if !active {
        return Ok(None);
    }
    let catalog =
        catalog.ok_or_else(|| sqlx::Error::Protocol("missing work method catalog seed".into()))?;
    let goal = sqlx::query_scalar::<_, String>(
        "SELECT g.title || E'\\n' || g.intent || E'\\n' || g.acceptance_criteria
         FROM goal_issue gi
         JOIN goal g ON g.id = gi.goal_id AND g.workspace_id = gi.workspace_id
         JOIN issue i ON i.id = gi.issue_id AND i.workspace_id = gi.workspace_id
         WHERE gi.workspace_id = $1::uuid AND gi.issue_id = $2::uuid
           AND (g.project_id IS NULL OR g.project_id = i.project_id)",
    )
    .bind(workspace_id)
    .bind(issue_id)
    .fetch_optional(&mut *conn)
    .await?;
    let stored: String =
        sqlx::query_scalar("SELECT work_method_templates::text FROM workspace WHERE id = $1::uuid")
            .bind(workspace_id)
            .fetch_one(conn)
            .await?;
    Ok(catalog.recommend(&stored, title, description, goal.as_deref().unwrap_or(""))?)
}

async fn bind_attachments(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    ids: &[String],
    bump_revision: bool,
) -> Result<bool, sqlx::Error> {
    sqlx::query_scalar(
        "WITH linked AS (
           UPDATE attachment
           SET issue_id = $2::uuid
           WHERE workspace_id = $1::uuid AND issue_id IS NULL AND source_context_id IS NULL
             AND id = ANY($3::uuid[])
           RETURNING id
         ), bumped AS (
           UPDATE issue SET revision = revision + 1, updated_at = now()
           WHERE workspace_id = $1::uuid AND id = $2::uuid AND $4
             AND EXISTS (SELECT 1 FROM linked)
         )
         SELECT EXISTS (SELECT 1 FROM linked)",
    )
    .bind(workspace_id)
    .bind(issue_id)
    .bind(ids)
    .bind(bump_revision)
    .fetch_one(conn)
    .await
}
