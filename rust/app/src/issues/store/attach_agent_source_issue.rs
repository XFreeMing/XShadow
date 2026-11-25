//! Agent quick-create: lock pending source context and attach after shared create insert.

use serde::Deserialize;
use serde_json::Value;
use sqlx::{Postgres, Row, Transaction};

use super::agent_source_queue_lock::{
    AgentRuntimeShareBindingErrors, lock_workspace_member_agent_runtime_share,
};
use super::create_source_context::{
    SourceContextCloneTrust, validate_locked_pending_source_clones,
};
use super::create_write::IssueCreateError;
use super::{CallerActor, IssueCreateRequest, is_uuid};

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PendingAgentOriginTrust {
    pub expected_origin_task_id: String,
    pub expected_agent_id: String,
    pub expected_runtime_id: String,
    pub expected_runtime_cli_version: String,
    pub expected_context_id: String,
    pub expected_capture_digest: String,
    pub expected_source_issue_id: String,
    pub expected_requester_id: String,
    #[serde(default)]
    pub expected_clones: Vec<SourceContextCloneTrust>,
    #[serde(default)]
    pub expected_source_project_id: Option<String>,
}

pub fn validate_pending_agent_origin_request(
    actor: &CallerActor,
    request: &IssueCreateRequest,
    trust: &PendingAgentOriginTrust,
) -> Result<(), IssueCreateError> {
    let CallerActor::Agent {
        agent_id,
        task_id,
        user_id: Some(user_id),
    } = actor
    else {
        return Err(IssueCreateError::InvalidInput);
    };
    if !is_uuid(&trust.expected_origin_task_id)
        || !is_uuid(&trust.expected_agent_id)
        || !is_uuid(&trust.expected_runtime_id)
        || !is_uuid(&trust.expected_context_id)
        || !is_uuid(&trust.expected_source_issue_id)
        || !is_uuid(&trust.expected_requester_id)
        || trust.expected_capture_digest.len() != 64
        || trust.expected_runtime_cli_version.is_empty()
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if agent_id != &trust.expected_agent_id
        || task_id != &trust.expected_origin_task_id
        || user_id != &trust.expected_requester_id
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if request.creator_type != "agent" || request.creator_id != *agent_id {
        return Err(IssueCreateError::InvalidInput);
    }
    if request.status != "todo"
        || !matches!(
            request.priority.as_str(),
            "none" | "low" | "medium" | "high" | "urgent"
        )
        || request.assignee_type.is_some()
        || request.assignee_id.is_some()
        || request.project_id.is_some()
        || request.issue_type_id.is_some()
        || request.prepared_enqueue.is_some()
        || request.squad_create_trust.is_some()
        || request.source_context_create_trust.is_some()
        || !request.label_ids.is_empty()
        || !request.attachment_ids.is_empty()
        || request.pr_auto_complete_disabled
    {
        return Err(IssueCreateError::InvalidInput);
    }
    let Some(parent) = request.parent_issue_id.as_deref() else {
        return Err(IssueCreateError::InvalidInput);
    };
    if parent != trust.expected_source_issue_id.as_str() {
        return Err(IssueCreateError::InvalidInput);
    }
    Ok(())
}

fn validate_quick_create_task_context(
    workspace_id: &str,
    trust: &PendingAgentOriginTrust,
    context: &Value,
) -> Result<(), IssueCreateError> {
    let obj = context.as_object().ok_or(IssueCreateError::InvalidInput)?;
    if obj.get("type").and_then(Value::as_str) != Some("quick_create") {
        return Err(IssueCreateError::InvalidInput);
    }
    if obj.get("workspace_id").and_then(Value::as_str) != Some(workspace_id) {
        return Err(IssueCreateError::InvalidInput);
    }
    if obj.get("requester_id").and_then(Value::as_str) != Some(trust.expected_requester_id.as_str())
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if obj.get("source_context_id").and_then(Value::as_str)
        != Some(trust.expected_context_id.as_str())
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if obj.get("parent_issue_id").and_then(Value::as_str)
        != Some(trust.expected_source_issue_id.as_str())
    {
        return Err(IssueCreateError::InvalidInput);
    }
    match obj.get("project_id") {
        None | Some(Value::Null) => {}
        Some(Value::String(value)) if value.is_empty() => {}
        _ => return Err(IssueCreateError::InvalidInput),
    }
    if obj
        .get("squad_id")
        .and_then(Value::as_str)
        .is_some_and(|value| !value.is_empty())
    {
        return Err(IssueCreateError::InvalidInput);
    }
    match obj.get("attachment_ids") {
        None | Some(Value::Null) => {}
        Some(Value::Array(items)) if items.is_empty() => {}
        _ => return Err(IssueCreateError::InvalidInput),
    }
    Ok(())
}

