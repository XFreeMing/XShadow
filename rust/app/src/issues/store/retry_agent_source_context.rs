//! Human manual retry of failed issue-less source-context quick-create.

use serde::Deserialize;
use serde_json::Value;
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::agent_source_queue_lock::{
    AgentRuntimeShareBindingErrors, insert_queue_notify_outbox,
    lock_workspace_member_agent_runtime_share,
};
use super::create_source_context::{
    SourceContextCloneTrust, validate_locked_pending_source_clones,
};
use super::create_validate::lock_source_inherited_project;
use super::create_write::IssueCreateError;
use super::is_uuid;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AgentSourceRetryWrite {
    pub source_task_id: String,
    pub new_task_id: String,
    pub actor_user_id: String,
    pub expected_context_id: String,
    pub expected_source_issue_id: String,
    pub expected_agent_id: String,
    pub expected_runtime_id: String,
    pub expected_task_context: Value,
    pub expected_capture_digest: String,
    #[serde(default)]
    pub expected_clones: Vec<SourceContextCloneTrust>,
    #[serde(default)]
    pub runtime_mcp_overlay: Option<Value>,
    #[serde(default)]
    pub runtime_connected_apps: Option<Value>,
    pub runtime_cli_version: String,
    #[serde(default)]
    pub expected_source_project_id: Option<String>,
}

#[derive(Debug, serde::Serialize)]
pub struct AgentSourceRetryResult {
    pub task_id: String,
    pub source_context_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source_project_id: Option<String>,
}

const CREATE_MANUAL_RETRY_TASK: &str = r#"
INSERT INTO agent_task_queue (
    agent_id, runtime_id, status, priority, context,
    force_fresh_session, is_leader_task, squad_id,
    originator_user_id, accountable_user_id,
    runtime_mcp_overlay, runtime_connected_apps,
    originator_source, rerun_of_task_id, id
)
SELECT
    p.agent_id, p.runtime_id, 'queued', p.priority, p.context,
    TRUE, p.is_leader_task, p.squad_id,
    $1::uuid, $1::uuid,
    $2::jsonb, $3::jsonb,
    'direct_human', p.id, $4::uuid
FROM agent_task_queue p
WHERE p.id = $5::uuid
  AND p.agent_id = $6::uuid
  AND p.runtime_id = $7::uuid
  AND p.status = 'failed'
  AND p.issue_id IS NULL
  AND p.chat_session_id IS NULL
  AND p.autopilot_run_id IS NULL
  AND p.squad_id IS NULL
  AND lock_task_owner_rows(p.agent_id, p.issue_id, p.runtime_id)
RETURNING id::text
"#;

const TRANSFER_PENDING_SOURCE_CONTEXT: &str = r#"
UPDATE issue_source_context
SET origin_task_id = $1::uuid
WHERE workspace_id = $2::uuid
  AND id = $3::uuid
  AND source_issue_id = $4::uuid
  AND state = 'pending'
  AND issue_id IS NULL
  AND origin_task_id = $5::uuid
RETURNING id::text
"#;

fn validate_quick_create_retry_context(
    workspace_id: &str,
    member_id: &str,
    expected_context_id: &str,
    expected_source_issue_id: &str,
    context: &Value,
) -> Result<(), IssueCreateError> {
    let obj = context.as_object().ok_or(IssueCreateError::InvalidInput)?;
    if obj.get("type").and_then(Value::as_str) != Some("quick_create") {
        return Err(IssueCreateError::InvalidInput);
    }
    if obj.get("workspace_id").and_then(Value::as_str) != Some(workspace_id) {
        return Err(IssueCreateError::InvalidInput);
    }
    if obj.get("requester_id").and_then(Value::as_str) != Some(member_id) {
        return Err(IssueCreateError::InvalidInput);
    }
    if obj.get("source_context_id").and_then(Value::as_str) != Some(expected_context_id) {
        return Err(IssueCreateError::SourceContextRetryUnavailable);
    }
    if obj.get("parent_issue_id").and_then(Value::as_str) != Some(expected_source_issue_id) {
        return Err(IssueCreateError::SourceContextRetryUnavailable);
    }
    match obj.get("project_id") {
        None | Some(Value::Null) => {}
        Some(Value::String(value)) if value.is_empty() => {}
        _ => return Err(IssueCreateError::SourceContextRetryUnavailable),
    }
    if obj
        .get("squad_id")
        .and_then(Value::as_str)
        .is_some_and(|value| !value.is_empty())
    {
        return Err(IssueCreateError::SourceContextRetryUnavailable);
    }
    match obj.get("attachment_ids") {
        None | Some(Value::Null) => {}
        Some(Value::Array(items)) if items.is_empty() => {}
        _ => return Err(IssueCreateError::SourceContextRetryUnavailable),
    }
    Ok(())
}

