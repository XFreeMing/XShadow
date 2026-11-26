use std::collections::{BTreeMap, HashSet};

use serde_json::value::RawValue;
use sqlx::{PgConnection, Row};

use super::create_write::{IssueCreateError, IssueCreateRequest};
use super::{
    CallerActor, IssueReadError, LabelResponse, format_issue_timestamp, is_uuid,
    resolve_caller_access_on,
};

pub(super) async fn validate_labels_before_number(
    conn: &mut PgConnection,
    workspace_id: &str,
    label_ids: &[String],
) -> Result<Vec<LabelResponse>, IssueCreateError> {
    let mut seen = HashSet::new();
    let mut labels = Vec::new();
    for label_id in label_ids {
        if !is_uuid(label_id) {
            return Err(IssueCreateError::LabelNotFound);
        }
        if !seen.insert(label_id) {
            continue;
        }
        let row = sqlx::query(
            "SELECT id::text, workspace_id::text, resource_type, name,
                    COALESCE(description, ''), color,
                    to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
                    to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')
             FROM issue_label
             WHERE id = $1::uuid AND workspace_id = $2::uuid AND resource_type = 'issue'
             FOR SHARE",
        )
        .bind(label_id)
        .bind(workspace_id)
        .fetch_optional(&mut *conn)
        .await?
        .ok_or(IssueCreateError::LabelNotFound)?;
        labels.push(LabelResponse {
            id: row.try_get(0)?,
            workspace_id: row.try_get(1)?,
            resource_type: row.try_get(2)?,
            name: row.try_get(3)?,
            description: row.try_get(4)?,
            color: row.try_get(5)?,
            usage_count: 0,
            created_at: format_issue_timestamp(row.try_get(6)?),
            updated_at: format_issue_timestamp(row.try_get(7)?),
        });
    }
    Ok(labels)
}

#[derive(Clone, Copy)]
enum ParentIssueLock {
    Unlocked,
    Share,
    Update,
}

