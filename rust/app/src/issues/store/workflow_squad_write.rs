//! Project workflow squad handoff: status, assignee, activity, and deferred
//! leader run in one transaction.

use std::collections::HashMap;

use serde::Deserialize;
use serde_json::{Value, json, value::RawValue};
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError, is_uuid,
    ordinary::{self, ISSUE_ROW_SNAPSHOT},
    resolve_caller_access_on,
    status_dispatch::{self, PreparedStatusEnqueue, StatusPromotionError},
    status_write::is_active_builtin_status,
};

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WorkflowHandoffPlan {
    pub project_id: String,
    pub workflow_id: String,
    pub workflow_name: String,
    pub target_status: String,
    pub squad_id: String,
    #[serde(default)]
    pub instructions: String,
    #[serde(default)]
    pub next_status_key: String,
    #[serde(default)]
    pub back_status_key: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WorkflowSquadBatchTrust {
    pub leader_id: String,
    pub runtime_id: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueWorkflowSquadEdit {
    pub expected_revision: i64,
    pub status: String,
    pub assignee_type: String,
    pub assignee_id: String,
    pub position: Option<f64>,
    pub source_task_id: Option<String>,
    #[serde(default)]
    pub suppress_run: bool,
    pub activity_id: String,
    pub workflow_plan: WorkflowHandoffPlan,
    #[serde(default)]
    pub activity_handoff: HashMap<String, String>,
    pub prepared_workflow_enqueue: Option<PreparedWorkflowHandoffEnqueue>,
    pub workflow_squad_batch: Option<WorkflowSquadBatchTrust>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PreparedWorkflowHandoffEnqueue {
    #[serde(flatten)]
    pub task: PreparedStatusEnqueue,
    pub workflow_handoff_revision: i64,
}

#[derive(Debug, Serialize)]
pub struct IssueWorkflowSquadWrite {
    pub before: Box<RawValue>,
    pub after: Box<RawValue>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status_activity_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub deferred_task_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub coalesced_deferred_task_id: Option<String>,
}

use serde::Serialize;

#[derive(Debug)]
pub enum IssueWorkflowSquadEditError {
    Access(IssueReadError),
    InvalidInput,
    DispatchSensitive,
    RevisionConflict(i64),
    Database(sqlx::Error),
    CommitFailed(sqlx::Error),
    Snapshot(serde_json::Error),
}

impl From<sqlx::Error> for IssueWorkflowSquadEditError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssueWorkflowSquadEditError {
    fn from(error: serde_json::Error) -> Self {
        Self::Snapshot(error)
    }
}

impl From<StatusPromotionError> for IssueWorkflowSquadEditError {
    fn from(error: StatusPromotionError) -> Self {
        match error {
            StatusPromotionError::RouteChanged => Self::DispatchSensitive,
            StatusPromotionError::Database(error) => Self::Database(error),
        }
    }
}

fn actor_refs(actor: &CallerActor) -> (&'static str, &str) {
    match actor {
        CallerActor::Member { user_id } => ("member", user_id.as_str()),
        CallerActor::Agent { agent_id, .. } => ("agent", agent_id.as_str()),
    }
}

fn step_opt_string<'a>(step: &'a Value, key: &str) -> &'a str {
    step.get(key).and_then(Value::as_str).unwrap_or("").trim()
}

async fn validate_workflow_handoff_plan(
    conn: &mut PgConnection,
    workspace_id: &str,
    plan: &WorkflowHandoffPlan,
    require_named: bool,
) -> Result<(), StatusPromotionError> {
    if !is_uuid(&plan.project_id) || !is_uuid(&plan.squad_id) {
        return Err(StatusPromotionError::RouteChanged);
    }
    if !plan.workflow_id.is_empty() && !is_uuid(&plan.workflow_id) {
        return Err(StatusPromotionError::RouteChanged);
    }
    if plan.target_status != plan.target_status.trim() || plan.workflow_name.is_empty() {
        return Err(StatusPromotionError::RouteChanged);
    }
    let row: Option<(String, String, Value, bool)> = sqlx::query_as(
        "SELECT
            CASE
                WHEN named.id IS NOT NULL THEN named.id::text
                WHEN default_wf.id IS NOT NULL THEN default_wf.id::text
                ELSE ''
            END,
            CASE
                WHEN named.id IS NOT NULL THEN named.name
                WHEN default_wf.id IS NOT NULL THEN default_wf.name
                ELSE 'Default'
            END,
            CASE
                WHEN named.id IS NOT NULL THEN named.steps
                WHEN default_wf.id IS NOT NULL THEN default_wf.steps
                ELSE '[]'::jsonb
            END,
            COALESCE(NOT named.is_default, false)
         FROM project p
         LEFT JOIN issue_workflow named
           ON named.id = p.workflow_id AND named.workspace_id = p.workspace_id
         LEFT JOIN issue_workflow default_wf
           ON default_wf.workspace_id = p.workspace_id AND default_wf.is_default
         WHERE p.id = $1::uuid AND p.workspace_id = $2::uuid
         FOR SHARE OF p",
    )
    .bind(&plan.project_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await
    .map_err(StatusPromotionError::Database)?;
    let Some((workflow_id, workflow_name, steps, is_named)) = row else {
        return Err(StatusPromotionError::RouteChanged);
    };
    if workflow_name != plan.workflow_name
        || workflow_id != plan.workflow_id
        || (require_named && !is_named)
    {
        return Err(StatusPromotionError::RouteChanged);
    }
    let step = steps
        .as_array()
        .and_then(|all_steps| {
            all_steps
                .iter()
                .find(|step| step_opt_string(step, "status_key") == plan.target_status)
        })
        .ok_or(StatusPromotionError::RouteChanged)?;
    let handler = step
        .get("handler")
        .ok_or(StatusPromotionError::RouteChanged)?;
    if step_opt_string(handler, "type") != "squad"
        || step_opt_string(handler, "id") != plan.squad_id
        || step_opt_string(step, "instructions") != plan.instructions.trim()
        || step_opt_string(step, "next_status_key") != plan.next_status_key.trim()
        || step_opt_string(step, "back_status_key") != plan.back_status_key.trim()
    {
        return Err(StatusPromotionError::RouteChanged);
    }
    Ok(())
}

const LOCK_WORKFLOW_SQUAD_HANDOFF: &str = "SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text || ':workflow_squad_handoff', 0))";

const FIND_WORKFLOW_HANDOFF_TASK: &str = r#"
SELECT id::text FROM agent_task_queue
WHERE issue_id = $1::uuid
  AND agent_id = $2::uuid
  AND squad_id = $3::uuid
  AND is_leader_task
  AND context->>'workflow_handoff_revision' = $4::bigint::text
ORDER BY created_at DESC, id DESC
LIMIT 1
"#;

const INSERT_DEFERRED_WORKFLOW_HANDOFF_TASK: &str = r#"
INSERT INTO agent_task_queue (
    id, agent_id, runtime_id, issue_id, status, priority, trigger_comment_id,
    coalesced_comment_ids, trigger_summary, force_fresh_session, is_leader_task, handoff_note,
    squad_id, context, originator_user_id, accountable_user_id, runtime_mcp_overlay, runtime_connected_apps,
    originator_source, delegated_from_task_id, rule_version_id, rerun_of_task_id,
    trigger_evidence_kind, trigger_evidence_ref_id, workflow_step, fire_at, comment_thread_id
)
SELECT
    $1::uuid, $2::uuid, $3::uuid, $4::uuid, 'deferred', $5,
    NULL::uuid, '{}'::uuid[], NULL::text, FALSE, TRUE,
    NULLIF($6::text, ''),
    $7::uuid,
    NULLIF(CASE
        WHEN COALESCE($8::text, '') <> ''
        THEN jsonb_build_object('head_sha', $8::text)
        ELSE '{}'::jsonb
    END || jsonb_build_object('workflow_handoff_revision', $9::bigint), '{}'::jsonb),
    NULLIF($10::text, '')::uuid, NULLIF($11::text, '')::uuid,
    $12::jsonb,
    $13::jsonb,
    NULLIF($14::text, ''),
    NULLIF($15::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    NULLIF($16::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    NULLIF($17::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    NULLIF($18::text, ''),
    NULLIF($19::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    $20::text,
    now(),
    NULL::uuid
WHERE lock_task_owner_rows($2::uuid, $4::uuid, $3::uuid)
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_queue
    WHERE issue_id = $4::uuid
      AND agent_id = $2::uuid
      AND squad_id = $7::uuid
      AND is_leader_task
      AND context->>'workflow_handoff_revision' = $9::bigint::text
  )
RETURNING id::text
"#;

fn opt_uuid(value: Option<&str>) -> &str {
    value
        .filter(|s| !s.is_empty())
        .unwrap_or("00000000-0000-0000-0000-000000000000")
}

#[derive(Debug, Clone)]
enum DeferredHandoffOutcome {
    Inserted(String),
    Coalesced(String),
}

async fn apply_prepared_workflow_handoff_enqueue(
    conn: &mut PgConnection,
    issue_id: &str,
    prep: &PreparedWorkflowHandoffEnqueue,
) -> Result<DeferredHandoffOutcome, StatusPromotionError> {
    if let Some(existing) = sqlx::query_scalar::<_, String>(FIND_WORKFLOW_HANDOFF_TASK)
        .bind(issue_id)
        .bind(&prep.task.agent_id)
        .bind(&prep.task.squad_id)
        .bind(prep.workflow_handoff_revision)
        .fetch_optional(&mut *conn)
        .await
        .map_err(StatusPromotionError::Database)?
    {
        return Ok(DeferredHandoffOutcome::Coalesced(existing));
    }

    let plan = &prep.task;
    let inserted: Option<String> = sqlx::query_scalar(INSERT_DEFERRED_WORKFLOW_HANDOFF_TASK)
        .bind(&plan.task_id)
        .bind(&plan.agent_id)
        .bind(&plan.runtime_id)
        .bind(issue_id)
        .bind(plan.priority)
        .bind(plan.handoff_note.as_deref().unwrap_or(""))
        .bind(&plan.squad_id)
        .bind(&plan.head_sha)
        .bind(prep.workflow_handoff_revision)
        .bind(&plan.originator_user_id)
        .bind(&plan.accountable_user_id)
        .bind(plan.runtime_mcp_overlay.as_ref())
        .bind(plan.runtime_connected_apps.as_ref())
        .bind(&plan.originator_source)
        .bind(opt_uuid(plan.delegated_from_task_id.as_deref()))
        .bind(opt_uuid(plan.rule_version_id.as_deref()))
        .bind("00000000-0000-0000-0000-000000000000")
        .bind(plan.trigger_evidence_kind.as_deref().unwrap_or(""))
        .bind(opt_uuid(plan.trigger_evidence_ref_id.as_deref()))
        .bind(&plan.workflow_step)
        .fetch_optional(&mut *conn)
        .await
        .map_err(StatusPromotionError::Database)?;

    if let Some(id) = inserted {
        return Ok(DeferredHandoffOutcome::Inserted(id));
    }

    if let Some(existing) = sqlx::query_scalar::<_, String>(FIND_WORKFLOW_HANDOFF_TASK)
        .bind(issue_id)
        .bind(&prep.task.agent_id)
        .bind(&prep.task.squad_id)
        .bind(prep.workflow_handoff_revision)
        .fetch_optional(&mut *conn)
        .await
        .map_err(StatusPromotionError::Database)?
    {
        return Ok(DeferredHandoffOutcome::Coalesced(existing));
    }
    Err(StatusPromotionError::RouteChanged)
}

pub async fn update_issue_workflow_squad_handoff(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    edit: IssueWorkflowSquadEdit,
) -> Result<IssueWorkflowSquadWrite, IssueWorkflowSquadEditError> {
    if edit.assignee_type != "squad"
        || !is_uuid(&edit.assignee_id)
        || !is_active_builtin_status(&edit.status)
        || edit.status != edit.workflow_plan.target_status
        || edit.assignee_id != edit.workflow_plan.squad_id
        || !is_uuid(&edit.activity_id)
    {
        return Err(IssueWorkflowSquadEditError::InvalidInput);
    }
    let (actor_type, actor_id) = actor_refs(actor);
    if !is_uuid(actor_id) {
        return Err(IssueWorkflowSquadEditError::InvalidInput);
    }

    let want_enqueue = edit.prepared_workflow_enqueue.is_some();
    if edit.suppress_run == want_enqueue {
        return Err(IssueWorkflowSquadEditError::DispatchSensitive);
    }

    let batch_trust = edit.workflow_squad_batch.as_ref();
    if batch_trust.is_some() && !matches!(actor, CallerActor::Member { .. }) {
        return Err(IssueWorkflowSquadEditError::InvalidInput);
    }

    let mut tx = pool.begin().await?;
    let prep = edit.prepared_workflow_enqueue.as_ref();
    if prep.is_some() {
        // The Go deferred enqueue takes this advisory lock before pinning its
        // task owners, including the issue. Use the same order during rollout.
        sqlx::query(LOCK_WORKFLOW_SQUAD_HANDOFF)
            .bind(issue_id)
            .execute(&mut *tx)
            .await?;
    }
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;
    // Workflow and project workflow writes use the same catalog lock before
    // changing their definitions or moving a project's issues.
    sqlx::query(
        "SELECT pg_advisory_xact_lock_shared(hashtextextended($1::uuid::text || ':issue_status', 0))",
    )
    .bind(workspace_id)
    .execute(&mut *tx)
    .await?;
    validate_workflow_handoff_plan(
        &mut tx,
        workspace_id,
        &edit.workflow_plan,
        batch_trust.is_some(),
    )
    .await?;

    let locked_leader = status_dispatch::lock_active_squad_leader(
        &mut tx,
        workspace_id,
        &edit.workflow_plan.squad_id,
        prep.map(|plan| &plan.task),
    )
    .await?;
    let agent_owner = locked_leader.owner_id;

    let locked = sqlx::query(&format!(
        "SELECT i.project_id::text, i.revision, i.status,
                i.triage_state IS NOT NULL AS triage_state_present,
                i.duplicate_of_issue_id IS NOT NULL AS has_duplicate,
                i.work_method_run IS NOT NULL AS has_work_method,
                {ISSUE_ROW_SNAPSHOT} AS snapshot
         FROM issue i WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid FOR UPDATE"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueWorkflowSquadEditError::Access(
        IssueReadError::NotFound("issue not found"),
    ))?;

    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueWorkflowSquadEditError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssueWorkflowSquadEditError::Access(
            IssueReadError::NotFound("issue not found"),
        ));
    }
    let project_id = project_id.ok_or(IssueWorkflowSquadEditError::DispatchSensitive)?;
    if !is_uuid(&project_id) || project_id != edit.workflow_plan.project_id {
        return Err(IssueWorkflowSquadEditError::DispatchSensitive);
    }

    let revision: i64 = locked.try_get("revision")?;
    if edit.expected_revision != revision {
        return Err(IssueWorkflowSquadEditError::RevisionConflict(revision));
    }
    let prev_status: &str = locked.try_get("status")?;
    let triage_state_present: bool = locked.try_get("triage_state_present")?;
    let has_duplicate: bool = locked.try_get("has_duplicate")?;
    let has_work_method: bool = locked.try_get("has_work_method")?;
    if let Some(trust) = batch_trust {
        if has_work_method
            || agent_owner.as_deref() != Some(actor_id)
            || trust.leader_id != locked_leader.leader_id
            || locked_leader.runtime_id.as_deref() != Some(trust.runtime_id.as_str())
        {
            return Err(IssueWorkflowSquadEditError::DispatchSensitive);
        }
        status_dispatch::validate_runtime_for_active_enqueue(
            &mut tx,
            workspace_id,
            actor_id,
            &trust.runtime_id,
            status_dispatch::RuntimeEnqueueCaller::SquadLeader,
        )
        .await?;
    }
    if (!is_active_builtin_status(prev_status) && !matches!(prev_status, "done" | "cancelled"))
        || (prev_status == "cancelled" && has_duplicate)
        || triage_state_present
        || prev_status == edit.status
    {
        return Err(IssueWorkflowSquadEditError::DispatchSensitive);
    }
    if let Some(plan) = prep {
        let post_revision = revision + 1;
        if plan.workflow_handoff_revision != post_revision
            || plan.task.issue_id != issue_id
            || plan.task.workflow_step != edit.status
            || !plan.task.is_leader_task
            || plan.task.squad_id != edit.workflow_plan.squad_id
        {
            return Err(IssueWorkflowSquadEditError::DispatchSensitive);
        }
        status_dispatch::validate_prepared_status_enqueue(
            &mut tx,
            workspace_id,
            issue_id,
            Some(project_id.as_str()),
            agent_owner,
            &plan.task,
        )
        .await?;
    } else if let Some(owner) = agent_owner {
        status_dispatch::validate_agent_issue_project_access(
            &mut tx,
            workspace_id,
            Some(project_id.as_str()),
            vec![owner],
        )
        .await?;
    }

    ordinary::set_write_session(
        &mut tx,
        actor,
        edit.source_task_id.as_deref(),
        Some(locked_leader.leader_id.as_str()),
    )
    .await?;

    let (after_text, status_activity_id) = match apply_workflow_handoff_write(
        &mut tx,
        workspace_id,
        issue_id,
        prev_status,
        &edit,
        (actor_type, actor_id),
    )
    .await?
    {
        Some(row) => row,
        None => {
            let actual: i64 = sqlx::query_scalar(
                "SELECT revision FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid",
            )
            .bind(issue_id)
            .bind(workspace_id)
            .fetch_one(&mut *tx)
            .await?;
            return Err(IssueWorkflowSquadEditError::RevisionConflict(actual));
        }
    };
    let status_changed = prev_status != edit.status;
    if status_changed && status_activity_id.as_deref() != Some(edit.activity_id.as_str()) {
        return Err(IssueWorkflowSquadEditError::Database(
            sqlx::Error::Protocol("status activity receipt missing".into()),
        ));
    }

    let (deferred_task_id, coalesced_deferred_task_id) = match prep {
        Some(plan) => match apply_prepared_workflow_handoff_enqueue(&mut tx, issue_id, plan).await?
        {
            DeferredHandoffOutcome::Inserted(id) => (Some(id), None),
            DeferredHandoffOutcome::Coalesced(id) => (None, Some(id)),
        },
        None => (None, None),
    };

    let write = IssueWorkflowSquadWrite {
        before: serde_json::from_str(locked.try_get("snapshot")?)?,
        after: serde_json::from_str(&after_text)?,
        status_activity_id,
        deferred_task_id,
        coalesced_deferred_task_id,
    };
    tx.commit()
        .await
        .map_err(IssueWorkflowSquadEditError::CommitFailed)?;
    Ok(write)
}

async fn apply_workflow_handoff_write(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    prev_status: &str,
    edit: &IssueWorkflowSquadEdit,
    actor: (&str, &str),
) -> Result<Option<(String, Option<String>)>, sqlx::Error> {
    let mut details = json!({
        "from": prev_status,
        "to": edit.status,
    });
    if let Some(obj) = details.as_object_mut() {
        for (key, value) in &edit.activity_handoff {
            obj.insert(format!("handoff_{key}"), json!(value));
        }
    }
    let sql = format!(
        "WITH candidate AS MATERIALIZED (
            SELECT
                i.id,
                i.status,
                i.position,
                i.assignee_type,
                i.assignee_id,
                i.duplicate_of_issue_id,
                $3::text AS next_status,
                $4::text AS next_assignee_type,
                $5::uuid AS next_assignee_id,
                CASE
                    WHEN $6::double precision IS NOT NULL THEN $6::double precision
                    WHEN i.status IS DISTINCT FROM $3::text THEN (
                        SELECT COALESCE(MIN(target.position), 0) - 1
                        FROM issue AS target
                        WHERE target.workspace_id = i.workspace_id
                          AND target.status = $3::text
                    )
                    ELSE i.position
                END AS next_position,
                CASE
                    WHEN i.status = 'cancelled' AND $3::text = 'cancelled'
                        THEN i.duplicate_of_issue_id
                    ELSE NULL
                END AS next_duplicate_of_issue_id,
                (i.status IS DISTINCT FROM $3::text) AS did_status_change,
                ROW(i.assignee_type, i.assignee_id)
                    IS DISTINCT FROM ROW($4::text, $5::uuid) AS did_assignee_change
            FROM issue AS i
            WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        ), changed AS MATERIALIZED (
            SELECT candidate.*,
                (
                    did_status_change
                    OR did_assignee_change
                    OR ROW(candidate.duplicate_of_issue_id)
                        IS DISTINCT FROM ROW(candidate.next_duplicate_of_issue_id)
                ) AS did_change,
                (did_status_change OR did_assignee_change) AS did_activity
            FROM candidate
        ), updated AS (
            UPDATE issue AS i SET
                status = changed.next_status,
                position = changed.next_position,
                assignee_type = changed.next_assignee_type,
                assignee_id = changed.next_assignee_id,
                duplicate_of_issue_id = changed.next_duplicate_of_issue_id,
                revision = i.revision + changed.did_change::integer,
                last_activity_at = CASE
                    WHEN changed.did_activity
                        THEN GREATEST(COALESCE(i.last_activity_at, i.updated_at), now())
                    ELSE i.last_activity_at
                END,
                updated_at = CASE WHEN changed.did_change THEN now() ELSE i.updated_at END
            FROM changed
            WHERE i.id = changed.id
              AND i.workspace_id = $2::uuid
              AND i.revision = $7
            RETURNING {ISSUE_ROW_SNAPSHOT} AS after_snapshot
        ), activity_insert AS (
            INSERT INTO activity_log (id, workspace_id, issue_id, actor_type, actor_id, action, details)
            SELECT
                $8::uuid,
                $2::uuid,
                $1::uuid,
                $9,
                $10::uuid,
                'status_changed',
                $11::jsonb
            FROM changed CROSS JOIN updated
            WHERE changed.did_status_change
            RETURNING id::text AS activity_id
        )
        SELECT updated.after_snapshot, activity_insert.activity_id
        FROM updated LEFT JOIN activity_insert ON true"
    );
    let row = sqlx::query(&sql)
        .bind(issue_id)
        .bind(workspace_id)
        .bind(&edit.status)
        .bind(&edit.assignee_type)
        .bind(&edit.assignee_id)
        .bind(edit.position)
        .bind(edit.expected_revision)
        .bind(&edit.activity_id)
        .bind(actor.0)
        .bind(actor.1)
        .bind(details)
        .fetch_optional(conn)
        .await?;
    let Some(row) = row else {
        return Ok(None);
    };
    let after: String = row.try_get(0)?;
    let activity: Option<String> = row.try_get(1)?;
    Ok(Some((after, activity)))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn workflow_batch_wire_accepts_expected_leader_and_runtime() {
        let edit: IssueWorkflowSquadEdit = serde_json::from_str(
            r#"{
                "expected_revision": 1,
                "status": "in_progress",
                "assignee_type": "squad",
                "assignee_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
                "activity_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
                "workflow_plan": {
                    "project_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
                    "workflow_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
                    "workflow_name": "Flow",
                    "target_status": "in_progress",
                    "squad_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
                },
                "suppress_run": true,
                "workflow_squad_batch": {
                    "leader_id": "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
                    "runtime_id": "ffffffff-ffff-4fff-8fff-ffffffffffff"
                }
            }"#,
        )
        .unwrap();
        assert!(edit.workflow_squad_batch.is_some());
    }

    #[test]
    fn workflow_edit_requires_squad_assignee() {
        let edit: IssueWorkflowSquadEdit = serde_json::from_str(
            r#"{
                "expected_revision": 1,
                "status": "in_progress",
                "assignee_type": "squad",
                "assignee_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
                "activity_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
                "workflow_plan": {
                    "project_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
                    "workflow_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
                    "workflow_name": "Flow",
                    "target_status": "in_progress",
                    "squad_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
                },
                "suppress_run": true
            }"#,
        )
        .unwrap();
        assert_eq!(edit.workflow_plan.target_status, "in_progress");
        assert!(edit.prepared_workflow_enqueue.is_none());
    }
}
