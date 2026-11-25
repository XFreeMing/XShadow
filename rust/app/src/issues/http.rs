use std::sync::Arc;

use axum::{
    Json, Router,
    extract::{DefaultBodyLimit, Extension, Path, State},
    http::{HeaderMap, HeaderValue, StatusCode},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post, put},
};
use serde_json::json;
use sqlx::PgPool;

use super::store::{
    self, CallerActor, IssueListQuery, IssueReadError, IssueTableFacetsQuery,
    IssueTableGroupsQuery, IssueTableQuery, is_uuid,
};
use super::store::{
    AgentSourceQuickCreateWrite, IssueBatchDeleteWrite, IssueDeleteWrite, IssueDeleteWriteError,
    IssuePropertyWrite, IssuePropertyWriteError,
};
use super::store::{
    CommentResolutionError, IssueAcceptanceWrite, IssueAcceptanceWriteError, IssueAssignEdit,
    IssueAssignEditError, IssueCommentCreate, IssueCommentCreateError, IssueCreateError,
    IssueCreateRequest, IssueMethodAdoptWrite, IssueMethodAdoptWriteError, IssueReactionWrite,
    IssueReactionWriteError, IssueStatusEdit, IssueStatusEditError, IssueWorkflowSquadEdit,
    IssueWorkflowSquadEditError, OrdinaryIssueEdit, OrdinaryIssueEditError,
};
use super::store::{IssueLabelWrite, IssueLabelWriteError};
use super::store::{IssueMetadataWrite, IssueMetadataWriteError};

struct Issues {
    pool: PgPool,
    token: String,
}

#[derive(Clone, Debug)]
struct TrustedContext {
    workspace_id: String,
    actor: CallerActor,
}

#[derive(Debug)]
struct AuthFailure {
    status: StatusCode,
    message: &'static str,
}

/// Validates the internal loopback token, trusted workspace id, and caller actor
/// stamped by the Go gateway. Client `X-Workspace-ID` and `X-User-ID` headers
/// are never read here.
fn decide_access(
    configured_token: &str,
    headers: &HeaderMap,
) -> Result<TrustedContext, AuthFailure> {
    if configured_token.is_empty() {
        return Err(AuthFailure {
            status: StatusCode::SERVICE_UNAVAILABLE,
            message: "rust issue read service unavailable",
        });
    }
    let presented = header_str(headers, "x-xshadow-internal-token");
    if !presented.is_some_and(|token| tokens_match(configured_token, token)) {
        return Err(AuthFailure {
            status: StatusCode::UNAUTHORIZED,
            message: "unauthorized",
        });
    }
    let workspace_id = match header_str(headers, "x-xshadow-workspace-id")
        .map(str::trim)
        .filter(|id| is_uuid(id))
    {
        Some(id) => id.to_owned(),
        None => {
            return Err(AuthFailure {
                status: StatusCode::BAD_REQUEST,
                message: "invalid workspace_id",
            });
        }
    };
    let actor = parse_actor(headers)?;
    Ok(TrustedContext {
        workspace_id,
        actor,
    })
}

fn parse_actor(headers: &HeaderMap) -> Result<CallerActor, AuthFailure> {
    let source = header_str(headers, "x-xshadow-actor-source").map(str::trim);
    let actor_id = header_str(headers, "x-xshadow-actor-id")
        .map(str::trim)
        .filter(|id| is_uuid(id));
    match source {
        Some("member") => {
            let Some(user_id) = actor_id else {
                return Err(AuthFailure {
                    status: StatusCode::UNAUTHORIZED,
                    message: "user not authenticated",
                });
            };
            Ok(CallerActor::Member {
                user_id: user_id.to_owned(),
            })
        }
        Some("agent") => {
            let Some(agent_id) = actor_id else {
                return Err(AuthFailure {
                    status: StatusCode::UNAUTHORIZED,
                    message: "user not authenticated",
                });
            };
            let Some(task_id) = header_str(headers, "x-xshadow-task-id")
                .map(str::trim)
                .filter(|id| is_uuid(id))
            else {
                return Err(AuthFailure {
                    status: StatusCode::UNAUTHORIZED,
                    message: "user not authenticated",
                });
            };
            let user_id = header_str(headers, "x-xshadow-user-id")
                .map(str::trim)
                .filter(|id| is_uuid(id))
                .map(str::to_owned);
            Ok(CallerActor::Agent {
                agent_id: agent_id.to_owned(),
                task_id: task_id.to_owned(),
                user_id,
            })
        }
        _ => Err(AuthFailure {
            status: StatusCode::UNAUTHORIZED,
            message: "user not authenticated",
        }),
    }
}

