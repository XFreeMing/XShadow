//! Transactional status and column-rank writes for the grid pilot. Mirrors the
//! `UpdateIssue` sqlc statement for status, position, duplicate mark clearing,
//! revision, `updated_at`, and `last_activity_at` only.

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError, is_uuid,
    ordinary::{self, ISSUE_ROW_SNAPSHOT},
    resolve_caller_access_on,
    status_dispatch::{self, PreparedStatusEnqueue, PromotionEnqueueOutcome, StatusPromotionError},
    terminal_wakeup::{self, CancelledTaskReceipt},
};

const ACTIVE_BUILTINS: &[&str] = &["backlog", "todo", "in_progress", "in_review", "blocked"];
const TERMINAL_BUILTINS: &[&str] = &["done", "cancelled"];
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct OrdinaryActiveBatchTrust {
    #[serde(default)]
    pub expected_project_id: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueStatusEdit {
    pub expected_revision: i64,
    pub status: String,
    pub position: Option<f64>,
    pub source_task_id: Option<String>,
    #[serde(default)]
    pub suppress_run: bool,
    pub activity_id: String,
    pub prepared_enqueue: Option<PreparedStatusEnqueue>,
    pub terminal_scope: Option<TerminalScope>,
    #[serde(default)]
    pub ordinary_active_batch: Option<OrdinaryActiveBatchTrust>,
    #[serde(default)]
    pub ordinary_terminal_batch: bool,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TerminalScope {
    #[serde(default)]
    pub project_id: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct IssueStatusWrite {
    pub before: Box<RawValue>,
    pub after: Box<RawValue>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status_activity_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub queued_task_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub coalesced_queued_task_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cancelled_tasks: Option<Vec<CancelledTaskReceipt>>,
}

#[derive(Debug)]
pub enum IssueStatusEditError {
    Access(IssueReadError),
    InvalidStatus,
    DispatchSensitive,
    RevisionConflict(i64),
    Database(sqlx::Error),
    CommitFailed(sqlx::Error),
    Snapshot(serde_json::Error),
}

impl From<sqlx::Error> for IssueStatusEditError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssueStatusEditError {
    fn from(error: serde_json::Error) -> Self {
        Self::Snapshot(error)
    }
}

impl From<StatusPromotionError> for IssueStatusEditError {
    fn from(error: StatusPromotionError) -> Self {
        match error {
            StatusPromotionError::RouteChanged => Self::DispatchSensitive,
            StatusPromotionError::Database(error) => Self::Database(error),
        }
    }
}

pub fn is_active_builtin_status(status: &str) -> bool {
    ACTIVE_BUILTINS.contains(&status)
}

pub fn is_terminal_builtin_status(status: &str) -> bool {
    TERMINAL_BUILTINS.contains(&status)
}

fn is_grid_builtin_status(status: &str) -> bool {
    is_active_builtin_status(status) || is_terminal_builtin_status(status)
}

fn dispatch_sensitive_locked(
    prev_status: &str,
    assignee_type: Option<&str>,
    assignee_id: Option<&str>,
    target_status: &str,
    suppress_run: bool,
) -> bool {
    if suppress_run {
        return false;
    }
    if prev_status != "backlog" {
        return false;
    }
    if !is_active_builtin_status(target_status) || target_status == "backlog" {
        return false;
    }
    matches!(assignee_type, Some("agent") | Some("squad")) && assignee_id.is_some()
}

fn actor_refs(actor: &CallerActor) -> (&'static str, &str) {
    match actor {
        CallerActor::Member { user_id } => ("member", user_id.as_str()),
        CallerActor::Agent { agent_id, .. } => ("agent", agent_id.as_str()),
    }
}

pub async fn update_issue_status(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    edit: IssueStatusEdit,
) -> Result<IssueStatusWrite, IssueStatusEditError> {
    let terminal = is_terminal_builtin_status(&edit.status);
    let terminal_scope = edit.terminal_scope.as_ref();
    let prepared = edit.prepared_enqueue.as_ref();
    let batch_trust = edit.ordinary_active_batch.as_ref();
    let terminal_batch = edit.ordinary_terminal_batch;
    if batch_trust.is_some() && terminal_batch {
        return Err(IssueStatusEditError::InvalidStatus);
    }
    if !is_grid_builtin_status(&edit.status)
        || !is_uuid(&edit.activity_id)
        || terminal != terminal_scope.is_some()
        || (terminal && !matches!(actor, CallerActor::Member { .. }))
    {
        return Err(IssueStatusEditError::InvalidStatus);
    }
    if terminal && prepared.is_some() {
        return Err(IssueStatusEditError::DispatchSensitive);
    }
    if terminal_batch && (!terminal || prepared.is_some() || terminal_scope.is_none()) {
        return Err(IssueStatusEditError::InvalidStatus);
    }
    if batch_trust.is_some()
        && (!is_active_builtin_status(&edit.status)
            || !matches!(actor, CallerActor::Member { .. })
            || prepared.is_some()
            || terminal)
    {
        return Err(IssueStatusEditError::InvalidStatus);
    }
    let (actor_type, actor_id) = actor_refs(actor);
    if !is_uuid(actor_id) {
        return Err(IssueStatusEditError::InvalidStatus);
    }
    let mut tx = pool.begin().await?;
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;
    let scoped_project = batch_trust
        .and_then(|scope| scope.expected_project_id.as_deref())
        .or_else(|| terminal_scope.and_then(|scope| scope.project_id.as_deref()));
    if let Some(project) = scoped_project {
        if !is_uuid(project) {
            return Err(IssueStatusEditError::InvalidStatus);
        }
        let default_project = sqlx::query_scalar::<_, bool>(
            "SELECT workflow_id IS NULL
             FROM project WHERE id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
        )
        .bind(project)
        .bind(workspace_id)
        .fetch_optional(&mut *tx)
        .await?;
        if default_project != Some(true) {
            return Err(IssueStatusEditError::DispatchSensitive);
        }
    }
    let agent_owner = match prepared {
        Some(plan) if plan.is_leader_task => {
            status_dispatch::lock_active_squad_leader(
                &mut tx,
                workspace_id,
                &plan.squad_id,
                Some(plan),
            )
            .await?
            .owner_id
        }
        Some(plan) => status_dispatch::lock_prepared_agent(&mut tx, workspace_id, plan).await?,
        None => None,
    };
    let locked = sqlx::query(&format!(
        "SELECT i.project_id::text, i.revision, i.status, i.assignee_type, i.assignee_id::text,
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
    .ok_or(IssueStatusEditError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueStatusEditError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssueStatusEditError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    let revision: i64 = locked.try_get("revision")?;
    if edit.expected_revision != revision {
        return Err(IssueStatusEditError::RevisionConflict(revision));
    }
    let prev_status: &str = locked.try_get("status")?;
    let assignee_type: Option<String> = locked.try_get("assignee_type")?;
    let assignee_id: Option<String> = locked.try_get("assignee_id")?;
    let triage_state_present: bool = locked.try_get("triage_state_present")?;
    let has_duplicate: bool = locked.try_get("has_duplicate")?;
    let has_work_method: bool = locked.try_get("has_work_method")?;
    if let Some(trust) = batch_trust {
        if project_id.as_deref() != trust.expected_project_id.as_deref() {
            return Err(IssueStatusEditError::DispatchSensitive);
        }
        if triage_state_present
            || has_work_method
            || !is_active_builtin_status(prev_status)
            || !matches!(
                assignee_type.as_deref(),
                None | Some("member" | "agent" | "squad")
            )
        {
            return Err(IssueStatusEditError::DispatchSensitive);
        }
    }
    if terminal_batch
        && !matches!(
            assignee_type.as_deref(),
            None | Some("member" | "agent" | "squad")
        )
    {
        return Err(IssueStatusEditError::DispatchSensitive);
    }
    if !is_grid_builtin_status(prev_status) || (prev_status == "cancelled" && has_duplicate) {
        return Err(IssueStatusEditError::DispatchSensitive);
    }
    if let Some(scope) = terminal_scope
        && (triage_state_present
            || has_work_method
            || has_duplicate
            || scope.project_id.as_deref() != project_id.as_deref())
    {
        return Err(IssueStatusEditError::DispatchSensitive);
    }
    let promotion_sensitive = dispatch_sensitive_locked(
        prev_status,
        assignee_type.as_deref(),
        assignee_id.as_deref(),
        &edit.status,
        edit.suppress_run,
    );
    if batch_trust.is_some() && promotion_sensitive {
        return Err(IssueStatusEditError::DispatchSensitive);
    }
    if promotion_sensitive != prepared.is_some() {
        return Err(IssueStatusEditError::DispatchSensitive);
    }
    if let Some(plan) = prepared {
        let assignee_matches = match assignee_type.as_deref() {
            Some("agent") if !plan.is_leader_task => {
                assignee_id.as_deref() == Some(plan.agent_id.as_str())
            }
            Some("squad") if plan.is_leader_task && is_uuid(&plan.squad_id) => {
                assignee_id.as_deref() == Some(plan.squad_id.as_str())
            }
            _ => false,
        };
        if !assignee_matches
            || triage_state_present
            || issue_id != plan.issue_id
            || edit.status != plan.workflow_step
        {
            return Err(IssueStatusEditError::DispatchSensitive);
        }
        status_dispatch::validate_prepared_status_enqueue(
            &mut tx,
            workspace_id,
            issue_id,
            project_id.as_deref(),
            agent_owner,
            plan,
        )
        .await?;
    }

    ordinary::set_write_actor(&mut tx, actor, edit.source_task_id.as_deref()).await?;

    let saved = apply_status_write(
        &mut tx,
        workspace_id,
        issue_id,
        &edit,
        (actor_type, actor_id),
    )
    .await?;
    let (after_text, status_activity_id) = match saved {
        Some(row) => (
            row.try_get::<String, _>(0)?,
            row.try_get::<Option<String>, _>(1)?,
        ),
        None => {
            let actual: i64 = sqlx::query_scalar(
                "SELECT revision FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid",
            )
            .bind(issue_id)
            .bind(workspace_id)
            .fetch_one(&mut *tx)
            .await?;
            return Err(IssueStatusEditError::RevisionConflict(actual));
        }
    };
    let status_changed = prev_status != edit.status;
    if status_changed && status_activity_id.as_deref() != Some(edit.activity_id.as_str()) {
        return Err(IssueStatusEditError::Database(sqlx::Error::Protocol(
            "status activity receipt missing".into(),
        )));
    }
    if !status_changed && status_activity_id.is_some() {
        return Err(IssueStatusEditError::Database(sqlx::Error::Protocol(
            "unexpected status activity receipt".into(),
        )));
    }
    let (queued_task_id, coalesced_queued_task_id) = match prepared {
        Some(plan) => match status_dispatch::apply_prepared_status_enqueue(
            &mut tx,
            issue_id,
            &plan.head_sha,
            plan,
        )
        .await?
        {
            PromotionEnqueueOutcome::Inserted(id) => (Some(id), None),
            PromotionEnqueueOutcome::Coalesced(id) => (None, Some(id)),
        },
        None => (None, None),
    };
    let cancelled_tasks = if terminal {
        Some(terminal_wakeup::stop_closed_issue_wakeups(&mut tx, issue_id).await?)
    } else {
        None
    };
    let write = IssueStatusWrite {
        before: serde_json::from_str(locked.try_get("snapshot")?)?,
        after: serde_json::from_str(&after_text)?,
        status_activity_id,
        queued_task_id,
        coalesced_queued_task_id,
        cancelled_tasks,
    };
    tx.commit()
        .await
        .map_err(IssueStatusEditError::CommitFailed)?;
    Ok(write)
}

async fn apply_status_write(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    edit: &IssueStatusEdit,
    actor: (&str, &str),
) -> Result<Option<sqlx::postgres::PgRow>, sqlx::Error> {
    let sql = format!(
        "WITH candidate AS MATERIALIZED (
            SELECT
                i.id,
                i.status,
                i.position,
                i.duplicate_of_issue_id,
                $3::text AS next_status,
                CASE
                    WHEN $4::double precision IS NOT NULL THEN $4::double precision
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
                END AS next_duplicate_of_issue_id
            FROM issue AS i
            WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        ), changed AS MATERIALIZED (
            SELECT
                candidate.*,
                (candidate.status IS DISTINCT FROM candidate.next_status) AS did_status_change,
                ROW(candidate.status, candidate.position, candidate.duplicate_of_issue_id)
                    IS DISTINCT FROM ROW(
                        candidate.next_status,
                        candidate.next_position,
                        candidate.next_duplicate_of_issue_id
                    ) AS did_change,
                ROW(candidate.status, candidate.duplicate_of_issue_id)
                    IS DISTINCT FROM ROW(
                        candidate.next_status,
                        candidate.next_duplicate_of_issue_id
                    ) AS did_activity
            FROM candidate
        ), updated AS (
            UPDATE issue AS i SET
                status = changed.next_status,
                position = changed.next_position,
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
              AND i.revision = $5
            RETURNING {ISSUE_ROW_SNAPSHOT} AS after_snapshot
        ), activity_insert AS (
            INSERT INTO activity_log (id, workspace_id, issue_id, actor_type, actor_id, action, details)
            SELECT
                $6::uuid,
                $2::uuid,
                $1::uuid,
                $7,
                $8::uuid,
                'status_changed',
                jsonb_build_object('from', changed.status, 'to', changed.next_status)
            FROM changed CROSS JOIN updated
            WHERE changed.did_status_change
            RETURNING id::text AS activity_id
        )
        SELECT updated.after_snapshot, activity_insert.activity_id
        FROM updated LEFT JOIN activity_insert ON true"
    );
    sqlx::query(&sql)
        .bind(issue_id)
        .bind(workspace_id)
        .bind(&edit.status)
        .bind(edit.position)
        .bind(edit.expected_revision)
        .bind(&edit.activity_id)
        .bind(actor.0)
        .bind(actor.1)
        .fetch_optional(conn)
        .await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn active_builtin_statuses_match_grid_pilot() {
        for key in [
            "backlog",
            "todo",
            "in_progress",
            "in_review",
            "blocked",
            "done",
            "cancelled",
        ] {
            let active = is_active_builtin_status(key);
            assert_eq!(
                active,
                matches!(
                    key,
                    "backlog" | "todo" | "in_progress" | "in_review" | "blocked"
                ),
                "{key}"
            );
        }
    }

    #[test]
    fn dispatch_sensitive_requires_backlog_promotion_with_assignee() {
        assert!(!dispatch_sensitive_locked(
            "todo",
            Some("agent"),
            Some("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
            "in_progress",
            false,
        ));
        assert!(dispatch_sensitive_locked(
            "backlog",
            Some("agent"),
            Some("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
            "todo",
            false,
        ));
        assert!(!dispatch_sensitive_locked(
            "backlog",
            Some("agent"),
            Some("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
            "todo",
            true,
        ));
        assert!(dispatch_sensitive_locked(
            "backlog",
            Some("squad"),
            Some("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
            "todo",
            false,
        ));
    }

    #[test]
    fn terminal_and_active_builtin_statuses_are_disjoint() {
        for key in ACTIVE_BUILTINS.iter().chain(TERMINAL_BUILTINS) {
            let active = is_active_builtin_status(key);
            let terminal = is_terminal_builtin_status(key);
            assert_ne!(
                active, terminal,
                "{key}: cannot be both active and terminal"
            );
        }
        assert!(is_terminal_builtin_status("done"));
        assert!(is_terminal_builtin_status("cancelled"));
    }

    #[test]
    fn wire_accepts_terminal_batch_payload() {
        let edit: IssueStatusEdit = serde_json::from_str(
            r#"{
                "expected_revision": 2,
                "status": "done",
                "activity_id": "22222222-2222-4222-8222-222222222222",
                "terminal_scope": { "project_id": "33333333-3333-4333-8333-333333333333" },
                "ordinary_terminal_batch": true
            }"#,
        )
        .unwrap();
        assert_eq!(edit.status, "done");
        assert!(edit.terminal_scope.is_some());
        assert!(edit.ordinary_terminal_batch);
    }

    #[test]
    fn wire_accepts_status_edit_payload() {
        let edit: IssueStatusEdit = serde_json::from_str(
            r#"{
                "expected_revision": 2,
                "status": "in_progress",
                "position": 1500.5,
                "source_task_id": "11111111-1111-4111-8111-111111111111",
                "activity_id": "22222222-2222-4222-8222-222222222222"
            }"#,
        )
        .unwrap();
        assert_eq!(edit.status, "in_progress");
        assert_eq!(edit.expected_revision, 2);
        assert_eq!(edit.position, Some(1500.5));
        assert_eq!(edit.activity_id, "22222222-2222-4222-8222-222222222222");
    }
}
