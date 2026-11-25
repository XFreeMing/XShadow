//! Agent-mode comment sub-issue quick-create: queue task + pending source context.

use serde::Deserialize;
use serde_json::Value;
use sqlx::{PgPool, Postgres, Transaction};

use super::create_source_context::{
    SOURCE_CONTEXT_VERSION, SourceContextCreateTrust, reconcile_source_context_project,
    validate_agent_pending_source,
};
use super::create_write::IssueCreateError;
use super::is_uuid;

const SOURCE_CONTEXT_QUICK_CREATE_CAPABILITY: &str = "source_context_quick_create_v1";
const QUICK_CREATE_HIGH_PRIORITY: i32 = 3;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AgentSourceQuickCreateWrite {
    pub task_id: String,
    pub agent_id: String,
    pub runtime_id: String,
    pub runtime_cli_version: String,
    pub priority: i32,
    pub context: Value,
    pub originator_user_id: String,
    pub accountable_user_id: String,
    #[serde(default)]
    pub runtime_mcp_overlay: Option<Value>,
    #[serde(default)]
    pub runtime_connected_apps: Option<Value>,
    #[serde(default)]
    pub originator_source: Option<String>,
    #[serde(default)]
    pub trigger_evidence_kind: Option<String>,
    #[serde(default)]
    pub trigger_evidence_ref_id: Option<String>,
    pub source_context: SourceContextCreateTrust,
}

#[derive(Debug, serde::Serialize)]
pub struct AgentSourceQuickCreateResult {
    pub task_id: String,
    pub source_context_id: String,
}

const CREATE_QUICK_CREATE_TASK: &str = r#"
INSERT INTO agent_task_queue (
    agent_id, runtime_id, issue_id, status, priority, context, originator_user_id,
    accountable_user_id, runtime_mcp_overlay, runtime_connected_apps,
    originator_source, trigger_evidence_kind, trigger_evidence_ref_id, id
)
SELECT
    $1::uuid, $2::uuid, NULL, 'queued', $3, $4::jsonb,
    $5::uuid, $6::uuid, $7::jsonb, $8::jsonb,
    $9, $10, NULLIF($11::text, '')::uuid, $12::uuid
WHERE lock_task_owner_rows($1::uuid, NULL, $2::uuid)
RETURNING id::text
"#;

const INSERT_PENDING_SOURCE_CONTEXT: &str = r#"
INSERT INTO issue_source_context (
    id, workspace_id, issue_id, origin_task_id, source_issue_id,
    anchor_comment_id, captured_by_user_id, snapshot_version, snapshot,
    capture_digest, state, captured_at, attached_at
) SELECT
    $1::uuid, $2::uuid, NULL, $3::uuid, $4::uuid,
    $5::uuid, $6::uuid, $7, $8::jsonb, $9,
    'pending', $10::text::timestamptz, NULL
FROM workspace WHERE id = $2::uuid AND issue_prefix = $11
RETURNING id::text
"#;

const ENQUEUE_NATIVE_QUEUE_NOTIFY: &str = r#"
INSERT INTO agent_task_queue_notify_outbox (task_id)
VALUES ($1::uuid)
ON CONFLICT (task_id) DO NOTHING
"#;

fn runtime_metadata_has_capability(metadata: &Value, capability: &str) -> bool {
    metadata
        .get("capabilities")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .any(|item| item == capability)
}

fn validate_trusted_actor_ids(
    member_id: &str,
    request: &AgentSourceQuickCreateWrite,
) -> Result<(), IssueCreateError> {
    if request.originator_user_id != member_id
        || request.accountable_user_id != member_id
        || request.source_context.captured_by_user_id != member_id
        || request.originator_source.as_deref() != Some("direct_human")
        || request.trigger_evidence_kind.is_some()
        || request.trigger_evidence_ref_id.is_some()
    {
        return Err(IssueCreateError::InvalidInput);
    }
    Ok(())
}

