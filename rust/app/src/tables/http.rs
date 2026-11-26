use std::{collections::BTreeMap, sync::Arc};

use axum::{
    Json, Router,
    body::Bytes,
    extract::{Extension, Path, Query, State},
    http::{HeaderMap, StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use serde::Deserialize;
use serde_json::{Value, json};
use sqlx::PgPool;

use super::batch::{self, BatchError, BatchErrorKind};
use super::collab::{self, Actor};
use super::comments;
use super::history::{self, HistoryError};
use super::store::{self, RecordDto, StoreError};
use super::template::{self, TemplateError, TemplateImportRequest};
use super::validate::{StoredFilter, StoredSort, is_uuid};

struct Tables {
    pool: PgPool,
    token: String,
}

#[derive(Clone)]
struct Workspace(String);

#[derive(Debug)]
struct AuthFailure {
    status: StatusCode,
    code: &'static str,
    message: &'static str,
}

/// Decides access from the internal token and the workspace id the bridge
/// already resolved. A client `X-Workspace-ID` is not an input.
fn decide_access(
    configured_token: &str,
    presented_token: Option<&str>,
    workspace_id: Option<&str>,
) -> Result<String, AuthFailure> {
    if configured_token.is_empty() {
        return Err(AuthFailure {
            status: StatusCode::SERVICE_UNAVAILABLE,
            code: "tables_unavailable",
            message: "表格服务未启用",
        });
    }
    let accepted = presented_token.is_some_and(|token| tokens_match(configured_token, token));
    if !accepted {
        return Err(AuthFailure {
            status: StatusCode::UNAUTHORIZED,
            code: "unauthorized",
            message: "unauthorized",
        });
    }
    match workspace_id.map(str::trim).filter(|id| is_uuid(id)) {
        Some(id) => Ok(id.to_owned()),
        None => Err(AuthFailure {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_workspace",
            message: "工作区标识无效",
        }),
    }
}

pub fn router(pool: PgPool, token: String) -> Router {
    let state = Arc::new(Tables { pool, token });
    Router::new()
        .route("/internal/tables", get(list_tables).post(create_table))
        .route("/internal/tables/templates/import", post(import_template))
        .route("/internal/tables/{id}/template", get(export_template))
        .route(
            "/internal/tables/{id}",
            get(get_sheet).patch(rename_table).delete(delete_table),
        )
        .route(
            "/internal/tables/{id}/fields",
            get(list_fields).post(create_field),
        )
        .route(
            "/internal/tables/{id}/fields/{field_id}",
            get(get_field).patch(update_field).delete(delete_field),
        )
        .route(
            "/internal/tables/{id}/record-options",
            get(list_record_options),
        )
        .route(
            "/internal/tables/{id}/records",
            get(list_records).post(create_record),
        )
        .route("/internal/tables/{id}/records/batch", post(batch_records))
        .route(
            "/internal/tables/{id}/records/{record_id}",
            get(get_record).patch(update_record).delete(delete_record),
        )
        .route(
            "/internal/tables/{id}/records/{record_id}/comments",
            get(list_record_comments).post(create_record_comment),
        )
        .route("/internal/tables/{id}/changes", get(list_changes))
        .route("/internal/tables/{id}/history", get(list_history))
        .route(
            "/internal/tables/{id}/history/snapshot",
            get(get_history_snapshot),
        )
        .route(
            "/internal/tables/{id}/history/deleted",
            get(list_history_deleted),
        )
        .route(
            "/internal/tables/{id}/presence",
            get(get_presence).post(touch_presence),
        )
        .route(
            "/internal/tables/{id}/views",
            get(list_views).post(create_view),
        )
        .route(
            "/internal/tables/{id}/views/{view_id}",
            get(get_view).patch(save_view).delete(delete_view),
        )
        .route(
            "/internal/tables/{id}/views/{view_id}/submit",
            post(submit_form),
        )
        .route_layer(middleware::from_fn_with_state(state.clone(), authorize))
        .with_state(state)
}

async fn authorize(
    State(app): State<Arc<Tables>>,
    headers: HeaderMap,
    mut request: axum::extract::Request,
    next: Next,
) -> Response {
    let presented = headers
        .get("x-xshadow-internal-token")
        .and_then(|value| value.to_str().ok());
    // Only the bridge header is read. `X-Workspace-ID` is ignored on purpose.
    let workspace = headers
        .get("x-xshadow-workspace-id")
        .and_then(|value| value.to_str().ok());
    match decide_access(&app.token, presented, workspace) {
        Ok(id) => {
            request.extensions_mut().insert(Workspace(id));
            next.run(request).await
        }
        Err(failure) => error_response(failure.status, failure.code, failure.message, None),
    }
}

async fn list_tables(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
) -> Response {
    match store::list_tables(&app.pool, &ws).await {
        Ok(tables) => Json(json!({ "tables": tables })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn create_table(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    body: Result<Json<CreateTableBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    if !body.fields.is_empty() {
        return match template::create_with_fields(&app.pool, &ws, body.name, body.fields).await {
            Ok(created) => (StatusCode::CREATED, Json(created)).into_response(),
            Err(err) => template_error(err),
        };
    }
    match store::create_table(&app.pool, &ws, &body.name).await {
        Ok(created) => (StatusCode::CREATED, Json(created)).into_response(),
        Err(err) => store_error(err),
    }
}

#[derive(Deserialize)]
struct TemplateExportQuery {
    sample_record_ids: Option<String>,
}

async fn export_template(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    Query(query): Query<TemplateExportQuery>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let samples = match template::parse_sample_record_ids_query(query.sample_record_ids.as_deref())
    {
        Ok(samples) => samples,
        Err(err) => return template_error(err),
    };
    match template::export_template(&app.pool, &ws, &id, &samples).await {
        Ok(template) => Json(template).into_response(),
        Err(err) => template_error(err),
    }
}

async fn import_template(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    body: Result<Json<TemplateImportRequest>, axum::extract::rejection::JsonRejection>,
) -> Response {
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    let Ok(actor) = require_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match template::import_template(&app.pool, &ws, &body, Some(&actor)).await {
        Ok(created) => (StatusCode::CREATED, Json(created)).into_response(),
        Err(err) => template_error(err),
    }
}

fn template_error(err: TemplateError) -> Response {
    match err {
        TemplateError::Store(err) => store_error(err),
        TemplateError::NotFound(message) => store_error(StoreError::NotFound(message)),
        TemplateError::Invalid { code, message } => (
            StatusCode::BAD_REQUEST,
            Json(json!({ "error": message, "code": code })),
        )
            .into_response(),
        TemplateError::UnsupportedDependency { message, dependencies } => (
            StatusCode::CONFLICT,
            Json(json!({ "error": message, "code": "unsupported_template_dependency", "dependencies": dependencies })),
        )
            .into_response(),
    }
}

async fn rename_table(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path(id): Path<String>,
    body: Result<Json<NameBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::rename_table(&app.pool, &ws, &id, &body.name, actor.as_ref()).await {
        Ok(table) => Json(json!({ "table": table })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn delete_table(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    match store::delete_table(&app.pool, &ws, &id).await {
        Ok(deleted) => Json(deleted).into_response(),
        Err(err) => store_error(err),
    }
}

#[derive(Deserialize)]
struct SheetQuery {
    view: Option<String>,
}

async fn get_sheet(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    Query(query): Query<SheetQuery>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    if let Some(view) = query.view.as_deref()
        && let Some(response) = bad_id(view)
    {
        return response;
    }
    match store::get_sheet(&app.pool, &ws, &id, query.view.as_deref()).await {
        Ok(sheet) => Json(sheet).into_response(),
        Err(err) => store_error(err),
    }
}

#[derive(Deserialize)]
struct NameBody {
    name: String,
}

#[derive(Deserialize)]
struct CreateTableBody {
    name: String,
    #[serde(default)]
    fields: Vec<template::TemplateFieldInput>,
}

#[derive(Deserialize)]
struct FieldBody {
    name: String,
    kind: String,
    #[serde(default)]
    config: Value,
}

async fn list_fields(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    match store::list_fields(&app.pool, &ws, &id).await {
        Ok(fields) => Json(json!({ "fields": fields })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn get_field(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path((id, field_id)): Path<(String, String)>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&field_id)) {
        return response;
    }
    match store::get_field(&app.pool, &ws, &id, &field_id).await {
        Ok(field) => Json(json!({ "field": field })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn create_field(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path(id): Path<String>,
    body: Result<Json<FieldBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::create_field(
        &app.pool,
        &ws,
        &id,
        &body.name,
        &body.kind,
        &body.config,
        actor.as_ref(),
    )
    .await
    {
        Ok(field) => (StatusCode::CREATED, Json(json!({ "field": field }))).into_response(),
        Err(err) => store_error(err),
    }
}

#[derive(Deserialize)]
struct FieldPatchBody {
    #[serde(default)]
    name: Option<String>,
    #[serde(default)]
    config: Option<Value>,
}

async fn update_field(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path((id, field_id)): Path<(String, String)>,
    body: Result<Json<FieldPatchBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&field_id)) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::update_field(
        &app.pool,
        &ws,
        &id,
        &field_id,
        body.name.as_deref(),
        body.config.as_ref(),
        actor.as_ref(),
    )
    .await
    {
        Ok(field) => Json(json!({ "field": field })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn delete_field(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path((id, field_id)): Path<(String, String)>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&field_id)) {
        return response;
    }
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::delete_field(&app.pool, &ws, &id, &field_id, actor.as_ref()).await {
        Ok(deleted) => Json(deleted).into_response(),
        Err(err) => store_error(err),
    }
}

#[derive(Deserialize)]
struct RecordListQuery {
    #[serde(default)]
    view: Option<String>,
    #[serde(default)]
    limit: Option<i64>,
    #[serde(default)]
    offset: Option<i64>,
}

#[derive(Deserialize)]
struct DeleteRecordQuery {
    #[serde(default)]
    expected_revision: Option<i64>,
}

#[derive(Deserialize)]
struct DeleteRecordBody {
    expected_revision: i64,
}

async fn list_records(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    Query(query): Query<RecordListQuery>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    if let Some(view) = query.view.as_deref()
        && let Some(response) = bad_id(view)
    {
        return response;
    }
    match store::list_records(
        &app.pool,
        &ws,
        &id,
        query.view.as_deref(),
        query.limit,
        query.offset,
    )
    .await
    {
        Ok(list) => Json(list).into_response(),
        Err(err) => store_error(err),
    }
}

async fn get_record(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path((id, record_id)): Path<(String, String)>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&record_id)) {
        return response;
    }
    match store::get_record(&app.pool, &ws, &id, &record_id).await {
        Ok(record) => Json(json!({ "record": record })).into_response(),
        Err(err) => store_error(err),
    }
}

#[derive(Deserialize)]
struct RecordBody {
    #[serde(default)]
    values: BTreeMap<String, Value>,
}

#[derive(Deserialize)]
struct RecordPatch {
    expected_revision: i64,
    values: BTreeMap<String, Value>,
}

async fn list_record_options(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    match store::list_record_options(&app.pool, &ws, &id).await {
        Ok(records) => Json(json!({ "records": records })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn create_record(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path(id): Path<String>,
    body: Result<Json<RecordBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    for field_id in body.values.keys() {
        if bad_id(field_id).is_some() {
            return invalid("字段标识无效");
        }
    }
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::create_record(&app.pool, &ws, &id, &body.values, actor.as_ref()).await {
        Ok(record) => (StatusCode::CREATED, Json(json!({ "record": record }))).into_response(),
        Err(err) => store_error(err),
    }
}

async fn update_record(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path((id, record_id)): Path<(String, String)>,
    body: Result<Json<RecordPatch>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&record_id)) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    for field_id in body.values.keys() {
        if bad_id(field_id).is_some() {
            return invalid("字段标识无效");
        }
    }
    let Ok(actor) = require_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::update_record(
        &app.pool,
        &ws,
        &id,
        &record_id,
        body.expected_revision,
        &body.values,
        &actor,
    )
    .await
    {
        Ok(record) => Json(json!({ "record": record })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn delete_record(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path((id, record_id)): Path<(String, String)>,
    Query(query): Query<DeleteRecordQuery>,
    body: Bytes,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&record_id)) {
        return response;
    }
    let body_rev = if body.iter().all(u8::is_ascii_whitespace) {
        None
    } else {
        let Ok(parsed) = serde_json::from_slice::<DeleteRecordBody>(&body) else {
            return invalid("请求格式不正确");
        };
        Some(parsed.expected_revision)
    };
    let expected_revision = match (query.expected_revision, body_rev) {
        (Some(a), Some(b)) if a != b => return invalid("记录版本号不一致"),
        (Some(a), _) => a,
        (None, Some(b)) => b,
        (None, None) => return invalid("删除记录需要提供 expected_revision"),
    };
    if expected_revision <= 0 {
        return invalid("记录版本号无效");
    }
    let Ok(actor) = require_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::delete_record(&app.pool, &ws, &id, &record_id, expected_revision, &actor).await {
        Ok(deleted) => Json(deleted).into_response(),
        Err(err) => store_error(err),
    }
}

async fn batch_records(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path(id): Path<String>,
    body: Result<Json<Value>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(raw)) = body else {
        return invalid("请求格式不正确");
    };
    let Ok(actor) = require_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    let req = match batch::BatchRequest::parse(&raw) {
        Ok(req) => req,
        Err(err) => return batch_error(err),
    };
    match batch::execute_batch(&app.pool, &ws, &id, &actor, &req).await {
        Ok(result) => Json(result).into_response(),
        Err(err) => batch_error(err),
    }
}

#[derive(Deserialize)]
struct SinceQuery {
    #[serde(default)]
    since: i64,
}

async fn list_changes(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    Query(query): Query<SinceQuery>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    match collab::list_since(&app.pool, &ws, &id, query.since).await {
        Ok(snapshot) => Json(snapshot).into_response(),
        Err(err) => store_error(err),
    }
}

#[derive(Deserialize)]
struct PresenceBody {
    #[serde(default)]
    record_id: Option<String>,
    #[serde(default)]
    field_id: Option<String>,
}

async fn get_presence(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    match collab::get_presence(&app.pool, &ws, &id).await {
        Ok(snapshot) => Json(snapshot).into_response(),
        Err(err) => store_error(err),
    }
}

async fn touch_presence(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path(id): Path<String>,
    body: Result<Json<PresenceBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    let Ok(actor) = require_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    let Ok(record_id) = optional_id(body.record_id) else {
        return invalid("标识无效");
    };
    let Ok(field_id) = optional_id(body.field_id) else {
        return invalid("标识无效");
    };
    match collab::touch_presence(
        &app.pool,
        &ws,
        &id,
        &actor,
        record_id.as_deref(),
        field_id.as_deref(),
    )
    .await
    {
        Ok(snapshot) => Json(snapshot).into_response(),
        Err(err) => store_error(err),
    }
}

fn optional_id(value: Option<String>) -> Result<Option<String>, ()> {
    match value {
        None => Ok(None),
        Some(id) if id.is_empty() => Ok(None),
        Some(id) if is_uuid(&id) => Ok(Some(id)),
        Some(_) => Err(()),
    }
}

fn optional_actor(headers: &HeaderMap) -> Result<Option<Actor>, ()> {
    let any_present = [
        "x-xshadow-actor-source",
        "x-xshadow-actor-id",
        "x-xshadow-task-id",
        "x-xshadow-actor-label",
    ]
    .iter()
    .any(|name| !header_str(headers, name).is_empty() || !header_utf8(headers, name).is_empty());
    if !any_present {
        return Ok(None);
    }
    require_actor(headers).map(Some)
}

fn require_actor(headers: &HeaderMap) -> Result<Actor, ()> {
    let source = header_str(headers, "x-xshadow-actor-source");
    let id = header_str(headers, "x-xshadow-actor-id");
    if (source != "agent" && source != "member") || !is_uuid(id) {
        return Err(());
    }
    let task = header_str(headers, "x-xshadow-task-id");
    let task_id = if source == "agent" {
        if !is_uuid(task) {
            return Err(());
        }
        Some(task.to_owned())
    } else {
        None
    };
    Ok(Actor {
        source: source.to_owned(),
        id: id.to_owned(),
        task_id,
        label: clean_label(&header_utf8(headers, "x-xshadow-actor-label"), source, id),
    })
}

fn header_str<'a>(headers: &'a HeaderMap, name: &str) -> &'a str {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .unwrap_or("")
        .trim()
}

/// Labels are UTF-8 names. `HeaderValue::to_str` rejects non-ASCII, so the
/// nickname is decoded from the raw bytes. Identity headers stay on `to_str`.
fn header_utf8(headers: &HeaderMap, name: &str) -> String {
    headers
        .get(name)
        .and_then(|value| std::str::from_utf8(value.as_bytes()).ok())
        .unwrap_or("")
        .trim()
        .to_owned()
}

fn clean_label(raw: &str, source: &str, id: &str) -> String {
    let mut label = String::new();
    for ch in raw.chars() {
        if ch.is_control() {
            continue;
        }
        label.push(ch);
        if label.chars().count() >= 40 {
            break;
        }
    }
    let label = label.trim();
    if !label.is_empty() {
        return label.to_owned();
    }
    let tail: String = id
        .chars()
        .filter(|ch| ch.is_ascii_hexdigit())
        .rev()
        .take(4)
        .collect::<String>()
        .chars()
        .rev()
        .collect();
    if source == "agent" {
        format!("智能体{tail}")
    } else {
        format!("成员{tail}")
    }
}

#[derive(Deserialize)]
struct CommentsQuery {
    #[serde(default)]
    after: Option<i64>,
    #[serde(default)]
    limit: Option<i64>,
}

#[derive(Deserialize)]
struct CommentBody {
    body: String,
    #[serde(default)]
    parent_id: Option<String>,
}

async fn list_record_comments(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path((id, record_id)): Path<(String, String)>,
    Query(query): Query<CommentsQuery>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&record_id)) {
        return response;
    }
    match comments::list_comments(&app.pool, &ws, &id, &record_id, query.after, query.limit).await {
        Ok(list) => Json(list).into_response(),
        Err(err) => store_error(err),
    }
}

async fn create_record_comment(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path((id, record_id)): Path<(String, String)>,
    body: Result<Json<CommentBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&record_id)) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    let Ok(actor) = require_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    let Ok(parent_id) = optional_id(body.parent_id) else {
        return invalid("父评论标识无效");
    };
    match comments::create_comment(
        &app.pool,
        &ws,
        &id,
        &record_id,
        &actor,
        &body.body,
        parent_id.as_deref(),
    )
    .await
    {
        Ok(comment) => (StatusCode::CREATED, Json(json!({ "comment": comment }))).into_response(),
        Err(err) => store_error(err),
    }
}

#[derive(Deserialize)]
struct ViewBody {
    name: String,
    #[serde(default)]
    kind: Option<String>,
    #[serde(default, deserialize_with = "config_field")]
    config: Option<Value>,
    columns: Option<Vec<String>>,
    filter: Option<StoredFilter>,
    sort: Option<StoredSort>,
    #[serde(default, deserialize_with = "group_field")]
    group: Option<Option<String>>,
}

fn config_field<'de, D>(deserializer: D) -> Result<Option<Value>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let value = Value::deserialize(deserializer)?;
    Ok(Some(value))
}

fn group_field<'de, D>(deserializer: D) -> Result<Option<Option<String>>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let value = Option::<String>::deserialize(deserializer)?;
    Ok(Some(value))
}

fn view_write(body: &ViewBody) -> store::ViewWrite<'_> {
    store::ViewWrite {
        name: &body.name,
        kind: body.kind.as_deref(),
        config: match &body.config {
            None => store::ConfigUpdate::Keep,
            Some(value) => store::ConfigUpdate::Set(value),
        },
        columns: body.columns.as_deref(),
        filter: body.filter.as_ref(),
        sort: body.sort.as_ref(),
        group: match &body.group {
            None => store::GroupUpdate::Keep,
            Some(value) => store::GroupUpdate::Set(value.as_deref()),
        },
    }
}

async fn submit_form(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path((id, view_id)): Path<(String, String)>,
    body: Result<Json<RecordBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&view_id)) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    for field_id in body.values.keys() {
        if bad_id(field_id).is_some() {
            return invalid("字段标识无效");
        }
    }
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::submit_form(&app.pool, &ws, &id, &view_id, &body.values, actor.as_ref()).await {
        Ok(record) => (StatusCode::CREATED, Json(json!({ "record": record }))).into_response(),
        Err(err) => store_error(err),
    }
}