pub fn router(pool: PgPool, token: String) -> Router {
    let state = Arc::new(Issues { pool, token });
    // Go caps the public table body at 1 MiB. Compiling a property value
    // into containment alternatives and escaping it can expand the JSON.
    let table_body_limit = DefaultBodyLimit::max(16 * 1024 * 1024);
    Router::new()
        .route("/internal/issues", post(list_issues))
        .route(
            "/internal/issues/create",
            post(create_issue).layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/issues/agent-source-quick-create",
            post(create_agent_source_quick_create_handler).layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/issues/agent-source-retry",
            post(retry_agent_source_quick_create_handler).layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/issues/batch-delete",
            post(batch_delete_issues_handler).layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/issues/table/rows",
            post(table_rows).layer(table_body_limit),
        )
        .route(
            "/internal/issues/table/groups",
            post(table_groups).layer(table_body_limit),
        )
        .route(
            "/internal/issues/table/facets",
            post(table_facets).layer(table_body_limit),
        )
        .route(
            "/internal/issues/{id}",
            // The gateway accepted the public text and supplies its editor
            // baseline and catalog seed in this authenticated request.
            get(get_issue)
                .put(update_ordinary_issue)
                .delete(delete_issue_handler)
                .layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/issues/{id}/properties/{property_id}",
            put(write_issue_property),
        )
        .route(
            "/internal/issues/{id}/metadata/{key}",
            put(write_issue_metadata).layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/issues/{id}/labels/{label_id}",
            put(write_issue_label),
        )
        .route(
            "/internal/issues/{id}/acceptance",
            put(write_issue_acceptance).layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/issues/{id}/work-method-adopt",
            put(write_issue_method_adopt).layer(DefaultBodyLimit::disable()),
        )
        .route("/internal/issues/{id}/status", put(update_issue_status))
        .route(
            "/internal/issues/{id}/workflow-squad-handoff",
            put(update_issue_workflow_squad_handoff),
        )
        .route("/internal/issues/{id}/assign", put(update_issue_assign))
        .route(
            "/internal/issues/{id}/comments",
            post(create_issue_comment).layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/issues/{id}/reactions",
            post(add_issue_reaction_handler)
                .delete(remove_issue_reaction_handler)
                .layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/internal/comments/{id}/resolve",
            post(resolve_comment_handler).delete(unresolve_comment_handler),
        )
        .route_layer(middleware::from_fn_with_state(state.clone(), authorize))
        .with_state(state)
}

async fn write_issue_method_adopt(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(request): Json<IssueMethodAdoptWrite>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    match store::write_issue_method_adopt(&app.pool, &ctx.workspace_id, &ctx.actor, &id, request)
        .await
    {
        Ok(result) => Json(result).into_response(),
        Err(IssueMethodAdoptWriteError::RevisionConflict)
        | Err(IssueMethodAdoptWriteError::RunConflict)
        | Err(IssueMethodAdoptWriteError::CatalogConflict)
        | Err(IssueMethodAdoptWriteError::ProjectConflict) => {
            error_response(StatusCode::CONFLICT, "事项已变化，请重新读取")
        }
        Err(IssueMethodAdoptWriteError::Access(error)) => {
            issue_request_error(error, "failed to commit method adoption")
        }
        Err(IssueMethodAdoptWriteError::Database(error)) => {
            eprintln!("rust issue method adopt write failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to commit method adoption",
            )
        }
        Err(IssueMethodAdoptWriteError::Snapshot(error)) => {
            eprintln!("rust issue method adopt snapshot failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to commit method adoption",
            )
        }
        Err(IssueMethodAdoptWriteError::InvalidCommitProof) => {
            eprintln!("rust issue method adopt commit proof invalid; rolling back");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to commit method adoption",
            )
        }
    }
}

async fn write_issue_acceptance(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(request): Json<IssueAcceptanceWrite>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    match store::write_issue_acceptance_trace(
        &app.pool,
        &ctx.workspace_id,
        &ctx.actor,
        &id,
        request,
    )
    .await
    {
        Ok(result) => Json(result).into_response(),
        Err(IssueAcceptanceWriteError::RevisionConflict) => error_response(
            StatusCode::CONFLICT,
            "acceptance records changed; reload before saving",
        ),
        Err(IssueAcceptanceWriteError::Access(error)) => {
            issue_request_error(error, "failed to save acceptance change")
        }
        Err(IssueAcceptanceWriteError::Database(error)) => {
            eprintln!("rust issue acceptance write failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to save acceptance change",
            )
        }
        Err(IssueAcceptanceWriteError::Snapshot(error)) => {
            eprintln!("rust issue acceptance write snapshot failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to save acceptance change",
            )
        }
        Err(IssueAcceptanceWriteError::InvalidCommitProof) => {
            eprintln!("rust issue acceptance write commit proof invalid; rolling back");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to save acceptance change",
            )
        }
    }
}

