//! Member resolve/unresolve for projectless task discussion threads. Lock order:
//! workspace/member share, write actor, issue SHARE + thread root NO KEY UPDATE,
//! then clear sibling resolutions and apply the target change without bumping issue
//! revision.

use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError, comment_write::IssueCommentRow, format_issue_timestamp, is_uuid,
    ordinary::set_write_actor, resolve_caller_access_on,
};

const COMMENT_ROW_RETURNING: &str = "id::text, issue_id::text, author_type, author_id::text, content, type, parent_id::text, \
     to_json(created_at)#>>'{}', to_json(updated_at)#>>'{}', revision, to_json(resolved_at)#>>'{}', \
     resolved_by_type, resolved_by_id::text, source_task_id::text, to_json(deleted_at)#>>'{}'";

#[derive(Debug, serde::Serialize)]
pub struct CommentResolutionResult {
    pub target_changed: bool,
    pub target: IssueCommentRow,
    pub cleared: Vec<IssueCommentRow>,
}

#[derive(Debug)]
pub enum CommentResolutionError {
    Access(IssueReadError),
    InvalidInput,
    RouteChanged,
    Database(sqlx::Error),
}

impl From<sqlx::Error> for CommentResolutionError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

async fn lock_workspace_member(
    tx: &mut PgConnection,
    workspace_id: &str,
    user_id: &str,
) -> Result<(), CommentResolutionError> {
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;
    let member = sqlx::query_scalar::<_, i32>(
        "SELECT 1 FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
    )
    .bind(user_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?;
    if member.is_none() {
        return Err(CommentResolutionError::Access(IssueReadError::NotFound(
            "workspace not found",
        )));
    }
    Ok(())
}

fn map_comment_row(row: &sqlx::postgres::PgRow) -> Result<IssueCommentRow, sqlx::Error> {
    let ts = |idx: usize| -> Result<String, sqlx::Error> {
        Ok(format_issue_timestamp(row.try_get::<String, _>(idx)?))
    };
    let opt_ts = |idx: usize| -> Result<Option<String>, sqlx::Error> {
        Ok(row
            .try_get::<Option<String>, _>(idx)?
            .map(format_issue_timestamp))
    };
    Ok(IssueCommentRow {
        id: row.try_get(0)?,
        issue_id: row.try_get(1)?,
        author_type: row.try_get(2)?,
        author_id: row.try_get(3)?,
        content: row.try_get(4)?,
        r#type: row.try_get(5)?,
        parent_id: row.try_get(6)?,
        created_at: ts(7)?,
        updated_at: ts(8)?,
        revision: row.try_get(9)?,
        resolved_at: opt_ts(10)?,
        resolved_by_type: row.try_get(11)?,
        resolved_by_id: row.try_get(12)?,
        source_task_id: row.try_get(13)?,
        deleted_at: opt_ts(14)?,
        reactions: Vec::new(),
        attachments: Vec::new(),
        issue_revision: None,
    })
}

/// Acquire issue SHARE + thread-root NO KEY UPDATE, then read the live target
/// in a separate statement so the snapshot reflects the held locks.
async fn lock_and_read_target(
    tx: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
) -> Result<(bool, String), CommentResolutionError> {
    let issue_id = sqlx::query_scalar::<_, String>(
        "WITH target AS (
            SELECT c0.id, c0.issue_id, c0.workspace_id
            FROM comment c0
            WHERE c0.id = $1::uuid
              AND c0.workspace_id = $2::uuid
        ),
        thread_root AS (
            SELECT comment_thread_root_id((SELECT id FROM target))::uuid AS id
        ),
        locked_issue AS MATERIALIZED (
            SELECT issue.id
            FROM issue
            JOIN target t ON issue.id = t.issue_id AND issue.workspace_id = t.workspace_id
            FOR SHARE OF issue
        ),
        locked_root AS MATERIALIZED (
            SELECT root.id AS root_id
            FROM comment root
            JOIN thread_root tr ON root.id = tr.id
            CROSS JOIN locked_issue
            WHERE root.workspace_id = (SELECT workspace_id FROM target)
              AND root.issue_id = (SELECT issue_id FROM target)
            FOR NO KEY UPDATE OF root
        )
        SELECT issue_id::text FROM target WHERE EXISTS (SELECT 1 FROM locked_root)",
    )
    .bind(comment_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(CommentResolutionError::Access(IssueReadError::NotFound(
        "comment not found",
    )))?;

    let was_resolved = sqlx::query_scalar::<_, bool>(
        "SELECT resolved_at IS NOT NULL FROM comment
         WHERE id = $1::uuid AND workspace_id = $2::uuid AND issue_id = $3::uuid AND deleted_at IS NULL",
    )
    .bind(comment_id)
    .bind(workspace_id)
    .bind(&issue_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(CommentResolutionError::Access(IssueReadError::NotFound(
        "comment not found",
    )))?;

    Ok((was_resolved, issue_id))
}

async fn recheck_projectless_issue(
    tx: &mut PgConnection,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
) -> Result<(), CommentResolutionError> {
    let row = sqlx::query(
        "SELECT project_id::text FROM issue
         WHERE id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(CommentResolutionError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let project_id: Option<String> = row.try_get(0)?;
    if project_id.is_some() {
        let hidden = resolve_caller_access_on(tx, workspace_id, actor)
            .await
            .map_err(CommentResolutionError::Access)?;
        if !hidden.can_see(project_id.as_deref()) {
            return Err(CommentResolutionError::Access(IssueReadError::NotFound(
                "comment not found",
            )));
        }
        return Err(CommentResolutionError::RouteChanged);
    }
    Ok(())
}

async fn clear_other_thread_resolutions(
    tx: &mut PgConnection,
    target_id: &str,
    issue_id: &str,
    workspace_id: &str,
) -> Result<Vec<IssueCommentRow>, sqlx::Error> {
    let rows = sqlx::query(&format!(
        "WITH RECURSIVE root_of AS (
            SELECT c.id, c.parent_id
            FROM comment c
            WHERE c.id = $1::uuid AND c.issue_id = $2::uuid AND c.workspace_id = $3::uuid
            UNION ALL
            SELECT p.id, p.parent_id
            FROM comment p
            JOIN root_of r ON p.id = r.parent_id
        ),
        thread_root AS (
            SELECT id FROM root_of WHERE parent_id IS NULL LIMIT 1
        ),
        descendants AS (
            SELECT c.id
            FROM comment c
            JOIN thread_root tr ON c.id = tr.id
            UNION
            SELECT c.id
            FROM comment c
            JOIN descendants d ON c.parent_id = d.id
            WHERE c.issue_id = $2::uuid AND c.workspace_id = $3::uuid
        )
        UPDATE comment SET
            resolved_at = NULL,
            resolved_by_type = NULL,
            resolved_by_id = NULL,
            revision = revision + 1,
            updated_at = now()
        WHERE comment.id IN (SELECT id FROM descendants)
          AND comment.id <> $1::uuid
          AND comment.resolved_at IS NOT NULL
        RETURNING {COMMENT_ROW_RETURNING}"
    ))
    .bind(target_id)
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_all(&mut *tx)
    .await?;
    rows.iter().map(map_comment_row).collect()
}

async fn apply_resolve(
    tx: &mut PgConnection,
    comment_id: &str,
    actor_type: &str,
    actor_id: &str,
) -> Result<IssueCommentRow, sqlx::Error> {
    let row = sqlx::query(&format!(
        "UPDATE comment SET
            resolved_at = COALESCE(resolved_at, now()),
            resolved_by_type = COALESCE(resolved_by_type, $2),
            resolved_by_id = COALESCE(resolved_by_id, $3::uuid),
            revision = revision + CASE WHEN resolved_at IS NULL THEN 1 ELSE 0 END,
            updated_at = CASE WHEN resolved_at IS NULL THEN now() ELSE updated_at END
         WHERE id = $1::uuid AND deleted_at IS NULL
         RETURNING {COMMENT_ROW_RETURNING}"
    ))
    .bind(comment_id)
    .bind(actor_type)
    .bind(actor_id)
    .fetch_optional(&mut *tx)
    .await?;
    let Some(row) = row else {
        return Err(sqlx::Error::RowNotFound);
    };
    map_comment_row(&row)
}

async fn apply_unresolve(
    tx: &mut PgConnection,
    comment_id: &str,
) -> Result<IssueCommentRow, sqlx::Error> {
    let row = sqlx::query(&format!(
        "UPDATE comment SET
            resolved_at = NULL,
            resolved_by_type = NULL,
            resolved_by_id = NULL,
            revision = revision + CASE WHEN resolved_at IS NOT NULL THEN 1 ELSE 0 END,
            updated_at = CASE WHEN resolved_at IS NOT NULL THEN now() ELSE updated_at END
         WHERE id = $1::uuid
         RETURNING {COMMENT_ROW_RETURNING}"
    ))
    .bind(comment_id)
    .fetch_one(&mut *tx)
    .await?;
    map_comment_row(&row)
}

pub async fn resolve_comment(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    comment_id: &str,
) -> Result<CommentResolutionResult, CommentResolutionError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(CommentResolutionError::InvalidInput);
    };
    if !is_uuid(comment_id) {
        return Err(CommentResolutionError::InvalidInput);
    }
    let mut tx = pool.begin().await?;
    lock_workspace_member(&mut tx, workspace_id, user_id).await?;
    set_write_actor(&mut tx, actor, None).await?;
    let (was_resolved, issue_id) = lock_and_read_target(&mut tx, comment_id, workspace_id).await?;
    recheck_projectless_issue(&mut tx, workspace_id, actor, &issue_id).await?;
    let cleared =
        clear_other_thread_resolutions(&mut tx, comment_id, &issue_id, workspace_id).await?;
    let target = apply_resolve(&mut tx, comment_id, "member", user_id).await?;
    tx.commit().await?;
    Ok(CommentResolutionResult {
        target_changed: !was_resolved,
        target,
        cleared,
    })
}

pub async fn unresolve_comment(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    comment_id: &str,
) -> Result<CommentResolutionResult, CommentResolutionError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(CommentResolutionError::InvalidInput);
    };
    if !is_uuid(comment_id) {
        return Err(CommentResolutionError::InvalidInput);
    }
    let mut tx = pool.begin().await?;
    lock_workspace_member(&mut tx, workspace_id, user_id).await?;
    set_write_actor(&mut tx, actor, None).await?;
    let (was_resolved, issue_id) = lock_and_read_target(&mut tx, comment_id, workspace_id).await?;
    recheck_projectless_issue(&mut tx, workspace_id, actor, &issue_id).await?;
    let target = apply_unresolve(&mut tx, comment_id).await?;
    tx.commit().await?;
    Ok(CommentResolutionResult {
        target_changed: was_resolved,
        target,
        cleared: Vec::new(),
    })
}