async fn list_views(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    match store::list_views(&app.pool, &ws, &id).await {
        Ok(views) => Json(json!({ "views": views })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn get_view(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path((id, view_id)): Path<(String, String)>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&view_id)) {
        return response;
    }
    match store::get_view(&app.pool, &ws, &id, &view_id).await {
        Ok(view) => Json(json!({ "view": view })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn create_view(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path(id): Path<String>,
    body: Result<Json<ViewBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    if let Some(response) = bad_view_ids(&body) {
        return response;
    }
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::create_view(&app.pool, &ws, &id, &view_write(&body), actor.as_ref()).await {
        Ok(view) => (StatusCode::CREATED, Json(json!({ "view": view }))).into_response(),
        Err(err) => store_error(err),
    }
}

async fn save_view(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path((id, view_id)): Path<(String, String)>,
    body: Result<Json<ViewBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&view_id)) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    if let Some(response) = bad_view_ids(&body) {
        return response;
    }
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::save_view(
        &app.pool,
        &ws,
        &id,
        &view_id,
        &view_write(&body),
        actor.as_ref(),
    )
    .await
    {
        Ok(view) => Json(json!({ "view": view })).into_response(),
        Err(err) => store_error(err),
    }
}

async fn delete_view(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    headers: HeaderMap,
    Path((id, view_id)): Path<(String, String)>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&view_id)) {
        return response;
    }
    let Ok(actor) = optional_actor(&headers) else {
        return invalid("协作者身份无效");
    };
    match store::delete_view(&app.pool, &ws, &id, &view_id, actor.as_ref()).await {
        Ok(deleted) => Json(deleted).into_response(),
        Err(err) => store_error(err),
    }
}

