use std::collections::{HashMap, HashSet};

use chrono::{DateTime, FixedOffset, Local, SecondsFormat};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use sqlx::{PgConnection, PgPool, Row};

mod list;
pub use list::{
    IssueListQuery, IssueTableFacetsQuery, IssueTableGroupsQuery, IssueTableQuery, list_issues,
    table_facets, table_groups, table_rows,
};
mod ordinary;
pub use ordinary::{OrdinaryIssueEdit, OrdinaryIssueEditError, update_ordinary_issue};
mod metadata_write;
pub use metadata_write::{IssueMetadataWrite, IssueMetadataWriteError, write_issue_metadata};
mod property_write;
pub use property_write::{IssuePropertyWrite, IssuePropertyWriteError, write_issue_property};
mod label_write;
pub use label_write::{IssueLabelWrite, IssueLabelWriteError, write_issue_label};
mod acceptance_write;
pub use acceptance_write::{
    IssueAcceptanceWrite, IssueAcceptanceWriteError, write_issue_acceptance_trace,
};
mod status_write;
pub use status_write::{IssueStatusEdit, IssueStatusEditError, update_issue_status};

#[derive(Debug)]
pub enum IssueReadError {
    NotFound(&'static str),
    Forbidden(&'static str),
    ProjectAccessFailed,
    DatabaseFailed,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CallerActor {
    Member {
        user_id: String,
    },
    Agent {
        agent_id: String,
        task_id: String,
        user_id: Option<String>,
    },
}

#[derive(Debug)]
pub struct IssueDetailProjection {
    pub issue: IssueResponse,
    pub has_attachments: bool,
    pub has_source_context: bool,
}

#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct IssueResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub acceptance_trace: Option<AcceptanceTrace>,
    pub id: String,
    pub workspace_id: String,
    pub number: i32,
    pub identifier: String,
    pub title: String,
    pub description: Option<String>,
    pub status: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status_category: Option<String>,
    pub status_name: String,
    pub priority: String,
    pub assignee_type: Option<String>,
    pub assignee_id: Option<String>,
    pub creator_type: String,
    pub creator_id: String,
    pub requester_id: Option<String>,
    pub parent_issue_id: Option<String>,
    pub duplicate_of: Option<IssueRefResponse>,
    pub project_id: Option<String>,
    #[serde(serialize_with = "serialize_go_float")]
    pub position: f64,
    pub stage: Option<i32>,
    pub start_date: Option<String>,
    pub due_date: Option<String>,
    pub created_at: String,
    pub updated_at: String,
    pub revision: i64,
    pub last_activity_at: Option<String>,
    pub metadata: Map<String, Value>,
    pub properties: Map<String, Value>,
    pub issue_type_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reactions: Option<Vec<IssueReactionResponse>>,
    pub labels: Vec<LabelResponse>,
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct IssueRefResponse {
    pub id: String,
    pub identifier: String,
    pub title: String,
    pub status: String,
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct LabelResponse {
    pub id: String,
    pub workspace_id: String,
    pub resource_type: String,
    pub name: String,
    pub description: String,
    pub color: String,
    pub usage_count: i64,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct IssueReactionResponse {
    pub id: String,
    pub issue_id: String,
    pub actor_type: String,
    pub actor_id: String,
    pub emoji: String,
    pub created_at: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct AcceptanceTrace {
    #[serde(default)]
    pub revision: i64,
    pub criteria: Vec<AcceptanceCriterion>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct AcceptanceCriterion {
    #[serde(default)]
    pub id: String,
    pub versions: Vec<String>,
    #[serde(default)]
    pub cases: Option<Vec<AcceptanceTestCase>>,
    #[serde(default)]
    pub implementations: Option<Vec<AcceptanceImplementation>>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct AcceptanceTestCase {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub specification: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub artifact_sha256: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub previous_specifications: Vec<AcceptanceCaseSpecification>,
    #[serde(default)]
    pub runs: Option<Vec<AcceptanceRun>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub retired: Option<AcceptanceRetirement>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct AcceptanceCaseSpecification {
    #[serde(default)]
    pub text: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub artifact_sha256: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct AcceptanceRetirement {
    #[serde(default)]
    pub revision: i64,
    #[serde(default)]
    pub reason: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub superseded_by: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct AcceptanceRun {
    #[serde(default)]
    pub version: i64,
    #[serde(default, skip_serializing_if = "is_zero_i64")]
    pub case_version: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub run_kind: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub expected_result: String,
    #[serde(default)]
    pub result: String,
    #[serde(default)]
    pub candidate: String,
    #[serde(default)]
    pub evidence: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub actor: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub task_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub recorded_at: Option<String>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct AcceptanceImplementation {
    #[serde(default)]
    pub version: i64,
    #[serde(default)]
    pub repository: String,
    #[serde(default)]
    pub branch: String,
    #[serde(default)]
    pub commit: String,
}

fn is_zero_i64(value: &i64) -> bool {
    *value == 0
}

fn serialize_go_float<S: serde::Serializer>(value: &f64, serializer: S) -> Result<S::Ok, S::Error> {
    if value.is_finite() && value.fract() == 0.0 && value.abs() < 9_007_199_254_740_992.0 {
        serializer.serialize_i64(*value as i64)
    } else {
        serializer.serialize_f64(*value)
    }
}

pub fn is_uuid(raw: &str) -> bool {
    let bytes = raw.as_bytes();
    if bytes.len() != 36 {
        return false;
    }
    for (idx, byte) in bytes.iter().enumerate() {
        if matches!(idx, 8 | 13 | 18 | 23) {
            if *byte != b'-' {
                return false;
            }
        } else if !byte.is_ascii_hexdigit() {
            return false;
        }
    }
    true
}

pub fn split_identifier(id: &str) -> Option<(&str, i32)> {
    let idx = id.rfind('-')?;
    if idx == 0 || idx >= id.len() - 1 {
        return None;
    }
    let num_str = &id[idx + 1..];
    let mut num: i64 = 0;
    for byte in num_str.bytes() {
        if !byte.is_ascii_digit() {
            return None;
        }
        num = num * 10 + i64::from(byte - b'0');
        if num > i64::from(i32::MAX) {
            return None;
        }
    }
    if num <= 0 {
        return None;
    }
    Some((&id[..idx], num as i32))
}

pub fn legacy_issue_prefix_from_name(name: &str) -> String {
    let mut letters = String::with_capacity(3);
    for ch in name.chars() {
        if ch.is_ascii_alphabetic() {
            letters.push(ch.to_ascii_uppercase());
            if letters.len() == 3 {
                break;
            }
        }
    }
    if letters.is_empty() {
        "WS".to_owned()
    } else {
        letters
    }
}

fn is_built_in_status(status: &str) -> bool {
    matches!(
        status,
        "backlog" | "todo" | "in_progress" | "in_review" | "blocked" | "done" | "cancelled"
    )
}

fn is_category(category: &str) -> bool {
    matches!(category, "unstarted" | "started" | "done" | "closed")
}

fn parse_category(value: &str) -> Option<&'static str> {
    match value {
        "unstarted" | "backlog" | "todo" => Some("unstarted"),
        "started" | "in_progress" | "in_review" | "blocked" => Some("started"),
        "done" | "completed" => Some("done"),
        "closed" | "canceled" | "cancelled" => Some("closed"),
        _ => None,
    }
}

pub fn wire_category(status: &str, category: &str) -> String {
    let normalized = parse_category(category).unwrap_or(category);
    if is_built_in_status(status) {
        return status.to_owned();
    }
    match normalized {
        "unstarted" => "todo".to_owned(),
        "started" => "in_progress".to_owned(),
        "done" => "done".to_owned(),
        "closed" => "cancelled".to_owned(),
        other => other.to_owned(),
    }
}

fn format_issue_timestamp(raw: String) -> String {
    let Ok(timestamp) = DateTime::parse_from_rfc3339(&raw) else {
        return raw;
    };
    format_timestamp(
        timestamp.with_timezone(&Local).fixed_offset(),
        raw.contains('.'),
    )
}

fn format_timestamp(timestamp: DateTime<FixedOffset>, include_fraction: bool) -> String {
    let formatted = timestamp.to_rfc3339_opts(SecondsFormat::Secs, true);
    let nanoseconds = timestamp.timestamp_subsec_nanos();
    if !include_fraction || nanoseconds == 0 {
        return formatted;
    }
    let offset_start = formatted
        .rfind(['+', '-', 'Z'])
        .expect("RFC3339 includes a time zone");
    let fraction = format!("{nanoseconds:09}");
    let (local_time, offset) = formatted.split_at(offset_start);
    format!("{local_time}.{}{offset}", fraction.trim_end_matches('0'))
}

pub fn json_object_or_empty(raw: Option<&str>) -> Map<String, Value> {
    let Some(raw) = raw else {
        return Map::new();
    };
    if raw.is_empty() {
        return Map::new();
    }
    match serde_json::from_str::<Value>(raw) {
        Ok(Value::Object(map)) => map,
        _ => Map::new(),
    }
}

pub fn decode_acceptance_trace(raw: Option<&str>) -> Option<AcceptanceTrace> {
    let trimmed = raw.map(str::trim).unwrap_or("");
    if trimmed.is_empty() || trimmed == "[]" {
        return Some(AcceptanceTrace {
            revision: 0,
            criteria: Vec::new(),
        });
    }
    let trace: AcceptanceTrace = serde_json::from_str(trimmed).ok()?;
    for criterion in &trace.criteria {
        if criterion.versions.is_empty() {
            return None;
        }
    }
    Some(trace)
}

#[derive(Default)]
struct HiddenProjects {
    ids: HashSet<String>,
}

impl HiddenProjects {
    fn can_see(&self, project_id: Option<&str>) -> bool {
        let Some(project_id) = project_id else {
            return true;
        };
        !self.ids.contains(project_id)
    }
}

struct IssueRow {
    id: String,
    workspace_id: String,
    number: i32,
    title: String,
    description: Option<String>,
    status: String,
    priority: String,
    assignee_type: Option<String>,
    assignee_id: Option<String>,
    creator_type: String,
    creator_id: String,
    requester_user_id: Option<String>,
    parent_issue_id: Option<String>,
    duplicate_of_issue_id: Option<String>,
    project_id: Option<String>,
    position: f64,
    stage: Option<i32>,
    start_date: Option<String>,
    due_date: Option<String>,
    created_at: String,
    updated_at: String,
    revision: i64,
    last_activity_at: Option<String>,
    metadata: Option<String>,
    properties: Option<String>,
    issue_type_id: Option<String>,
    acceptance_criteria: Option<String>,
}

impl IssueRow {
    fn into_response(self, prefix: &str) -> IssueResponse {
        IssueResponse {
            acceptance_trace: None,
            id: self.id,
            workspace_id: self.workspace_id,
            number: self.number,
            identifier: format!("{prefix}-{}", self.number),
            title: self.title,
            description: self.description,
            status: self.status,
            status_category: None,
            status_name: String::new(),
            priority: self.priority,
            assignee_type: self.assignee_type,
            assignee_id: self.assignee_id,
            creator_type: self.creator_type,
            creator_id: self.creator_id,
            requester_id: self.requester_user_id,
            parent_issue_id: self.parent_issue_id,
            duplicate_of: None,
            project_id: self.project_id,
            position: self.position,
            stage: self.stage,
            start_date: self.start_date,
            due_date: self.due_date,
            created_at: self.created_at,
            updated_at: self.updated_at,
            revision: self.revision,
            last_activity_at: self.last_activity_at,
            metadata: json_object_or_empty(self.metadata.as_deref()),
            properties: json_object_or_empty(self.properties.as_deref()),
            issue_type_id: self.issue_type_id,
            reactions: None,
            labels: Vec::new(),
        }
    }
}

const ISSUE_SELECT_COLUMNS: &str = "\
    id::text, \
    workspace_id::text, \
    number, \
    title, \
    description, \
    status, \
    priority, \
    assignee_type, \
    assignee_id::text, \
    creator_type, \
    creator_id::text, \
    requester_user_id::text, \
    parent_issue_id::text, \
    duplicate_of_issue_id::text, \
    project_id::text, \
    position::double precision, \
    stage, \
    to_char(start_date, 'YYYY-MM-DD'), \
    to_char(due_date, 'YYYY-MM-DD'), \
    to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'), \
    to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'), \
    revision, \
    to_char(last_activity_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'), \
    metadata::text, \
    properties::text, \
    issue_type_id::text, \
    acceptance_criteria::text";

fn issue_from_row(row: &sqlx::postgres::PgRow) -> Result<IssueRow, IssueReadError> {
    let try_err = |_| IssueReadError::DatabaseFailed;
    Ok(IssueRow {
        id: row.try_get(0).map_err(try_err)?,
        workspace_id: row.try_get(1).map_err(try_err)?,
        number: row.try_get(2).map_err(try_err)?,
        title: row.try_get(3).map_err(try_err)?,
        description: row.try_get(4).map_err(try_err)?,
        status: row.try_get(5).map_err(try_err)?,
        priority: row.try_get(6).map_err(try_err)?,
        assignee_type: row.try_get(7).map_err(try_err)?,
        assignee_id: row.try_get(8).map_err(try_err)?,
        creator_type: row.try_get(9).map_err(try_err)?,
        creator_id: row.try_get(10).map_err(try_err)?,
        requester_user_id: row.try_get(11).map_err(try_err)?,
        parent_issue_id: row.try_get(12).map_err(try_err)?,
        duplicate_of_issue_id: row.try_get(13).map_err(try_err)?,
        project_id: row.try_get(14).map_err(try_err)?,
        position: row.try_get(15).map_err(try_err)?,
        stage: row.try_get(16).map_err(try_err)?,
        start_date: row.try_get(17).map_err(try_err)?,
        due_date: row.try_get(18).map_err(try_err)?,
        created_at: format_issue_timestamp(row.try_get(19).map_err(try_err)?),
        updated_at: format_issue_timestamp(row.try_get(20).map_err(try_err)?),
        revision: row.try_get(21).map_err(try_err)?,
        last_activity_at: row
            .try_get::<Option<String>, _>(22)
            .map_err(try_err)?
            .map(format_issue_timestamp),
        metadata: row.try_get(23).map_err(try_err)?,
        properties: row.try_get(24).map_err(try_err)?,
        issue_type_id: row.try_get(25).map_err(try_err)?,
        acceptance_criteria: row.try_get(26).map_err(try_err)?,
    })
}

async fn hidden_projects_for_member(
    conn: &mut PgConnection,
    workspace_id: &str,
    user_id: &str,
    role: &str,
) -> Result<HiddenProjects, IssueReadError> {
    if role == "owner" || role == "admin" {
        return Ok(HiddenProjects::default());
    }
    let rows = sqlx::query_scalar::<_, String>(
        "SELECT p.id::text FROM project p
         WHERE p.workspace_id = $1::uuid
           AND p.visibility = 'private'
           AND NOT EXISTS (
               SELECT 1 FROM project_member pm
               WHERE pm.project_id = p.id AND pm.user_id = $2::uuid
           )",
    )
    .bind(workspace_id)
    .bind(user_id)
    .fetch_all(&mut *conn)
    .await
    .map_err(|_| IssueReadError::ProjectAccessFailed)?;
    Ok(HiddenProjects {
        ids: rows.into_iter().collect(),
    })
}

async fn list_private_project_ids(
    conn: &mut PgConnection,
    workspace_id: &str,
) -> Result<Vec<String>, IssueReadError> {
    sqlx::query_scalar::<_, String>(
        "SELECT id::text FROM project
         WHERE workspace_id = $1::uuid AND visibility = 'private'",
    )
    .bind(workspace_id)
    .fetch_all(&mut *conn)
    .await
    .map_err(|_| IssueReadError::ProjectAccessFailed)
}

async fn hidden_projects_for_principals(
    conn: &mut PgConnection,
    workspace_id: &str,
    principals: &[String],
) -> Result<HiddenProjects, IssueReadError> {
    let private = list_private_project_ids(conn, workspace_id).await?;
    if private.is_empty() {
        return Ok(HiddenProjects::default());
    }
    let all_private: HashSet<String> = private.into_iter().collect();
    let mut seen = HashSet::new();
    let mut valid = Vec::new();
    for principal in principals {
        if is_uuid(principal) && seen.insert(principal.as_str()) {
            valid.push(principal.as_str());
        }
    }
    if valid.is_empty() {
        return Ok(HiddenProjects { ids: all_private });
    }
    let mut hidden = HiddenProjects::default();
    for principal in valid {
        let role = sqlx::query_scalar::<_, String>(
            "SELECT role FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid",
        )
        .bind(principal)
        .bind(workspace_id)
        .fetch_optional(&mut *conn)
        .await
        .map_err(|_| IssueReadError::ProjectAccessFailed)?;
        let Some(role) = role else {
            return Ok(HiddenProjects { ids: all_private });
        };
        let one = hidden_projects_for_member(conn, workspace_id, principal, &role).await?;
        hidden.ids.extend(one.ids);
    }
    Ok(hidden)
}

async fn resolve_caller_access_on(
    conn: &mut PgConnection,
    workspace_id: &str,
    actor: &CallerActor,
) -> Result<HiddenProjects, IssueReadError> {
    match actor {
        CallerActor::Member { user_id } => {
            let role = sqlx::query_scalar::<_, String>(
                "SELECT role FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid",
            )
            .bind(user_id)
            .bind(workspace_id)
            .fetch_optional(&mut *conn)
            .await
            .map_err(|_| IssueReadError::ProjectAccessFailed)?;
            let Some(role) = role else {
                return Err(IssueReadError::NotFound("workspace not found"));
            };
            hidden_projects_for_member(conn, workspace_id, user_id, &role).await
        }
        CallerActor::Agent {
            agent_id,
            task_id,
            user_id,
        } => {
            if let Some(uid) = user_id {
                let member_exists = sqlx::query_scalar::<_, bool>(
                    "SELECT EXISTS(
                         SELECT 1 FROM member
                         WHERE user_id = $1::uuid AND workspace_id = $2::uuid
                     )",
                )
                .bind(uid)
                .bind(workspace_id)
                .fetch_one(&mut *conn)
                .await
                .map_err(|_| IssueReadError::ProjectAccessFailed)?;
                if !member_exists {
                    return Err(IssueReadError::NotFound("workspace not found"));
                }
            }

            let task_row = sqlx::query(
                "SELECT agent_id::text, originator_user_id::text, accountable_user_id::text
                 FROM agent_task_queue
                 WHERE id = $1::uuid",
            )
            .bind(task_id)
            .fetch_optional(&mut *conn)
            .await
            .map_err(|_| IssueReadError::ProjectAccessFailed)?;

            let Some(task_row) = task_row else {
                return hidden_projects_for_principals(conn, workspace_id, &[]).await;
            };
            let task_agent_id: String = task_row
                .try_get(0)
                .map_err(|_| IssueReadError::ProjectAccessFailed)?;
            if task_agent_id != *agent_id {
                return Err(IssueReadError::Forbidden(
                    "task token is bound to a different workspace",
                ));
            }

            let private = list_private_project_ids(conn, workspace_id).await?;
            if private.is_empty() {
                return Ok(HiddenProjects::default());
            }

            let agent_row = sqlx::query(
                "SELECT workspace_id::text, owner_id::text
                 FROM agent
                 WHERE id = $1::uuid",
            )
            .bind(agent_id)
            .fetch_optional(&mut *conn)
            .await
            .map_err(|_| IssueReadError::ProjectAccessFailed)?;

            let Some(agent_row) = agent_row else {
                return hidden_projects_for_principals(conn, workspace_id, &[]).await;
            };
            let agent_workspace_id: String = agent_row
                .try_get(0)
                .map_err(|_| IssueReadError::ProjectAccessFailed)?;
            if agent_workspace_id != workspace_id {
                return Err(IssueReadError::Forbidden(
                    "task token is bound to a different workspace",
                ));
            }

            let originator: Option<String> = task_row
                .try_get::<Option<String>, _>(1)
                .map_err(|_| IssueReadError::ProjectAccessFailed)?
                .or(task_row
                    .try_get::<Option<String>, _>(2)
                    .map_err(|_| IssueReadError::ProjectAccessFailed)?);
            let owner: Option<String> = agent_row
                .try_get(1)
                .map_err(|_| IssueReadError::ProjectAccessFailed)?;

            let mut principals = Vec::new();
            if let Some(originator) = originator {
                principals.push(originator);
            }
            if let Some(owner) = owner {
                principals.push(owner);
            }
            hidden_projects_for_principals(conn, workspace_id, &principals).await
        }
    }
}

async fn resolve_caller_access(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
) -> Result<HiddenProjects, IssueReadError> {
    let mut conn = pool
        .acquire()
        .await
        .map_err(|_| IssueReadError::ProjectAccessFailed)?;
    resolve_caller_access_on(&mut conn, workspace_id, actor).await
}

async fn workspace_issue_prefix(
    pool: &PgPool,
    workspace_id: &str,
) -> Result<Option<String>, IssueReadError> {
    let row = sqlx::query("SELECT name, issue_prefix FROM workspace WHERE id = $1::uuid")
        .bind(workspace_id)
        .fetch_optional(pool)
        .await
        .map_err(|_| IssueReadError::DatabaseFailed)?;
    let Some(row) = row else {
        return Ok(None);
    };
    let name: String = row.try_get(0).map_err(|_| IssueReadError::DatabaseFailed)?;
    let issue_prefix: String = row.try_get(1).map_err(|_| IssueReadError::DatabaseFailed)?;
    if !issue_prefix.is_empty() {
        Ok(Some(issue_prefix))
    } else {
        Ok(Some(legacy_issue_prefix_from_name(&name)))
    }
}

async fn load_issue_row(
    pool: &PgPool,
    workspace_id: &str,
    raw_id: &str,
    prefix: &str,
) -> Result<IssueRow, IssueReadError> {
    if let Some((part_prefix, number)) = split_identifier(raw_id)
        && !prefix.is_empty()
        && part_prefix.eq_ignore_ascii_case(prefix)
    {
        let sql = format!(
            "SELECT {ISSUE_SELECT_COLUMNS} FROM issue WHERE workspace_id = $1::uuid AND number = $2"
        );
        let row = sqlx::query(&sql)
            .bind(workspace_id)
            .bind(number)
            .fetch_optional(pool)
            .await
            .map_err(|_| IssueReadError::DatabaseFailed)?;
        if let Some(row) = row {
            return issue_from_row(&row);
        }
    }

    if !is_uuid(raw_id) {
        return Err(IssueReadError::NotFound("issue not found"));
    }

    let sql = format!(
        "SELECT {ISSUE_SELECT_COLUMNS} FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid"
    );
    let row = sqlx::query(&sql)
        .bind(raw_id)
        .bind(workspace_id)
        .fetch_optional(pool)
        .await
        .map_err(|_| IssueReadError::DatabaseFailed)?;
    match row {
        Some(row) => issue_from_row(&row),
        None => Err(IssueReadError::NotFound("issue not found")),
    }
}

async fn resolve_custom_status(
    pool: &PgPool,
    workspace_id: &str,
    status: &str,
) -> (Option<String>, String) {
    if is_built_in_status(status) {
        return (Some(status.to_owned()), String::new());
    }
    let row = sqlx::query(
        "SELECT category, name FROM issue_status
         WHERE workspace_id = $1::uuid AND key = $2",
    )
    .bind(workspace_id)
    .bind(status)
    .fetch_optional(pool)
    .await;
    let Ok(Some(row)) = row else {
        return (None, String::new());
    };
    let category: String = row.try_get(0).unwrap_or_default();
    let name: String = row.try_get(1).unwrap_or_default();
    let status_category = if is_category(&category) {
        let wired = wire_category(status, &category);
        if wired.is_empty() { None } else { Some(wired) }
    } else {
        None
    };
    (status_category, name)
}

async fn resolve_duplicate_of(
    pool: &PgPool,
    workspace_id: &str,
    prefix: &str,
    status: &str,
    duplicate_of_issue_id: Option<&str>,
    hidden: &HiddenProjects,
) -> Option<IssueRefResponse> {
    if status != "cancelled" {
        return None;
    }
    let id = duplicate_of_issue_id?;
    duplicate_issue_refs(pool, workspace_id, prefix, &[id.to_owned()], hidden)
        .await
        .remove(id)
}

async fn duplicate_issue_refs(
    pool: &PgPool,
    workspace_id: &str,
    prefix: &str,
    ids: &[String],
    hidden: &HiddenProjects,
) -> HashMap<String, IssueRefResponse> {
    if ids.is_empty() {
        return HashMap::new();
    }
    let rows = sqlx::query(
        "SELECT id::text, number, title, status, project_id::text
         FROM issue
         WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])",
    )
    .bind(workspace_id)
    .bind(ids)
    .fetch_all(pool)
    .await;
    let Ok(rows) = rows else {
        return HashMap::new();
    };
    rows.into_iter()
        .filter_map(|row| {
            let project_id: Option<String> = row.try_get(4).ok()?;
            if !hidden.can_see(project_id.as_deref()) {
                return None;
            }
            let id: String = row.try_get(0).ok()?;
            let number: i32 = row.try_get(1).ok()?;
            let reference = IssueRefResponse {
                id: id.clone(),
                identifier: format!("{prefix}-{number}"),
                title: row.try_get(2).ok()?,
                status: row.try_get(3).ok()?,
            };
            Some((id, reference))
        })
        .collect()
}

async fn list_labels_for_issue(
    pool: &PgPool,
    workspace_id: &str,
    issue_id: &str,
) -> Vec<LabelResponse> {
    labels_by_issue(pool, workspace_id, &[issue_id.to_owned()])
        .await
        .remove(issue_id)
        .unwrap_or_default()
}

async fn labels_by_issue(
    pool: &PgPool,
    workspace_id: &str,
    issue_ids: &[String],
) -> HashMap<String, Vec<LabelResponse>> {
    if issue_ids.is_empty() {
        return HashMap::new();
    }
    let rows = sqlx::query(
        "SELECT
             l.id::text,
             l.workspace_id::text,
             l.resource_type,
             l.name,
             COALESCE(l.description, ''),
             l.color,
             to_char(l.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
             to_char(l.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
             il.issue_id::text
         FROM issue_label l
         JOIN issue_to_label il ON il.label_id = l.id
         WHERE il.issue_id = ANY($1::uuid[])
           AND l.workspace_id = $2::uuid
           AND l.resource_type = 'issue'
         ORDER BY LOWER(l.name) ASC",
    )
    .bind(issue_ids)
    .bind(workspace_id)
    .fetch_all(pool)
    .await;
    let Ok(rows) = rows else {
        return HashMap::new();
    };
    let mut by_issue = HashMap::<String, Vec<LabelResponse>>::new();
    let labels = rows.into_iter().filter_map(|row| {
        let issue_id = row.try_get(8).ok()?;
        let label = LabelResponse {
            id: row.try_get(0).ok()?,
            workspace_id: row.try_get(1).ok()?,
            resource_type: row.try_get(2).ok()?,
            name: row.try_get(3).ok()?,
            description: row.try_get(4).ok()?,
            color: row.try_get(5).ok()?,
            usage_count: 0,
            created_at: format_issue_timestamp(row.try_get(6).ok()?),
            updated_at: format_issue_timestamp(row.try_get(7).ok()?),
        };
        Some((issue_id, label))
    });
    for (issue_id, label) in labels {
        by_issue.entry(issue_id).or_default().push(label);
    }
    by_issue
}

async fn list_reactions_for_issue(
    pool: &PgPool,
    issue_id: &str,
) -> Result<Option<Vec<IssueReactionResponse>>, IssueReadError> {
    let rows = sqlx::query(
        "SELECT
             id::text,
             issue_id::text,
             actor_type,
             actor_id::text,
             emoji,
             to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')
         FROM issue_reaction
         WHERE issue_id = $1::uuid
         ORDER BY created_at ASC",
    )
    .bind(issue_id)
    .fetch_all(pool)
    .await
    .map_err(|_| IssueReadError::DatabaseFailed)?;
    if rows.is_empty() {
        return Ok(None);
    }
    let mut reactions = Vec::with_capacity(rows.len());
    for row in rows {
        let try_err = |_| IssueReadError::DatabaseFailed;
        reactions.push(IssueReactionResponse {
            id: row.try_get(0).map_err(try_err)?,
            issue_id: row.try_get(1).map_err(try_err)?,
            actor_type: row.try_get(2).map_err(try_err)?,
            actor_id: row.try_get(3).map_err(try_err)?,
            emoji: row.try_get(4).map_err(try_err)?,
            created_at: format_issue_timestamp(row.try_get(5).map_err(try_err)?),
        });
    }
    Ok(Some(reactions))
}

pub async fn get_issue_detail(
    pool: &PgPool,
    workspace_id: &str,
    raw_id: &str,
    actor: &CallerActor,
) -> Result<IssueDetailProjection, IssueReadError> {
    let hidden = resolve_caller_access(pool, workspace_id, actor).await?;
    let prefix = workspace_issue_prefix(pool, workspace_id)
        .await?
        .unwrap_or_default();
    let row = load_issue_row(pool, workspace_id, raw_id, &prefix).await?;

    if !hidden.can_see(row.project_id.as_deref()) {
        return Err(IssueReadError::NotFound("issue not found"));
    }

    let (status_category, status_name) =
        resolve_custom_status(pool, workspace_id, &row.status).await;
    let duplicate_of = resolve_duplicate_of(
        pool,
        workspace_id,
        &prefix,
        &row.status,
        row.duplicate_of_issue_id.as_deref(),
        &hidden,
    )
    .await;
    let labels = list_labels_for_issue(pool, workspace_id, &row.id).await;
    let reactions = list_reactions_for_issue(pool, &row.id).await?;
    let has_attachments = sqlx::query_scalar::<_, bool>(
        "SELECT EXISTS(
             SELECT 1 FROM attachment
             WHERE issue_id = $1::uuid AND workspace_id = $2::uuid
         )",
    )
    .bind(&row.id)
    .bind(workspace_id)
    .fetch_one(pool)
    .await
    .map_err(|_| IssueReadError::DatabaseFailed)?;
    let has_source_context = sqlx::query_scalar::<_, bool>(
        "SELECT EXISTS(
             SELECT 1 FROM issue_source_context
             WHERE issue_id = $1::uuid AND workspace_id = $2::uuid
         )",
    )
    .bind(&row.id)
    .bind(workspace_id)
    .fetch_one(pool)
    .await
    .map_err(|_| IssueReadError::DatabaseFailed)?;

    let acceptance_trace = decode_acceptance_trace(row.acceptance_criteria.as_deref());
    let mut issue = row.into_response(&prefix);
    issue.acceptance_trace = acceptance_trace;
    issue.status_category = status_category;
    issue.status_name = status_name;
    issue.duplicate_of = duplicate_of;
    issue.reactions = reactions;
    issue.labels = labels;

    Ok(IssueDetailProjection {
        issue,
        has_attachments,
        has_source_context,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn split_identifier_handles_valid_and_invalid_inputs() {
        assert_eq!(split_identifier("JIA-42"), Some(("JIA", 42)));
        assert_eq!(split_identifier("trs-134"), Some(("trs", 134)));
        assert_eq!(split_identifier("A-B-9"), Some(("A-B", 9)));
        assert_eq!(split_identifier("JIA-0"), None);
        assert_eq!(split_identifier("-42"), None);
        assert_eq!(split_identifier("JIA-"), None);
        assert_eq!(split_identifier("JIA-abc"), None);
        // UUID whose final segment happens to be all digits exceeds i32::MAX.
        assert_eq!(
            split_identifier("11111111-1111-4111-8111-421234567890"),
            None
        );
    }

    #[test]
    fn legacy_prefix_matches_frozen_go_derivation() {
        assert_eq!(legacy_issue_prefix_from_name("Multica"), "MUL");
        assert_eq!(legacy_issue_prefix_from_name("a1-b"), "AB");
        assert_eq!(legacy_issue_prefix_from_name("12345"), "WS");
        assert_eq!(legacy_issue_prefix_from_name("中文工作区"), "WS");
    }

    #[test]
    fn wire_category_matches_go_semantics() {
        assert_eq!(wire_category("in_review", "started"), "in_review");
        assert_eq!(wire_category("qa_gate", "unstarted"), "todo");
        assert_eq!(wire_category("qa_gate", "started"), "in_progress");
        assert_eq!(wire_category("qa_gate", "done"), "done");
        assert_eq!(wire_category("qa_gate", "closed"), "cancelled");
    }

    #[test]
    fn timestamps_match_go_precision_and_offset() {
        let timestamp = DateTime::parse_from_rfc3339("2026-11-25T17:30:00.123400+08:00").unwrap();
        assert_eq!(
            format_timestamp(timestamp, false),
            "2026-11-25T17:30:00+08:00"
        );
        assert_eq!(
            format_timestamp(timestamp, true),
            "2026-11-25T17:30:00.1234+08:00"
        );
        let utc = DateTime::parse_from_rfc3339("2026-11-25T09:30:00Z").unwrap();
        assert_eq!(format_timestamp(utc, true), "2026-11-25T09:30:00Z");
    }

    #[test]
    fn decode_acceptance_trace_preserves_empty_and_valid_and_rejects_corrupt() {
        let empty = decode_acceptance_trace(Some("[]")).unwrap();
        assert_eq!(empty.revision, 0);
        assert!(empty.criteria.is_empty());

        let none_empty = decode_acceptance_trace(None).unwrap();
        assert_eq!(none_empty, empty);

        let valid = decode_acceptance_trace(Some(
            r#"{"revision":2,"criteria":[{"id":"c1","versions":["v1"],"cases":[],"implementations":[]}]}"#,
        ))
        .unwrap();
        assert_eq!(valid.revision, 2);
        assert_eq!(valid.criteria.len(), 1);

        assert!(
            decode_acceptance_trace(Some(
                r#"{"revision":1,"criteria":[{"id":"c1","versions":[]}]}"#
            ))
            .is_none()
        );
        assert!(decode_acceptance_trace(Some(r#"{"revision":1}"#)).is_none());
    }

    #[test]
    fn issue_response_preserves_zero_false_null_and_omitted_fields() {
        let mut properties = Map::new();
        properties.insert("reads".to_owned(), Value::from(0));
        properties.insert("confirmed".to_owned(), Value::from(false));

        let resp = IssueResponse {
            acceptance_trace: Some(AcceptanceTrace {
                revision: 0,
                criteria: Vec::new(),
            }),
            id: "11111111-1111-4111-8111-111111111111".to_owned(),
            workspace_id: "22222222-2222-4222-8222-222222222222".to_owned(),
            number: 1,
            identifier: "TST-1".to_owned(),
            title: "Task".to_owned(),
            description: None,
            status: "todo".to_owned(),
            status_category: Some("todo".to_owned()),
            status_name: String::new(),
            priority: "none".to_owned(),
            assignee_type: None,
            assignee_id: None,
            creator_type: "member".to_owned(),
            creator_id: "33333333-3333-4333-8333-333333333333".to_owned(),
            requester_id: None,
            parent_issue_id: None,
            duplicate_of: None,
            project_id: None,
            position: 1000.0,
            stage: None,
            start_date: None,
            due_date: None,
            created_at: "2026-11-25T09:00:00Z".to_owned(),
            updated_at: "2026-11-25T09:00:00Z".to_owned(),
            revision: 1,
            last_activity_at: None,
            metadata: Map::new(),
            properties,
            issue_type_id: None,
            reactions: None,
            labels: Vec::new(),
        };

        let encoded = serde_json::to_value(&resp).unwrap();
        let obj = encoded.as_object().unwrap();
        assert_eq!(obj.get("position"), Some(&Value::from(1000)));
        assert_eq!(obj.get("status_name"), Some(&Value::from("")));
        assert_eq!(obj.get("description"), Some(&Value::Null));
        assert_eq!(obj.get("duplicate_of"), Some(&Value::Null));
        assert_eq!(obj.get("issue_type_id"), Some(&Value::Null));
        assert_eq!(obj.get("labels"), Some(&Value::Array(Vec::new())));
        assert!(!obj.contains_key("reactions"));
        assert_eq!(obj["properties"]["reads"], Value::from(0));
        assert_eq!(obj["properties"]["confirmed"], Value::from(false));
    }
}