async fn lock_failed_parent_task(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    request: &AgentSourceRetryWrite,
    member_id: &str,
) -> Result<(), IssueCreateError> {
    let context: Option<Value> = sqlx::query_scalar(
        "SELECT p.context
           FROM agent_task_queue p
           JOIN agent a ON a.id = p.agent_id AND a.workspace_id = $2::uuid
          WHERE p.id = $1::uuid
            AND p.status = 'failed'
            AND p.issue_id IS NULL
            AND p.chat_session_id IS NULL
            AND p.autopilot_run_id IS NULL
            AND p.squad_id IS NULL
            AND p.agent_id = $3::uuid
            AND p.runtime_id = $4::uuid
            AND p.context IS NOT NULL
          FOR UPDATE OF p",
    )
    .bind(&request.source_task_id)
    .bind(workspace_id)
    .bind(&request.expected_agent_id)
    .bind(&request.expected_runtime_id)
    .fetch_optional(&mut **tx)
    .await?;
    let Some(context) = context else {
        return Err(IssueCreateError::SourceContextRetryUnavailable);
    };
    if context != request.expected_task_context {
        return Err(IssueCreateError::SourceContextRetryUnavailable);
    }
    validate_quick_create_retry_context(
        workspace_id,
        member_id,
        &request.expected_context_id,
        &request.expected_source_issue_id,
        &context,
    )?;
    Ok(())
}

async fn lock_retry_source_issue(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    member_id: &str,
    request: &AgentSourceRetryWrite,
) -> Result<Option<String>, IssueCreateError> {
    let expected_project = request.expected_source_project_id.as_deref();
    if let Some(project_id) = expected_project {
        lock_source_inherited_project(
            tx,
            workspace_id,
            member_id,
            project_id,
            IssueCreateError::SourceContextRetryUnavailable,
        )
        .await?;
    }
    let unavailable = IssueCreateError::SourceContextRetryUnavailable;
    let locked_project = sqlx::query_scalar::<_, Option<String>>(
        "SELECT project_id::text FROM issue
          WHERE workspace_id = $1::uuid AND id = $2::uuid
          FOR SHARE",
    )
    .bind(workspace_id)
    .bind(&request.expected_source_issue_id)
    .fetch_optional(&mut **tx)
    .await?;
    let Some(locked_project) = locked_project else {
        return Err(unavailable);
    };
    if locked_project.as_deref() != expected_project {
        return Err(unavailable);
    }
    Ok(locked_project)
}

async fn lock_pending_source_context(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    request: &AgentSourceRetryWrite,
    member_id: &str,
) -> Result<(), IssueCreateError> {
    let row = sqlx::query(
        "SELECT snapshot
           FROM issue_source_context
          WHERE workspace_id = $1::uuid
            AND id = $2::uuid
            AND source_issue_id = $3::uuid
            AND state = 'pending'
            AND issue_id IS NULL
            AND origin_task_id = $4::uuid
            AND captured_by_user_id = $5::uuid
            AND capture_digest = $6
          FOR UPDATE",
    )
    .bind(workspace_id)
    .bind(&request.expected_context_id)
    .bind(&request.expected_source_issue_id)
    .bind(&request.source_task_id)
    .bind(member_id)
    .bind(&request.expected_capture_digest)
    .fetch_optional(&mut **tx)
    .await?;
    let Some(row) = row else {
        return Err(IssueCreateError::SourceContextRetryUnavailable);
    };
    let snapshot: serde_json::Value = row.try_get(0).map_err(IssueCreateError::Database)?;
    validate_locked_pending_source_clones(
        tx,
        workspace_id,
        &request.expected_context_id,
        member_id,
        &snapshot,
        &request.expected_clones,
        IssueCreateError::SourceContextRetryUnavailable,
    )
    .await
}

