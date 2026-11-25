//! Transactional direct-agent and ordinary squad assignment writes for the grid
//! pilot. Mirrors the assignee columns of `UpdateIssue` without touching status
//! or position.

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError, is_uuid,
    ordinary::{self, ISSUE_ROW_SNAPSHOT},
    resolve_caller_access_on,
    status_dispatch::{self, PreparedStatusEnqueue, PromotionEnqueueOutcome, StatusPromotionError},
    status_write::is_active_builtin_status,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueAssignEdit {
    pub expected_revision: i64,
    pub assignee_type: String,
    pub assignee_id: String,
    pub source_task_id: Option<String>,
    #[serde(default)]
    pub suppress_run: bool,
    pub activity_id: String,
    pub prepared_enqueue: Option<PreparedStatusEnqueue>,
}

#[derive(Debug, Serialize)]
pub struct IssueAssignWrite {
    pub before: Box<RawValue>,
    pub after: Box<RawValue>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub assignee_activity_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub queued_task_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub coalesced_queued_task_id: Option<String>,
}

#[derive(Debug)]
pub enum IssueAssignEditError {
    Access(IssueReadError),
    InvalidAssignee,
    DispatchSensitive,
    RevisionConflict(i64),
    Database(sqlx::Error),
    Snapshot(serde_json::Error),
}

impl From<sqlx::Error> for IssueAssignEditError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssueAssignEditError {
    fn from(error: serde_json::Error) -> Self {
        Self::Snapshot(error)
    }
}

