use std::{collections::BTreeMap, sync::Arc};

use axum::{
    Json, Router,
    extract::{Extension, Path, Query, State},
    http::{HeaderMap, StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, patch, post},
};
use serde::Deserialize;
use serde_json::{Value, json};
use sqlx::PgPool;

use super::collab::{self, Actor};
use super::store::{self, RecordDto, StoreError};
use super::validate::{StoredFilter, StoredSort};

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
        .route("/internal/tables/{id}", get(get_sheet).patch(rename_table))
        .route("/internal/tables/{id}/fields", post(create_field))
        .route(
            "/internal/tables/{id}/fields/{field_id}",
            patch(rename_field),
        )
        .route("/internal/tables/{id}/records", post(create_record))
        .route(
            "/internal/tables/{id}/records/{record_id}",
            patch(update_record),
        )
        .route("/internal/tables/{id}/changes", get(list_changes))
        .route("/internal/tables/{id}/presence", post(touch_presence))
        .route("/internal/tables/{id}/views", post(create_view))
        .route("/internal/tables/{id}/views/{view_id}", patch(save_view))
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
    body: Result<Json<NameBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    match store::create_table(&app.pool, &ws, &body.name).await {
        Ok(created) => (StatusCode::CREATED, Json(created)).into_response(),
        Err(err) => store_error(err),
    }
}

async fn rename_table(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    body: Result<Json<NameBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    match store::rename_table(&app.pool, &ws, &id, &body.name).await {
        Ok(table) => Json(json!({ "table": table })).into_response(),
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
struct FieldBody {
    name: String,
    kind: String,
    #[serde(default)]
    config: Value,
}

async fn create_field(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    body: Result<Json<FieldBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    match store::create_field(&app.pool, &ws, &id, &body.name, &body.kind, &body.config).await {
        Ok(field) => (StatusCode::CREATED, Json(json!({ "field": field }))).into_response(),
        Err(err) => store_error(err),
    }
}

async fn rename_field(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path((id, field_id)): Path<(String, String)>,
    body: Result<Json<NameBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id).or_else(|| bad_id(&field_id)) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    match store::rename_field(&app.pool, &ws, &id, &field_id, &body.name).await {
        Ok(field) => Json(json!({ "field": field })).into_response(),
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

async fn create_record(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
    Path(id): Path<String>,
    body: Result<Json<RecordBody>, axum::extract::rejection::JsonRejection>,
) -> Response {
    if let Some(response) = bad_id(&id) {
        return response;
    }
    let Ok(Json(body)) = body else {
        return invalid("请求格式不正确");
    };
    match store::create_record(&app.pool, &ws, &id, &body.values).await {
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
struct ViewBody {
    name: String,
    columns: Option<Vec<String>>,
    filter: Option<StoredFilter>,
    sort: Option<StoredSort>,
    #[serde(default, deserialize_with = "group_field")]
    group: Option<Option<String>>,
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
        columns: body.columns.as_deref(),
        filter: body.filter.as_ref(),
        sort: body.sort.as_ref(),
        group: match &body.group {
            None => store::GroupUpdate::Keep,
            Some(value) => store::GroupUpdate::Set(value.as_deref()),
        },
    }
}

async fn create_view(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
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
    match store::create_view(&app.pool, &ws, &id, &view_write(&body)).await {
        Ok(view) => (StatusCode::CREATED, Json(json!({ "view": view }))).into_response(),
        Err(err) => store_error(err),
    }
}

async fn save_view(
    State(app): State<Arc<Tables>>,
    Extension(Workspace(ws)): Extension<Workspace>,
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
    match store::save_view(&app.pool, &ws, &id, &view_id, &view_write(&body)).await {
        Ok(view) => Json(json!({ "view": view })).into_response(),
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
    match err {
        StoreError::NotFound(message) => {
            error_response(StatusCode::NOT_FOUND, "not_found", message, None)
        }
        StoreError::Invalid(message) => invalid(message),
        StoreError::NameTaken => error_response(
            StatusCode::CONFLICT,
            "field_name_taken",
            "这个表里已经有同名字段",
            None,
        ),
        StoreError::Conflict { message, record } => error_response(
            StatusCode::CONFLICT,
            "revision_conflict",
            message,
            Some(record),
        ),
        StoreError::Failed => error_response(
            StatusCode::INTERNAL_SERVER_ERROR,
            "tables_failed",
            "表格暂时无法保存",
            None,
        ),
    }
}

fn invalid(message: &'static str) -> Response {
    error_response(StatusCode::BAD_REQUEST, "invalid_value", message, None)
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

fn is_uuid(value: &str) -> bool {
    let bytes = value.as_bytes();
    if bytes.len() != 36 {
        return false;
    }
    let groups = [8usize, 4, 4, 4, 12];
    let mut index = 0;
    for (group, len) in groups.iter().enumerate() {
        if group > 0 {
            if bytes[index] != b'-' {
                return false;
            }
            index += 1;
        }
        for _ in 0..*len {
            if !bytes[index].is_ascii_hexdigit() {
                return false;
            }
            index += 1;
        }
    }
    index == bytes.len()
}

#[cfg(test)]
mod tests {
    use axum::http::{HeaderMap, HeaderValue};

    use super::{decide_access, require_actor};

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
}
