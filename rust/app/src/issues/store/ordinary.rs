use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgPool, Row};

use super::{CallerActor, IssueReadError, resolve_caller_access_on};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct OrdinaryIssueEdit {
    expected_revision: Option<i64>,
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
}

#[derive(Debug)]
pub enum OrdinaryIssueEditError {
    Access(IssueReadError),
    RevisionConflict(i64),
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
    let locked = sqlx::query(&format!(
        "SELECT i.project_id::text, i.revision, {SNAPSHOT}
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
    let revision: i64 = locked.try_get(1)?;
    if edit
        .expected_revision
        .is_some_and(|expected| expected != revision)
    {
        return Err(OrdinaryIssueEditError::RevisionConflict(revision));
    }

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
    .bind(edit.source_task_id.as_deref().unwrap_or(""))
    .execute(&mut *tx)
    .await?;

    // Keep PostgreSQL's activity and duplicate rules. A position-only change
    // orders the issue without marking it recently active. Assigning status to
    // itself preserves the legacy work-method closure constraint trigger.
    let after: String = sqlx::query_scalar(&format!(
        "WITH next AS (
            SELECT COALESCE($3::text, i.priority) AS priority,
                   COALESCE($4::double precision, i.position) AS position,
                   CASE WHEN $5 THEN date '2000-01-01' + $6::int ELSE i.start_date END AS start_date,
                   CASE WHEN $7 THEN date '2000-01-01' + $8::int ELSE i.due_date END AS due_date,
                   CASE WHEN i.status = 'cancelled' THEN i.duplicate_of_issue_id END AS duplicate
            FROM issue i WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        ), changes AS (
            SELECT next.*,
                   ROW(next.priority, next.start_date, next.due_date, next.duplicate)
                       IS DISTINCT FROM ROW(i.priority, i.start_date, i.due_date, i.duplicate_of_issue_id)
                       AS activity,
                   next.position IS DISTINCT FROM i.position AS moved
            FROM issue i CROSS JOIN next
            WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        )
        UPDATE issue i
           SET priority = changes.priority, position = changes.position,
               start_date = changes.start_date, due_date = changes.due_date,
               duplicate_of_issue_id = changes.duplicate,
               status = i.status,
               revision = i.revision + (changes.activity OR changes.moved)::int,
               updated_at = CASE WHEN changes.activity OR changes.moved THEN now() ELSE i.updated_at END,
               last_activity_at = CASE WHEN changes.activity
                   THEN GREATEST(COALESCE(i.last_activity_at, i.updated_at), now())
                   ELSE i.last_activity_at END
          FROM changes
         WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        RETURNING {SNAPSHOT}"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .bind(edit.priority)
    .bind(edit.position)
    .bind(edit.start_date.is_some())
    .bind(edit.start_date.and_then(|change| change.day))
    .bind(edit.due_date.is_some())
    .bind(edit.due_date.and_then(|change| change.day))
    .fetch_one(&mut *tx)
    .await?;
    let write = OrdinaryIssueWrite {
        before: serde_json::from_str(locked.try_get(2)?)?,
        after: serde_json::from_str(&after)?,
    };
    tx.commit().await?;
    Ok(write)
}