impl From<StatusPromotionError> for IssueAssignEditError {
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

fn assignment_requires_enqueue(status: &str, assignee_changed: bool, suppress_run: bool) -> bool {
    status != "backlog" && assignee_changed && !suppress_run
}

fn assignee_pair_changed(
    prev_type: Option<&str>,
    prev_id: Option<&str>,
    next_type: &str,
    next_id: &str,
) -> bool {
    prev_type.unwrap_or("") != next_type || prev_id.unwrap_or("") != next_id
}

pub async fn update_issue_assign(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    edit: IssueAssignEdit,
) -> Result<IssueAssignWrite, IssueAssignEditError> {
    if edit.assignee_type != "agent" && edit.assignee_type != "squad" {
        return Err(IssueAssignEditError::InvalidAssignee);
    }
    if !is_uuid(&edit.assignee_id) || !is_uuid(&edit.activity_id) {
        return Err(IssueAssignEditError::InvalidAssignee);
    }
    let (actor_type, actor_id) = actor_refs(actor);
    if !is_uuid(actor_id) {
        return Err(IssueAssignEditError::InvalidAssignee);
    }

    let mut tx = pool.begin().await?;
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;

    let prepared = edit.prepared_enqueue.as_ref();
    let (target_agent, agent_owner) = if edit.assignee_type == "agent" {
        let owner = match prepared {
            Some(plan)
                if !plan.is_leader_task
                    && plan.squad_id.is_empty()
                    && plan.agent_id == edit.assignee_id =>
            {
                status_dispatch::lock_prepared_agent(&mut tx, workspace_id, plan).await?
            }
            Some(_) => return Err(IssueAssignEditError::DispatchSensitive),
            None => {
                status_dispatch::lock_target_agent_for_assignment(
                    &mut tx,
                    workspace_id,
                    &edit.assignee_id,
                )
                .await?
            }
        };
        (edit.assignee_id.clone(), owner)
    } else {
        if prepared.is_some_and(|plan| !plan.is_leader_task || plan.squad_id != edit.assignee_id) {
            return Err(IssueAssignEditError::DispatchSensitive);
        }
        let target = status_dispatch::lock_active_squad_leader(
            &mut tx,
            workspace_id,
            &edit.assignee_id,
            prepared,
        )
        .await?;
        (target.leader_id, target.owner_id)
    };

    let locked = sqlx::query(&format!(
        "SELECT i.project_id::text, i.revision, i.status, i.assignee_type, i.assignee_id::text,
                i.triage_state IS NOT NULL AS triage_state_present,
                {ISSUE_ROW_SNAPSHOT} AS snapshot
         FROM issue i WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid FOR UPDATE"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueAssignEditError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;

    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueAssignEditError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssueAssignEditError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }

    let revision: i64 = locked.try_get("revision")?;
    if edit.expected_revision != revision {
        return Err(IssueAssignEditError::RevisionConflict(revision));
    }

    let prev_status: &str = locked.try_get("status")?;
    let prev_assignee_type: Option<String> = locked.try_get("assignee_type")?;
    let prev_assignee_id: Option<String> = locked.try_get("assignee_id")?;
    let triage_state_present: bool = locked.try_get("triage_state_present")?;
    if !matches!(
        prev_assignee_type.as_deref(),
        None | Some("" | "member" | "agent" | "squad")
    ) || !is_active_builtin_status(prev_status)
        || triage_state_present
    {
        return Err(IssueAssignEditError::DispatchSensitive);
    }

    let assignee_changed = assignee_pair_changed(
        prev_assignee_type.as_deref(),
        prev_assignee_id.as_deref(),
        &edit.assignee_type,
        &edit.assignee_id,
    );
    if assignment_requires_enqueue(prev_status, assignee_changed, edit.suppress_run)
        != prepared.is_some()
    {
        return Err(IssueAssignEditError::DispatchSensitive);
    }
    if let Some(plan) = prepared {
        if plan.issue_id != issue_id || plan.workflow_step != prev_status {
            return Err(IssueAssignEditError::DispatchSensitive);
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
    } else if let Some(owner) = agent_owner {
        status_dispatch::validate_agent_issue_project_access(
            &mut tx,
            workspace_id,
            project_id.as_deref(),
            vec![owner],
        )
        .await?;
    }

    let handed_to = assignee_changed.then_some(target_agent.as_str());
    ordinary::set_write_session(&mut tx, actor, edit.source_task_id.as_deref(), handed_to).await?;

    let (after_text, assignee_activity_id) = apply_assign_write(
        &mut tx,
        workspace_id,
        issue_id,
        &edit,
        (actor_type, actor_id),
    )
    .await?;
    let expected_activity = assignee_changed.then_some(edit.activity_id.as_str());
    if assignee_activity_id.as_deref() != expected_activity {
        return Err(IssueAssignEditError::Database(sqlx::Error::Protocol(
            "incorrect assignee activity receipt".into(),
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

    let write = IssueAssignWrite {
        before: serde_json::from_str(locked.try_get("snapshot")?)?,
        after: serde_json::from_str(&after_text)?,
        assignee_activity_id,
        queued_task_id,
        coalesced_queued_task_id,
    };
    tx.commit().await?;
    Ok(write)
}

async fn apply_assign_write(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    edit: &IssueAssignEdit,
    actor: (&str, &str),
) -> Result<(String, Option<String>), sqlx::Error> {
    let sql = format!(
        "WITH candidate AS (
            SELECT i.id, i.assignee_type, i.assignee_id, i.duplicate_of_issue_id,
                ROW(i.assignee_type, i.assignee_id) IS DISTINCT FROM ROW($3::text, $4::uuid)
                    AS assignee_changed
            FROM issue AS i
            WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        ), changed AS (
            SELECT candidate.*, (assignee_changed OR duplicate_of_issue_id IS NOT NULL) AS did_change
            FROM candidate
        ), updated AS (
            UPDATE issue AS i SET
                assignee_type = $3::text,
                assignee_id = $4::uuid,
                duplicate_of_issue_id = NULL,
                revision = i.revision + changed.did_change::integer,
                last_activity_at = CASE
                    WHEN changed.did_change
                        THEN GREATEST(COALESCE(i.last_activity_at, i.updated_at), now())
                    ELSE i.last_activity_at
                END,
                updated_at = CASE WHEN changed.did_change THEN now() ELSE i.updated_at END
            FROM changed
            WHERE i.id = changed.id AND i.workspace_id = $2::uuid
            RETURNING {ISSUE_ROW_SNAPSHOT} AS after_snapshot
        ), activity_insert AS (
            INSERT INTO activity_log (id, workspace_id, issue_id, actor_type, actor_id, action, details)
            SELECT $5::uuid, $2::uuid, $1::uuid, $6, $7::uuid, 'assignee_changed',
                jsonb_strip_nulls(jsonb_build_object(
                    'from_type', changed.assignee_type,
                    'from_id', changed.assignee_id,
                    'to_type', $3::text,
                    'to_id', $4::uuid
                ))
            FROM changed CROSS JOIN updated
            WHERE changed.assignee_changed
            RETURNING id::text AS activity_id
        )
        SELECT updated.after_snapshot, activity_insert.activity_id
        FROM updated LEFT JOIN activity_insert ON true"
    );
    sqlx::query_as(&sql)
        .bind(issue_id)
        .bind(workspace_id)
        .bind(&edit.assignee_type)
        .bind(&edit.assignee_id)
        .bind(&edit.activity_id)
        .bind(actor.0)
        .bind(actor.1)
        .fetch_one(conn)
        .await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn assignment_enqueue_requires_changed_active_unsuppressed_issue() {
        assert!(!assignment_requires_enqueue("backlog", true, false));
        assert!(!assignment_requires_enqueue("todo", true, true));
        assert!(assignment_requires_enqueue("todo", true, false));
        assert!(!assignment_requires_enqueue("todo", false, false));
    }

    #[test]
    fn assignee_pair_changed_matches_go_row_semantics() {
        let squad = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
        assert!(!assignee_pair_changed(
            Some("squad"),
            Some(squad),
            "squad",
            squad,
        ));
        assert!(assignee_pair_changed(
            Some("squad"),
            Some(squad),
            "agent",
            squad
        ));
        assert!(assignee_pair_changed(
            Some("squad"),
            Some("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
            "squad",
            squad,
        ));
    }
}