fn validate_quick_create_context(
    workspace_id: &str,
    member_id: &str,
    trust: &SourceContextCreateTrust,
    context: &Value,
    queue_priority: i32,
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
    if obj.get("source_context_id").and_then(Value::as_str) != Some(trust.context_id.as_str()) {
        return Err(IssueCreateError::InvalidInput);
    }
    if obj.get("parent_issue_id").and_then(Value::as_str) != Some(trust.source_issue_id.as_str()) {
        return Err(IssueCreateError::InvalidInput);
    }
    match obj.get("project_id") {
        None | Some(Value::Null) => {}
        Some(Value::String(s)) if s.is_empty() => {}
        _ => return Err(IssueCreateError::InvalidInput),
    }
    if obj.get("squad_id").is_some() {
        return Err(IssueCreateError::InvalidInput);
    }
    match obj.get("attachment_ids") {
        None | Some(Value::Null) => {}
        Some(Value::Array(items)) if items.is_empty() => {}
        _ => return Err(IssueCreateError::InvalidInput),
    }
    if queue_priority != QUICK_CREATE_HIGH_PRIORITY {
        return Err(IssueCreateError::InvalidInput);
    }
    Ok(())
}

async fn lock_agent_runtime_member(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    member_id: &str,
    request: &AgentSourceQuickCreateWrite,
) -> Result<(), IssueCreateError> {
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut **tx)
        .await
        .map_err(IssueCreateError::Database)?;
    let member = sqlx::query_scalar::<_, i32>(
        "SELECT 1 FROM member WHERE workspace_id = $1::uuid AND user_id = $2::uuid FOR SHARE",
    )
    .bind(workspace_id)
    .bind(member_id)
    .fetch_optional(&mut **tx)
    .await
    .map_err(IssueCreateError::Database)?;
    if member.is_none() {
        return Err(IssueCreateError::InvalidInput);
    }
    // SHARE protects the binding while remaining compatible with the task
    // owner fence used by the legacy writer, which starts at the source row.
    let agent = sqlx::query_scalar::<_, i32>(
        "SELECT 1
           FROM agent
          WHERE id = $1::uuid AND workspace_id = $2::uuid AND archived_at IS NULL
            AND owner_id = $3::uuid AND runtime_id = $4::uuid AND runtime_mode = 'local'
          FOR SHARE",
    )
    .bind(&request.agent_id)
    .bind(workspace_id)
    .bind(member_id)
    .bind(&request.runtime_id)
    .fetch_optional(&mut **tx)
    .await?;
    if agent.is_none() {
        return Err(IssueCreateError::SourceContextQuickCreateUnavailable);
    }

    let metadata: Option<Value> = sqlx::query_scalar(
        "SELECT COALESCE(metadata, '{}'::jsonb) FROM agent_runtime
          WHERE id = $1::uuid AND workspace_id = $2::uuid
            AND status = 'online' AND runtime_mode = 'local' AND owner_id = $3::uuid
          FOR SHARE",
    )
    .bind(&request.runtime_id)
    .bind(workspace_id)
    .bind(member_id)
    .fetch_optional(&mut **tx)
    .await?;
    let metadata = metadata.ok_or(IssueCreateError::SourceContextQuickCreateUnavailable)?;
    if !runtime_metadata_has_capability(&metadata, SOURCE_CONTEXT_QUICK_CREATE_CAPABILITY) {
        return Err(IssueCreateError::SourceContextQuickCreateUnsupported);
    }
    if metadata
        .get("cli_version")
        .and_then(Value::as_str)
        .unwrap_or("")
        != request.runtime_cli_version
    {
        return Err(IssueCreateError::SourceContextQuickCreateUnavailable);
    }
    Ok(())
}

