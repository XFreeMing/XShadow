use std::collections::{BTreeMap, HashMap};

use serde::Serialize;
use serde_json::Value;
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::validate::{self, FieldDef, StoredFilter, StoredSort, StoredView};
use super::{projection, relation};

#[derive(Debug)]
pub enum StoreError {
    NotFound(&'static str),
    Conflict {
        message: &'static str,
        record: Box<RecordDto>,
    },
    Invalid(&'static str),
    NameTaken,
    Failed,
}

#[derive(Clone, Serialize)]
pub struct TableDto {
    pub id: String,
    pub name: String,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Clone, Serialize)]
pub struct FieldDto {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub config: Value,
    pub position: i32,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Clone, Debug, Serialize)]
pub struct RecordDto {
    pub id: String,
    pub revision: i64,
    pub created_at: String,
    pub updated_at: String,
    pub values: BTreeMap<String, Value>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub errors: BTreeMap<String, String>,
}

#[derive(Clone, Serialize)]
pub struct ViewDto {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub config: Value,
    pub columns: Option<Vec<String>>,
    pub filter: Option<StoredFilter>,
    pub sort: Option<StoredSort>,
    #[serde(rename = "group")]
    pub group: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Serialize)]
pub struct SheetDto {
    pub table: TableDto,
    pub fields: Vec<FieldDto>,
    pub views: Vec<ViewDto>,
    pub records: Vec<RecordDto>,
    pub active_view_id: Option<String>,
}

#[derive(Clone, Debug, Serialize)]
pub struct RecordOptionDto {
    pub id: String,
    pub label: String,
}

#[derive(Serialize)]
pub struct TableCreated {
    pub table: TableDto,
    pub view: ViewDto,
}

const TS: &str = "to_char(%s AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')";

pub(super) fn ts(column: &str) -> String {
    TS.replace("%s", column)
}

pub async fn list_tables(pool: &PgPool, workspace: &str) -> Result<Vec<TableDto>, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, name, {created}, {updated}
         FROM generic_table
         WHERE workspace_id = $1::uuid
         ORDER BY created_at DESC, id DESC"
    );
    let rows = sqlx::query(&sql)
        .bind(workspace)
        .fetch_all(pool)
        .await
        .map_err(failed)?;
    rows.iter().map(table_from_row).collect()
}

pub async fn create_table(
    pool: &PgPool,
    workspace: &str,
    name: &str,
) -> Result<TableCreated, StoreError> {
    let name = validate::clean_name(name).map_err(StoreError::Invalid)?;
    let mut tx = begin(pool).await?;
    let table = insert_table(&mut tx, workspace, &name).await?;
    let view = insert_view(
        &mut tx,
        workspace,
        &table.id,
        &StoredView {
            name: "全部".to_owned(),
            kind: "grid".to_owned(),
            config: Value::Object(serde_json::Map::new()),
            columns: None,
            filter: None,
            sort: None,
            group: None,
        },
    )
    .await?;
    commit(tx).await?;
    Ok(TableCreated { table, view })
}

