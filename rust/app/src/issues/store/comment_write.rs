//! Member-authored top-level comment creates for the comment pilot. Mirrors
//! `CreateComment` issue touch + insert in one transaction so collaboration
//! wakeup capture stays on the existing comment trigger.

use serde::{Deserialize, Serialize};
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError, format_issue_timestamp, is_uuid, ordinary::set_write_session,
    resolve_caller_access_on,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueCommentCreate {
    pub id: String,
    pub content: String,
    #[serde(rename = "type")]
    pub comment_type: String,
    pub author_type: String,
    pub author_id: String,
}

#[derive(Debug, Serialize)]
pub struct IssueCommentPayload {
    pub created: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub comment: Option<IssueCommentRow>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub issue_revision: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub issue_title: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub issue_status: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub issue_assignee_type: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub issue_assignee_id: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct IssueCommentRow {
    pub id: String,
    pub issue_id: String,
    pub author_type: String,
    pub author_id: String,
    pub content: String,
    #[serde(rename = "type")]
    pub r#type: String,
    pub parent_id: Option<String>,
    pub created_at: String,
    pub updated_at: String,
    pub revision: i64,
    pub resolved_at: Option<String>,
    pub resolved_by_type: Option<String>,
    pub resolved_by_id: Option<String>,
    pub source_task_id: Option<String>,
    pub deleted_at: Option<String>,
    pub reactions: Vec<serde_json::Value>,
    pub attachments: Vec<serde_json::Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub issue_revision: Option<i64>,
}

#[derive(Debug)]
pub enum IssueCommentCreateError {
    Access(IssueReadError),
    InvalidInput,
    RouteChanged,
    IdConflict { code: &'static str },
    Database(sqlx::Error),
}

impl From<sqlx::Error> for IssueCommentCreateError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

fn comment_route_allowed(assignee_type: Option<&str>) -> bool {
    matches!(assignee_type, None | Some("") | Some("member"))
}

fn map_comment_row(
    row: &sqlx::postgres::PgRow,
    issue_revision: Option<i64>,
) -> Result<IssueCommentRow, sqlx::Error> {
    let optional_ts = |col: &str| -> Result<Option<String>, sqlx::Error> {
        Ok(row
            .try_get::<Option<String>, _>(col)?
            .map(format_issue_timestamp))
    };
    Ok(IssueCommentRow {
        id: row.try_get::<String, _>("id")?,
        issue_id: row.try_get::<String, _>("issue_id")?,
        author_type: row.try_get::<String, _>("author_type")?,
        author_id: row.try_get::<String, _>("author_id")?,
        content: row.try_get::<String, _>("content")?,
        r#type: row.try_get::<String, _>("type")?,
        parent_id: row.try_get::<Option<String>, _>("parent_id")?,
        created_at: format_issue_timestamp(row.try_get::<String, _>("created_at")?),
        updated_at: format_issue_timestamp(row.try_get::<String, _>("updated_at")?),
        revision: row.try_get::<i64, _>("revision")?,
        resolved_at: optional_ts("resolved_at")?,
        resolved_by_type: row.try_get::<Option<String>, _>("resolved_by_type")?,
        resolved_by_id: row.try_get::<Option<String>, _>("resolved_by_id")?,
        source_task_id: row.try_get::<Option<String>, _>("source_task_id")?,
        deleted_at: optional_ts("deleted_at")?,
        reactions: Vec::new(),
        attachments: Vec::new(),
        issue_revision,
    })
}

async fn classify_existing_comment_id(
    conn: &mut PgConnection,
    comment_id: &str,
    issue_id: &str,
    author_type: &str,
    author_id: &str,
) -> Result<(), IssueCommentCreateError> {
    let row = sqlx::query(
        "SELECT issue_id::text, author_type, author_id::text FROM comment WHERE id = $1::uuid",
    )
    .bind(comment_id)
    .fetch_optional(conn)
    .await?;
    let Some(row) = row else {
        return Err(IssueCommentCreateError::Database(sqlx::Error::Protocol(
            "comment id conflict without owning row".into(),
        )));
    };
    let existing_issue: String = row.try_get(0)?;
    let existing_author_type: String = row.try_get(1)?;
    let existing_author_id: String = row.try_get(2)?;
    if existing_issue != issue_id {
        return Err(IssueCommentCreateError::IdConflict {
            code: "comment_id_other_issue",
        });
    }
    if existing_author_type != author_type || existing_author_id != author_id {
        return Err(IssueCommentCreateError::IdConflict {
            code: "comment_id_other_author",
        });
    }
    Ok(())
}

pub async fn create_issue_comment(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    request: IssueCommentCreate,
) -> Result<IssueCommentPayload, IssueCommentCreateError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(IssueCommentCreateError::InvalidInput);
    };
    if !is_uuid(&request.id)
        || !is_uuid(issue_id)
        || request.author_id != *user_id
        || request.author_type != "member"
        || request.comment_type != "comment"
        || request.content.is_empty()
        || request.content.contains('\0')
    {
        return Err(IssueCommentCreateError::InvalidInput);
    }

