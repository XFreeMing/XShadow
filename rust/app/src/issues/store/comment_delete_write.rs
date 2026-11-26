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
    delete_write::{
        IssueDeleteAttachmentPlanItem, IssueDeleteWriteError, record_and_delete_direct_uploads,
        scoped_storage_key_valid,
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
    #[serde(default)]
    attachment_cleanup: Vec<IssueDeleteAttachmentPlanItem>,
}

#[derive(Debug, serde::Serialize)]
pub struct IssueCommentDeleteResult {
    pub comment_id: String,
    pub issue_id: String,
    pub issue_revision: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tombstone: Option<IssueCommentRow>,
    #[serde(skip_serializing_if = "Vec::is_empty", default)]
    pub removed_ids: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty", default)]
    pub object_cleanup_attachment_ids: Vec<String>,
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
    ReplyLeaf,
}

const COMMENT_TOMBSTONE_PRUNE_DEPTH: usize = 256;

const DELETE_LEAF_COMMENT_SQL: &str = "
    DELETE FROM comment
     WHERE id = $1::uuid AND workspace_id = $2::uuid AND issue_id = $3::uuid
       AND deleted_at IS NULL
       AND NOT EXISTS (
           SELECT 1 FROM comment child WHERE child.parent_id = comment.id
       )
     RETURNING id::text, parent_id::text";

const DELETE_REPLYLESS_TOMBSTONE_SQL: &str = "
    DELETE FROM comment
     WHERE comment.id = $1::uuid AND comment.workspace_id = $2::uuid AND comment.issue_id = $3::uuid
       AND comment.deleted_at IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM comment child WHERE child.parent_id = comment.id
       )
     RETURNING comment.id::text, comment.parent_id::text";

fn decode_deleted_comment_row(
    row: &sqlx::postgres::PgRow,
) -> Result<(String, Option<String>), sqlx::Error> {
    let id: String = row.try_get(0)?;
    let parent: Option<String> = row.try_get(1)?;
    Ok((id, parent))
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
                    SELECT 1 FROM comment child WHERE child.parent_id = c.id
                ) AS has_replies,
                EXISTS(
                    SELECT 1 FROM attachment a WHERE a.comment_id = c.id
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

fn map_attachment_delete_error(error: IssueDeleteWriteError) -> IssueCommentDeleteError {
    match error {
        IssueDeleteWriteError::Database(database) => IssueCommentDeleteError::Database(database),
        _ => IssueCommentDeleteError::RouteChanged,
    }
}

async fn ensure_comment_attachments_for_delete(
    tx: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    comment_id: &str,
    plan: &[IssueDeleteAttachmentPlanItem],
) -> Result<Vec<String>, IssueCommentDeleteError> {
    let rows = sqlx::query(
        "SELECT a.id::text,
                a.url,
                (a.workspace_id IS NOT DISTINCT FROM $2::uuid
                 AND a.issue_id IS NOT DISTINCT FROM $3::uuid
                 AND a.task_id IS NULL
                 AND a.chat_session_id IS NULL
                 AND a.chat_message_id IS NULL
                 AND a.source_context_id IS NULL) AS scoped_ordinary,
                NOT EXISTS (
                    SELECT 1 FROM attachment other
                    WHERE other.url = a.url AND other.id <> a.id
                ) AS unshared_url
           FROM attachment a
          WHERE a.comment_id = $1::uuid
          ORDER BY a.id
          FOR UPDATE",
    )
    .bind(comment_id)
    .bind(workspace_id)
    .bind(issue_id)
    .fetch_all(&mut *tx)
    .await?;
    if plan.is_empty() {
        if rows.is_empty() {
            return Ok(Vec::new());
        }
        return Err(IssueCommentDeleteError::RouteChanged);
    }
    if rows.len() != plan.len() {
        return Err(IssueCommentDeleteError::RouteChanged);
    }
    let mut planned = plan.to_vec();
    planned.sort_by(|a, b| a.attachment_id.cmp(&b.attachment_id));
    for (row, planned) in rows.iter().zip(&planned) {
        let id: String = row.try_get(0)?;
        let url: String = row.try_get(1)?;
        let scoped_ordinary: bool = row.try_get(2)?;
        let unshared_url: bool = row.try_get(3)?;
        if !scoped_ordinary
            || !unshared_url
            || id != planned.attachment_id
            || url != planned.object_url
            || !scoped_storage_key_valid(workspace_id, &planned.attachment_id, &planned.storage_key)
        {
            return Err(IssueCommentDeleteError::RouteChanged);
        }
    }
    Ok(planned
        .iter()
        .map(|item| item.attachment_id.clone())
        .collect())
}

fn validate_delete_shape(
    shape: &LockedDeleteShape,
    actor: &CallerActor,
    hidden_can_see: bool,
    mode: CommentDeleteGoMode,
    attachment_plan_len: usize,
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
    if shape.author_type != "member"
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
        return Err(IssueCommentDeleteError::RouteChanged);
    }
    let matches_route = match mode {
        CommentDeleteGoMode::Leaf => !shape.is_reply && !shape.has_replies,
        CommentDeleteGoMode::Tombstone => !shape.is_reply && shape.has_replies,
        CommentDeleteGoMode::ReplyLeaf => shape.is_reply && !shape.has_replies,
    };
    if !matches_route || shape.has_attachments != (attachment_plan_len > 0) {
        return Err(IssueCommentDeleteError::RouteChanged);
    }
    Ok(())
}