pub async fn rename_table(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    name: &str,
) -> Result<TableDto, StoreError> {
    let name = validate::clean_name(name).map_err(StoreError::Invalid)?;
    let updated = ts("updated_at");
    let created = ts("created_at");
    let sql = format!(
        "UPDATE generic_table
         SET name = $3, updated_at = now()
         WHERE id = $1::uuid AND workspace_id = $2::uuid
         RETURNING id::text, name, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(table)
        .bind(workspace)
        .bind(name)
        .fetch_optional(pool)
        .await
        .map_err(failed)?
        .ok_or(StoreError::NotFound("找不到这张表"))?;
    table_from_row(&row)
}

pub async fn get_sheet(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    view: Option<&str>,
) -> Result<SheetDto, StoreError> {
    let mut tx = begin(pool).await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
        .execute(&mut *tx)
        .await
        .map_err(failed)?;
    let sheet = load_sheet(&mut tx, workspace, table, view).await?;
    commit(tx).await?;
    Ok(sheet)
}

pub async fn list_record_options(
    pool: &PgPool,
    workspace: &str,
    table: &str,
) -> Result<Vec<RecordOptionDto>, StoreError> {
    let mut tx = begin(pool).await?;
    let table = load_table(&mut tx, workspace, table).await?;
    let fields = load_fields(&mut tx, workspace, &table.id).await?;
    let records = load_records(&mut tx, workspace, &table.id, &fields).await?;
    commit(tx).await?;
    Ok(records
        .iter()
        .map(|record| RecordOptionDto {
            id: record.id.clone(),
            label: record_option_label(&fields, record),
        })
        .collect())
}

fn record_option_label(fields: &[FieldDto], record: &RecordDto) -> String {
    if let Some(first) = fields.first()
        && let Some(label) = format_option_value(first, record.values.get(&first.id))
    {
        return label;
    }
    for field in fields.iter().skip(1) {
        if field.kind == "text"
            && let Some(label) = format_option_value(field, record.values.get(&field.id))
        {
            return label;
        }
    }
    let short: String = record
        .id
        .chars()
        .filter(|ch| ch.is_ascii_hexdigit())
        .take(8)
        .collect();
    format!("未命名记录 ({short})")
}

fn format_option_value(field: &FieldDto, value: Option<&Value>) -> Option<String> {
    let value = value.filter(|item| !item.is_null())?;
    let raw = match field.kind.as_str() {
        "text" | "date" => value.as_str()?.trim().to_owned(),
        "number" => value.as_number()?.to_string(),
        "checkbox" => {
            if value.as_bool()? {
                "是".to_owned()
            } else {
                "否".to_owned()
            }
        }
        "single_select" => {
            let picked = value.as_str()?;
            field
                .config
                .get("options")
                .and_then(Value::as_array)
                .and_then(|options| {
                    options.iter().find_map(|option| {
                        let id = option.get("id").and_then(Value::as_str)?;
                        if id == picked {
                            option.get("label").and_then(Value::as_str)
                        } else {
                            None
                        }
                    })
                })
                .unwrap_or(picked)
                .trim()
                .to_owned()
        }
        _ => return None,
    };
    let first_line = raw.lines().next().unwrap_or("").trim();
    if first_line.is_empty() {
        return None;
    }
    Some(first_line.chars().take(80).collect())
}

pub async fn create_field(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    name: &str,
    kind: &str,
    config: &Value,
) -> Result<FieldDto, StoreError> {
    let name = validate::clean_name(name).map_err(StoreError::Invalid)?;
    let config = validate::normalize_config(kind, config).map_err(StoreError::Invalid)?;
    let mut tx = begin(pool).await?;
    if kind == "relation" {
        let field =
            relation::create_relation_field(&mut tx, workspace, table, &name, &config).await?;
        commit(tx).await?;
        return Ok(field);
    }
    if kind == "lookup" || kind == "formula" {
        let field =
            projection::create_derived_field(&mut tx, workspace, table, &name, kind, &config)
                .await?;
        commit(tx).await?;
        return Ok(field);
    }
    lock_table(&mut tx, workspace, table).await?;
    let position = next_position(&mut tx, workspace, table).await?;
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "INSERT INTO generic_field (workspace_id, table_id, name, kind, config, position)
         VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6)
         RETURNING id::text, name, kind, config::text, position, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(workspace)
        .bind(table)
        .bind(name)
        .bind(kind)
        .bind(config.to_string())
        .bind(position)
        .fetch_one(&mut *tx)
        .await
        .map_err(|err| map_write(err, "请重试添加字段"))?;
    let field = field_from_row(&row)?;
    commit(tx).await?;
    Ok(field)
}