    let mut tx = pool.begin().await?;
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;

    let locked = sqlx::query(
        "SELECT project_id::text, title, status, assignee_type, assignee_id::text
         FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueCommentCreateError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;

    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueCommentCreateError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssueCommentCreateError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }

    let issue_assignee_type: Option<String> = locked.try_get(3)?;
    if !comment_route_allowed(issue_assignee_type.as_deref()) {
        return Err(IssueCommentCreateError::RouteChanged);
    }

    set_write_session(&mut tx, actor, None, None).await?;

    let issue_title: String = locked.try_get(1)?;
    let issue_status: String = locked.try_get(2)?;
    let issue_assignee_id: Option<String> = locked.try_get(4)?;

    let inserted = sqlx::query(
        "INSERT INTO comment (
            id, issue_id, workspace_id, author_type, author_id, content, type,
            parent_id, source_task_id, quick_action_id, via_plugin_id, suppressed_agent_ids
        ) VALUES (
            $7::uuid, $1::uuid, $2::uuid, $3, $4::uuid, $5, $6,
            NULL, NULL, NULL, NULL, NULL::uuid[]
        )
        ON CONFLICT (id) DO NOTHING
        RETURNING id::text, issue_id::text, author_type, author_id::text, content, type,
                  parent_id::text, created_at::text, updated_at::text, revision,
                  resolved_at::text, resolved_by_type, resolved_by_id::text,
                  source_task_id::text, deleted_at::text",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .bind(&request.author_type)
    .bind(&request.author_id)
    .bind(&request.content)
    .bind(&request.comment_type)
    .bind(&request.id)
    .fetch_optional(&mut *tx)
    .await?;

    let Some(insert_row) = inserted else {
        classify_existing_comment_id(
            &mut tx,
            &request.id,
            issue_id,
            &request.author_type,
            &request.author_id,
        )
        .await?;
        tx.commit().await?;
        return Ok(IssueCommentPayload {
            created: false,
            comment: None,
            issue_revision: None,
            issue_title: None,
            issue_status: None,
            issue_assignee_type: None,
            issue_assignee_id: None,
        });
    };
    let issue_row = sqlx::query(
        "UPDATE issue SET
            updated_at = now(),
            revision = revision + 1,
            last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
         WHERE id = $1::uuid AND workspace_id = $2::uuid
         RETURNING revision",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueCommentCreateError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;

    let issue_revision: i64 = issue_row.try_get(0)?;
    let comment = map_comment_row(&insert_row, Some(issue_revision))?;
    tx.commit().await?;
    Ok(IssueCommentPayload {
        created: true,
        comment: Some(comment),
        issue_revision: Some(issue_revision),
        issue_title: Some(issue_title),
        issue_status: Some(issue_status),
        issue_assignee_type,
        issue_assignee_id,
    })
}

pub(super) async fn lock_comment_write_member(
    conn: &mut PgConnection,
    workspace_id: &str,
    user_id: &str,
) -> Result<bool, sqlx::Error> {
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *conn)
        .await?;
    let member = sqlx::query_scalar::<_, i32>(
        "SELECT 1 FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
    )
    .bind(user_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await?;
    Ok(member.is_some())
}

pub(super) async fn lock_issue_for_comment(
    conn: &mut PgConnection,
    comment_id: &str,
    workspace_id: &str,
) -> Result<Option<String>, sqlx::Error> {
    sqlx::query_scalar(
        "SELECT i.id::text FROM issue i
         WHERE i.id = (
             SELECT c.issue_id FROM comment c
             WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid
         ) AND i.workspace_id = $2::uuid
         FOR UPDATE OF i",
    )
    .bind(comment_id)
    .bind(workspace_id)
    .fetch_optional(conn)
    .await
}

pub(super) async fn comment_has_active_trigger_task(
    conn: &mut PgConnection,
    comment_id: &str,
    issue_id: &str,
) -> Result<bool, sqlx::Error> {
    sqlx::query_scalar(
        "SELECT EXISTS (
            SELECT 1 FROM agent_task_queue q
            WHERE q.issue_id = $2::uuid
              AND (q.trigger_comment_id = $1::uuid OR $1::uuid = ANY(q.coalesced_comment_ids))
              AND q.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
        )",
    )
    .bind(comment_id)
    .bind(issue_id)
    .fetch_one(conn)
    .await
}

pub(super) async fn touch_issue_activity(
    conn: &mut PgConnection,
    issue_id: &str,
    workspace_id: &str,
) -> Result<i64, sqlx::Error> {
    sqlx::query_scalar(
        "UPDATE issue SET
            revision = revision + 1,
            last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
         WHERE id = $1::uuid AND workspace_id = $2::uuid
         RETURNING revision",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_one(conn)
    .await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn comment_route_allows_unassigned_and_member_only() {
        assert!(comment_route_allowed(None));
        assert!(comment_route_allowed(Some("")));
        assert!(comment_route_allowed(Some("member")));
        assert!(!comment_route_allowed(Some("agent")));
        assert!(!comment_route_allowed(Some("squad")));
    }
}