async fn delete_replyless_tombstone(
    tx: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
    issue_id: &str,
) -> Result<Option<(String, Option<String>)>, sqlx::Error> {
    let row = sqlx::query(DELETE_REPLYLESS_TOMBSTONE_SQL)
        .bind(comment_id)
        .bind(workspace_id)
        .bind(issue_id)
        .fetch_optional(&mut *tx)
        .await?;
    match row {
        None => Ok(None),
        Some(row) => decode_deleted_comment_row(&row).map(Some),
    }
}

async fn delete_reply_leaf(
    tx: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
    issue_id: &str,
) -> Result<(Vec<String>, String), IssueCommentDeleteError> {
    let (removed_id, mut parent_id) =
        delete_leaf_comment(tx, comment_id, workspace_id, issue_id).await?;
    let mut removed_ids = vec![removed_id.clone()];
    for _ in 0..COMMENT_TOMBSTONE_PRUNE_DEPTH {
        let Some(parent) = parent_id else {
            break;
        };
        let Some((pruned_id, next_parent)) =
            delete_replyless_tombstone(tx, &parent, workspace_id, issue_id).await?
        else {
            break;
        };
        removed_ids.push(pruned_id);
        parent_id = next_parent;
    }
    Ok((removed_ids, removed_id))
}