pub async fn update_field(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    field: &str,
    name: Option<&str>,
    config: Option<&Value>,
) -> Result<FieldDto, StoreError> {
    if name.is_none() && config.is_none() {
        return Err(StoreError::Invalid("没有要保存的字段变更"));
    }
    let clean_name = match name {
        Some(raw) => Some(validate::clean_name(raw).map_err(StoreError::Invalid)?),
        None => None,
    };
    let mut tx = begin(pool).await?;
    if let Some(raw_config) = config {
        let updated = projection::update_field_config(
            &mut tx,
            workspace,
            table,
            field,
            clean_name.as_deref(),
            raw_config,
        )
        .await?;
        commit(tx).await?;
        return Ok(updated);
    }
    lock_table(&mut tx, workspace, table).await?;
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "UPDATE generic_field
         SET name = $4, updated_at = now()
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
         RETURNING id::text, name, kind, config::text, position, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(field)
        .bind(table)
        .bind(workspace)
        .bind(clean_name.expect("checked above"))
        .fetch_optional(&mut *tx)
        .await
        .map_err(|err| map_write(err, "请重试重命名字段"))?
        .ok_or(StoreError::NotFound("找不到这个字段"))?;
    let mut updated_field = field_from_row(&row)?;
    if updated_field.kind == "formula" {
        let fields = load_fields(&mut tx, workspace, table).await?;
        let names: BTreeMap<String, String> = fields
            .into_iter()
            .map(|item| (item.id, item.name))
            .collect();
        projection::hydrate_single_formula(&mut updated_field, &names);
    }
    commit(tx).await?;
    Ok(updated_field)
}

pub async fn create_record(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    values: &BTreeMap<String, Value>,
    actor: Option<&super::collab::Actor>,
) -> Result<RecordDto, StoreError> {
    let mut tx = begin(pool).await?;
    let touched: Vec<String> = values.keys().cloned().collect();
    let locked_tables = relation::prepare_table_locks(&mut tx, workspace, table, &touched).await?;
    let fields = load_fields(&mut tx, workspace, table).await?;
    let normalized = normalize_values(&fields, values)?;
    let record = create_record_in_tx(
        &mut tx,
        workspace,
        table,
        &fields,
        &normalized,
        &locked_tables,
        actor,
    )
    .await?;
    commit(tx).await?;
    Ok(record)
}

pub async fn submit_form(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    view: &str,
    values: &BTreeMap<String, Value>,
    actor: Option<&super::collab::Actor>,
) -> Result<RecordDto, StoreError> {
    let mut tx = begin(pool).await?;
    let touched: Vec<String> = values.keys().cloned().collect();
    let locked_tables = relation::prepare_table_locks(&mut tx, workspace, table, &touched).await?;
    let view_dto = load_view(&mut tx, workspace, table, view).await?;
    let fields = load_fields(&mut tx, workspace, table).await?;
    let defs: Vec<FieldDef> = fields.iter().map(def_of).collect();
    let normalized = validate::validate_form_submission(
        &view_dto.kind,
        view_dto.columns.as_deref(),
        &view_dto.config,
        &defs,
        values,
    )
    .map_err(StoreError::Invalid)?;
    let record = create_record_in_tx(
        &mut tx,
        workspace,
        table,
        &fields,
        &normalized,
        &locked_tables,
        actor,
    )
    .await?;
    commit(tx).await?;
    Ok(record)
}

async fn create_record_in_tx(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    fields: &[FieldDto],
    normalized: &BTreeMap<String, Option<Value>>,
    locked_tables: &[String],
    actor: Option<&super::collab::Actor>,
) -> Result<RecordDto, StoreError> {
    let reverse_plan =
        relation::validate_and_plan_reverse(tx, workspace, fields, None, normalized, locked_tables)
            .await?;
    if reverse_plan.has_writes() && actor.is_none() {
        return Err(StoreError::Invalid("创建包含双向关联的记录需要协作者身份"));
    }
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "INSERT INTO generic_record (workspace_id, table_id)
         VALUES ($1::uuid, $2::uuid)
         RETURNING id::text, revision, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(workspace)
        .bind(table)
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?;
    let id: String = row.get(0);
    write_cells(tx, workspace, &id, normalized).await?;
    if let Some(actor) = actor
        && reverse_plan.has_writes()
    {
        relation::apply_reverse_plan(tx, workspace, &id, &reverse_plan, actor).await?;
    }
    load_record(tx, workspace, fields, &id).await
}

pub async fn update_record(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    record: &str,
    expected_revision: i64,
    values: &BTreeMap<String, Value>,
    actor: &super::collab::Actor,
) -> Result<RecordDto, StoreError> {
    super::collab::update_record(
        pool,
        workspace,
        table,
        record,
        expected_revision,
        values,
        actor,
    )
    .await
}

