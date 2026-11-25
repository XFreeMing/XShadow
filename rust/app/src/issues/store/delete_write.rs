//! Member-authored single issue delete for the delete pilot. Mirrors
//! `deleteIssuesAndCollectAttachmentURLs` after lock and eligibility checks.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError, is_uuid,
    ordinary::{self, ISSUE_ROW_SNAPSHOT},
};

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueDeleteAttachmentPlanItem {
    pub attachment_id: String,
    pub object_url: String,
    pub storage_key: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueDeleteWrite {
    pub source_task_id: Option<String>,
    pub attachment_cleanup: Option<Vec<IssueDeleteAttachmentPlanItem>>,
}

#[derive(Debug, Serialize)]
pub struct ClearedDuplicateSnapshot {
    pub issue: String,
    pub original_issue_id: String,
}

#[derive(Debug, Serialize)]
pub struct IssueDeleteWriteResult {
    pub deleted: bool,
    pub issue_id: String,
    pub detached_children: Vec<String>,
    pub cleared_duplicates: Vec<ClearedDuplicateSnapshot>,
    pub goal_links_changed: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub object_cleanup_attachment_ids: Vec<String>,
}

#[derive(Debug)]
pub enum IssueDeleteWriteError {
    Access(IssueReadError),
    AlreadyDeleted,
    DeleteSensitive,
    Database(sqlx::Error),
}

impl From<sqlx::Error> for IssueDeleteWriteError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

const DELETE_ISSUE: &str = r#"
WITH target AS (
    SELECT issue.id FROM issue WHERE issue.id = $1::uuid AND issue.workspace_id = $2::uuid
),
cleared_wakeup_receipts AS (
 DELETE FROM issue_wakeup_receipt WHERE wakeup_id IN (SELECT id FROM issue_wakeup WHERE issue_id IN (SELECT target.id FROM target))
),
cleared_wakeups AS (
 DELETE FROM issue_wakeup WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_child_events AS (
 DELETE FROM issue_child_event WHERE parent_id IN (SELECT target.id FROM target) OR child_id IN (SELECT target.id FROM target)
),
cleared_task_supplements AS (
 DELETE FROM task_supplement WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_task_supplement_capabilities AS (
 DELETE FROM task_supplement_capability WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_vcs_pr_links AS (
    DELETE FROM issue_vcs_pull_request WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_pr_automation AS (
    DELETE FROM issue_pr_automation WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_pr_exclusions AS (
    DELETE FROM issue_pull_request_exclusion WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_unread AS (
    DELETE FROM issue_unread WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_environments AS (
    DELETE FROM issue_environment WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_skill_observations AS (
    DELETE FROM skill_run_observation
    WHERE workspace_id = $2::uuid AND task_id IN (
        SELECT agent_task_queue.id FROM agent_task_queue
        WHERE issue_id IN (SELECT target.id FROM target)
    )
),
cleared_environment_commands AS (
    DELETE FROM issue_environment_command WHERE issue_id IN (SELECT target.id FROM target)
),
cleared_environment_configs AS (
    DELETE FROM issue_environment_config WHERE issue_id IN (SELECT target.id FROM target)
)
DELETE FROM issue WHERE issue.id IN (SELECT target.id FROM target)
"#;

fn scoped_storage_key_valid(workspace_id: &str, attachment_id: &str, key: &str) -> bool {
    if !is_uuid(workspace_id) || !is_uuid(attachment_id) {
        return false;
    }
    let basename = format!("workspaces/{workspace_id}/{attachment_id}");
    if key == basename {
        return true;
    }
    key.strip_prefix(&format!("{basename}."))
        .is_some_and(|ext| {
            !ext.is_empty()
                && ext
                    .bytes()
                    .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'-'))
        })
}

async fn verify_attachment_delete_plan(
    tx: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    plan: Option<&[IssueDeleteAttachmentPlanItem]>,
) -> Result<Vec<IssueDeleteAttachmentPlanItem>, IssueDeleteWriteError> {
    let rows = sqlx::query(
        "SELECT a.id::text, a.workspace_id::text, a.url,
                a.comment_id IS NULL AND a.chat_session_id IS NULL
                 AND a.chat_message_id IS NULL AND a.task_id IS NULL
                 AND a.source_context_id IS NULL AS direct_upload
           FROM attachment a
          WHERE a.issue_id = $1::uuid
             OR a.comment_id IN (SELECT c.id FROM comment c WHERE c.issue_id = $1::uuid)
          ORDER BY a.id
          FOR UPDATE",
    )
    .bind(issue_id)
    .fetch_all(&mut *tx)
    .await?;

    let Some(plan) = plan else {
        return if rows.is_empty() {
            Ok(Vec::new())
        } else {
            Err(IssueDeleteWriteError::DeleteSensitive)
        };
    };
    if rows.len() != plan.len() {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }
    let mut planned = plan.to_vec();
    planned.sort_by(|a, b| a.attachment_id.cmp(&b.attachment_id));
    for (row, planned) in rows.iter().zip(&planned) {
        if !row.try_get::<bool, _>(3)?
            || row.try_get::<&str, _>(1)? != workspace_id
            || row.try_get::<&str, _>(0)? != planned.attachment_id
            || row.try_get::<&str, _>(2)? != planned.object_url
            || !scoped_storage_key_valid(workspace_id, &planned.attachment_id, &planned.storage_key)
        {
            return Err(IssueDeleteWriteError::DeleteSensitive);
        }
    }
    Ok(planned)
}

async fn pilot_shape_sensitive(
    tx: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    creator_type: &str,
    assignee_type: Option<&str>,
    origin_present: bool,
    attachment_plan: Option<&[IssueDeleteAttachmentPlanItem]>,
) -> Result<Vec<IssueDeleteAttachmentPlanItem>, IssueDeleteWriteError> {
    if creator_type != "member" {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }
    if assignee_type.is_some_and(|kind| kind != "member") {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }
    if origin_present {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }
    let source_context = sqlx::query(
        "SELECT 1 FROM issue_source_context WHERE workspace_id = $1::uuid AND issue_id = $2::uuid",
    )
    .bind(workspace_id)
    .bind(issue_id)
    .fetch_optional(&mut *tx)
    .await?;
    if source_context.is_some() {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }
    let queued = sqlx::query("SELECT 1 FROM agent_task_queue WHERE issue_id = $1::uuid LIMIT 1")
        .bind(issue_id)
        .fetch_optional(&mut *tx)
        .await?;
    if queued.is_some() {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }
    let autopilot = sqlx::query(
        "SELECT 1 FROM autopilot_run
         WHERE issue_id = $1::uuid AND status IN ('issue_created', 'running') LIMIT 1",
    )
    .bind(issue_id)
    .fetch_optional(&mut *tx)
    .await?;
    if autopilot.is_some() {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }
    // Comment attachments reference comments directly. Lock those rows as well
    // as their issue before deciding the delete has no storage side effects.
    sqlx::query("SELECT id FROM comment WHERE issue_id = $1::uuid ORDER BY id FOR UPDATE")
        .bind(issue_id)
        .execute(&mut *tx)
        .await?;
    verify_attachment_delete_plan(tx, workspace_id, issue_id, attachment_plan).await
}

async fn record_and_delete_direct_uploads(
    tx: &mut PgConnection,
    workspace_id: &str,
    direct_uploads: &[IssueDeleteAttachmentPlanItem],
) -> Result<(), IssueDeleteWriteError> {
    for upload in direct_uploads {
        sqlx::query(
            "INSERT INTO issue_attachment_object_delete_outbox (
                attachment_id, workspace_id, storage_key, object_url
             ) VALUES ($1::uuid, $2::uuid, $3, $4)",
        )
        .bind(&upload.attachment_id)
        .bind(workspace_id)
        .bind(&upload.storage_key)
        .bind(&upload.object_url)
        .execute(&mut *tx)
        .await?;
    }
    if !direct_uploads.is_empty() {
        let ids: Vec<String> = direct_uploads
            .iter()
            .map(|row| row.attachment_id.clone())
            .collect();
        let deleted = sqlx::query(
            "DELETE FROM attachment
             WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])",
        )
        .bind(workspace_id)
        .bind(&ids)
        .execute(&mut *tx)
        .await?;
        if deleted.rows_affected() != ids.len() as u64 {
            return Err(IssueDeleteWriteError::DeleteSensitive);
        }
    }
    Ok(())
}

const DELETE_FORBIDDEN: &str = "issue deletion is restricted to its creator, the creating agent's owner, and workspace owners or admins";

fn role_may_delete_any(role: &str) -> bool {
    matches!(role, "owner" | "admin")
}

struct IssueDeletePreview {
    issue_id: String,
    preview_project: Option<String>,
}

async fn preview_delete_targets(
    tx: &mut PgConnection,
    workspace_id: &str,
    issue_ids: &[String],
) -> Result<Vec<IssueDeletePreview>, IssueDeleteWriteError> {
    let mut previews = Vec::with_capacity(issue_ids.len());
    for issue_id in issue_ids {
        let preview_project: Option<String> = sqlx::query_scalar(
            "SELECT project_id::text FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid",
        )
        .bind(issue_id)
        .bind(workspace_id)
        .fetch_optional(&mut *tx)
        .await?
        .ok_or(IssueDeleteWriteError::AlreadyDeleted)?;
        previews.push(IssueDeletePreview {
            issue_id: issue_id.clone(),
            preview_project,
        });
    }
    Ok(previews)
}

async fn lock_delete_projects(
    tx: &mut PgConnection,
    workspace_id: &str,
    previews: &[IssueDeletePreview],
) -> Result<HashMap<String, bool>, IssueDeleteWriteError> {
    let mut project_ids: Vec<String> = previews
        .iter()
        .filter_map(|preview| preview.preview_project.clone())
        .collect();
    project_ids.sort();
    project_ids.dedup();
    let mut private = HashMap::with_capacity(project_ids.len());
    for project_id in project_ids {
        let project_private = sqlx::query_scalar::<_, bool>(
            "SELECT visibility = 'private'
             FROM project WHERE id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
        )
        .bind(&project_id)
        .bind(workspace_id)
        .fetch_optional(&mut *tx)
        .await?
        .ok_or(IssueDeleteWriteError::AlreadyDeleted)?;
        private.insert(project_id, project_private);
    }
    Ok(private)
}

async fn validate_locked_issue_for_delete(
    tx: &mut PgConnection,
    workspace_id: &str,
    preview: &IssueDeletePreview,
    project_private: &HashMap<String, bool>,
    privileged: bool,
    user_id: &str,
    attachment_plan: Option<&[IssueDeleteAttachmentPlanItem]>,
) -> Result<Vec<IssueDeleteAttachmentPlanItem>, IssueDeleteWriteError> {
    let issue_id = &preview.issue_id;
    let locked = sqlx::query(
        "SELECT project_id::text, creator_type, creator_id::text, assignee_type,
                origin_type IS NOT NULL AS has_origin
         FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueDeleteWriteError::AlreadyDeleted)?;

    let actual_project: Option<String> = locked.try_get(0)?;
    if actual_project != preview.preview_project {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }

    let needs_project_membership = actual_project
        .as_ref()
        .and_then(|id| project_private.get(id).copied())
        .unwrap_or(false);
    if needs_project_membership && !privileged {
        let project_member = sqlx::query_scalar::<_, i32>(
            "SELECT 1 FROM project_member WHERE project_id = $1::uuid AND user_id = $2::uuid FOR SHARE",
        )
        .bind(actual_project.as_deref())
        .bind(user_id)
        .fetch_optional(&mut *tx)
        .await?;
        if project_member.is_none() {
            return Err(IssueDeleteWriteError::AlreadyDeleted);
        }
    }

    let creator_type: String = locked.try_get(1)?;
    let creator_id: String = locked.try_get(2)?;
    if !privileged && (creator_type != "member" || creator_id != user_id) {
        return Err(IssueDeleteWriteError::Access(IssueReadError::Forbidden(
            DELETE_FORBIDDEN,
        )));
    }

    let assignee_type: Option<String> = locked.try_get(3)?;
    let origin_present: bool = locked.try_get(4)?;
    pilot_shape_sensitive(
        tx,
        workspace_id,
        issue_id,
        &creator_type,
        assignee_type.as_deref(),
        origin_present,
        attachment_plan,
    )
    .await
}

/// Locks every selected project before any issue row, then locks and validates
/// each issue in deterministic UUID order.
pub(super) async fn lock_issues_for_delete(
    tx: &mut PgConnection,
    workspace_id: &str,
    actor: &CallerActor,
    issue_ids: &[String],
    attachment_plan: Option<&[IssueDeleteAttachmentPlanItem]>,
) -> Result<Vec<IssueDeleteAttachmentPlanItem>, IssueDeleteWriteError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(IssueDeleteWriteError::Access(IssueReadError::Forbidden(
            "issue deletion requires a human or a bound agent identity",
        )));
    };
    if !is_uuid(user_id) {
        return Err(IssueDeleteWriteError::Access(IssueReadError::Forbidden(
            DELETE_FORBIDDEN,
        )));
    }
    if attachment_plan.is_some_and(|plan| issue_ids.len() != 1 || plan.is_empty()) {
        return Err(IssueDeleteWriteError::DeleteSensitive);
    }
    if issue_ids.is_empty() {
        return Ok(Vec::new());
    }

    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;

    let previews = preview_delete_targets(tx, workspace_id, issue_ids).await?;
    let project_private = lock_delete_projects(tx, workspace_id, &previews).await?;

    let role: String = sqlx::query_scalar(
        "SELECT role FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
    )
    .bind(user_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueDeleteWriteError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let privileged = role_may_delete_any(&role);

    let mut uploads = Vec::new();
    for preview in &previews {
        uploads.extend(
            validate_locked_issue_for_delete(
                tx,
                workspace_id,
                preview,
                &project_private,
                privileged,
                user_id,
                attachment_plan,
            )
            .await?,
        );
    }
    Ok(uploads)
}

pub(super) async fn execute_issue_delete_steps(
    tx: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    excluded_issue_ids: &[String],
) -> Result<(Vec<String>, Vec<ClearedDuplicateSnapshot>, bool), IssueDeleteWriteError> {
    let detach_sql = format!(
        "UPDATE issue AS i
            SET parent_issue_id = NULL,
                stage = NULL,
                revision = revision + 1,
                updated_at = now(),
                last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
          WHERE workspace_id = $1::uuid
            AND parent_issue_id = $2::uuid
            AND NOT COALESCE(id = ANY($3::uuid[]), false)
          RETURNING {ISSUE_ROW_SNAPSHOT}"
    );
    let detached = sqlx::query(&detach_sql)
        .bind(workspace_id)
        .bind(issue_id)
        .bind(excluded_issue_ids)
        .fetch_all(&mut *tx)
        .await?;
    let detached_children: Vec<String> = detached
        .iter()
        .map(|row| row.try_get::<String, _>(0))
        .collect::<Result<_, _>>()?;

    let clear_sql = format!(
        "UPDATE issue AS i
            SET duplicate_of_issue_id = NULL,
                revision = revision + 1,
                updated_at = now(),
                last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
          WHERE workspace_id = $1::uuid
            AND duplicate_of_issue_id = $2::uuid
            AND NOT COALESCE(id = ANY($3::uuid[]), false)
          RETURNING {ISSUE_ROW_SNAPSHOT}"
    );
    let cleared = sqlx::query(&clear_sql)
        .bind(workspace_id)
        .bind(issue_id)
        .bind(excluded_issue_ids)
        .fetch_all(&mut *tx)
        .await?;
    let cleared_duplicates: Vec<ClearedDuplicateSnapshot> = cleared
        .iter()
        .map(|row| {
            Ok(ClearedDuplicateSnapshot {
                issue: row.try_get::<String, _>(0)?,
                original_issue_id: issue_id.to_owned(),
            })
        })
        .collect::<Result<_, sqlx::Error>>()?;

    let goal_links_changed: i64 = sqlx::query_scalar(
        "WITH removed AS (
            DELETE FROM goal_issue WHERE workspace_id = $1::uuid AND issue_id = $2::uuid RETURNING 1
         ) SELECT COUNT(*)::bigint FROM removed",
    )
    .bind(workspace_id)
    .bind(issue_id)
    .fetch_one(&mut *tx)
    .await?;

    sqlx::query(
        "DELETE FROM project_skill_proposal WHERE workspace_id = $1::uuid AND issue_id = $2::uuid",
    )
    .bind(workspace_id)
    .bind(issue_id)
    .execute(&mut *tx)
    .await?;

    let deleted = sqlx::query(DELETE_ISSUE)
        .bind(issue_id)
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;
    if deleted.rows_affected() != 1 {
        return Err(IssueDeleteWriteError::Database(sqlx::Error::RowNotFound));
    }
    Ok((
        detached_children,
        cleared_duplicates,
        goal_links_changed > 0,
    ))
}

pub async fn delete_issue(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    request: IssueDeleteWrite,
) -> Result<IssueDeleteWriteResult, IssueDeleteWriteError> {
    if !is_uuid(workspace_id) || !is_uuid(issue_id) {
        return Err(IssueDeleteWriteError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    let mut tx = pool.begin().await?;
    let direct_uploads = lock_issues_for_delete(
        &mut tx,
        workspace_id,
        actor,
        &[issue_id.to_owned()],
        request.attachment_cleanup.as_deref(),
    )
    .await?;

    ordinary::set_write_actor(&mut tx, actor, request.source_task_id.as_deref()).await?;

    record_and_delete_direct_uploads(&mut tx, workspace_id, &direct_uploads).await?;
    let object_cleanup_attachment_ids: Vec<String> = direct_uploads
        .iter()
        .map(|row| row.attachment_id.clone())
        .collect();

    let empty: &[String] = &[];
    let (detached_children, cleared_duplicates, goal_links_changed) =
        execute_issue_delete_steps(&mut tx, workspace_id, issue_id, empty).await?;

    tx.commit().await?;
    Ok(IssueDeleteWriteResult {
        deleted: true,
        issue_id: issue_id.to_owned(),
        detached_children,
        cleared_duplicates,
        goal_links_changed,
        object_cleanup_attachment_ids,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scoped_storage_key_valid_accepts_workspace_upload_keys() {
        let workspace = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
        let attachment = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
        assert!(scoped_storage_key_valid(
            workspace,
            attachment,
            &format!("workspaces/{workspace}/{attachment}.png")
        ));
        assert!(!scoped_storage_key_valid(
            workspace,
            attachment,
            "workspaces/other/x.png"
        ));
    }

    #[test]
    fn delete_wire_accepts_optional_attachment_plan() {
        let request: IssueDeleteWrite = serde_json::from_str(
            r#"{"attachment_cleanup":[{"attachment_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","object_url":"http://x/u","storage_key":"workspaces/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.png"}]}"#,
        )
        .unwrap();
        assert_eq!(request.attachment_cleanup.as_ref().map(Vec::len), Some(1));
    }

    #[test]
    fn delete_wire_accepts_optional_source_task_id() {
        let request: IssueDeleteWrite =
            serde_json::from_str(r#"{"source_task_id":"11111111-1111-4111-8111-111111111111"}"#)
                .unwrap();
        assert_eq!(
            request.source_task_id.as_deref(),
            Some("11111111-1111-4111-8111-111111111111")
        );
        let empty: IssueDeleteWrite = serde_json::from_str("{}").unwrap();
        assert!(empty.source_task_id.is_none());
    }
}
