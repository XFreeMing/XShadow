use std::{sync::Arc, time::Duration};

use axum::{
    Router,
    extract::{Request, State},
    http::{HeaderValue, StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{MethodFilter, MethodRouter},
};
use chrono::{SecondsFormat, Utc};
use serde::Serialize;
use sqlx::{Connection, PgPool};
use tokio::{
    sync::Mutex,
    time::{Instant, timeout_at},
};

const VERSION: &str = match option_env!("XSHADOW_BUILD_COMMIT") {
    Some(commit) => commit,
    None => "unknown",
};
const MIGRATIONS: &str = include_str!(concat!(env!("OUT_DIR"), "/migration-versions.txt"));
const READINESS_TTL: Duration = Duration::from_secs(3);
const READINESS_TIMEOUT: Duration = Duration::from_secs(2);
const CSP: &str = concat!(
    "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; ",
    "img-src 'self' https: data:; connect-src 'self' wss:; frame-ancestors 'none'; ",
    "object-src 'none'; base-uri 'self'; form-action 'self'"
);

struct Health {
    started_at: String,
    database: PgPool,
    versions: Vec<&'static str>,
    readiness: Mutex<Option<(Instant, Readiness)>>,
}

#[derive(Serialize)]
struct LiveResponse<'a> {
    status: &'static str,
    pid: u32,
    commit: &'static str,
    started_at: &'a str,
}

#[derive(Serialize)]
struct ReadinessResponse {
    status: &'static str,
    checks: ReadinessChecks,
}

#[derive(Serialize)]
struct ReadinessChecks {
    db: &'static str,
    migrations: &'static str,
}

#[derive(Clone, Copy)]
enum Readiness {
    Ready,
    DatabaseUnavailable,
    MigrationQueryFailed,
    MigrationsMissing,
}

impl IntoResponse for Readiness {
    fn into_response(self) -> Response {
        let (db, migrations) = match self {
            Self::Ready => ("ok", "ok"),
            Self::DatabaseUnavailable => ("error", "unknown"),
            Self::MigrationQueryFailed => ("ok", "error"),
            Self::MigrationsMissing => ("ok", "out_of_date"),
        };
        let (code, status) = match self {
            Self::Ready => (StatusCode::OK, "ok"),
            _ => (StatusCode::SERVICE_UNAVAILABLE, "not_ready"),
        };
        json(
            code,
            ReadinessResponse {
                status,
                checks: ReadinessChecks { db, migrations },
            },
        )
    }
}

impl Health {
    async fn readiness(&self) -> Readiness {
        let mut cached = self.readiness.lock().await;
        let now = Instant::now();
        if let Some((expires, result)) = *cached
            && now < expires
        {
            return result;
        }
        let result = self.check_database(now + READINESS_TIMEOUT).await;
        *cached = Some((now + READINESS_TTL, result));
        result
    }

    async fn check_database(&self, deadline: Instant) -> Readiness {
        let Ok(Ok(mut connection)) = timeout_at(deadline, self.database.acquire()).await else {
            return Readiness::DatabaseUnavailable;
        };
        if !matches!(timeout_at(deadline, connection.ping()).await, Ok(Ok(()))) {
            return Readiness::DatabaseUnavailable;
        }

        let count = sqlx::query_scalar::<_, i64>(
            "SELECT COUNT(*) FROM schema_migrations WHERE version = ANY($1)",
        )
        .bind(&self.versions)
        .fetch_one(&mut *connection);
        match timeout_at(deadline, count).await {
            Ok(Ok(applied)) if applied >= self.versions.len() as i64 => Readiness::Ready,
            Ok(Ok(_)) => Readiness::MigrationsMissing,
            _ => Readiness::MigrationQueryFailed,
        }
    }
}

pub fn router(database: PgPool) -> Router {
    let health = Arc::new(Health {
        started_at: Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true),
        database,
        versions: MIGRATIONS.lines().collect(),
        readiness: Mutex::new(None),
    });
    let liveness = MethodRouter::new()
        .on(MethodFilter::GET, live)
        .head(method_not_allowed)
        .fallback(method_not_allowed);
    let readiness = MethodRouter::new()
        .on(MethodFilter::GET, ready)
        .head(method_not_allowed)
        .fallback(method_not_allowed);
    Router::new()
        .route("/health", liveness)
        .route("/readyz", readiness.clone())
        .route("/healthz", readiness)
        .fallback(not_found)
        .layer(middleware::from_fn(probe_headers))
        .with_state(health)
}

async fn live(State(health): State<Arc<Health>>) -> Response {
    json(
        StatusCode::OK,
        LiveResponse {
            status: "ok",
            pid: std::process::id(),
            commit: VERSION,
            started_at: &health.started_at,
        },
    )
}

fn json(status: StatusCode, body: impl Serialize) -> Response {
    let (status, mut bytes) = match serde_json::to_vec(&body) {
        Ok(bytes) => (status, bytes),
        Err(_) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            br#"{"error":"failed to encode response"}"#.to_vec(),
        ),
    };
    bytes.push(b'\n');
    (status, [(header::CONTENT_TYPE, "application/json")], bytes).into_response()
}

async fn ready(State(health): State<Arc<Health>>) -> Readiness {
    health.readiness().await
}

async fn method_not_allowed() -> impl IntoResponse {
    (StatusCode::METHOD_NOT_ALLOWED, [(header::ALLOW, "GET")])
}

async fn not_found() -> impl IntoResponse {
    (
        StatusCode::NOT_FOUND,
        [(header::X_CONTENT_TYPE_OPTIONS, "nosniff")],
        "404 page not found\n",
    )
}

async fn probe_headers(request: Request, next: Next) -> Response {
    let mut response = next.run(request).await;
    let headers = response.headers_mut();
    headers.insert(
        header::CONTENT_SECURITY_POLICY,
        HeaderValue::from_static(CSP),
    );
    headers.insert(header::VARY, HeaderValue::from_static("Origin"));
    response
}