pub enum GroupUpdate<'a> {
    Keep,
    Set(Option<&'a str>),
}

pub enum ConfigUpdate<'a> {
    Keep,
    Set(&'a Value),
}

pub struct ViewWrite<'a> {
    pub name: &'a str,
    pub kind: Option<&'a str>,
    pub config: ConfigUpdate<'a>,
    pub columns: Option<&'a [String]>,
    pub filter: Option<&'a StoredFilter>,
    pub sort: Option<&'a StoredSort>,
    pub group: GroupUpdate<'a>,
}

pub async fn create_view(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    write: &ViewWrite<'_>,
) -> Result<ViewDto, StoreError> {
    let mut tx = begin(pool).await?;
    lock_table(&mut tx, workspace, table).await?;
    let fields = load_field_defs(&mut tx, workspace, table).await?;
    let kind = write.kind.unwrap_or("grid");
    let empty = Value::Object(serde_json::Map::new());
    let config = match write.config {
        ConfigUpdate::Set(value) => value,
        ConfigUpdate::Keep => &empty,
    };
    let group = match write.group {
        GroupUpdate::Set(value) => value,
        GroupUpdate::Keep => None,
    };
    let view = normalize_write(table, write, kind, config, group, &fields)?;
    let saved = insert_view(&mut tx, workspace, table, &view).await?;
    commit(tx).await?;
    Ok(saved)
}

pub async fn save_view(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    view: &str,
    write: &ViewWrite<'_>,
) -> Result<ViewDto, StoreError> {
    let mut tx = begin(pool).await?;
    lock_table(&mut tx, workspace, table).await?;
    let existing = load_view_for_update(&mut tx, workspace, table, view).await?;
    if let Some(requested_kind) = write.kind {
        if !validate::VIEW_KINDS.contains(&requested_kind) {
            return Err(StoreError::Invalid("不支持的视图类型"));
        }
        if requested_kind != existing.kind {
            return Err(StoreError::Invalid("视图类型创建后不能修改"));
        }
    }
    let fields = load_field_defs(&mut tx, workspace, table).await?;
    let group = match write.group {
        GroupUpdate::Set(value) => value.map(str::to_owned),
        GroupUpdate::Keep => existing.group,
    };
    let config = match write.config {
        ConfigUpdate::Set(value) => value,
        ConfigUpdate::Keep => &existing.config,
    };
    let spec = normalize_write(
        table,
        write,
        &existing.kind,
        config,
        group.as_deref(),
        &fields,
    )?;
    let saved = update_view(&mut tx, workspace, table, view, &spec).await?;
    commit(tx).await?;
    Ok(saved)
}

fn normalize_write(
    table: &str,
    write: &ViewWrite<'_>,
    kind: &str,
    config: &Value,
    group: Option<&str>,
    fields: &[FieldDef],
) -> Result<validate::StoredView, StoreError> {
    validate::normalize_view(
        table,
        write.name,
        kind,
        config,
        write.columns,
        write.filter,
        write.sort,
        group,
        fields,
    )
    .map_err(StoreError::Invalid)
}

struct ExistingViewMeta {
    kind: String,
    config: Value,
    group: Option<String>,
}

async fn load_view_for_update(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view: &str,
) -> Result<ExistingViewMeta, StoreError> {
    let row = sqlx::query(
        "SELECT kind, config::text, group_field
         FROM generic_view
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
         FOR UPDATE",
    )
    .bind(view)
    .bind(table)
    .bind(workspace)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?
    .ok_or(StoreError::NotFound("找不到这个视图"))?;
    let config_raw: String = row.get(1);
    let config = serde_json::from_str(&config_raw).map_err(|_| StoreError::Failed)?;
    Ok(ExistingViewMeta {
        kind: row.get(0),
        config,
        group: row.get(2),
    })
}