fn bad_view_ids(body: &ViewBody) -> Option<Response> {
    if let Some(columns) = &body.columns {
        for id in columns {
            if bad_id(id).is_some() {
                return Some(invalid("可见列里的字段标识无效"));
            }
        }
    }
    if let Some(filter) = &body.filter
        && bad_id(&filter.field_id).is_some()
    {
        return Some(invalid("筛选字段标识无效"));
    }
    if let Some(sort) = &body.sort
        && bad_id(&sort.field_id).is_some()
    {
        return Some(invalid("排序字段标识无效"));
    }
    if let Some(Some(group)) = &body.group
        && bad_id(group).is_some()
    {
        return Some(invalid("分组字段标识无效"));
    }
    None
}

fn store_error(err: StoreError) -> Response {
    let (status, body) = store_error_parts(err);
    (
        status,
        [(header::CONTENT_TYPE, "application/json")],
        body.to_string(),
    )
        .into_response()
}

fn batch_error(err: BatchError) -> Response {
    let (status, mut body) = match err.kind {
        BatchErrorKind::Store(store_err) => store_error_parts(store_err),
        BatchErrorKind::IdempotencyConflict(message) => (
            StatusCode::CONFLICT,
            json!({ "error": message, "code": "idempotency_conflict" }),
        ),
    };
    if let Some(index) = err.index {
        body["failed_index"] = json!(index);
    }
    if let Some(record_id) = err.record_id {
        body["record_id"] = json!(record_id);
    }
    (
        status,
        [(header::CONTENT_TYPE, "application/json")],
        body.to_string(),
    )
        .into_response()
}