pub async fn lock_agent_origin(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    member_id: &str,
    trust: &PendingAgentOriginTrust,
) -> Result<(), IssueCreateError> {
    if member_id != trust.expected_requester_id {
        return Err(IssueCreateError::InvalidInput);
    }
    lock_workspace_member_agent_runtime_share(
        tx,
        workspace_id,
        member_id,
        &trust.expected_agent_id,
        &trust.expected_runtime_id,
        &trust.expected_runtime_cli_version,
        AgentRuntimeShareBindingErrors {
            binding_mismatch: IssueCreateError::ConfigurationChanged,
            capability_missing: IssueCreateError::ConfigurationChanged,
            version_mismatch: IssueCreateError::ConfigurationChanged,
        },
    )
    .await
    .map_err(|error| match error {
        IssueCreateError::InvalidInput => IssueCreateError::ConfigurationChanged,
        other => other,
    })?;

    let context: Option<Value> = sqlx::query_scalar(
        "SELECT p.context
           FROM agent_task_queue p
          WHERE p.id = $1::uuid
            AND p.agent_id = $2::uuid
            AND p.runtime_id = $3::uuid
            AND p.issue_id IS NULL
            AND p.chat_session_id IS NULL
            AND p.autopilot_run_id IS NULL
            AND p.squad_id IS NULL
            AND p.status = 'running'
            AND p.originator_user_id = $4::uuid
            AND p.originator_source = 'direct_human'
            AND p.context IS NOT NULL
          FOR UPDATE OF p",
    )
    .bind(&trust.expected_origin_task_id)
    .bind(&trust.expected_agent_id)
    .bind(&trust.expected_runtime_id)
    .bind(&trust.expected_requester_id)
    .fetch_optional(&mut **tx)
    .await?;
    let context = context.ok_or(IssueCreateError::ConfigurationChanged)?;
    validate_quick_create_task_context(workspace_id, trust, &context)
        .map_err(|_| IssueCreateError::ConfigurationChanged)?;
    Ok(())
}

pub async fn lock_pending_agent_origin_context(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    trust: &PendingAgentOriginTrust,
) -> Result<(), IssueCreateError> {
    let row = sqlx::query(
        "SELECT capture_digest, snapshot
           FROM issue_source_context
          WHERE workspace_id = $1::uuid
            AND id = $2::uuid
            AND source_issue_id = $3::uuid
            AND state = 'pending'
            AND issue_id IS NULL
            AND origin_task_id = $4::uuid
            AND captured_by_user_id = $5::uuid
          FOR UPDATE",
    )
    .bind(workspace_id)
    .bind(&trust.expected_context_id)
    .bind(&trust.expected_source_issue_id)
    .bind(&trust.expected_origin_task_id)
    .bind(&trust.expected_requester_id)
    .fetch_optional(&mut **tx)
    .await?;
    let Some(row) = row else {
        return Err(IssueCreateError::SourceContextAlreadyAttached);
    };
    let digest: String = row.try_get(0).map_err(IssueCreateError::Database)?;
    let snapshot: Value = row.try_get(1).map_err(IssueCreateError::Database)?;
    if digest != trust.expected_capture_digest {
        return Err(IssueCreateError::SourceContextChanged);
    }
    validate_locked_pending_source_clones(
        tx,
        workspace_id,
        &trust.expected_context_id,
        &trust.expected_requester_id,
        &snapshot,
        &trust.expected_clones,
        IssueCreateError::ConfigurationChanged,
    )
    .await
}

pub async fn attach_pending_agent_origin_context(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    issue_id: &str,
    trust: &PendingAgentOriginTrust,
) -> Result<(), IssueCreateError> {
    let updated: Option<String> = sqlx::query_scalar(
        "UPDATE issue_source_context
         SET issue_id = $1::uuid, state = 'attached', attached_at = now()
         WHERE workspace_id = $2::uuid
           AND id = $3::uuid
           AND source_issue_id = $4::uuid
           AND state = 'pending'
           AND origin_task_id = $5::uuid
           AND captured_by_user_id = $6::uuid
         RETURNING id::text",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .bind(&trust.expected_context_id)
    .bind(&trust.expected_source_issue_id)
    .bind(&trust.expected_origin_task_id)
    .bind(&trust.expected_requester_id)
    .fetch_optional(&mut **tx)
    .await?;
    if updated.as_deref() != Some(trust.expected_context_id.as_str()) {
        return Err(IssueCreateError::SourceContextAlreadyAttached);
    }
    Ok(())
}