async fn write_issue_label(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path((id, label_id)): Path<(String, String)>,
    Json(request): Json<IssueLabelWrite>,
) -> Response {
    if !is_uuid(&id) || !is_uuid(&label_id) {
        return error_response(StatusCode::NOT_FOUND, "issue or label not found");
    }
    let failure = if request.detach {
        "failed to detach label"
    } else {
        "failed to attach label"
    };
    match store::write_issue_label(
        &app.pool,
        &ctx.workspace_id,
        &ctx.actor,
        &id,
        &label_id.to_ascii_lowercase(),
        request,
    )
    .await
    {
        Ok(result) => Json(result).into_response(),
        Err(IssueLabelWriteError::LabelNotFound) => {
            error_response(StatusCode::NOT_FOUND, "label not found")
        }
        Err(IssueLabelWriteError::Access(error)) => issue_request_error(error, failure),
        Err(IssueLabelWriteError::Database(error)) => {
            eprintln!("rust issue label write failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, failure)
        }
    }
}

async fn write_issue_metadata(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path((id, key)): Path<(String, String)>,
    Json(request): Json<IssueMetadataWrite>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    let failure = if request.is_delete() {
        "failed to delete metadata key"
    } else {
        "failed to set metadata key"
    };
    match store::write_issue_metadata(&app.pool, &ctx.workspace_id, &ctx.actor, &id, &key, request)
        .await
    {
        Ok(result) => Json(result).into_response(),
        Err(IssueMetadataWriteError::Access(error)) => issue_request_error(error, failure),
        Err(IssueMetadataWriteError::KeyLimit) => {
            error_response(StatusCode::BAD_REQUEST, "metadata cannot exceed 50 keys")
        }
        Err(IssueMetadataWriteError::SizeLimit) => error_response(
            StatusCode::BAD_REQUEST,
            "metadata exceeds the 8KB size limit",
        ),
        Err(IssueMetadataWriteError::Database(error)) => {
            eprintln!("rust issue metadata write failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, failure)
        }
        Err(IssueMetadataWriteError::Json(error)) => {
            eprintln!("rust issue metadata write returned invalid JSON: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, failure)
        }
    }
}

async fn write_issue_property(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path((id, property_id)): Path<(String, String)>,
    Json(request): Json<IssuePropertyWrite>,
) -> Response {
    if !is_uuid(&id) || !is_uuid(&property_id) {
        return error_response(StatusCode::NOT_FOUND, "issue or property not found");
    }
    let failure = if request.is_unset() {
        "failed to unset property"
    } else {
        "failed to set property"
    };
    let property_id = property_id.to_ascii_lowercase();
    match store::write_issue_property(
        &app.pool,
        &ctx.workspace_id,
        &ctx.actor,
        &id,
        &property_id,
        request,
    )
    .await
    {
        Ok(result) => Json(result).into_response(),
        Err(IssuePropertyWriteError::Access(error)) => issue_request_error(error, failure),
        Err(IssuePropertyWriteError::PropertyNotFound) => {
            error_response(StatusCode::NOT_FOUND, "property not found")
        }
        Err(IssuePropertyWriteError::Archived(name)) => (
            StatusCode::BAD_REQUEST,
            Json(json!({ "code": "archived", "name": name })),
        )
            .into_response(),
        Err(IssuePropertyWriteError::Required(name)) => (
            StatusCode::BAD_REQUEST,
            Json(json!({ "code": "required", "name": name })),
        )
            .into_response(),
        Err(IssuePropertyWriteError::DefinitionChanged) => (
            StatusCode::CONFLICT,
            Json(json!({ "code": "definition_changed" })),
        )
            .into_response(),
        Err(IssuePropertyWriteError::SizeLimit) => error_response(
            StatusCode::BAD_REQUEST,
            "issue properties exceed the 16KB size limit",
        ),
        Err(IssuePropertyWriteError::Database(error)) => {
            eprintln!("rust issue property write failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, failure)
        }
        Err(IssuePropertyWriteError::Json(error)) => {
            eprintln!("rust issue property write returned invalid JSON: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, failure)
        }
    }
}

async fn update_issue_assign(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(edit): Json<IssueAssignEdit>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    match store::update_issue_assign(&app.pool, &ctx.workspace_id, &ctx.actor, &id, edit).await {
        Ok(write) => Json(write).into_response(),
        Err(IssueAssignEditError::Access(error)) => {
            issue_request_error(error, "failed to update issue")
        }
        Err(IssueAssignEditError::InvalidAssignee) => {
            error_response(StatusCode::BAD_REQUEST, "invalid assignee for assign pilot")
        }
        Err(IssueAssignEditError::DispatchSensitive) => (
            StatusCode::CONFLICT,
            Json(json!({ "dispatch_sensitive": true })),
        )
            .into_response(),
        Err(IssueAssignEditError::RevisionConflict(actual_revision)) => (
            StatusCode::CONFLICT,
            Json(json!({ "actual_revision": actual_revision })),
        )
            .into_response(),
        Err(IssueAssignEditError::Database(error)) => {
            eprintln!("rust issue assign write failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
        Err(IssueAssignEditError::Snapshot(error)) => {
            eprintln!("rust issue assign write snapshot failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
    }
}

async fn create_issue(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Json(request): Json<IssueCreateRequest>,
) -> Response {
    match store::create_issue(&app.pool, &ctx.workspace_id, &ctx.actor, request).await {
        Ok(result) => (StatusCode::CREATED, Json(result)).into_response(),
        Err(IssueCreateError::ActiveDuplicate(payload)) => (
            StatusCode::CONFLICT,
            Json(payload),
        )
            .into_response(),
        Err(IssueCreateError::Access(error)) => issue_request_error(error, "failed to create issue"),
        Err(IssueCreateError::InvalidInput) => {
            error_response(StatusCode::BAD_REQUEST, "invalid issue create request")
        }
        Err(IssueCreateError::ConfigurationChanged) => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "issue_create_configuration_changed",
                "error": "task configuration changed while creating the task; reload and retry",
            })),
        )
            .into_response(),
        Err(IssueCreateError::DispatchSensitive) => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "issue_create_configuration_changed",
                "error": "task assignment requirements changed; reload and create again",
            })),
        )
            .into_response(),
        Err(IssueCreateError::AgentInvokeForbidden) => error_response(
            StatusCode::FORBIDDEN,
            "you do not have permission to assign work to this agent",
        ),
        Err(IssueCreateError::SquadInvokeForbidden) => error_response(
            StatusCode::FORBIDDEN,
            "you do not have permission to assign work to this squad",
        ),
        Err(IssueCreateError::ParentNotFound) => {
            error_response(StatusCode::BAD_REQUEST, "parent issue not found in this workspace")
        }
        Err(IssueCreateError::ProjectNotFound) => {
            error_response(StatusCode::BAD_REQUEST, "project not found in this workspace")
        }
        Err(IssueCreateError::LabelNotFound) => error_response(
            StatusCode::BAD_REQUEST,
            "one or more labels not found in this workspace",
        ),
        Err(IssueCreateError::InvalidIssueType(message)) => (
            StatusCode::BAD_REQUEST,
            Json(json!({
                "code": "invalid_issue_type",
                "error": message,
            })),
        )
            .into_response(),
        Err(IssueCreateError::InvalidIssueProperty { property_id, message }) => (
            StatusCode::BAD_REQUEST,
            Json(json!({
                "code": "invalid_issue_property",
                "property_id": property_id,
                "error": message,
            })),
        )
            .into_response(),
        Err(IssueCreateError::InvalidAssignee) => error_response(
            StatusCode::BAD_REQUEST,
            "assignee_id does not refer to a member of this workspace",
        ),
        Err(IssueCreateError::PropertiesTooLarge) => error_response(
            StatusCode::BAD_REQUEST,
            "issue properties exceed the 16KB size limit",
        ),
        Err(IssueCreateError::IssueLimit { limit, policy_revision }) => (
            StatusCode::PAYMENT_REQUIRED,
            Json(json!({
                "code": "issue_limit_reached",
                "error": "workspace has reached its issue limit",
                "limit": limit,
                "policy_revision": policy_revision
            })),
        )
            .into_response(),
        Err(IssueCreateError::NumberCollision { allocated }) => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "issue_number_collision",
                "error": "workspace issue counter is behind its highest issue number, so card creation cannot allocate a free number",
                "allocated": allocated,
                "constraint": "uq_issue_workspace_number",
                "repair": "Ask a workspace administrator to repair the issue counter, then retry.",
            })),
        )
            .into_response(),
        Err(IssueCreateError::SourceContextChanged) => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_context_changed",
                "error": "source context changed",
            })),
        )
            .into_response(),
        Err(IssueCreateError::AnchorCommentDeleted) => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "anchor_comment_deleted",
                "error": "anchor comment deleted",
            })),
        )
            .into_response(),
        Err(IssueCreateError::SourceIssueDeleted) => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_issue_deleted",
                "error": "source issue deleted",
            })),
        )
            .into_response(),
        Err(IssueCreateError::SourceContextInvalidPath) => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_context_invalid_path",
                "error": "source context invalid comment thread",
            })),
        )
            .into_response(),
        Err(IssueCreateError::SourceContextAlreadyAttached) => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_context_already_attached",
                "error": "source context is already attached",
            })),
        )
            .into_response(),
        Err(error @ (IssueCreateError::SourceContextQuickCreateUnsupported
            | IssueCreateError::SourceContextQuickCreateUnavailable
            | IssueCreateError::SourceContextRetryUnavailable)) => map_agent_source_quick_create_error(error),
        Err(IssueCreateError::Database(error)) => {
            eprintln!("rust issue create failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to create issue")
        }
    }
}

