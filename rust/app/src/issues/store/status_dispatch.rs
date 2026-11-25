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
    NULL::uuid, '{}'::uuid[], NULL::text, FALSE, $19::boolean,
    NULLIF($6::text, ''),
    NULLIF($20::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
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

const ENQUEUE_NATIVE_QUEUE_NOTIFY: &str = r#"
INSERT INTO agent_task_queue_notify_outbox (task_id)
VALUES ($1::uuid)
ON CONFLICT (task_id) DO NOTHING
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
    #[serde(default)]
    pub is_leader_task: bool,
    #[serde(default)]
    pub squad_id: String,
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

#[derive(Debug, Clone)]
pub struct LockedSquadLeader {
    pub leader_id: String,
    pub owner_id: Option<String>,
}

/// Locks the squad before its current leader, matching both native writers.
pub async fn lock_active_squad_leader(
    conn: &mut PgConnection,
    workspace_id: &str,
    squad_id: &str,
    prepared: Option<&PreparedStatusEnqueue>,
) -> Result<LockedSquadLeader, StatusPromotionError> {
    if !is_uuid(squad_id) {
        return Err(StatusPromotionError::RouteChanged);
    }
    let (leader_id, owner_id, runtime_id, runtime_in_workspace) =
        sqlx::query_as::<_, (String, Option<String>, Option<String>, bool)>(
            "WITH locked_squad AS MATERIALIZED (
                SELECT leader_id FROM squad
                WHERE id = $1::uuid AND workspace_id = $2::uuid AND archived_at IS NULL
                FOR SHARE
            )
            SELECT a.id::text, a.owner_id::text, a.runtime_id::text,
                EXISTS (SELECT 1 FROM agent_runtime r
                        WHERE r.id = a.runtime_id AND r.workspace_id = a.workspace_id)
            FROM locked_squad s
            JOIN agent a ON a.id = s.leader_id AND a.workspace_id = $2::uuid
            WHERE a.archived_at IS NULL
            FOR SHARE OF a",
        )
        .bind(squad_id)
        .bind(workspace_id)
        .fetch_optional(conn)
        .await
        .map_err(StatusPromotionError::Database)?
        .ok_or(StatusPromotionError::RouteChanged)?;
    if prepared.is_some_and(|plan| {
        plan.agent_id != leader_id
            || runtime_id.as_deref() != Some(plan.runtime_id.as_str())
            || !runtime_in_workspace
    }) {
        return Err(StatusPromotionError::RouteChanged);
    }
    Ok(LockedSquadLeader {
        leader_id,
        owner_id,
    })
}

/// Which active create path is rechecking a bound runtime under the transaction.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RuntimeEnqueueCaller {
    /// Active squad creates mirror AgentReadiness::Ready and require an online machine.
    SquadLeader,
    /// Active direct-agent creates may queue for an offline machine; only the
    /// machine consent for the agent owner is rechecked.
    DirectAgent,
}

/// Re-reads the bound runtime under the transaction for active creates and
/// rechecks consent for the agent owner on private runtimes. The squad caller
/// also requires an online machine.
pub async fn validate_runtime_for_active_enqueue(
    conn: &mut PgConnection,
    workspace_id: &str,
    agent_owner_id: &str,
    runtime_id: &str,
    caller: RuntimeEnqueueCaller,
) -> Result<(), StatusPromotionError> {
    if !is_uuid(runtime_id) || !is_uuid(agent_owner_id) {
        return Err(StatusPromotionError::RouteChanged);
    }
    let require_online = caller == RuntimeEnqueueCaller::SquadLeader;
    let ready = sqlx::query_scalar::<_, bool>(
        "SELECT (NOT $4::bool OR status = 'online') AND (
             visibility IS DISTINCT FROM 'private'
             OR owner_id IS NULL
             OR owner_id = $3::uuid
             OR COALESCE($3::uuid = ANY(shared_with), false)
         )
         FROM agent_runtime
         WHERE id = $1::uuid AND workspace_id = $2::uuid
         FOR SHARE",
    )
    .bind(runtime_id)
    .bind(workspace_id)
    .bind(agent_owner_id)
    .bind(require_online)
    .fetch_optional(conn)
    .await
    .map_err(StatusPromotionError::Database)?;
    if ready != Some(true) {
        return Err(StatusPromotionError::RouteChanged);
    }
    Ok(())
}