async fn lock_project_row_member_grant(
    conn: &mut PgConnection,
    workspace_id: &str,
    member_id: &str,
    privileged: bool,
    project_id: &str,
    missing: IssueCreateError,
) -> Result<(String, bool), IssueCreateError> {
    let Some((visibility, has_workflow)) = sqlx::query_as::<_, (String, bool)>(
        "SELECT visibility, workflow_id IS NOT NULL
         FROM project WHERE id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
    )
    .bind(project_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await?
    else {
        return Err(missing);
    };
    if visibility == "private" && !privileged {
        let access = sqlx::query_scalar::<_, i32>(
            "SELECT 1 FROM project_member WHERE project_id = $1::uuid AND user_id = $2::uuid FOR SHARE",
        )
        .bind(project_id)
        .bind(member_id)
        .fetch_optional(&mut *conn)
        .await?;
        if access.is_none() {
            return Err(missing);
        }
    }
    Ok((visibility, has_workflow))
}

pub(super) async fn lock_source_inherited_project(
    conn: &mut PgConnection,
    workspace_id: &str,
    member_id: &str,
    project_id: &str,
    missing: IssueCreateError,
) -> Result<(), IssueCreateError> {
    if !is_uuid(project_id) {
        return Err(missing);
    }
    let role = sqlx::query_scalar::<_, String>(
        "SELECT role FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
    )
    .bind(member_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await?;
    let Some(role) = role else {
        return Err(missing);
    };
    let privileged = matches!(role.as_str(), "owner" | "admin");
    lock_project_row_member_grant(
        conn,
        workspace_id,
        member_id,
        privileged,
        project_id,
        missing,
    )
    .await?;
    Ok(())
}

async fn read_parent_project(
    conn: &mut PgConnection,
    workspace_id: &str,
    parent: &str,
    lock: ParentIssueLock,
    missing: IssueCreateError,
) -> Result<Option<String>, IssueCreateError> {
    let sql = match lock {
        ParentIssueLock::Unlocked => {
            "SELECT project_id::text FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid"
        }
        ParentIssueLock::Share => {
            "SELECT project_id::text FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid FOR SHARE"
        }
        ParentIssueLock::Update => {
            "SELECT project_id::text FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE"
        }
    };
    sqlx::query_scalar(sql)
        .bind(parent)
        .bind(workspace_id)
        .fetch_optional(conn)
        .await?
        .ok_or(missing)
}

pub(super) async fn guard_parent_projects_and_assignee(
    conn: &mut PgConnection,
    workspace_id: &str,
    member_id: &str,
    request: &IssueCreateRequest,
) -> Result<Option<String>, IssueCreateError> {
    let role = sqlx::query_scalar::<_, String>(
        "SELECT role FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
    )
    .bind(member_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await?
    .ok_or(IssueCreateError::Access(IssueReadError::NotFound(
        "workspace not found",
    )))?;
    let privileged = matches!(role.as_str(), "owner" | "admin");
    let source_capture = request.source_context_create_trust.is_some();
    let missing_parent = || {
        if source_capture {
            IssueCreateError::SourceIssueDeleted
        } else {
            IssueCreateError::ParentNotFound
        }
    };

    let parent_project = match request.parent_issue_id.as_deref() {
        Some(parent) if is_uuid(parent) => {
            read_parent_project(
                conn,
                workspace_id,
                parent,
                ParentIssueLock::Unlocked,
                missing_parent(),
            )
            .await?
        }
        Some(_) => return Err(IssueCreateError::ParentNotFound),
        None => None,
    };
    let effective = request.project_id.as_deref().or(parent_project.as_deref());
    if let Some(project) = effective
        && !is_uuid(project)
    {
        return Err(IssueCreateError::ProjectNotFound);
    }

    let mut projects = [parent_project.as_deref(), effective];
    projects.sort_unstable();
    let mut previous = None;
    for project in projects.into_iter().flatten() {
        if previous == Some(project) {
            continue;
        }
        previous = Some(project);
        let is_parent = parent_project.as_deref() == Some(project);
        let (_, has_workflow) = lock_project_row_member_grant(
            conn,
            workspace_id,
            member_id,
            privileged,
            project,
            if is_parent {
                IssueCreateError::ParentNotFound
            } else {
                IssueCreateError::ProjectNotFound
            },
        )
        .await?;
        if has_workflow && effective == Some(project) {
            return Err(IssueCreateError::ConfigurationChanged);
        }
    }

    // The workflow catalog's shared advisory lock also protects creation of a
    // previously unstored Default. Project locks precede the parent issue lock.
    let default_accepts = sqlx::query_scalar::<_, bool>(
        "SELECT EXISTS (
            SELECT 1 FROM jsonb_array_elements(w.steps) AS step
            WHERE step->>'status_key' = $2
              AND ($3 OR COALESCE(step->'handler'->>'type', '')
                    NOT IN ('agent', 'squad', 'member', 'project_lead', 'creator'))
         ) FROM issue_workflow w WHERE workspace_id = $1::uuid AND is_default FOR SHARE OF w",
    )
    .bind(workspace_id)
    .bind(&request.status)
    .bind(effective.is_none() || request.assignee_id.is_some())
    .fetch_optional(&mut *conn)
    .await?;
    if default_accepts == Some(false) {
        return Err(IssueCreateError::ConfigurationChanged);
    }
    // Source capture needs an exclusive issue lock to serialize comment and
    // attachment edits. Taking it here avoids two creators upgrading SHARE.
    let parent_lock = if source_capture {
        ParentIssueLock::Update
    } else {
        ParentIssueLock::Share
    };
    if let Some(parent) = request.parent_issue_id.as_deref() {
        let locked_project =
            read_parent_project(conn, workspace_id, parent, parent_lock, missing_parent()).await?;
        if locked_project != parent_project {
            if source_capture && let Some(project) = locked_project.as_deref() {
                let actor = CallerActor::Member {
                    user_id: member_id.to_owned(),
                };
                let hidden = resolve_caller_access_on(conn, workspace_id, &actor)
                    .await
                    .map_err(IssueCreateError::Access)?;
                if !hidden.can_see(Some(project)) {
                    return Err(IssueCreateError::AnchorCommentDeleted);
                }
            }
            return Err(IssueCreateError::ConfigurationChanged);
        }
    }
    if let (Some(assignee_type), Some(assignee)) = (
        request.assignee_type.as_deref(),
        request.assignee_id.as_deref(),
    ) {
        match assignee_type {
            "member" => {
                if !lock_member(conn, workspace_id, assignee).await? {
                    return Err(IssueCreateError::InvalidAssignee);
                }
            }
            // Direct dispatch validates its target and binding together in the writer.
            "agent" | "squad" => {}
            _ => return Err(IssueCreateError::InvalidAssignee),
        }
    }
    Ok(effective.map(str::to_owned))
}

async fn lock_member(
    conn: &mut PgConnection,
    workspace_id: &str,
    member: &str,
) -> Result<bool, sqlx::Error> {
    Ok(sqlx::query_scalar::<_, i32>(
        "SELECT 1 FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid FOR KEY SHARE",
    )
    .bind(member)
    .bind(workspace_id)
    .fetch_optional(conn)
    .await?
    .is_some())
}

pub(super) async fn revalidate_type_and_properties(
    conn: &mut PgConnection,
    workspace_id: &str,
    request: &IssueCreateRequest,
) -> Result<(), IssueCreateError> {
    let properties: BTreeMap<String, &RawValue> = serde_json::from_str(request.properties.get())
        .map_err(|_| IssueCreateError::InvalidInput)?;
    if properties.len() != request.property_versions.len() {
        return Err(IssueCreateError::InvalidInput);
    }
    if let Some(type_id) = request.issue_type_id.as_deref() {
        if !is_uuid(type_id) {
            return Err(IssueCreateError::InvalidIssueType("issue type not found"));
        }
        lock_definition(conn, format!("issue_types:{workspace_id}")).await?;
        let (version, archived) = sqlx::query_as::<_, (String, bool)>(
            "SELECT xmin::text, archived_at IS NOT NULL
             FROM issue_type WHERE id = $1::uuid AND workspace_id = $2::uuid",
        )
        .bind(type_id)
        .bind(workspace_id)
        .fetch_optional(&mut *conn)
        .await?
        .ok_or(IssueCreateError::InvalidIssueType("issue type not found"))?;
        if archived {
            return Err(IssueCreateError::InvalidIssueType("issue type is archived"));
        }
        if Some(&version) != request.issue_type_version.as_ref() {
            return Err(IssueCreateError::ConfigurationChanged);
        }
    }

    for property_id in request.property_versions.keys() {
        if !is_uuid(property_id) {
            return Err(IssueCreateError::InvalidInput);
        }
        lock_definition(conn, format!("prop:{property_id}")).await?;
    }
    for (property_id, expected_version) in &request.property_versions {
        let value = properties
            .get(property_id)
            .ok_or(IssueCreateError::InvalidInput)?;
        let (version, kind, archived, config) =
            sqlx::query_as::<_, (String, String, bool, String)>(
                "SELECT xmin::text, type, archived_at IS NOT NULL, config::text
             FROM issue_property WHERE id = $1::uuid AND workspace_id = $2::uuid",
            )
            .bind(property_id)
            .bind(workspace_id)
            .fetch_optional(&mut *conn)
            .await?
            .ok_or(IssueCreateError::ConfigurationChanged)?;
        if archived || version != *expected_version {
            return Err(IssueCreateError::ConfigurationChanged);
        }
        match kind.as_str() {
            "actor" => {
                let actor: String = serde_json::from_str(value.get())
                    .map_err(|_| IssueCreateError::InvalidInput)?;
                validate_actor(conn, workspace_id, property_id, &actor).await?;
            }
            "multi_actor" => {
                let actors: Vec<String> = serde_json::from_str(value.get())
                    .map_err(|_| IssueCreateError::InvalidInput)?;
                for actor in actors {
                    validate_actor(conn, workspace_id, property_id, &actor).await?;
                }
            }
            "record_relation" => {
                super::property_validate::validate_record_relation_value(
                    &mut *conn,
                    workspace_id,
                    &config,
                    value,
                )
                .await
                .map_err(|error| match error {
                    super::property_validate::RecordRelationError::Invalid => {
                        IssueCreateError::InvalidIssueProperty {
                            property_id: property_id.to_owned(),
                            message:
                                "one or more record ids are invalid for the configured target table"
                                    .to_owned(),
                        }
                    }
                    super::property_validate::RecordRelationError::Database(error) => {
                        IssueCreateError::Database(error)
                    }
                })?;
            }
            _ => {}
        }
    }
    Ok(())
}

async fn lock_definition(conn: &mut PgConnection, key: String) -> Result<(), sqlx::Error> {
    sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))")
        .bind(key)
        .execute(conn)
        .await?;
    Ok(())
}

async fn validate_actor(
    conn: &mut PgConnection,
    workspace_id: &str,
    property_id: &str,
    actor: &str,
) -> Result<(), IssueCreateError> {
    let member = actor
        .strip_prefix("member:")
        .filter(|id| is_uuid(id))
        .ok_or(IssueCreateError::InvalidInput)?;
    if !lock_member(conn, workspace_id, member).await? {
        return Err(IssueCreateError::InvalidIssueProperty {
            property_id: property_id.to_owned(),
            message: format!("{actor:?} does not refer to a member of this workspace"),
        });
    }
    Ok(())
}