async fn create_agent_source_quick_create_handler(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Json(request): Json<AgentSourceQuickCreateWrite>,
) -> Response {
    let CallerActor::Member { user_id } = ctx.actor else {
        return error_response(StatusCode::FORBIDDEN, "user not authenticated");
    };
    match store::create_agent_source_quick_create(&app.pool, &ctx.workspace_id, &user_id, request)
        .await
    {
        Ok(result) => (StatusCode::OK, Json(result)).into_response(),
        Err(error) => map_agent_source_quick_create_error(error),
    }
}

async fn retry_agent_source_quick_create_handler(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Json(request): Json<store::AgentSourceRetryWrite>,
) -> Response {
    let CallerActor::Member { user_id } = ctx.actor else {
        return error_response(StatusCode::FORBIDDEN, "user not authenticated");
    };
    match store::retry_agent_source_quick_create(&app.pool, &ctx.workspace_id, &user_id, request)
        .await
    {
        Ok(result) => (StatusCode::OK, Json(result)).into_response(),
        Err(error) => map_agent_source_retry_error(error),
    }
}

fn map_agent_source_retry_error(error: IssueCreateError) -> Response {
    match error {
        IssueCreateError::InvalidInput => error_response(
            StatusCode::BAD_REQUEST,
            "invalid agent source retry request",
        ),
        IssueCreateError::SourceContextRetryUnavailable
        | IssueCreateError::SourceContextQuickCreateUnsupported
        | IssueCreateError::SourceContextQuickCreateUnavailable => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_context_retry_unavailable",
                "error": "This context can no longer be retried. Start again from the branch point.",
            })),
        )
            .into_response(),
        IssueCreateError::Database(error) => {
            eprintln!("rust agent source retry failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to retry agent source quick-create",
            )
        }
        other => map_agent_source_quick_create_error(other),
    }
}

