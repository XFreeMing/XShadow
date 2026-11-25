use std::sync::Arc;

use axum::{
    Json, Router,
    extract::{DefaultBodyLimit, Extension, Path, State},
    http::{HeaderMap, HeaderValue, StatusCode},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use serde_json::json;
use sqlx::PgPool;

use super::store::{self, CallerActor, IssueListQuery, IssueReadError, IssueTableQuery, is_uuid};
use super::store::{OrdinaryIssueEdit, OrdinaryIssueEditError};

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
    Router::new()
        .route("/internal/issues", post(list_issues))
        // Go caps the public table body at 1 MiB. Compiling a property value
        // into containment alternatives and escaping it can expand the JSON.
        .route(
            "/internal/issues/table/rows",
            post(table_rows).layer(DefaultBodyLimit::max(16 * 1024 * 1024)),
        )
        .route(
            "/internal/issues/{id}",
            get(get_issue).put(update_ordinary_issue),
        )
        .route_layer(middleware::from_fn_with_state(state.clone(), authorize))
        .with_state(state)
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
        Err(IssueReadError::NotFound(message)) => error_response(StatusCode::NOT_FOUND, message),
        Err(IssueReadError::Forbidden(message)) => error_response(StatusCode::FORBIDDEN, message),
        Err(IssueReadError::ProjectAccessFailed) => error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to resolve project access",
        ),
        Err(IssueReadError::DatabaseFailed) => error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to list table rows",
        ),
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