async fn load_view(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view: &str,
) -> Result<ViewDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, name, kind, config::text, columns::text, filter::text, sort::text, group_field, {created}, {updated}
         FROM generic_view
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid"
    );
    let row = sqlx::query(&sql)
        .bind(view)
        .bind(table)
        .bind(workspace)
        .fetch_optional(&mut **tx)
        .await
        .map_err(failed)?
        .ok_or(StoreError::NotFound("找不到这个视图"))?;
    view_from_row(&row)
}

async fn load_sheet(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view_id: Option<&str>,
) -> Result<SheetDto, StoreError> {
    let table = load_table(tx, workspace, table).await?;
    let mut fields = load_fields(tx, workspace, &table.id).await?;
    let views = load_views(tx, workspace, &table.id).await?;
    let active = match view_id {
        None => views.first().map(|view| view.id.clone()),
        Some(id) => {
            if !views.iter().any(|view| view.id == id) {
                return Err(StoreError::NotFound("找不到这个视图"));
            }
            Some(id.to_owned())
        }
    };
    let active_view = active
        .as_ref()
        .and_then(|id| views.iter().find(|view| &view.id == id));
    let mut records = load_records(tx, workspace, &table.id, &fields).await?;
    projection::project_sheet(tx, workspace, &table.id, &mut fields, &mut records).await?;
    if let Some(view) = active_view {
        if let Some(filter) = &view.filter {
            let field = fields.iter().find(|item| item.id == filter.field_id);
            if let Some(field) = field {
                let def = def_of(field);
                records.retain(|record| {
                    let cell = record
                        .values
                        .get(&field.id)
                        .filter(|value| !value.is_null());
                    validate::matches(&def, cell, filter)
                });
            }
        }
        if let Some(sort) = &view.sort
            && let Some(field) = fields.iter().find(|item| item.id == sort.field_id)
        {
            let def = def_of(field);
            let desc = sort.direction == "desc";
            records.sort_by(|left, right| {
                let left_key = validate::sort_key(
                    &def,
                    left.values.get(&field.id).filter(|value| !value.is_null()),
                );
                let right_key = validate::sort_key(
                    &def,
                    right.values.get(&field.id).filter(|value| !value.is_null()),
                );
                match (left_key, right_key) {
                    (None, None) => left.id.cmp(&right.id),
                    (None, Some(_)) => std::cmp::Ordering::Greater,
                    (Some(_), None) => std::cmp::Ordering::Less,
                    (Some(a), Some(b)) => {
                        let order = a.partial_cmp(&b).unwrap_or(std::cmp::Ordering::Equal);
                        let order = if desc { order.reverse() } else { order };
                        order.then(left.id.cmp(&right.id))
                    }
                }
            });
        }
    }
    Ok(SheetDto {
        table,
        fields,
        views,
        records,
        active_view_id: active,
    })
}

fn def_of(field: &FieldDto) -> FieldDef {
    FieldDef {
        id: field.id.clone(),
        kind: field.kind.clone(),
        config: field.config.clone(),
    }
}

pub(super) async fn begin(pool: &PgPool) -> Result<Transaction<'_, Postgres>, StoreError> {
    pool.begin().await.map_err(failed)
}

pub(super) async fn commit(tx: Transaction<'_, Postgres>) -> Result<(), StoreError> {
    tx.commit().await.map_err(failed)
}

pub(super) async fn lock_table(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<(), StoreError> {
    let found = sqlx::query("SELECT id::text FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE")
        .bind(table)
        .bind(workspace)
        .fetch_optional(&mut **tx)
        .await
        .map_err(failed)?;
    if found.is_none() {
        return Err(StoreError::NotFound("找不到这张表"));
    }
    Ok(())
}

pub(super) async fn next_position(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<i32, StoreError> {
    let row = sqlx::query(
        "SELECT (COALESCE(MAX(position), 0) + 1)::int FROM generic_field WHERE table_id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(table)
    .bind(workspace)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    let position: i32 = row.get(0);
    Ok(position)
}

async fn insert_table(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    name: &str,
) -> Result<TableDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "INSERT INTO generic_table (workspace_id, name)
         VALUES ($1::uuid, $2)
         RETURNING id::text, name, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(workspace)
        .bind(name)
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?;
    table_from_row(&row)
}

async fn insert_view(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view: &StoredView,
) -> Result<ViewDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "INSERT INTO generic_view (workspace_id, table_id, name, kind, config, columns, filter, sort, group_field)
         VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9)
         RETURNING id::text, name, kind, config::text, columns::text, filter::text, sort::text, group_field, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(workspace)
        .bind(table)
        .bind(&view.name)
        .bind(&view.kind)
        .bind(view.config.to_string())
        .bind(json_option(&view.columns)?)
        .bind(json_option(&view.filter)?)
        .bind(json_option(&view.sort)?)
        .bind(&view.group)
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?;
    view_from_row(&row)
}