fn map_agent_source_quick_create_error(error: IssueCreateError) -> Response {
    match error {
        IssueCreateError::InvalidInput => error_response(
            StatusCode::BAD_REQUEST,
            "invalid agent source quick-create request",
        ),
        IssueCreateError::SourceContextChanged => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_context_changed",
                "error": "source context changed",
            })),
        )
            .into_response(),
        IssueCreateError::AnchorCommentDeleted => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "anchor_comment_deleted",
                "error": "anchor comment deleted",
            })),
        )
            .into_response(),
        IssueCreateError::SourceIssueDeleted => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_issue_deleted",
                "error": "source issue deleted",
            })),
        )
            .into_response(),
        IssueCreateError::SourceContextInvalidPath => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_context_invalid_path",
                "error": "source context invalid comment thread",
            })),
        )
            .into_response(),
        IssueCreateError::SourceContextQuickCreateUnsupported => (
            StatusCode::UNPROCESSABLE_ENTITY,
            Json(json!({
                "code": "source_context_quick_create_unsupported",
                "error": "selected agent runtime must be updated before using captured context",
            })),
        )
            .into_response(),
        IssueCreateError::SourceContextQuickCreateUnavailable => (
            StatusCode::UNPROCESSABLE_ENTITY,
            Json(json!({
                "code": "agent_unavailable",
                "reason": "the selected agent or runtime changed before the task was queued",
            })),
        )
            .into_response(),
        IssueCreateError::SourceContextRetryUnavailable => (
            StatusCode::CONFLICT,
            Json(json!({
                "code": "source_context_retry_unavailable",
                "error": "This context can no longer be retried. Start again from the branch point.",
            })),
        )
            .into_response(),
        IssueCreateError::Database(error) => {
            eprintln!("rust agent source quick-create failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to enqueue agent source quick-create",
            )
        }
        _ => error_response(
            StatusCode::BAD_REQUEST,
            "invalid agent source quick-create request",
        ),
    }
}

async fn add_issue_reaction_handler(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(request): Json<IssueReactionWrite>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    match store::add_issue_reaction(&app.pool, &ctx.workspace_id, &ctx.actor, &id, request).await {
        Ok(result) => (StatusCode::CREATED, Json(result)).into_response(),
        Err(IssueReactionWriteError::Access(error)) => {
            issue_request_error(error, "failed to add reaction")
        }
        Err(IssueReactionWriteError::InvalidInput) => {
            error_response(StatusCode::BAD_REQUEST, "invalid issue reaction request")
        }
        Err(IssueReactionWriteError::RouteChanged) => {
            (StatusCode::CONFLICT, Json(json!({ "route_changed": true }))).into_response()
        }
        Err(IssueReactionWriteError::Database(error)) => {
            eprintln!("rust issue reaction write failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to add reaction")
        }
    }
}

async fn remove_issue_reaction_handler(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(request): Json<IssueReactionWrite>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    match store::remove_issue_reaction(&app.pool, &ctx.workspace_id, &ctx.actor, &id, request).await
    {
        Ok(result) => Json(result).into_response(),
        Err(IssueReactionWriteError::Access(error)) => {
            issue_request_error(error, "failed to remove reaction")
        }
        Err(IssueReactionWriteError::InvalidInput) => {
            error_response(StatusCode::BAD_REQUEST, "invalid issue reaction request")
        }
        Err(IssueReactionWriteError::RouteChanged) => {
            (StatusCode::CONFLICT, Json(json!({ "route_changed": true }))).into_response()
        }
        Err(IssueReactionWriteError::Database(error)) => {
            eprintln!("rust issue reaction write failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to remove reaction",
            )
        }
    }
}

async fn resolve_comment_handler(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "comment not found");
    }
    match store::resolve_comment(&app.pool, &ctx.workspace_id, &ctx.actor, &id).await {
        Ok(result) => Json(result).into_response(),
        Err(CommentResolutionError::Access(error)) => {
            issue_request_error(error, "failed to resolve comment")
        }
        Err(CommentResolutionError::InvalidInput) => {
            error_response(StatusCode::BAD_REQUEST, "invalid comment resolve request")
        }
        Err(CommentResolutionError::RouteChanged) => {
            (StatusCode::CONFLICT, Json(json!({ "route_changed": true }))).into_response()
        }
        Err(CommentResolutionError::Database(error)) => {
            eprintln!("rust comment resolve failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to resolve comment",
            )
        }
    }
}

async fn unresolve_comment_handler(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "comment not found");
    }
    match store::unresolve_comment(&app.pool, &ctx.workspace_id, &ctx.actor, &id).await {
        Ok(result) => Json(result).into_response(),
        Err(CommentResolutionError::Access(error)) => {
            issue_request_error(error, "failed to unresolve comment")
        }
        Err(CommentResolutionError::InvalidInput) => {
            error_response(StatusCode::BAD_REQUEST, "invalid comment resolve request")
        }
        Err(CommentResolutionError::RouteChanged) => {
            (StatusCode::CONFLICT, Json(json!({ "route_changed": true }))).into_response()
        }
        Err(CommentResolutionError::Database(error)) => {
            eprintln!("rust comment unresolve failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to unresolve comment",
            )
        }
    }
}