async fn insert_manual_retry_task(
    tx: &mut Transaction<'_, Postgres>,
    request: &AgentSourceRetryWrite,
) -> Result<String, IssueCreateError> {
    let task_id: Option<String> = sqlx::query_scalar(CREATE_MANUAL_RETRY_TASK)
        .bind(&request.actor_user_id)
        .bind(request.runtime_mcp_overlay.as_ref())
        .bind(request.runtime_connected_apps.as_ref())
        .bind(&request.new_task_id)
        .bind(&request.source_task_id)
        .bind(&request.expected_agent_id)
        .bind(&request.expected_runtime_id)
        .fetch_optional(&mut **tx)
        .await?;
    task_id.ok_or(IssueCreateError::SourceContextRetryUnavailable)
}

async fn transfer_pending_source(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    request: &AgentSourceRetryWrite,
    new_task_id: &str,
) -> Result<(), IssueCreateError> {
    let updated: Option<String> = sqlx::query_scalar(TRANSFER_PENDING_SOURCE_CONTEXT)
        .bind(new_task_id)
        .bind(workspace_id)
        .bind(&request.expected_context_id)
        .bind(&request.expected_source_issue_id)
        .bind(&request.source_task_id)
        .fetch_optional(&mut **tx)
        .await?;
    if updated.as_deref() != Some(request.expected_context_id.as_str()) {
        return Err(IssueCreateError::SourceContextRetryUnavailable);
    }
    Ok(())
}

pub async fn retry_agent_source_quick_create(
    pool: &PgPool,
    workspace_id: &str,
    member_id: &str,
    request: AgentSourceRetryWrite,
) -> Result<AgentSourceRetryResult, IssueCreateError> {
    if !is_uuid(workspace_id)
        || !is_uuid(member_id)
        || !is_uuid(&request.source_task_id)
        || !is_uuid(&request.new_task_id)
        || !is_uuid(&request.expected_context_id)
        || !is_uuid(&request.expected_source_issue_id)
        || !is_uuid(&request.expected_agent_id)
        || !is_uuid(&request.expected_runtime_id)
        || request.actor_user_id != member_id
    {
        return Err(IssueCreateError::InvalidInput);
    }

    let retry_binding = AgentRuntimeShareBindingErrors {
        binding_mismatch: IssueCreateError::SourceContextRetryUnavailable,
        capability_missing: IssueCreateError::SourceContextRetryUnavailable,
        version_mismatch: IssueCreateError::SourceContextRetryUnavailable,
    };

    let mut tx = pool.begin().await?;
    lock_workspace_member_agent_runtime_share(
        &mut tx,
        workspace_id,
        member_id,
        &request.expected_agent_id,
        &request.expected_runtime_id,
        &request.runtime_cli_version,
        retry_binding,
    )
    .await
    .map_err(|error| match error {
        IssueCreateError::InvalidInput => IssueCreateError::SourceContextRetryUnavailable,
        other => other,
    })?;
    lock_failed_parent_task(&mut tx, workspace_id, &request, member_id).await?;
    let locked_source_project =
        lock_retry_source_issue(&mut tx, workspace_id, member_id, &request).await?;
    lock_pending_source_context(&mut tx, workspace_id, &request, member_id).await?;

    let new_task_id = insert_manual_retry_task(&mut tx, &request).await?;
    if new_task_id != request.new_task_id {
        return Err(IssueCreateError::InvalidInput);
    }
    transfer_pending_source(&mut tx, workspace_id, &request, &new_task_id).await?;
    insert_queue_notify_outbox(&mut tx, &new_task_id).await?;

    tx.commit().await?;
    Ok(AgentSourceRetryResult {
        task_id: new_task_id,
        source_context_id: request.expected_context_id,
        source_project_id: locked_source_project,
    })
}