fn store_error_parts(err: StoreError) -> (StatusCode, Value) {
    match err {
        StoreError::NotFound(message) => (
            StatusCode::NOT_FOUND,
            json!({ "error": message, "code": "not_found" }),
        ),
        StoreError::Invalid(message) => (
            StatusCode::BAD_REQUEST,
            json!({ "error": message, "code": "invalid_value" }),
        ),
        StoreError::NameTaken => (
            StatusCode::CONFLICT,
            json!({ "error": "这个表里已经有同名字段", "code": "field_name_taken" }),
        ),
        StoreError::Conflict { message, record } => (
            StatusCode::CONFLICT,
            json!({ "error": message, "code": "revision_conflict", "record": *record }),
        ),
        StoreError::ReferenceConflict {
            code,
            message,
            hint,
        } => (
            StatusCode::CONFLICT,
            json!({
                "error": message,
                "code": code,
                "hint": hint,
            }),
        ),
        StoreError::Failed => (
            StatusCode::INTERNAL_SERVER_ERROR,
            json!({ "error": "表格暂时无法保存", "code": "tables_failed" }),
        ),
    }
}

fn invalid(message: &'static str) -> Response {
    error_response(StatusCode::BAD_REQUEST, "invalid_value", message, None)
}

#[derive(Deserialize)]
struct HistoryListQuery {
    limit: Option<i64>,
    cursor: Option<String>,
}