async fn update_view(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view: &str,
    spec: &StoredView,
) -> Result<ViewDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "UPDATE generic_view
         SET name = $4, config = $5::jsonb, columns = $6::jsonb, filter = $7::jsonb, sort = $8::jsonb, group_field = $9, updated_at = now()
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
         RETURNING id::text, name, kind, config::text, columns::text, filter::text, sort::text, group_field, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(view)
        .bind(table)
        .bind(workspace)
        .bind(&spec.name)
        .bind(spec.config.to_string())
        .bind(json_option(&spec.columns)?)
        .bind(json_option(&spec.filter)?)
        .bind(json_option(&spec.sort)?)
        .bind(&spec.group)
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?;
    view_from_row(&row)
}

async fn load_table(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<TableDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, name, {created}, {updated}
         FROM generic_table
         WHERE id = $1::uuid AND workspace_id = $2::uuid"
    );
    let row = sqlx::query(&sql)
        .bind(table)
        .bind(workspace)
        .fetch_optional(&mut **tx)
        .await
        .map_err(failed)?
        .ok_or(StoreError::NotFound("找不到这张表"))?;
    table_from_row(&row)
}

pub(super) async fn load_fields(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<Vec<FieldDto>, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, name, kind, config::text, position, {created}, {updated}
         FROM generic_field
         WHERE table_id = $1::uuid AND workspace_id = $2::uuid
         ORDER BY position, id"
    );
    let rows = sqlx::query(&sql)
        .bind(table)
        .bind(workspace)
        .fetch_all(&mut **tx)
        .await
        .map_err(failed)?;
    rows.iter().map(field_from_row).collect()
}

async fn load_field_defs(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<Vec<FieldDef>, StoreError> {
    let fields = load_fields(tx, workspace, table).await?;
    Ok(fields.iter().map(def_of).collect())
}

async fn load_views(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<Vec<ViewDto>, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, name, kind, config::text, columns::text, filter::text, sort::text, group_field, {created}, {updated}
         FROM generic_view
         WHERE table_id = $1::uuid AND workspace_id = $2::uuid
         ORDER BY created_at, id"
    );
    let rows = sqlx::query(&sql)
        .bind(table)
        .bind(workspace)
        .fetch_all(&mut **tx)
        .await
        .map_err(failed)?;
    rows.iter().map(view_from_row).collect()
}

pub(super) async fn load_records(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    fields: &[FieldDto],
) -> Result<Vec<RecordDto>, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, revision, {created}, {updated}
         FROM generic_record
         WHERE table_id = $1::uuid AND workspace_id = $2::uuid
         ORDER BY created_at, id"
    );
    let rows = sqlx::query(&sql)
        .bind(table)
        .bind(workspace)
        .fetch_all(&mut **tx)
        .await
        .map_err(failed)?;
    let cells = sqlx::query(
        "SELECT c.record_id::text, c.field_id::text, c.value::text
         FROM generic_cell c
         JOIN generic_record r ON r.id = c.record_id
         WHERE r.table_id = $1::uuid AND r.workspace_id = $2::uuid AND c.workspace_id = $2::uuid",
    )
    .bind(table)
    .bind(workspace)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let mut by_record: HashMap<String, HashMap<String, Value>> = HashMap::new();
    for cell in cells {
        let record_id: String = cell.get(0);
        let field_id: String = cell.get(1);
        let raw: Option<String> = cell.get(2);
        let value = match raw {
            Some(text) => serde_json::from_str(&text).map_err(|_| StoreError::Failed)?,
            None => Value::Null,
        };
        by_record
            .entry(record_id)
            .or_default()
            .insert(field_id, value);
    }
    rows.iter()
        .map(|row| {
            let id: String = row.get(0);
            let values = by_record.remove(&id).unwrap_or_default();
            Ok(record_from_maps(row, fields, &values))
        })
        .collect()
}