async fn insert_quick_create(
    tx: &mut Transaction<'_, Postgres>,
    request: &AgentSourceQuickCreateWrite,
) -> Result<String, IssueCreateError> {
    let task_id: Option<String> = sqlx::query_scalar(CREATE_QUICK_CREATE_TASK)
        .bind(&request.agent_id)
        .bind(&request.runtime_id)
        .bind(request.priority)
        .bind(&request.context)
        .bind(&request.originator_user_id)
        .bind(&request.accountable_user_id)
        .bind(request.runtime_mcp_overlay.as_ref())
        .bind(request.runtime_connected_apps.as_ref())
        .bind(request.originator_source.as_deref())
        .bind(request.trigger_evidence_kind.as_deref())
        .bind(request.trigger_evidence_ref_id.as_deref().unwrap_or(""))
        .bind(&request.task_id)
        .fetch_optional(&mut **tx)
        .await
        .map_err(IssueCreateError::Database)?;
    task_id.ok_or(IssueCreateError::InvalidInput)
}

async fn insert_pending_source(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    task_id: &str,
    trust: &SourceContextCreateTrust,
    expected_issue_prefix: &str,
) -> Result<String, IssueCreateError> {
    let context_id = sqlx::query_scalar(INSERT_PENDING_SOURCE_CONTEXT)
        .bind(&trust.context_id)
        .bind(workspace_id)
        .bind(task_id)
        .bind(&trust.source_issue_id)
        .bind(&trust.anchor_comment_id)
        .bind(&trust.captured_by_user_id)
        .bind(SOURCE_CONTEXT_VERSION)
        .bind(&trust.snapshot)
        .bind(&trust.capture_digest)
        .bind(&trust.captured_at)
        .bind(expected_issue_prefix)
        .fetch_optional(&mut **tx)
        .await?;
    context_id.ok_or(IssueCreateError::SourceContextChanged)
}

async fn insert_queue_notify_outbox(
    tx: &mut Transaction<'_, Postgres>,
    task_id: &str,
) -> Result<(), IssueCreateError> {
    sqlx::query(ENQUEUE_NATIVE_QUEUE_NOTIFY)
        .bind(task_id)
        .execute(&mut **tx)
        .await
        .map_err(IssueCreateError::Database)?;
    Ok(())
}

pub async fn create_agent_source_quick_create(
    pool: &PgPool,
    workspace_id: &str,
    member_id: &str,
    request: AgentSourceQuickCreateWrite,
) -> Result<AgentSourceQuickCreateResult, IssueCreateError> {
    if !is_uuid(workspace_id)
        || !is_uuid(member_id)
        || !is_uuid(&request.task_id)
        || !is_uuid(&request.agent_id)
        || !is_uuid(&request.runtime_id)
    {
        return Err(IssueCreateError::InvalidInput);
    }
    validate_trusted_actor_ids(member_id, &request)?;
    validate_quick_create_context(
        workspace_id,
        member_id,
        &request.source_context,
        &request.context,
        request.priority,
    )?;
    reconcile_source_context_project(&request.source_context, None)?;

    let mut tx = pool.begin().await.map_err(IssueCreateError::Database)?;
    lock_agent_runtime_member(&mut tx, workspace_id, member_id, &request).await?;
    let issue_prefix =
        validate_agent_pending_source(&mut tx, workspace_id, member_id, &request.source_context)
            .await?;

    let task_id = insert_quick_create(&mut tx, &request).await?;
    if task_id != request.task_id {
        return Err(IssueCreateError::InvalidInput);
    }
    let context_id = insert_pending_source(
        &mut tx,
        workspace_id,
        &task_id,
        &request.source_context,
        &issue_prefix,
    )
    .await?;
    if context_id != request.source_context.context_id {
        return Err(IssueCreateError::InvalidInput);
    }
    insert_queue_notify_outbox(&mut tx, &task_id).await?;

    tx.commit().await.map_err(IssueCreateError::Database)?;
    Ok(AgentSourceQuickCreateResult {
        task_id,
        source_context_id: context_id,
    })
}
