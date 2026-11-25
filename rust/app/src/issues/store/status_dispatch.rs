//! Durable backlog→active promotion enqueue committed with the status pilot.

use serde::Deserialize;
use serde_json::Value;
use sqlx::PgConnection;

use super::{hidden_projects_for_principals, is_uuid};

const ISSUE_REVIEW_HEAD_SHA: &str = r#"
SELECT head_sha::text FROM (
    SELECT pr.head_sha AS head_sha, pr.state AS state, pr.pr_updated_at AS pr_updated_at
    FROM github_pull_request pr
    JOIN issue_pull_request ipr ON ipr.pull_request_id = pr.id
    WHERE ipr.issue_id = $1::uuid AND pr.head_sha <> ''
    UNION ALL
    SELECT pr.head_sha AS head_sha, pr.state AS state, pr.pr_updated_at AS pr_updated_at
    FROM vcs_pull_request pr
    JOIN issue_vcs_pull_request ipr ON ipr.pull_request_id = pr.id
    WHERE ipr.issue_id = $1::uuid AND pr.head_sha <> ''
) combined
ORDER BY (state IN ('open', 'draft')) DESC, pr_updated_at DESC
LIMIT 1
"#;

const CREATE_PREPARED_AGENT_TASK: &str = r#"
INSERT INTO agent_task_queue (
    id, agent_id, runtime_id, issue_id, status, priority, trigger_comment_id,
    coalesced_comment_ids, trigger_summary, force_fresh_session, is_leader_task, handoff_note,
    squad_id, context, originator_user_id, accountable_user_id, runtime_mcp_overlay, runtime_connected_apps,
    originator_source, delegated_from_task_id, rule_version_id, rerun_of_task_id,
    trigger_evidence_kind, trigger_evidence_ref_id, workflow_step, comment_thread_id
)
SELECT
    $1::uuid, $2::uuid, $3::uuid, $4::uuid, 'queued', $5,
    NULL::uuid, '{}'::uuid[], NULL::text, FALSE, FALSE,
    NULLIF($6::text, ''),
    NULL::uuid,
    NULLIF(CASE
        WHEN COALESCE($7::text, '') <> '' THEN jsonb_build_object('head_sha', $7::text)
        ELSE '{}'::jsonb
    END, '{}'::jsonb),
    NULLIF($8::text, '')::uuid, NULLIF($9::text, '')::uuid,
    $10::jsonb,
    $11::jsonb,
    NULLIF($12::text, ''),
    NULLIF($13::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    NULLIF($14::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    NULLIF($15::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    NULLIF($16::text, ''),
    NULLIF($17::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    $18::text,
    NULL::uuid
WHERE lock_task_owner_rows($2::uuid, $4::uuid, $3::uuid)
ON CONFLICT (issue_id, agent_id, (COALESCE(comment_thread_id, '00000000-0000-0000-0000-000000000000'::uuid)))
WHERE (
    status IN ('queued', 'dispatched')
    OR (status = 'deferred' AND (context->>'channel_issue_media_pending') = 'true')
)
DO NOTHING
RETURNING id::text
"#;

const FIND_COALESCED_PENDING: &str = r#"
SELECT id::text FROM agent_task_queue
WHERE context->>'wakeup_id' IS NULL
  AND issue_id = $1::uuid
  AND agent_id = $2::uuid
  AND (
    status IN ('queued', 'dispatched')
    OR (status = 'deferred' AND context->>'channel_issue_media_pending' = 'true')
  )
  AND (
    COALESCE($3::text, '') = ''
    OR context->>'head_sha' = $3::text
  )
ORDER BY created_at DESC, id DESC
LIMIT 1
"#;

const FIND_OCCUPIED_QUEUE_SLOT: &str = r#"
SELECT id::text FROM agent_task_queue
WHERE issue_id = $1::uuid AND agent_id = $2::uuid
  AND COALESCE(comment_thread_id, '00000000-0000-0000-0000-000000000000'::uuid)
      = '00000000-0000-0000-0000-000000000000'::uuid
  AND (
    status IN ('queued', 'dispatched')
    OR (status = 'deferred' AND context->>'channel_issue_media_pending' = 'true')
  )
ORDER BY created_at DESC, id DESC
LIMIT 1
"#;

#[derive(Debug, Deserialize)]
pub struct PreparedStatusEnqueue {
    pub task_id: String,
    pub agent_id: String,
    pub runtime_id: String,
    pub issue_id: String,
    pub priority: i32,
    pub workflow_step: String,
    #[serde(default)]
    pub head_sha: String,
    pub originator_user_id: String,
    pub accountable_user_id: String,
    pub originator_source: String,
    #[serde(default)]
    pub delegated_from_task_id: Option<String>,
    #[serde(default)]
    pub rule_version_id: Option<String>,
    #[serde(default)]
    pub trigger_evidence_kind: Option<String>,
    #[serde(default)]
    pub trigger_evidence_ref_id: Option<String>,
    #[serde(default)]
    pub handoff_note: Option<String>,
    #[serde(default)]
    pub runtime_mcp_overlay: Option<Value>,
    #[serde(default)]
    pub runtime_connected_apps: Option<Value>,
    #[serde(default)]
    pub queue_key_head_sha: String,
}

#[derive(Debug)]
pub enum StatusPromotionError {
    RouteChanged,
    Database(sqlx::Error),
}

#[derive(Debug, Clone)]
pub enum PromotionEnqueueOutcome {
    Inserted(String),
    Coalesced(String),
}

fn zero_uuid() -> &'static str {
    "00000000-0000-0000-0000-000000000000"
}

fn opt_uuid(value: Option<&str>) -> &str {
    value.filter(|s| !s.is_empty()).unwrap_or(zero_uuid())
}

async fn issue_review_head_sha(
    conn: &mut PgConnection,
    issue_id: &str,
) -> Result<String, sqlx::Error> {
    Ok(
        sqlx::query_scalar::<_, Option<String>>(ISSUE_REVIEW_HEAD_SHA)
            .bind(issue_id)
            .fetch_optional(&mut *conn)
            .await?
            .flatten()
            .unwrap_or_default(),
    )
}

pub async fn lock_prepared_agent(
    conn: &mut PgConnection,
    workspace_id: &str,
    prepared: &PreparedStatusEnqueue,
) -> Result<Option<String>, StatusPromotionError> {
    sqlx::query_scalar::<_, Option<String>>(
        "SELECT a.owner_id::text FROM agent a
         WHERE a.id = $1::uuid AND a.workspace_id = $2::uuid
           AND a.archived_at IS NULL AND a.runtime_id = $3::uuid
           AND EXISTS (SELECT 1 FROM agent_runtime r
                       WHERE r.id = a.runtime_id AND r.workspace_id = a.workspace_id)
         FOR SHARE",
    )
    .bind(&prepared.agent_id)
    .bind(workspace_id)
    .bind(&prepared.runtime_id)
    .fetch_optional(conn)
    .await
    .map_err(StatusPromotionError::Database)?
    .ok_or(StatusPromotionError::RouteChanged)
}

pub async fn validate_prepared_status_enqueue(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    project_id: Option<&str>,
    agent_owner: Option<String>,
    prepared: &PreparedStatusEnqueue,
) -> Result<(), StatusPromotionError> {
    let head_sha = issue_review_head_sha(conn, issue_id)
        .await
        .map_err(StatusPromotionError::Database)?;
    if head_sha != prepared.queue_key_head_sha || head_sha != prepared.head_sha {
        return Err(StatusPromotionError::RouteChanged);
    }
    let Some(project_id) = project_id.filter(|id| is_uuid(id)) else {
        return Ok(());
    };
    let visibility: Option<String> = sqlx::query_scalar(
        "SELECT visibility::text FROM project WHERE id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(project_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await
    .map_err(StatusPromotionError::Database)?;
    let Some(visibility) = visibility else {
        return Ok(());
    };
    if visibility != "private" {
        return Ok(());
    }
    let human = if is_uuid(&prepared.originator_user_id) {
        &prepared.originator_user_id
    } else {
        &prepared.accountable_user_id
    };
    let principals: Vec<_> = std::iter::once(human.clone()).chain(agent_owner).collect();
    let hidden = hidden_projects_for_principals(conn, workspace_id, &principals)
        .await
        .map_err(|_| StatusPromotionError::RouteChanged)?;
    if !hidden.can_see(Some(project_id)) {
        return Err(StatusPromotionError::RouteChanged);
    }
    Ok(())
}

pub async fn apply_prepared_status_enqueue(
    conn: &mut PgConnection,
    issue_id: &str,
    head_key: &str,
    prepared: &PreparedStatusEnqueue,
) -> Result<PromotionEnqueueOutcome, StatusPromotionError> {
    let already_pending: Option<String> = sqlx::query_scalar(FIND_COALESCED_PENDING)
        .bind(issue_id)
        .bind(&prepared.agent_id)
        .bind(head_key)
        .fetch_optional(&mut *conn)
        .await
        .map_err(StatusPromotionError::Database)?;
    if let Some(id) = already_pending {
        return Ok(PromotionEnqueueOutcome::Coalesced(id));
    }

    let owners_present: bool =
        sqlx::query_scalar("SELECT lock_task_owner_rows($1::uuid, $2::uuid, $3::uuid)")
            .bind(&prepared.agent_id)
            .bind(issue_id)
            .bind(&prepared.runtime_id)
            .fetch_one(&mut *conn)
            .await
            .map_err(StatusPromotionError::Database)?;
    if !owners_present {
        return Err(StatusPromotionError::RouteChanged);
    }

    let inserted: Option<String> = sqlx::query_scalar(CREATE_PREPARED_AGENT_TASK)
        .bind(&prepared.task_id)
        .bind(&prepared.agent_id)
        .bind(&prepared.runtime_id)
        .bind(&prepared.issue_id)
        .bind(prepared.priority)
        .bind(prepared.handoff_note.as_deref().unwrap_or(""))
        .bind(head_key)
        .bind(&prepared.originator_user_id)
        .bind(&prepared.accountable_user_id)
        .bind(prepared.runtime_mcp_overlay.as_ref())
        .bind(prepared.runtime_connected_apps.as_ref())
        .bind(&prepared.originator_source)
        .bind(opt_uuid(prepared.delegated_from_task_id.as_deref()))
        .bind(opt_uuid(prepared.rule_version_id.as_deref()))
        .bind(zero_uuid())
        .bind(prepared.trigger_evidence_kind.as_deref().unwrap_or(""))
        .bind(opt_uuid(prepared.trigger_evidence_ref_id.as_deref()))
        .bind(&prepared.workflow_step)
        .fetch_optional(&mut *conn)
        .await
        .map_err(StatusPromotionError::Database)?;

    if let Some(id) = inserted {
        return Ok(PromotionEnqueueOutcome::Inserted(id));
    }

    // The database's pending slot can remain occupied by another review head.
    // The Go status path retains that run when the unique index rejects an insert.
    let occupied: Option<String> = sqlx::query_scalar(FIND_OCCUPIED_QUEUE_SLOT)
        .bind(issue_id)
        .bind(&prepared.agent_id)
        .fetch_optional(&mut *conn)
        .await
        .map_err(StatusPromotionError::Database)?;
    if let Some(id) = occupied {
        return Ok(PromotionEnqueueOutcome::Coalesced(id));
    }
    Err(StatusPromotionError::RouteChanged)
}