pub async fn lock_target_agent_for_assignment(
    conn: &mut PgConnection,
    workspace_id: &str,
    agent_id: &str,
) -> Result<Option<String>, StatusPromotionError> {
    sqlx::query_scalar::<_, Option<String>>(
        "SELECT a.owner_id::text FROM agent a
         WHERE a.id = $1::uuid AND a.workspace_id = $2::uuid
           AND a.archived_at IS NULL
         FOR SHARE",
    )
    .bind(agent_id)
    .bind(workspace_id)
    .fetch_optional(conn)
    .await
    .map_err(StatusPromotionError::Database)?
    .ok_or(StatusPromotionError::RouteChanged)
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

pub async fn validate_agent_issue_project_access(
    conn: &mut PgConnection,
    workspace_id: &str,
    project_id: Option<&str>,
    principals: Vec<String>,
) -> Result<(), StatusPromotionError> {
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
    if visibility.as_deref() != Some("private") {
        return Ok(());
    }
    let hidden = hidden_projects_for_principals(conn, workspace_id, &principals)
        .await
        .map_err(|_| StatusPromotionError::RouteChanged)?;
    if !hidden.can_see(Some(project_id)) {
        return Err(StatusPromotionError::RouteChanged);
    }
    Ok(())
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
    if prepared.is_leader_task && !is_uuid(&prepared.squad_id) {
        return Err(StatusPromotionError::RouteChanged);
    }
    let human = if is_uuid(&prepared.originator_user_id) {
        &prepared.originator_user_id
    } else {
        &prepared.accountable_user_id
    };
    let principals = std::iter::once(human.clone()).chain(agent_owner).collect();
    validate_agent_issue_project_access(conn, workspace_id, project_id, principals).await
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
        .bind(prepared.is_leader_task)
        .bind(opt_uuid(Some(&prepared.squad_id)))
        .fetch_optional(&mut *conn)
        .await
        .map_err(StatusPromotionError::Database)?;

    if let Some(id) = inserted {
        sqlx::query(ENQUEUE_NATIVE_QUEUE_NOTIFY)
            .bind(&id)
            .execute(&mut *conn)
            .await
            .map_err(StatusPromotionError::Database)?;
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn prepared_enqueue_defaults_direct_agent_shape() {
        let plan: PreparedStatusEnqueue = serde_json::from_str(
            r#"{
                "task_id": "11111111-1111-4111-8111-111111111111",
                "agent_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
                "runtime_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
                "issue_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
                "priority": 0,
                "workflow_step": "todo",
                "originator_user_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
                "accountable_user_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
                "originator_source": "derived",
                "queue_key_head_sha": ""
            }"#,
        )
        .unwrap();
        assert!(!plan.is_leader_task);
        assert!(plan.squad_id.is_empty());
    }

    #[test]
    fn prepared_enqueue_accepts_squad_leader_fields() {
        let plan: PreparedStatusEnqueue = serde_json::from_str(
            r#"{
                "task_id": "11111111-1111-4111-8111-111111111111",
                "agent_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
                "runtime_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
                "issue_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
                "priority": 0,
                "workflow_step": "todo",
                "originator_user_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
                "accountable_user_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
                "originator_source": "derived",
                "queue_key_head_sha": "",
                "is_leader_task": true,
                "squad_id": "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
            }"#,
        )
        .unwrap();
        assert!(plan.is_leader_task);
        assert_eq!(plan.squad_id, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee");
    }
}
