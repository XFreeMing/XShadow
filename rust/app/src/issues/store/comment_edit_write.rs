//! Member strict text edits for plain top-level task comments on projectless
//! issues. Mirrors `UpdateComment` lock order and CAS semantics without task
//! dispatch side effects.

use serde::Deserialize;
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError,
    comment_resolution_write::{COMMENT_VERSION_COLUMNS, map_comment_version},
    comment_write::IssueCommentRow,
    is_uuid,
    ordinary::set_write_session,
    resolve_caller_access_on,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueCommentEdit {
    pub content: String,
    pub content_base: Option<String>,
    pub expected_revision: i64,
}

#[derive(Debug, serde::Serialize)]
pub struct IssueCommentEditResult {
    pub changed: bool,
    pub comment: IssueCommentRow,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub issue_revision: Option<i64>,
}

#[derive(Debug)]
pub enum IssueCommentEditError {
    Access(IssueReadError),
    InvalidInput,
    RouteChanged,
    RevisionConflict { actual_revision: i64 },
    ContentConflict,
    Database(sqlx::Error),
}

impl From<sqlx::Error> for IssueCommentEditError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

#[derive(Debug)]
struct LockedCommentShape {
    issue_id: String,
    is_reply: bool,
    is_deleted: bool,
    author_type: String,
    author_id: String,
    comment_type: String,
    has_source: bool,
    has_suppress: bool,
    has_quick_action: bool,
    has_plugin: bool,
    project_id: Option<String>,
    assignee_type: Option<String>,
    has_supplement: bool,
    content: String,
    revision: i64,
}

fn map_locked_comment_shape(
    row: &sqlx::postgres::PgRow,
) -> Result<LockedCommentShape, sqlx::Error> {
    Ok(LockedCommentShape {
        issue_id: row.try_get("issue_id")?,
        is_reply: row.try_get("is_reply")?,
        is_deleted: row.try_get("is_deleted")?,
        author_type: row.try_get("author_type")?,
        author_id: row.try_get("author_id")?,
        comment_type: row.try_get("comment_type")?,
        has_source: row.try_get("has_source")?,
        has_suppress: row.try_get("has_suppress")?,
        has_quick_action: row.try_get("has_quick_action")?,
        has_plugin: row.try_get("has_plugin")?,
        project_id: row.try_get("project_id")?,
        assignee_type: row.try_get("assignee_type")?,
        has_supplement: row.try_get("has_supplement")?,
        content: row.try_get("content")?,
        revision: row.try_get("revision")?,
    })
}

fn comment_route_allowed(assignee_type: Option<&str>) -> bool {
    matches!(assignee_type, None | Some("") | Some("member"))
}

async fn lock_workspace_member(
    tx: &mut PgConnection,
    workspace_id: &str,
    user_id: &str,
) -> Result<(), IssueCommentEditError> {
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;
    let locked = sqlx::query(
        "SELECT 1 FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
    )
    .bind(user_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?;
    if locked.is_none() {
        return Err(IssueCommentEditError::Access(IssueReadError::NotFound(
            "workspace not found",
        )));
    }
    Ok(())
}

async fn lock_issue_for_comment(
    tx: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
) -> Result<String, IssueCommentEditError> {
    let issue_id: Option<String> = sqlx::query_scalar(
        "SELECT i.id::text
         FROM issue i
         WHERE i.id = (
             SELECT c.issue_id FROM comment c
             WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid
         )
           AND i.workspace_id = $2::uuid
         FOR UPDATE",
    )
    .bind(comment_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?;
    issue_id.ok_or(IssueCommentEditError::Access(IssueReadError::NotFound(
        "comment not found",
    )))
}

async fn lock_comment_shape(
    tx: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
) -> Result<LockedCommentShape, IssueCommentEditError> {
    let row = sqlx::query(
        "SELECT c.issue_id::text AS issue_id,
                c.parent_id IS NOT NULL AS is_reply,
                c.deleted_at IS NOT NULL AS is_deleted,
                c.author_type,
                c.author_id::text AS author_id,
                c.type AS comment_type,
                c.source_task_id IS NOT NULL AS has_source,
                COALESCE(cardinality(c.suppressed_agent_ids), 0) > 0 AS has_suppress,
                c.quick_action_id IS NOT NULL AS has_quick_action,
                c.via_plugin_id IS NOT NULL AS has_plugin,
                i.project_id::text AS project_id,
                i.assignee_type::text AS assignee_type,
                EXISTS(
                    SELECT 1 FROM task_supplement s
                    WHERE s.comment_id = c.id AND s.workspace_id = c.workspace_id
                ) AS has_supplement,
                c.content,
                c.revision
         FROM comment c
         JOIN issue i ON i.id = c.issue_id AND i.workspace_id = c.workspace_id
         WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid
         FOR UPDATE OF c",
    )
    .bind(comment_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?;
    let Some(row) = row else {
        return Err(IssueCommentEditError::Access(IssueReadError::NotFound(
            "comment not found",
        )));
    };
    map_locked_comment_shape(&row).map_err(IssueCommentEditError::from)
}

async fn comment_has_active_trigger_task(
    tx: &mut PgConnection,
    comment_id: &str,
    issue_id: &str,
) -> Result<bool, sqlx::Error> {
    sqlx::query_scalar(
        "SELECT EXISTS (
            SELECT 1 FROM agent_task_queue q
            WHERE (q.trigger_comment_id = $1::uuid OR $1::uuid = ANY(q.coalesced_comment_ids))
              AND q.issue_id = $2::uuid
              AND q.status IN (
                  'queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred'
              )
        )",
    )
    .bind(comment_id)
    .bind(issue_id)
    .fetch_one(tx)
    .await
}

fn validate_route(
    shape: &LockedCommentShape,
    actor: &CallerActor,
    hidden_can_see: bool,
) -> Result<(), IssueCommentEditError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(IssueCommentEditError::InvalidInput);
    };
    if shape.is_deleted {
        return Err(IssueCommentEditError::Access(IssueReadError::NotFound(
            "comment not found",
        )));
    }
    if !hidden_can_see {
        return Err(IssueCommentEditError::Access(IssueReadError::NotFound(
            "comment not found",
        )));
    }
    if shape.is_reply
        || shape.author_type != "member"
        || shape.author_id != *user_id
        || shape.comment_type != "comment"
        || shape.has_source
        || shape.has_suppress
        || shape.has_quick_action
        || shape.has_plugin
        || shape.project_id.is_some()
        || shape.has_supplement
        || !comment_route_allowed(shape.assignee_type.as_deref())
    {
        return Err(IssueCommentEditError::RouteChanged);
    }
    Ok(())
}

