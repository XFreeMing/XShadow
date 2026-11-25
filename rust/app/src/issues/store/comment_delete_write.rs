//! Member delete for plain top-level projectless comments. Reactions are removed
//! before tombstone or hard delete so PostgreSQL triggers keep actor ordering.

use serde::Deserialize;
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError,
    comment_resolution_write::{COMMENT_VERSION_COLUMNS, map_comment_version},
    comment_write::{
        IssueCommentRow, comment_has_active_trigger_task, lock_comment_write_member,
        lock_issue_for_comment, touch_issue_activity,
    },
    is_uuid,
    ordinary::set_write_session,
    resolve_caller_access_on,
};

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueCommentDeleteRequest {
    #[serde(default)]
    go_mode: CommentDeleteGoMode,
}

#[derive(Debug, serde::Serialize)]
pub struct IssueCommentDeleteResult {
    pub comment_id: String,
    pub issue_id: String,
    pub issue_revision: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tombstone: Option<IssueCommentRow>,
}

#[derive(Debug)]
pub enum IssueCommentDeleteError {
    Access(IssueReadError),
    InvalidInput,
    RouteChanged,
    Database(sqlx::Error),
}

impl From<sqlx::Error> for IssueCommentDeleteError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

#[derive(Debug, Clone, Copy, Default, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
enum CommentDeleteGoMode {
    #[default]
    Leaf,
    Tombstone,
}

#[derive(Debug)]
struct LockedDeleteShape {
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
    has_replies: bool,
    has_attachments: bool,
}

fn comment_route_allowed(assignee_type: Option<&str>) -> bool {
    matches!(assignee_type, None | Some("") | Some("member"))
}

fn map_delete_shape(row: &sqlx::postgres::PgRow) -> Result<LockedDeleteShape, sqlx::Error> {
    Ok(LockedDeleteShape {
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
        has_replies: row.try_get("has_replies")?,
        has_attachments: row.try_get("has_attachments")?,
    })
}

async fn lock_comment_delete_shape(
    tx: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
) -> Result<LockedDeleteShape, IssueCommentDeleteError> {
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
                EXISTS(
                    SELECT 1 FROM comment child
                    WHERE child.parent_id = c.id AND child.workspace_id = c.workspace_id
                ) AS has_replies,
                EXISTS(
                    SELECT 1 FROM attachment a
                    WHERE a.comment_id = c.id AND a.workspace_id = c.workspace_id
                ) AS has_attachments
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
        return Err(IssueCommentDeleteError::Access(IssueReadError::NotFound(
            "comment not found",
        )));
    };
    map_delete_shape(&row).map_err(IssueCommentDeleteError::from)
}

fn validate_delete_shape(
    shape: &LockedDeleteShape,
    actor: &CallerActor,
    hidden_can_see: bool,
    mode: CommentDeleteGoMode,
) -> Result<(), IssueCommentDeleteError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(IssueCommentDeleteError::InvalidInput);
    };
    if shape.is_deleted {
        return Err(IssueCommentDeleteError::Access(IssueReadError::NotFound(
            "comment not found",
        )));
    }
    if !hidden_can_see {
        return Err(IssueCommentDeleteError::Access(IssueReadError::NotFound(
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
        || shape.has_attachments
        || !comment_route_allowed(shape.assignee_type.as_deref())
    {
        return Err(IssueCommentDeleteError::RouteChanged);
    }
    match mode {
        CommentDeleteGoMode::Leaf if shape.has_replies => {
            return Err(IssueCommentDeleteError::RouteChanged);
        }
        CommentDeleteGoMode::Tombstone if !shape.has_replies => {
            return Err(IssueCommentDeleteError::RouteChanged);
        }
        _ => {}
    }
    Ok(())
}

pub async fn delete_issue_comment(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    comment_id: &str,
    request: IssueCommentDeleteRequest,
) -> Result<IssueCommentDeleteResult, IssueCommentDeleteError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(IssueCommentDeleteError::InvalidInput);
    };
    if !is_uuid(comment_id) {
        return Err(IssueCommentDeleteError::InvalidInput);
    }
    let mode = request.go_mode;

    let mut tx = pool.begin().await?;
    if !lock_comment_write_member(&mut tx, workspace_id, user_id).await? {
        return Err(IssueCommentDeleteError::Access(IssueReadError::NotFound(
            "workspace not found",
        )));
    }

    let issue_id = lock_issue_for_comment(&mut tx, comment_id, workspace_id)
        .await?
        .ok_or(IssueCommentDeleteError::Access(IssueReadError::NotFound(
            "comment not found",
        )))?;
    let shape = lock_comment_delete_shape(&mut tx, comment_id, workspace_id).await?;
    if shape.issue_id != issue_id {
        return Err(IssueCommentDeleteError::Access(IssueReadError::NotFound(
            "comment not found",
        )));
    }

    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueCommentDeleteError::Access)?;
    validate_delete_shape(
        &shape,
        actor,
        hidden.can_see(shape.project_id.as_deref()),
        mode,
    )?;

    if comment_has_active_trigger_task(&mut tx, comment_id, &issue_id).await? {
        return Err(IssueCommentDeleteError::RouteChanged);
    }

    set_write_session(&mut tx, actor, None, None).await?;

    sqlx::query(
        "DELETE FROM comment_reaction
         WHERE comment_id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(comment_id)
    .bind(workspace_id)
    .execute(&mut *tx)
    .await?;

    let (comment_id_out, tombstone_row) = match mode {
        CommentDeleteGoMode::Leaf => {
            let deleted = sqlx::query(
                "DELETE FROM comment
                 WHERE id = $1::uuid AND workspace_id = $2::uuid AND deleted_at IS NULL
                   AND NOT EXISTS (
                       SELECT 1 FROM comment child
                       WHERE child.parent_id = comment.id AND child.workspace_id = comment.workspace_id
                   )
                 RETURNING id::text",
            )
            .bind(comment_id)
            .bind(workspace_id)
            .fetch_optional(&mut *tx)
            .await?;
            let Some(row) = deleted else {
                return Err(IssueCommentDeleteError::Access(IssueReadError::NotFound(
                    "comment not found",
                )));
            };
            let removed_id: String = row.try_get(0)?;
            (removed_id, None)
        }
        CommentDeleteGoMode::Tombstone => {
            let tombstone_sql = format!(
                "UPDATE comment SET
                    content = '',
                    deleted_at = now(),
                    resolved_at = NULL,
                    resolved_by_type = NULL,
                    resolved_by_id = NULL,
                    revision = revision + 1,
                    updated_at = now()
                 WHERE id = $1::uuid AND workspace_id = $2::uuid AND deleted_at IS NULL
                 RETURNING {COMMENT_VERSION_COLUMNS}"
            );
            let row = sqlx::query(&tombstone_sql)
                .bind(comment_id)
                .bind(workspace_id)
                .fetch_optional(&mut *tx)
                .await?;
            let Some(row) = row else {
                return Err(IssueCommentDeleteError::Access(IssueReadError::NotFound(
                    "comment not found",
                )));
            };
            let tombstone = map_comment_version(&row).map_err(IssueCommentDeleteError::from)?;
            (tombstone.id.clone(), Some(tombstone))
        }
    };

    let issue_revision = touch_issue_activity(&mut tx, &issue_id, workspace_id).await?;
    tx.commit().await?;
    Ok(IssueCommentDeleteResult {
        comment_id: comment_id_out,
        issue_id,
        issue_revision,
        tombstone: tombstone_row,
    })
}