pub(super) async fn lock_record(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    record: &str,
) -> Result<RecordDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, revision, {created}, {updated}
         FROM generic_record
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
         FOR UPDATE"
    );
    let row = sqlx::query(&sql)
        .bind(record)
        .bind(table)
        .bind(workspace)
        .fetch_optional(&mut **tx)
        .await
        .map_err(failed)?
        .ok_or(StoreError::NotFound("找不到这条记录"))?;
    Ok(record_from_maps(&row, &[], &HashMap::new()))
}

pub(super) async fn load_record(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    fields: &[FieldDto],
    record: &str,
) -> Result<RecordDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, revision, {created}, {updated}
         FROM generic_record
         WHERE id = $1::uuid AND workspace_id = $2::uuid"
    );
    let row = sqlx::query(&sql)
        .bind(record)
        .bind(workspace)
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?;
    let cells = sqlx::query(
        "SELECT field_id::text, value::text FROM generic_cell WHERE record_id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(record)
    .bind(workspace)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let mut values = HashMap::new();
    for cell in cells {
        let field_id: String = cell.get(0);
        let raw: Option<String> = cell.get(1);
        let value = match raw {
            Some(text) => serde_json::from_str(&text).map_err(|_| StoreError::Failed)?,
            None => Value::Null,
        };
        values.insert(field_id, value);
    }
    Ok(record_from_maps(&row, fields, &values))
}

pub(super) async fn write_cells(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    record: &str,
    values: &BTreeMap<String, Option<Value>>,
) -> Result<(), StoreError> {
    for (field, value) in values {
        match value {
            None => {
                sqlx::query(
                    "DELETE FROM generic_cell WHERE record_id = $1::uuid AND field_id = $2::uuid AND workspace_id = $3::uuid",
                )
                .bind(record)
                .bind(field)
                .bind(workspace)
                .execute(&mut **tx)
                .await
                .map_err(failed)?;
            }
            Some(stored) => {
                sqlx::query(
                    "INSERT INTO generic_cell (workspace_id, record_id, field_id, value)
                     VALUES ($1::uuid, $2::uuid, $3::uuid, $4::jsonb)
                     ON CONFLICT (record_id, field_id)
                     DO UPDATE SET value = EXCLUDED.value, workspace_id = EXCLUDED.workspace_id",
                )
                .bind(workspace)
                .bind(record)
                .bind(field)
                .bind(stored.to_string())
                .execute(&mut **tx)
                .await
                .map_err(failed)?;
            }
        }
    }
    Ok(())
}

pub(super) fn normalize_values(
    fields: &[FieldDto],
    values: &BTreeMap<String, Value>,
) -> Result<BTreeMap<String, Option<Value>>, StoreError> {
    let defs: Vec<FieldDef> = fields.iter().map(def_of).collect();
    let mut normalized = BTreeMap::new();
    for (field_id, value) in values {
        let field = defs
            .iter()
            .find(|field| &field.id == field_id)
            .ok_or(StoreError::Invalid("有字段不属于这张表"))?;
        let stored = validate::normalize_value(field, value).map_err(StoreError::Invalid)?;
        normalized.insert(field_id.clone(), stored);
    }
    Ok(normalized)
}

fn record_from_maps(
    row: &sqlx::postgres::PgRow,
    fields: &[FieldDto],
    values: &HashMap<String, Value>,
) -> RecordDto {
    let mut complete = BTreeMap::new();
    for field in fields {
        complete.insert(
            field.id.clone(),
            values.get(&field.id).cloned().unwrap_or(Value::Null),
        );
    }
    RecordDto {
        id: row.get(0),
        revision: row.get(1),
        created_at: row.get(2),
        updated_at: row.get(3),
        values: complete,
        errors: BTreeMap::new(),
    }
}