#[derive(Deserialize)]
struct HistorySnapshotQuery {
    revision: Option<i64>,
    timestamp: Option<String>,
}

async fn list_history(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    query: Result<Query<HistoryListQuery>, axum::extract::rejection::QueryRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Query(query)) = query else {
        return invalid("查询参数无效");
    };
    match history::list_history(&app.pool, &ws, &id, query.limit, query.cursor.as_deref()).await {
        Ok(list) => Json(list).into_response(),
        Err(err) => history_error(err),
    }
}

async fn get_history_snapshot(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    query: Result<Query<HistorySnapshotQuery>, axum::extract::rejection::QueryRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Query(query)) = query else {
        return invalid("查询参数无效");
    };
    let target = match history::parse_snapshot_target(query.revision, query.timestamp.as_deref()) {
        Ok(target) => target,
        Err(err) => return history_error(err),
    };
    match history::get_snapshot(&app.pool, &ws, &id, target).await {
        Ok(snapshot) => Json(snapshot).into_response(),
        Err(err) => history_error(err),
    }
}

async fn list_history_deleted(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    query: Result<Query<HistoryListQuery>, axum::extract::rejection::QueryRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Query(query)) = query else {
        return invalid("查询参数无效");
    };
    match history::list_deleted_records(&app.pool, &ws, &id, query.limit, query.cursor.as_deref())
        .await
    {
        Ok(list) => Json(list).into_response(),
        Err(err) => history_error(err),
    }
}