async fn delete_leaf_comment(
    tx: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
    issue_id: &str,
) -> Result<(String, Option<String>), IssueCommentDeleteError> {
    let deleted = sqlx::query(DELETE_LEAF_COMMENT_SQL)
        .bind(comment_id)
        .bind(workspace_id)
        .bind(issue_id)
        .fetch_optional(&mut *tx)
        .await?;
    let Some(row) = deleted else {
        return Err(IssueCommentDeleteError::Access(IssueReadError::NotFound(
            "comment not found",
        )));
    };
    Ok(decode_deleted_comment_row(&row)?)
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
    let attachment_plan = request.attachment_cleanup;
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
        attachment_plan.len(),
    )?;

    if comment_has_active_trigger_task(&mut tx, comment_id, &issue_id).await? {
        return Err(IssueCommentDeleteError::RouteChanged);
    }

    set_write_session(&mut tx, actor, None, None).await?;

    let object_cleanup_ids = ensure_comment_attachments_for_delete(
        &mut tx,
        workspace_id,
        &issue_id,
        comment_id,
        &attachment_plan,
    )
    .await?;
    if !attachment_plan.is_empty() {
        record_and_delete_direct_uploads(&mut tx, workspace_id, &attachment_plan)
            .await
            .map_err(map_attachment_delete_error)?;
    }

    sqlx::query(
        "DELETE FROM comment_reaction
         WHERE comment_id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(comment_id)
    .bind(workspace_id)
    .execute(&mut *tx)
    .await?;

    let (comment_id_out, tombstone_row, removed_ids, cleanup_ids) = match mode {
        CommentDeleteGoMode::ReplyLeaf => {
            let (removed_ids, removed_id) =
                delete_reply_leaf(&mut tx, comment_id, workspace_id, &issue_id).await?;
            (removed_id, None, removed_ids, object_cleanup_ids)
        }
        CommentDeleteGoMode::Leaf => {
            let (removed_id, _) =
                delete_leaf_comment(&mut tx, comment_id, workspace_id, &issue_id).await?;
            (removed_id, None, Vec::new(), object_cleanup_ids)
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
            (
                tombstone.id.clone(),
                Some(tombstone),
                Vec::new(),
                object_cleanup_ids,
            )
        }
    };

    let issue_revision = touch_issue_activity(&mut tx, &issue_id, workspace_id).await?;
    tx.commit().await?;
    Ok(IssueCommentDeleteResult {
        comment_id: comment_id_out,
        issue_id,
        issue_revision,
        tombstone: tombstone_row,
        removed_ids,
        object_cleanup_attachment_ids: cleanup_ids,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn locked_shape(is_reply: bool, has_replies: bool, has_attachments: bool) -> LockedDeleteShape {
        LockedDeleteShape {
            issue_id: String::new(),
            is_reply,
            is_deleted: false,
            author_type: "member".into(),
            author_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc".into(),
            comment_type: "comment".into(),
            has_source: false,
            has_suppress: false,
            has_quick_action: false,
            has_plugin: false,
            project_id: None,
            assignee_type: None,
            has_supplement: false,
            has_replies,
            has_attachments,
        }
    }

    fn member_actor() -> CallerActor {
        CallerActor::Member {
            user_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc".into(),
        }
    }

    #[test]
    fn tombstone_rejects_attachment_without_plan() {
        let err = validate_delete_shape(
            &locked_shape(false, true, true),
            &member_actor(),
            true,
            CommentDeleteGoMode::Tombstone,
            0,
        )
        .expect_err("late attachment without plan must route-change");
        assert!(matches!(err, IssueCommentDeleteError::RouteChanged));
    }

    #[test]
    fn tombstone_rejects_plan_without_attachment() {
        let err = validate_delete_shape(
            &locked_shape(false, true, false),
            &member_actor(),
            true,
            CommentDeleteGoMode::Tombstone,
            1,
        )
        .expect_err("plan without attachment must route-change");
        assert!(matches!(err, IssueCommentDeleteError::RouteChanged));
    }

    #[test]
    fn reply_leaf_rejects_attachment_without_plan() {
        let err = validate_delete_shape(
            &locked_shape(true, false, true),
            &member_actor(),
            true,
            CommentDeleteGoMode::ReplyLeaf,
            0,
        )
        .expect_err("late attachment without plan must route-change");
        assert!(matches!(err, IssueCommentDeleteError::RouteChanged));
    }

    #[test]
    fn comment_delete_wire_accepts_tombstone_attachment_cleanup() {
        let request: IssueCommentDeleteRequest = serde_json::from_str(
            r#"{"go_mode":"tombstone","attachment_cleanup":[{"attachment_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","object_url":"http://x/u","storage_key":"workspaces/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.png"}]}"#,
        )
        .unwrap();
        assert_eq!(request.go_mode, CommentDeleteGoMode::Tombstone);
        assert_eq!(request.attachment_cleanup.len(), 1);
    }
}