fn table_from_row(row: &sqlx::postgres::PgRow) -> Result<TableDto, StoreError> {
    Ok(TableDto {
        id: row.get(0),
        name: row.get(1),
        created_at: row.get(2),
        updated_at: row.get(3),
    })
}

pub(super) fn field_from_row(row: &sqlx::postgres::PgRow) -> Result<FieldDto, StoreError> {
    let config_text: String = row.get(3);
    let config = serde_json::from_str(&config_text).map_err(|_| StoreError::Failed)?;
    Ok(FieldDto {
        id: row.get(0),
        name: row.get(1),
        kind: row.get(2),
        config,
        position: row.get(4),
        created_at: row.get(5),
        updated_at: row.get(6),
    })
}

fn view_from_row(row: &sqlx::postgres::PgRow) -> Result<ViewDto, StoreError> {
    let config_raw: String = row.get(3);
    let config = serde_json::from_str(&config_raw).map_err(|_| StoreError::Failed)?;
    let columns: Option<String> = row.get(4);
    let filter: Option<String> = row.get(5);
    let sort: Option<String> = row.get(6);
    Ok(ViewDto {
        id: row.get(0),
        name: row.get(1),
        kind: row.get(2),
        config,
        columns: parse_json(columns)?,
        filter: parse_json(filter)?,
        sort: parse_json(sort)?,
        group: row.get(7),
        created_at: row.get(8),
        updated_at: row.get(9),
    })
}

fn parse_json<T: serde::de::DeserializeOwned>(
    raw: Option<String>,
) -> Result<Option<T>, StoreError> {
    match raw {
        None => Ok(None),
        Some(text) if text == "null" => Ok(None),
        Some(text) => serde_json::from_str(&text)
            .map(Some)
            .map_err(|_| StoreError::Failed),
    }
}

fn json_option<T: Serialize>(value: &Option<T>) -> Result<Option<String>, StoreError> {
    match value {
        None => Ok(None),
        Some(value) => serde_json::to_string(value)
            .map(Some)
            .map_err(|_| StoreError::Failed),
    }
}

pub(super) fn failed(err: sqlx::Error) -> StoreError {
    eprintln!("generic table query failed: {err}");
    StoreError::Failed
}

pub(super) fn map_write(err: sqlx::Error, retry: &'static str) -> StoreError {
    if let Some(db) = err.as_database_error()
        && db.code().as_deref() == Some("23505")
    {
        let text = db.to_string();
        if text.contains("generic_field_name") {
            return StoreError::NameTaken;
        }
        return StoreError::Invalid(retry);
    }
    failed(err)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn field(id: &str, kind: &str, config: Value) -> FieldDto {
        FieldDto {
            id: id.to_owned(),
            name: id.to_owned(),
            kind: kind.to_owned(),
            config,
            position: 1,
            created_at: String::new(),
            updated_at: String::new(),
        }
    }

    fn record(id: &str, values: BTreeMap<String, Value>) -> RecordDto {
        RecordDto {
            id: id.to_owned(),
            revision: 1,
            created_at: String::new(),
            updated_at: String::new(),
            values,
            errors: BTreeMap::new(),
        }
    }

    #[test]
    fn record_option_label_uses_first_field_then_text_then_fallback() {
        let f1 = field("f1", "text", json!({}));
        let f2 = field("f2", "text", json!({}));
        let rec_id = "12345678-1111-4111-8111-111111111111";

        let mut values = BTreeMap::new();
        values.insert("f1".to_owned(), json!("  主标题\n第二行 "));
        assert_eq!(
            record_option_label(&[f1.clone(), f2.clone()], &record(rec_id, values)),
            "主标题"
        );

        let mut second = BTreeMap::new();
        second.insert("f1".to_owned(), Value::Null);
        second.insert("f2".to_owned(), json!("备用标题"));
        assert_eq!(
            record_option_label(&[f1.clone(), f2.clone()], &record(rec_id, second)),
            "备用标题"
        );

        assert_eq!(
            record_option_label(&[f1, f2], &record(rec_id, BTreeMap::new())),
            "未命名记录 (12345678)"
        );
    }
}