async fn apply_comment_edit(
    tx: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
    new_content: &str,
    did_change: bool,
) -> Result<IssueCommentRow, sqlx::Error> {
    let row = if did_change {
        sqlx::query(&format!(
            "UPDATE comment SET
                content = $3,
                source_task_id = NULL,
                suppressed_agent_ids = ARRAY[]::uuid[],
                revision = revision + 1,
                updated_at = now()
             WHERE id = $1::uuid AND workspace_id = $2::uuid AND deleted_at IS NULL
             RETURNING {COMMENT_VERSION_COLUMNS}"
        ))
        .bind(comment_id)
        .bind(workspace_id)
        .bind(new_content)
        .fetch_one(&mut *tx)
        .await?
    } else {
        sqlx::query(&format!(
            "SELECT {COMMENT_VERSION_COLUMNS} FROM comment
             WHERE id = $1::uuid AND workspace_id = $2::uuid AND deleted_at IS NULL"
        ))
        .bind(comment_id)
        .bind(workspace_id)
        .fetch_one(&mut *tx)
        .await?
    };
    map_comment_version(&row)
}

async fn touch_issue_activity(
    tx: &mut PgConnection,
    issue_id: &str,
    workspace_id: &str,
) -> Result<i64, sqlx::Error> {
    sqlx::query_scalar(
        "UPDATE issue SET
            revision = issue.revision + 1,
            last_activity_at = GREATEST(COALESCE(issue.last_activity_at, issue.updated_at), now())
         WHERE id = $1::uuid AND workspace_id = $2::uuid
         RETURNING revision",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_one(tx)
    .await
}

pub async fn update_issue_comment(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    comment_id: &str,
    edit: IssueCommentEdit,
) -> Result<IssueCommentEditResult, IssueCommentEditError> {
    let CallerActor::Member { user_id: _ } = actor else {
        return Err(IssueCommentEditError::InvalidInput);
    };
    if !is_uuid(comment_id)
        || edit.expected_revision < 1
        || edit.content.is_empty()
        || edit.content.contains('\0')
        || edit
            .content_base
            .as_ref()
            .is_some_and(|base| base.contains('\0'))
    {
        return Err(IssueCommentEditError::InvalidInput);
    }

    let mut tx = pool.begin().await?;
    lock_workspace_member(
        &mut tx,
        workspace_id,
        match actor {
            CallerActor::Member { user_id } => user_id,
            _ => unreachable!(),
        },
    )
    .await?;

    let issue_id = lock_issue_for_comment(&mut tx, comment_id, workspace_id).await?;
    let shape = lock_comment_shape(&mut tx, comment_id, workspace_id).await?;
    if shape.issue_id != issue_id {
        return Err(IssueCommentEditError::Access(IssueReadError::NotFound(
            "comment not found",
        )));
    }

    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueCommentEditError::Access)?;
    validate_route(&shape, actor, hidden.can_see(shape.project_id.as_deref()))?;

    if comment_has_active_trigger_task(&mut tx, comment_id, &issue_id).await? {
        return Err(IssueCommentEditError::RouteChanged);
    }

    if shape.revision != edit.expected_revision {
        return Err(IssueCommentEditError::RevisionConflict {
            actual_revision: shape.revision,
        });
    }
    if let Some(base) = edit.content_base.as_deref()
        && shape.content != base
        && shape.content != edit.content
    {
        return Err(IssueCommentEditError::ContentConflict);
    }

    let did_change = shape.content != edit.content;
    if did_change {
        set_write_session(&mut tx, actor, None, None).await?;
    }

    let comment =
        apply_comment_edit(&mut tx, comment_id, workspace_id, &edit.content, did_change).await?;
    let issue_revision = if did_change {
        let revision = touch_issue_activity(&mut tx, &issue_id, workspace_id).await?;
        Some(revision)
    } else {
        None
    };

    tx.commit().await?;

    Ok(IssueCommentEditResult {
        changed: did_change,
        comment: IssueCommentRow {
            issue_revision,
            ..comment
        },
        issue_revision,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn comment_edit_route_allows_unassigned_and_member_only() {
        assert!(comment_route_allowed(None));
        assert!(comment_route_allowed(Some("")));
        assert!(comment_route_allowed(Some("member")));
        assert!(!comment_route_allowed(Some("agent")));
    }
}
