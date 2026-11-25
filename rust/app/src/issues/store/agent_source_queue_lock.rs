//! Shared workspace/agent/runtime SHARE locks for agent source quick-create paths.

use serde_json::Value;
use sqlx::{Postgres, Transaction};

use super::create_write::IssueCreateError;

pub(crate) const SOURCE_CONTEXT_QUICK_CREATE_CAPABILITY: &str = "source_context_quick_create_v1";

pub(crate) fn runtime_metadata_has_capability(metadata: &Value, capability: &str) -> bool {
    metadata
        .get("capabilities")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .any(|item| item == capability)
}

pub(crate) struct AgentRuntimeShareBindingErrors {
    pub binding_mismatch: IssueCreateError,
    pub capability_missing: IssueCreateError,
    pub version_mismatch: IssueCreateError,
}

pub(crate) async fn lock_workspace_member_agent_runtime_share(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    member_id: &str,
    agent_id: &str,
    runtime_id: &str,
    runtime_cli_version: &str,
    errors: AgentRuntimeShareBindingErrors,
) -> Result<(), IssueCreateError> {
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut **tx)
        .await?;
    let member = sqlx::query_scalar::<_, i32>(
        "SELECT 1 FROM member WHERE workspace_id = $1::uuid AND user_id = $2::uuid FOR SHARE",
    )
    .bind(workspace_id)
    .bind(member_id)
    .fetch_optional(&mut **tx)
    .await?;
    if member.is_none() {
        return Err(IssueCreateError::InvalidInput);
    }
    let agent = sqlx::query_scalar::<_, i32>(
        "SELECT 1
           FROM agent
          WHERE id = $1::uuid AND workspace_id = $2::uuid AND archived_at IS NULL
            AND owner_id = $3::uuid AND runtime_id = $4::uuid AND runtime_mode = 'local'
          FOR SHARE",
    )
    .bind(agent_id)
    .bind(workspace_id)
    .bind(member_id)
    .bind(runtime_id)
    .fetch_optional(&mut **tx)
    .await?;
    if agent.is_none() {
        return Err(errors.binding_mismatch);
    }
    let metadata: Option<Value> = sqlx::query_scalar(
        "SELECT COALESCE(metadata, '{}'::jsonb) FROM agent_runtime
          WHERE id = $1::uuid AND workspace_id = $2::uuid
            AND status = 'online' AND runtime_mode = 'local' AND owner_id = $3::uuid
          FOR SHARE",
    )
    .bind(runtime_id)
    .bind(workspace_id)
    .bind(member_id)
    .fetch_optional(&mut **tx)
    .await?;
    let metadata = metadata.ok_or(errors.binding_mismatch)?;
    if !runtime_metadata_has_capability(&metadata, SOURCE_CONTEXT_QUICK_CREATE_CAPABILITY) {
        return Err(errors.capability_missing);
    }
    if metadata
        .get("cli_version")
        .and_then(Value::as_str)
        .unwrap_or("")
        != runtime_cli_version
    {
        return Err(errors.version_mismatch);
    }
    Ok(())
}

pub(crate) async fn insert_queue_notify_outbox(
    tx: &mut Transaction<'_, Postgres>,
    task_id: &str,
) -> Result<(), IssueCreateError> {
    sqlx::query(
        "INSERT INTO agent_task_queue_notify_outbox (task_id)
         VALUES ($1::uuid)
         ON CONFLICT (task_id) DO NOTHING",
    )
    .bind(task_id)
    .execute(&mut **tx)
    .await?;
    Ok(())
}