fn history_error(err: HistoryError) -> Response {
    match err {
        HistoryError::NotFound(message) => {
            error_response(StatusCode::NOT_FOUND, "not_found", message, None)
        }
        HistoryError::NotAvailable(message) => error_response(
            StatusCode::NOT_FOUND,
            "history_not_available",
            message,
            None,
        ),
        HistoryError::Invalid(message) => invalid(message),
        HistoryError::Failed => error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "tables_failed",
            "无法读取表格历史",
            None,
        ),
    }
}

fn error_response(
    status: StatusCode,
    code: &'static str,
    message: &'static str,
    record: Option<RecordDto>,
) -> Response {
    let mut body = json!({ "error": message, "code": code });
    if let Some(record) = record {
        body["record"] = json!(record);
    }
    (
        status,
        [(header::CONTENT_TYPE, "application/json")],
        body.to_string(),
    )
        .into_response()
}

fn bad_id(id: &str) -> Option<Response> {
    if is_uuid(id) {
        None
    } else {
        Some(invalid("标识无效"))
    }
}

fn tokens_match(expected: &str, got: &str) -> bool {
    let left = expected.as_bytes();
    let right = got.as_bytes();
    if left.len() != right.len() {
        return false;
    }
    let mut diff = 0u8;
    for (a, b) in left.iter().zip(right) {
        diff |= a ^ b;
    }
    diff == 0
}

