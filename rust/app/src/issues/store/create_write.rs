//! Plain member issue creates for the create pilot. Mirrors IssueService.Create
//! transactional core: duplicate guard, numbering, position, labels, and row
//! insert without runtime enqueue side effects.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{Acquire, PgConnection, PgPool, Postgres, Row, Transaction};

use super::attach_agent_source_issue::{
    PendingAgentOriginTrust, attach_pending_agent_origin_context, lock_agent_origin,
    lock_pending_agent_origin_context, validate_pending_agent_origin_request,
};
use super::create_source_context::{
    SourceContextCreateTrust, insert_attached_source_context, prepare_source_context_create_trust,
    reconcile_source_context_project, validate_source_context_create_trust,
};
use super::create_validate::{
    guard_parent_projects_and_assignee, revalidate_type_and_properties,
    validate_labels_before_number,
};
use super::ordinary::set_write_actor;
use super::parent_link;
use super::status_dispatch::{
    self, PreparedStatusEnqueue, PromotionEnqueueOutcome, StatusPromotionError,
};

use super::{
    CallerActor, IssueReadError, IssueResponse, LabelResponse, format_issue_timestamp, is_uuid,
    legacy_issue_prefix_from_name,
};

#[derive(Debug, Deserialize)]
pub struct IssueCreateLimit {
    pub enforce: bool,
    pub limit: i64,
    pub policy_revision: i64,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SquadCreateTrust {
    pub squad_id: String,
    pub expected_leader_id: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueCreateRequest {
    pub id: String,
    pub title: String,
    pub description: Option<String>,
    pub status: String,
    #[serde(default)]
    pub normalized_title: String,
    pub priority: String,
    pub assignee_type: Option<String>,
    pub assignee_id: Option<String>,
    pub creator_type: String,
    pub creator_id: String,
    pub parent_issue_id: Option<String>,
    pub project_id: Option<String>,
    pub start_date: Option<String>,
    pub due_date: Option<String>,
    #[serde(default)]
    pub label_ids: Vec<String>,
    pub properties: Box<RawValue>,
    pub property_versions: BTreeMap<String, String>,
    pub issue_type_id: Option<String>,
    pub issue_type_version: Option<String>,
    #[serde(default)]
    pub allow_duplicate: bool,
    pub work_method_run: Option<Box<RawValue>>,
    pub issue_limit: IssueCreateLimit,
    #[serde(default)]
    pub pr_auto_complete_disabled: bool,
    pub prepared_enqueue: Option<PreparedStatusEnqueue>,
    pub squad_create_trust: Option<SquadCreateTrust>,
    #[serde(default)]
    pub attachment_ids: Vec<String>,
    pub source_context_create_trust: Option<SourceContextCreateTrust>,
    pub pending_agent_origin_trust: Option<PendingAgentOriginTrust>,
}

#[derive(Debug, Serialize)]
pub struct IssueCreatePayload {
    pub issue: IssueResponse,
    pub labels: Vec<LabelResponse>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub queued_task_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub coalesced_queued_task_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub linked_attachment_ids: Option<Vec<String>>,
}

#[derive(Debug, Serialize)]
pub struct ActiveDuplicatePayload {
    pub code: &'static str,
    pub error: String,
    pub issue: IssueResponse,
}

#[derive(Debug)]
pub enum IssueCreateError {
    Access(IssueReadError),
    InvalidInput,
    ConfigurationChanged,
    ParentNotFound,
    ProjectNotFound,
    LabelNotFound,
    InvalidIssueType(&'static str),
    InvalidIssueProperty {
        property_id: String,
        message: String,
    },
    InvalidAssignee,
    PropertiesTooLarge,
    IssueLimit {
        limit: i64,
        policy_revision: i64,
    },
    ActiveDuplicate(Box<ActiveDuplicatePayload>),
    NumberCollision {
        allocated: i32,
    },
    DispatchSensitive,
    AgentInvokeForbidden,
    SquadInvokeForbidden,
    SourceContextChanged,
    AnchorCommentDeleted,
    SourceIssueDeleted,
    SourceContextInvalidPath,
    SourceContextAlreadyAttached,
    SourceContextQuickCreateUnsupported,
    SourceContextQuickCreateUnavailable,
    SourceContextRetryUnavailable,
    Database(sqlx::Error),
}

impl From<sqlx::Error> for IssueCreateError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

pub(super) fn duplicate_lock_key(
    workspace_id: &str,
    project_id: Option<&str>,
    parent_issue_id: Option<&str>,
    normalized_title: &str,
) -> String {
    format!(
        "issue-active-duplicate|{workspace_id}|{}|{}|{normalized_title}",
        project_id.unwrap_or(""),
        parent_issue_id.unwrap_or(""),
    )
}

fn is_properties_size_violation(error: &sqlx::Error) -> bool {
    match error {
        sqlx::Error::Database(db) => {
            db.code().as_deref() == Some("23514")
                && db.constraint() == Some("issue_properties_size_limit")
        }
        _ => false,
    }
}

async fn link_create_attachments(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    issue_id: &str,
    ids: &[String],
) -> Result<Vec<String>, sqlx::Error> {
    let mut link_tx = tx.begin().await?;
    let linked = sqlx::query_scalar::<_, String>(
        "WITH to_claim AS MATERIALIZED (
            SELECT id
              FROM attachment
             WHERE workspace_id = $1::uuid
               AND issue_id IS NULL AND comment_id IS NULL
               AND chat_session_id IS NULL AND chat_message_id IS NULL
               AND task_id IS NULL AND source_context_id IS NULL
               AND id = ANY($2::uuid[])
             ORDER BY id
             FOR UPDATE
        ), updated AS (
            UPDATE attachment AS a
               SET issue_id = $3::uuid
              FROM to_claim AS t
             WHERE a.id = t.id
             RETURNING a.id::text AS id
        )
        SELECT id FROM updated ORDER BY id",
    )
    .bind(workspace_id)
    .bind(ids)
    .bind(issue_id)
    .fetch_all(&mut *link_tx)
    .await;

    match linked {
        Ok(ids) => {
            link_tx.commit().await?;
            Ok(ids)
        }
        Err(sqlx::Error::Database(error)) => {
            link_tx.rollback().await?;
            eprintln!("rust issue {issue_id}: optional attachment linkage failed: {error}");
            Ok(Vec::new())
        }
        Err(error) => Err(error),
    }
}

fn is_number_unique_violation(error: &sqlx::Error) -> bool {
    match error {
        sqlx::Error::Database(db) => {
            db.code().as_deref() == Some("23505")
                && db.constraint() == Some("uq_issue_workspace_number")
        }
        _ => false,
    }
}

async fn workspace_prefix(
    conn: &mut PgConnection,
    workspace_id: &str,
) -> Result<String, sqlx::Error> {
    let row = sqlx::query("SELECT name, issue_prefix FROM workspace WHERE id = $1::uuid")
        .bind(workspace_id)
        .fetch_optional(&mut *conn)
        .await?;
    let Some(row) = row else {
        return Ok(String::new());
    };
    let name: String = row.try_get(0)?;
    let issue_prefix: String = row.try_get(1)?;
    if !issue_prefix.is_empty() {
        Ok(issue_prefix)
    } else {
        Ok(legacy_issue_prefix_from_name(&name))
    }
}

async fn heal_issue_counter(pool: &PgPool, workspace_id: &str) -> Result<bool, sqlx::Error> {
    let healed = sqlx::query_scalar::<_, bool>(
        "WITH target AS (
            SELECT w.id, w.issue_counter,
                   (SELECT COALESCE(MAX(i.number), 0)::int FROM issue i WHERE i.workspace_id = w.id) AS max_number
            FROM workspace w
            WHERE w.id = $1::uuid
         )
         UPDATE workspace w SET issue_counter = t.max_number
         FROM target t
         WHERE w.id = t.id AND w.issue_counter < t.max_number
         RETURNING true",
    )
    .bind(workspace_id)
    .fetch_optional(pool)
    .await?;
    Ok(healed.is_some())
}

pub(super) async fn find_active_duplicate(
    conn: &mut PgConnection,
    workspace_id: &str,
    project_id: Option<&str>,
    parent_issue_id: Option<&str>,
    normalized_title: &str,
) -> Result<Option<IssueResponse>, sqlx::Error> {
    if normalized_title.is_empty() {
        return Ok(None);
    }
    let row = sqlx::query(
        "SELECT id::text, workspace_id::text, number, title, description, status, priority,
                assignee_type, assignee_id::text, creator_type, creator_id::text,
                requester_user_id::text, parent_issue_id::text, duplicate_of_issue_id::text,
                project_id::text, position::double precision, stage,
                to_char(start_date, 'YYYY-MM-DD'), to_char(due_date, 'YYYY-MM-DD'),
                to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
                to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
                revision,
                to_char(last_activity_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
                metadata::text, properties::text, issue_type_id::text
         FROM issue
         WHERE workspace_id = $1::uuid
           AND status NOT IN ('done', 'cancelled')
           AND status NOT IN (SELECT key FROM issue_status
                WHERE workspace_id = $1::uuid AND category IN ('done', 'closed'))
           AND triage_state IS NULL
           AND project_id IS NOT DISTINCT FROM $2::uuid
           AND parent_issue_id IS NOT DISTINCT FROM $3::uuid
           AND lower(btrim(regexp_replace(title, '[[:space:]]+', ' ', 'g'))) = $4
         ORDER BY created_at ASC
         LIMIT 1",
    )
    .bind(workspace_id)
    .bind(project_id)
    .bind(parent_issue_id)
    .bind(normalized_title)
    .fetch_optional(&mut *conn)
    .await?;
    let Some(row) = row else {
        return Ok(None);
    };
    let prefix = workspace_prefix(conn, workspace_id).await?;
    Ok(Some(map_create_row_to_response(&row, &prefix)?))
}

pub(super) fn duplicate_message(identifier: &str, title: &str, status: &str) -> String {
    format!(
        "Active duplicate issue exists: {identifier} {title} (status: {status}). Set allow_duplicate=true or use --allow-duplicate to create another."
    )
}

pub(super) fn map_create_row_to_response(
    row: &sqlx::postgres::PgRow,
    prefix: &str,
) -> Result<IssueResponse, sqlx::Error> {
    let number = row.try_get(2)?;
    let status: String = row.try_get(5)?;
    let status_category = is_built_in_status(&status).then(|| status.clone());
    Ok(IssueResponse {
        acceptance_trace: None,
        id: row.try_get(0)?,
        workspace_id: row.try_get(1)?,
        number,
        identifier: format!("{prefix}-{number}"),
        title: row.try_get(3)?,
        description: row.try_get(4)?,
        status,
        status_category,
        status_name: String::new(),
        priority: row.try_get(6)?,
        assignee_type: row.try_get(7)?,
        assignee_id: row.try_get(8)?,
        creator_type: row.try_get(9)?,
        creator_id: row.try_get(10)?,
        requester_id: row.try_get(11)?,
        parent_issue_id: row.try_get(12)?,
        duplicate_of: None,
        project_id: row.try_get(14)?,
        position: row.try_get(15)?,
        stage: row.try_get(16)?,
        start_date: row.try_get(17)?,
        due_date: row.try_get(18)?,
        created_at: format_issue_timestamp(row.try_get(19)?),
        updated_at: format_issue_timestamp(row.try_get(20)?),
        revision: row.try_get(21)?,
        last_activity_at: row
            .try_get::<Option<String>, _>(22)?
            .map(format_issue_timestamp),
        metadata: super::json_object_or_empty(row.try_get::<Option<String>, _>(23)?.as_deref()),
        properties: super::json_object_or_empty(row.try_get::<Option<String>, _>(24)?.as_deref()),
        issue_type_id: row.try_get(25)?,
        reactions: None,
        labels: Vec::new(),
    })
}

fn is_built_in_status(status: &str) -> bool {
    matches!(
        status,
        "backlog" | "todo" | "in_progress" | "in_review" | "blocked" | "done" | "cancelled"
    )
}

async fn attach_labels(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    labels: &[LabelResponse],
) -> Result<(), IssueCreateError> {
    for label in labels {
        let attached = sqlx::query(
            "WITH touched_issue AS (
                UPDATE issue
                SET last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
                WHERE issue.id = $1::uuid
                  AND issue.workspace_id = $2::uuid
                  AND NOT EXISTS (
                      SELECT 1 FROM issue_to_label
                      WHERE issue_to_label.issue_id = $1::uuid
                        AND issue_to_label.label_id = $3::uuid
                  )
                  AND EXISTS (
                      SELECT 1 FROM issue_label
                      WHERE issue_label.id = $3::uuid
                        AND issue_label.workspace_id = $2::uuid
                        AND issue_label.resource_type = 'issue'
                  )
                RETURNING issue.id
             )
             INSERT INTO issue_to_label (issue_id, label_id)
             SELECT $1::uuid, $3::uuid
             WHERE EXISTS (SELECT 1 FROM touched_issue)
             ON CONFLICT DO NOTHING
             RETURNING label_id",
        )
        .bind(issue_id)
        .bind(workspace_id)
        .bind(&label.id)
        .fetch_optional(&mut *conn)
        .await?;
        if attached.is_none() {
            return Err(IssueCreateError::LabelNotFound);
        }
    }
    Ok(())
}

fn create_requires_enqueue(status: &str, assignee_type: Option<&str>) -> bool {
    if status == "backlog" {
        return false;
    }
    matches!(assignee_type, Some("agent") | Some("squad"))
}

fn map_dispatch_error(error: StatusPromotionError) -> IssueCreateError {
    match error {
        StatusPromotionError::RouteChanged => IssueCreateError::DispatchSensitive,
        StatusPromotionError::Database(error) => IssueCreateError::Database(error),
    }
}

async fn create_issue_attempt(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    request: &IssueCreateRequest,
) -> Result<IssueCreatePayload, IssueCreateError> {
    let pending_origin = request.pending_agent_origin_trust.as_ref();
    let member_id = match (actor, pending_origin) {
        (CallerActor::Member { user_id }, None)
            if request.creator_type == "member" && request.creator_id == *user_id =>
        {
            user_id.as_str()
        }
        (CallerActor::Agent { .. }, Some(trust)) => {
            validate_pending_agent_origin_request(actor, request, trust)?;
            trust.expected_requester_id.as_str()
        }
        _ => return Err(IssueCreateError::InvalidInput),
    };
    if !is_uuid(&request.id)
        || !is_uuid(workspace_id)
        || request.title.is_empty()
        || request.creator_id.is_empty()
        || !matches!(
            request.status.as_str(),
            "backlog" | "todo" | "in_progress" | "in_review" | "blocked"
        )
    {
        return Err(IssueCreateError::InvalidInput);
    }
    for id in &request.attachment_ids {
        if !is_uuid(id) {
            return Err(IssueCreateError::InvalidInput);
        }
    }
    if !request.attachment_ids.is_empty() {
        match request.assignee_type.as_deref() {
            Some("agent") | Some("squad") => return Err(IssueCreateError::InvalidInput),
            _ => {}
        }
        if request.prepared_enqueue.is_some() {
            return Err(IssueCreateError::InvalidInput);
        }
    }
    if let Some(assignee_type) = request.assignee_type.as_deref() {
        if assignee_type != "member" && assignee_type != "agent" && assignee_type != "squad" {
            return Err(IssueCreateError::InvalidInput);
        }
        let Some(assignee_id) = request.assignee_id.as_deref() else {
            return Err(IssueCreateError::InvalidInput);
        };
        if !is_uuid(assignee_id) {
            return Err(IssueCreateError::InvalidInput);
        }
    }
    let prepared = request.prepared_enqueue.as_ref();
    if create_requires_enqueue(&request.status, request.assignee_type.as_deref())
        != prepared.is_some()
    {
        return Err(IssueCreateError::DispatchSensitive);
    }

    let mut tx = pool.begin().await?;
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;

    sqlx::query(
        "SELECT pg_advisory_xact_lock_shared(hashtextextended($1::uuid::text || ':issue_status', 0))",
    )
    .bind(workspace_id)
    .execute(&mut *tx)
    .await?;
    if let Some(trust) = pending_origin {
        lock_agent_origin(&mut tx, workspace_id, member_id, trust).await?;
    }
    let mut source_context_prepared = None;
    if let Some(trust) = request.source_context_create_trust.as_ref() {
        if request.status != "todo" {
            return Err(IssueCreateError::InvalidInput);
        }
        source_context_prepared = Some((
            trust,
            prepare_source_context_create_trust(workspace_id, member_id, request, trust)?,
        ));
    }
    revalidate_type_and_properties(&mut tx, workspace_id, request).await?;
    if request.parent_issue_id.is_some() {
        crate::issues::store::parent_link::lock_workspace_parent_hierarchy(&mut tx, workspace_id)
            .await?;
    }
    let effective_project =
        match guard_parent_projects_and_assignee(&mut tx, workspace_id, member_id, request).await {
            Err(IssueCreateError::ParentNotFound | IssueCreateError::ProjectNotFound)
                if source_context_prepared.is_some() =>
            {
                return Err(IssueCreateError::AnchorCommentDeleted);
            }
            Err(IssueCreateError::ConfigurationChanged) if source_context_prepared.is_some() => {
                return Err(IssueCreateError::SourceContextChanged);
            }
            other => other?,
        };
    if let Some(trust) = pending_origin
        && trust.expected_source_project_id.as_deref() != effective_project.as_deref()
    {
        return Err(IssueCreateError::ConfigurationChanged);
    }
    let source_context = if let Some((trust, prepared)) = source_context_prepared {
        reconcile_source_context_project(trust, effective_project.as_deref())?;
        let prefix = validate_source_context_create_trust(
            &mut tx,
            workspace_id,
            trust,
            &prepared,
            effective_project.as_deref(),
        )
        .await?;
        Some((trust, prefix))
    } else {
        None
    };
    let source_task_id = pending_origin.map(|trust| trust.expected_origin_task_id.as_str());
    set_write_actor(&mut tx, actor, source_task_id).await?;

    if request.assignee_type.as_deref() == Some("agent") {
        let Some(assignee_id) = request.assignee_id.as_deref() else {
            return Err(IssueCreateError::InvalidAssignee);
        };
        let agent_owner = match prepared {
            Some(plan)
                if !plan.is_leader_task
                    && plan.squad_id.is_empty()
                    && plan.agent_id == assignee_id
                    && plan.issue_id == request.id
                    && plan.workflow_step == request.status =>
            {
                status_dispatch::lock_prepared_agent(&mut tx, workspace_id, plan)
                    .await
                    .map_err(map_dispatch_error)?
            }
            Some(_) => return Err(IssueCreateError::DispatchSensitive),
            None => status_dispatch::lock_target_agent_for_assignment(
                &mut tx,
                workspace_id,
                assignee_id,
            )
            .await
            .map_err(map_dispatch_error)?,
        };
        if agent_owner.as_deref() != Some(member_id) {
            return Err(IssueCreateError::AgentInvokeForbidden);
        }
        if let Some(plan) = prepared {
            status_dispatch::validate_runtime_for_active_enqueue(
                &mut tx,
                workspace_id,
                member_id,
                &plan.runtime_id,
                status_dispatch::RuntimeEnqueueCaller::DirectAgent,
            )
            .await
            .map_err(map_dispatch_error)?;
            status_dispatch::validate_prepared_status_enqueue(
                &mut tx,
                workspace_id,
                &request.id,
                effective_project.as_deref(),
                Some(member_id.to_owned()),
                plan,
            )
            .await
            .map_err(map_dispatch_error)?;
        } else {
            status_dispatch::validate_agent_issue_project_access(
                &mut tx,
                workspace_id,
                effective_project.as_deref(),
                vec![member_id.to_owned()],
            )
            .await
            .map_err(map_dispatch_error)?;
        }
    } else if request.assignee_type.as_deref() == Some("squad") {
        let Some(assignee_id) = request.assignee_id.as_deref() else {
            return Err(IssueCreateError::InvalidAssignee);
        };
        let trust = request
            .squad_create_trust
            .as_ref()
            .ok_or(IssueCreateError::DispatchSensitive)?;
        if trust.squad_id != assignee_id || !is_uuid(&trust.expected_leader_id) {
            return Err(IssueCreateError::DispatchSensitive);
        }
        if let Some(plan) = prepared
            && (!plan.is_leader_task
                || plan.squad_id != assignee_id
                || plan.agent_id != trust.expected_leader_id
                || plan.issue_id != request.id
                || plan.workflow_step != request.status)
        {
            return Err(IssueCreateError::DispatchSensitive);
        }
        let leader =
            status_dispatch::lock_active_squad_leader(&mut tx, workspace_id, assignee_id, prepared)
                .await
                .map_err(map_dispatch_error)?;
        if leader.leader_id != trust.expected_leader_id {
            return Err(IssueCreateError::DispatchSensitive);
        }
        if leader.owner_id.as_deref() != Some(member_id) {
            return Err(IssueCreateError::SquadInvokeForbidden);
        }
        if let Some(plan) = prepared {
            status_dispatch::validate_runtime_for_active_enqueue(
                &mut tx,
                workspace_id,
                member_id,
                &plan.runtime_id,
                status_dispatch::RuntimeEnqueueCaller::SquadLeader,
            )
            .await
            .map_err(map_dispatch_error)?;
            status_dispatch::validate_prepared_status_enqueue(
                &mut tx,
                workspace_id,
                &request.id,
                effective_project.as_deref(),
                leader.owner_id,
                plan,
            )
            .await
            .map_err(map_dispatch_error)?;
        } else {
            status_dispatch::validate_agent_issue_project_access(
                &mut tx,
                workspace_id,
                effective_project.as_deref(),
                vec![member_id.to_owned()],
            )
            .await
            .map_err(map_dispatch_error)?;
        }
    }

    let receipt_labels =
        validate_labels_before_number(&mut tx, workspace_id, &request.label_ids).await?;

    let normalized = request.normalized_title.as_str();
    if !normalized.is_empty() {
        let lock_key = duplicate_lock_key(
            workspace_id,
            effective_project.as_deref(),
            request.parent_issue_id.as_deref(),
            normalized,
        );
        sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))")
            .bind(&lock_key)
            .execute(&mut *tx)
            .await?;
        if !request.allow_duplicate
            && let Some(dup) = find_active_duplicate(
                &mut tx,
                workspace_id,
                effective_project.as_deref(),
                request.parent_issue_id.as_deref(),
                normalized,
            )
            .await?
        {
            return Err(IssueCreateError::ActiveDuplicate(Box::new(
                ActiveDuplicatePayload {
                    code: "active_duplicate_issue",
                    error: duplicate_message(&dup.identifier, &dup.title, &dup.status),
                    issue: dup,
                },
            )));
        }
    }

    let issue_number: i32 = sqlx::query_scalar(
        "UPDATE workspace SET issue_counter = issue_counter + 1 WHERE id = $1::uuid RETURNING issue_counter",
    )
    .bind(workspace_id)
    .fetch_one(&mut *tx)
    .await?;

    if request.issue_limit.enforce {
        let sample_limit = request.issue_limit.limit.saturating_add(1);
        let used: i64 = sqlx::query_scalar(
            "SELECT COUNT(*)::bigint FROM (
                SELECT 1 FROM issue WHERE workspace_id = $1::uuid LIMIT $2
             ) bounded_issues",
        )
        .bind(workspace_id)
        .bind(sample_limit)
        .fetch_one(&mut *tx)
        .await?;
        if used >= request.issue_limit.limit {
            return Err(IssueCreateError::IssueLimit {
                limit: request.issue_limit.limit,
                policy_revision: request.issue_limit.policy_revision,
            });
        }
    }

    if let Some(trust) = pending_origin {
        // The legacy writer reserves the issue number before locking the pending capture.
        lock_pending_agent_origin_context(&mut tx, workspace_id, trust).await?;
    }

    let min_position: f64 = sqlx::query_scalar(
        "SELECT COALESCE(MIN(position), 0)::double precision FROM issue WHERE workspace_id = $1::uuid AND status = $2",
    )
    .bind(workspace_id)
    .bind(&request.status)
    .fetch_one(&mut *tx)
    .await?;
    let position = min_position - 1.0;

    let (origin_type, origin_id, requester_user_id) = if let Some(trust) = pending_origin {
        (
            Some("quick_create"),
            Some(trust.expected_origin_task_id.as_str()),
            Some(member_id),
        )
    } else {
        (None, None, None)
    };

    if let Some(parent) = request.parent_issue_id.as_deref() {
        parent_link::lock_issue_rows_ordered(
            &mut tx,
            workspace_id,
            [request.id.clone(), parent.to_string()],
        )
        .await?;
        if let Err(err) = parent_link::validate_parent_link(
            &mut tx,
            workspace_id,
            &request.id,
            Some(parent),
            &super::HiddenProjects::default(),
        )
        .await
        {
            return Err(IssueCreateError::Access(
                parent_link::map_parent_link_access(err),
            ));
        }
    }

    let inserted = sqlx::query(
        "INSERT INTO issue (
            id, workspace_id, title, description, status, priority,
            assignee_type, assignee_id, creator_type, creator_id,
            parent_issue_id, position, start_date, due_date, number, project_id,
            properties, issue_type_id, last_activity_at, work_method_run,
            origin_type, origin_id, requester_user_id
         ) VALUES (
            $1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8::uuid, $9, $10::uuid,
            $11::uuid, $12, $13::date, $14::date, $15, $16::uuid,
            $17::jsonb, $18::uuid, now(), CASE WHEN $19::jsonb IS NULL THEN NULL ELSE $19::jsonb END,
            $20, $21::uuid, $22::uuid
         )
         RETURNING id::text, workspace_id::text, number, title, description, status, priority,
                   assignee_type, assignee_id::text, creator_type, creator_id::text,
                   requester_user_id::text, parent_issue_id::text, duplicate_of_issue_id::text,
                   project_id::text, position::double precision, stage,
                   to_char(start_date, 'YYYY-MM-DD'), to_char(due_date, 'YYYY-MM-DD'),
                   to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
                   to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
                   revision,
                   to_char(last_activity_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
                   metadata::text, properties::text, issue_type_id::text",
    )
    .bind(&request.id)
    .bind(workspace_id)
    .bind(&request.title)
    .bind(&request.description)
    .bind(&request.status)
    .bind(&request.priority)
    .bind(request.assignee_type.as_deref())
    .bind(request.assignee_id.as_deref())
    .bind(&request.creator_type)
    .bind(&request.creator_id)
    .bind(request.parent_issue_id.as_deref())
    .bind(position)
    .bind(request.start_date.as_deref())
    .bind(request.due_date.as_deref())
    .bind(issue_number)
    .bind(effective_project.as_deref())
    .bind(request.properties.get())
    .bind(request.issue_type_id.as_deref())
    .bind(request.work_method_run.as_deref().map(RawValue::get))
    .bind(origin_type)
    .bind(origin_id)
    .bind(requester_user_id)
    .fetch_one(&mut *tx)
    .await
    .map_err(|error| {
        if is_properties_size_violation(&error) {
            IssueCreateError::PropertiesTooLarge
        } else if is_number_unique_violation(&error) {
            IssueCreateError::NumberCollision {
                allocated: issue_number,
            }
        } else {
            IssueCreateError::Database(error)
        }
    })?;

    if let Some((trust, plan)) = source_context {
        insert_attached_source_context(&mut tx, workspace_id, &request.id, trust, &plan).await?;
    }
    if let Some(trust) = pending_origin {
        attach_pending_agent_origin_context(&mut tx, workspace_id, &request.id, trust).await?;
    }

    if request.pr_auto_complete_disabled {
        sqlx::query(
            "INSERT INTO issue_pr_automation (
                issue_id, workspace_id, auto_complete_disabled, updated_by_type, updated_by_id
             ) VALUES ($1::uuid, $2::uuid, true, $3, $4::uuid)
             ON CONFLICT (issue_id) DO UPDATE SET
                auto_complete_disabled = EXCLUDED.auto_complete_disabled,
                updated_by_type = EXCLUDED.updated_by_type,
                updated_by_id = EXCLUDED.updated_by_id,
                updated_at = now()",
        )
        .bind(&request.id)
        .bind(workspace_id)
        .bind(&request.creator_type)
        .bind(&request.creator_id)
        .execute(&mut *tx)
        .await?;
    }

    attach_labels(&mut tx, workspace_id, &request.id, &receipt_labels).await?;

    let (queued_task_id, coalesced_queued_task_id) = match prepared {
        Some(plan) => match status_dispatch::apply_prepared_status_enqueue(
            &mut tx,
            &request.id,
            &plan.head_sha,
            plan,
        )
        .await
        .map_err(map_dispatch_error)?
        {
            PromotionEnqueueOutcome::Inserted(id) => (Some(id), None),
            PromotionEnqueueOutcome::Coalesced(id) => (None, Some(id)),
        },
        None => (None, None),
    };

    let linked_attachment_ids = if request.attachment_ids.is_empty() {
        None
    } else {
        Some(
            link_create_attachments(&mut tx, workspace_id, &request.id, &request.attachment_ids)
                .await?,
        )
    };

    let prefix = workspace_prefix(&mut tx, workspace_id).await?;
    let issue = map_create_row_to_response(&inserted, &prefix)?;

    tx.commit().await?;

    Ok(IssueCreatePayload {
        issue,
        labels: receipt_labels,
        queued_task_id,
        coalesced_queued_task_id,
        linked_attachment_ids,
    })
}

pub async fn create_issue(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    request: IssueCreateRequest,
) -> Result<IssueCreatePayload, IssueCreateError> {
    let result = create_issue_attempt(pool, workspace_id, actor, &request).await;
    if matches!(result, Err(IssueCreateError::NumberCollision { .. }))
        && heal_issue_counter(pool, workspace_id)
            .await
            .unwrap_or(false)
    {
        return create_issue_attempt(pool, workspace_id, actor, &request).await;
    }
    result
}