async fn create_issue_comment(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(request): Json<IssueCommentCreate>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    match store::create_issue_comment(&app.pool, &ctx.workspace_id, &ctx.actor, &id, request).await
    {
        Ok(result) => {
            let status = if result.created {
                StatusCode::CREATED
            } else {
                StatusCode::OK
            };
            (status, Json(result)).into_response()
        }
        Err(IssueCommentCreateError::Access(error)) => {
            issue_request_error(error, "failed to create comment")
        }
        Err(IssueCommentCreateError::InvalidInput) => {
            error_response(StatusCode::BAD_REQUEST, "invalid comment write request")
        }
        Err(IssueCommentCreateError::RouteChanged) => {
            (StatusCode::CONFLICT, Json(json!({ "route_changed": true }))).into_response()
        }
        Err(IssueCommentCreateError::IdConflict { code }) => {
            let message = match code {
                "comment_id_other_issue" => "comment id is already in use on another issue",
                _ => "comment id is already in use by another author",
            };
            error_response(StatusCode::CONFLICT, message)
        }
        Err(IssueCommentCreateError::Database(error)) => {
            eprintln!("rust issue comment write failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to create comment",
            )
        }
    }
}

async fn update_issue_workflow_squad_handoff(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(edit): Json<IssueWorkflowSquadEdit>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    let batch_scope = edit.workflow_squad_batch.is_some();
    match store::update_issue_workflow_squad_handoff(
        &app.pool,
        &ctx.workspace_id,
        &ctx.actor,
        &id,
        edit,
    )
    .await
    {
        Ok(write) => Json(write).into_response(),
        Err(IssueWorkflowSquadEditError::Access(error)) => {
            issue_request_error(error, "failed to update issue")
        }
        Err(IssueWorkflowSquadEditError::InvalidInput) => error_response(
            StatusCode::BAD_REQUEST,
            "invalid workflow squad handoff for status pilot",
        ),
        Err(IssueWorkflowSquadEditError::DispatchSensitive) => (
            StatusCode::CONFLICT,
            Json(json!({ "dispatch_sensitive": true })),
        )
            .into_response(),
        Err(IssueWorkflowSquadEditError::RevisionConflict(actual_revision)) => (
            StatusCode::CONFLICT,
            Json(json!({ "actual_revision": actual_revision })),
        )
            .into_response(),
        Err(
            error @ (IssueWorkflowSquadEditError::Database(_)
            | IssueWorkflowSquadEditError::Snapshot(_)),
        ) if batch_scope => {
            eprintln!("rust issue batch workflow squad handoff rolled back: {error:?}");
            (
                StatusCode::CONFLICT,
                Json(json!({ "batch_rollback": true })),
            )
                .into_response()
        }
        Err(IssueWorkflowSquadEditError::Database(error)) => {
            eprintln!("rust issue workflow squad write failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
        Err(IssueWorkflowSquadEditError::CommitFailed(error)) => {
            eprintln!("rust issue workflow squad handoff commit failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
        Err(IssueWorkflowSquadEditError::Snapshot(error)) => {
            eprintln!("rust issue workflow squad write snapshot failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
    }
}

async fn update_issue_status(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(edit): Json<IssueStatusEdit>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    let batch_scope = edit.ordinary_active_batch.is_some() || edit.ordinary_terminal_batch;
    match store::update_issue_status(&app.pool, &ctx.workspace_id, &ctx.actor, &id, edit).await {
        Ok(write) => Json(write).into_response(),
        Err(IssueStatusEditError::Access(error)) => {
            issue_request_error(error, "failed to update issue")
        }
        Err(IssueStatusEditError::InvalidStatus) => {
            error_response(StatusCode::BAD_REQUEST, "invalid status for status pilot")
        }
        Err(IssueStatusEditError::DispatchSensitive) => (
            StatusCode::CONFLICT,
            Json(json!({ "dispatch_sensitive": true })),
        )
            .into_response(),
        Err(IssueStatusEditError::RevisionConflict(actual_revision)) => (
            StatusCode::CONFLICT,
            Json(json!({ "actual_revision": actual_revision })),
        )
            .into_response(),
        Err(error @ (IssueStatusEditError::Database(_) | IssueStatusEditError::Snapshot(_)))
            if batch_scope =>
        {
            eprintln!("rust issue batch status rolled back: {error:?}");
            (
                StatusCode::CONFLICT,
                Json(json!({ "batch_rollback": true })),
            )
                .into_response()
        }
        Err(IssueStatusEditError::Database(error)) => {
            eprintln!("rust issue status write failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
        Err(IssueStatusEditError::CommitFailed(error)) => {
            eprintln!("rust issue status commit failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
        Err(IssueStatusEditError::Snapshot(error)) => {
            eprintln!("rust issue status write snapshot failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
    }
}

async fn update_ordinary_issue(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(edit): Json<OrdinaryIssueEdit>,
) -> Response {
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    match store::update_ordinary_issue(&app.pool, &ctx.workspace_id, &ctx.actor, &id, edit).await {
        Ok(write) => Json(write).into_response(),
        Err(OrdinaryIssueEditError::Access(IssueReadError::NotFound(message))) => {
            error_response(StatusCode::NOT_FOUND, message)
        }
        Err(OrdinaryIssueEditError::Access(IssueReadError::Forbidden(message))) => {
            error_response(StatusCode::FORBIDDEN, message)
        }
        Err(OrdinaryIssueEditError::RevisionConflict(actual_revision)) => (
            StatusCode::CONFLICT,
            Json(json!({ "actual_revision": actual_revision })),
        )
            .into_response(),
        Err(OrdinaryIssueEditError::FieldConflict) => (
            StatusCode::CONFLICT,
            Json(json!({ "field_conflict": true })),
        )
            .into_response(),
        Err(error) => {
            match error {
                OrdinaryIssueEditError::Database(error) => {
                    eprintln!("rust ordinary issue edit failed: {error}");
                }
                OrdinaryIssueEditError::Snapshot(error) => {
                    eprintln!("rust ordinary issue edit snapshot failed: {error}");
                }
                _ => {}
            }
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to update issue")
        }
    }
}

async fn table_rows(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Json(query): Json<IssueTableQuery>,
) -> Response {
    match store::table_rows(&app.pool, &ctx.workspace_id, &ctx.actor, query).await {
        Ok(response) => (StatusCode::OK, Json(response)).into_response(),
        Err(error) => issue_request_error(error, "failed to list table rows"),
    }
}

async fn table_groups(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Json(query): Json<IssueTableGroupsQuery>,
) -> Response {
    match store::table_groups(&app.pool, &ctx.workspace_id, &ctx.actor, query).await {
        Ok(response) => Json(response).into_response(),
        Err(error) => issue_request_error(error, "failed to list table groups"),
    }
}

async fn table_facets(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Json(query): Json<IssueTableFacetsQuery>,
) -> Response {
    match store::table_facets(&app.pool, &ctx.workspace_id, &ctx.actor, query).await {
        Ok(response) => Json(response).into_response(),
        Err(error) => issue_request_error(error, "failed to list table facets"),
    }
}

fn issue_request_error(error: IssueReadError, database_message: &'static str) -> Response {
    match error {
        IssueReadError::NotFound(message) => error_response(StatusCode::NOT_FOUND, message),
        IssueReadError::Forbidden(message) => error_response(StatusCode::FORBIDDEN, message),
        IssueReadError::ProjectAccessFailed => error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to resolve project access",
        ),
        IssueReadError::DatabaseFailed => {
            error_response(StatusCode::INTERNAL_SERVER_ERROR, database_message)
        }
    }
}

async fn list_issues(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Json(query): Json<IssueListQuery>,
) -> Response {
    match store::list_issues(&app.pool, &ctx.workspace_id, &ctx.actor, query).await {
        Ok(response) => (StatusCode::OK, Json(response)).into_response(),
        Err(IssueReadError::NotFound(message)) => error_response(StatusCode::NOT_FOUND, message),
        Err(IssueReadError::Forbidden(message)) => error_response(StatusCode::FORBIDDEN, message),
        Err(IssueReadError::ProjectAccessFailed) => error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to resolve project access",
        ),
        Err(IssueReadError::DatabaseFailed) => {
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to list issues")
        }
    }
}

async fn authorize(
    State(app): State<Arc<Issues>>,
    headers: HeaderMap,
    mut request: axum::extract::Request,
    next: Next,
) -> Response {
    match decide_access(&app.token, &headers) {
        Ok(ctx) => {
            request.extensions_mut().insert(ctx);
            next.run(request).await
        }
        Err(failure) => error_response(failure.status, failure.message),
    }
}

async fn batch_delete_issues_handler(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Json(request): Json<IssueBatchDeleteWrite>,
) -> Response {
    if !matches!(ctx.actor, CallerActor::Member { .. }) {
        return error_response(
            StatusCode::FORBIDDEN,
            "issue deletion requires a human or a bound agent identity",
        );
    }
    match store::batch_delete_issues(&app.pool, &ctx.workspace_id, &ctx.actor, request).await {
        Ok(result) => Json(result).into_response(),
        Err(IssueDeleteWriteError::Access(error)) => {
            issue_request_error(error, "failed to batch delete issues")
        }
        Err(IssueDeleteWriteError::AlreadyDeleted) => {
            error_response(StatusCode::NOT_FOUND, "issue not found")
        }
        Err(IssueDeleteWriteError::DeleteSensitive) => (
            StatusCode::CONFLICT,
            Json(json!({ "delete_sensitive": true })),
        )
            .into_response(),
        Err(IssueDeleteWriteError::Database(error)) => {
            eprintln!("rust issue batch delete failed: {error}");
            error_response(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to batch delete issues",
            )
        }
    }
}

async fn delete_issue_handler(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
    Json(request): Json<IssueDeleteWrite>,
) -> Response {
    if !matches!(ctx.actor, CallerActor::Member { .. }) {
        return error_response(
            StatusCode::FORBIDDEN,
            "issue deletion requires a human or a bound agent identity",
        );
    }
    if !is_uuid(&id) {
        return error_response(StatusCode::NOT_FOUND, "issue not found");
    }
    match store::delete_issue(&app.pool, &ctx.workspace_id, &ctx.actor, &id, request).await {
        Ok(result) => Json(result).into_response(),
        Err(IssueDeleteWriteError::Access(error)) => {
            issue_request_error(error, "failed to delete issue")
        }
        Err(IssueDeleteWriteError::AlreadyDeleted) => {
            error_response(StatusCode::NOT_FOUND, "issue not found")
        }
        Err(IssueDeleteWriteError::DeleteSensitive) => (
            StatusCode::CONFLICT,
            Json(json!({ "delete_sensitive": true })),
        )
            .into_response(),
        Err(IssueDeleteWriteError::Database(error)) => {
            eprintln!("rust issue delete failed: {error}");
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to delete issue")
        }
    }
}

async fn get_issue(
    State(app): State<Arc<Issues>>,
    Extension(ctx): Extension<TrustedContext>,
    Path(id): Path<String>,
) -> Response {
    match store::get_issue_detail(&app.pool, &ctx.workspace_id, &id, &ctx.actor).await {
        Ok(projection) => {
            let mut response = (StatusCode::OK, Json(projection.issue)).into_response();
            let headers = response.headers_mut();
            headers.insert("x-xshadow-issue-backend", HeaderValue::from_static("rust"));
            if projection.has_attachments {
                headers.insert(
                    "x-xshadow-enrich-attachments",
                    HeaderValue::from_static("1"),
                );
            }
            if projection.has_source_context {
                headers.insert(
                    "x-xshadow-enrich-source-context",
                    HeaderValue::from_static("1"),
                );
            }
            response
        }
        Err(IssueReadError::NotFound(message)) => error_response(StatusCode::NOT_FOUND, message),
        Err(IssueReadError::Forbidden(message)) => error_response(StatusCode::FORBIDDEN, message),
        Err(IssueReadError::ProjectAccessFailed) => error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to resolve project access",
        ),
        Err(IssueReadError::DatabaseFailed) => {
            error_response(StatusCode::INTERNAL_SERVER_ERROR, "failed to load issue")
        }
    }
}

fn header_str<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers.get(name).and_then(|value| value.to_str().ok())
}

fn error_response(status: StatusCode, message: &str) -> Response {
    (status, Json(json!({ "error": message }))).into_response()
}

fn tokens_match(expected: &str, presented: &str) -> bool {
    let left = expected.as_bytes();
    let right = presented.as_bytes();
    let mut diff = left.len() ^ right.len();
    for (a, b) in left.iter().zip(right.iter()) {
        diff |= usize::from(a ^ b);
    }
    diff == 0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn access_rejects_missing_or_wrong_token_and_ignores_client_workspace_header() {
        let mut headers = HeaderMap::new();
        headers.insert(
            "x-workspace-id",
            HeaderValue::from_static("11111111-1111-4111-8111-111111111111"),
        );
        headers.insert(
            "x-xshadow-internal-token",
            HeaderValue::from_static("secret"),
        );
        headers.insert("x-xshadow-actor-source", HeaderValue::from_static("member"));
        headers.insert(
            "x-xshadow-actor-id",
            HeaderValue::from_static("22222222-2222-4222-8222-222222222222"),
        );
        let err = decide_access("secret", &headers).unwrap_err();
        assert_eq!(err.status, StatusCode::BAD_REQUEST);

        headers.insert(
            "x-xshadow-workspace-id",
            HeaderValue::from_static("11111111-1111-4111-8111-111111111111"),
        );
        let ctx = decide_access("secret", &headers).unwrap();
        assert_eq!(ctx.workspace_id, "11111111-1111-4111-8111-111111111111");
        assert_eq!(
            ctx.actor,
            CallerActor::Member {
                user_id: "22222222-2222-4222-8222-222222222222".to_owned()
            }
        );

        let wrong = decide_access("other", &headers).unwrap_err();
        assert_eq!(wrong.status, StatusCode::UNAUTHORIZED);
    }

    #[test]
    fn access_requires_task_id_for_agent_actor() {
        let mut headers = HeaderMap::new();
        headers.insert(
            "x-xshadow-internal-token",
            HeaderValue::from_static("secret"),
        );
        headers.insert(
            "x-xshadow-workspace-id",
            HeaderValue::from_static("11111111-1111-4111-8111-111111111111"),
        );
        headers.insert("x-xshadow-actor-source", HeaderValue::from_static("agent"));
        headers.insert(
            "x-xshadow-actor-id",
            HeaderValue::from_static("22222222-2222-4222-8222-222222222222"),
        );
        let err = decide_access("secret", &headers).unwrap_err();
        assert_eq!(err.status, StatusCode::UNAUTHORIZED);

        headers.insert(
            "x-xshadow-task-id",
            HeaderValue::from_static("33333333-3333-4333-8333-333333333333"),
        );
        let ctx = decide_access("secret", &headers).unwrap();
        assert_eq!(
            ctx.actor,
            CallerActor::Agent {
                agent_id: "22222222-2222-4222-8222-222222222222".to_owned(),
                task_id: "33333333-3333-4333-8333-333333333333".to_owned(),
                user_id: None,
            }
        );
    }
}