#[cfg(test)]
mod tests {
    use axum::http::{HeaderMap, HeaderValue};

    use super::{ViewBody, decide_access, optional_actor, require_actor, view_write};
    use crate::tables::store::ConfigUpdate;

    #[test]
    fn disabled_token_rejects_before_a_client_could_supply_a_workspace() {
        let err = decide_access(
            "",
            Some("secret"),
            Some("11111111-1111-4111-8111-111111111111"),
        )
        .unwrap_err();
        assert_eq!(err.code, "tables_unavailable");
    }

    #[test]
    fn access_reads_only_the_internal_workspace_argument() {
        let workspace = "11111111-1111-4111-8111-111111111111";
        let allowed = decide_access("secret", Some("secret"), Some(workspace)).unwrap();
        assert_eq!(allowed, workspace);
        let missing = decide_access("secret", Some("secret"), None).unwrap_err();
        assert_eq!(missing.code, "invalid_workspace");
        let wrong = decide_access("secret", Some("other"), Some(workspace)).unwrap_err();
        assert_eq!(wrong.code, "unauthorized");
    }

    #[test]
    fn actor_label_keeps_utf8_name() {
        let mut headers = HeaderMap::new();
        headers.insert("x-xshadow-actor-source", HeaderValue::from_static("agent"));
        headers.insert(
            "x-xshadow-actor-id",
            HeaderValue::from_static("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
        );
        headers.insert(
            "x-xshadow-task-id",
            HeaderValue::from_static("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
        );
        headers.insert(
            "x-xshadow-actor-label",
            HeaderValue::from_bytes("核对智能体".as_bytes()).unwrap(),
        );
        let actor = require_actor(&headers).unwrap();
        assert_eq!(actor.label, "核对智能体");
        assert_eq!(actor.id, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa");
    }

    #[test]
    fn optional_actor_preserves_callers_without_actor_headers() {
        let empty = HeaderMap::new();
        assert!(optional_actor(&empty).unwrap().is_none());

        let mut partial = HeaderMap::new();
        partial.insert("x-xshadow-actor-source", HeaderValue::from_static("member"));
        assert!(optional_actor(&partial).is_err());

        partial.insert(
            "x-xshadow-actor-id",
            HeaderValue::from_static("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
        );
        let actor = optional_actor(&partial).unwrap().unwrap();
        assert_eq!(actor.source, "member");
        assert_eq!(actor.id, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa");
    }

    #[test]
    fn view_body_omitting_kind_and_config_keeps_existing_values() {
        let legacy: ViewBody = serde_json::from_str(r#"{"name":"旧视图"}"#).unwrap();
        let write = view_write(&legacy);
        assert_eq!(write.kind, None);
        assert!(matches!(write.config, ConfigUpdate::Keep));

        let explicit: ViewBody =
            serde_json::from_str(r#"{"name":"看板","kind":"kanban","config":{}}"#).unwrap();
        let write = view_write(&explicit);
        assert_eq!(write.kind, Some("kanban"));
        assert!(matches!(write.config, ConfigUpdate::Set(_)));
    }
}
