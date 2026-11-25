use std::collections::{BTreeMap, HashMap};

use serde::Serialize;
use serde_json::Value;
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::validate::{self, FieldDef, StoredFilter, StoredSort, StoredView};

#[derive(Debug)]
pub enum StoreError {
    NotFound(&'static str),
    Conflict {
        message: &'static str,
        record: RecordDto,
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
}

#[derive(Clone, Serialize)]
pub struct ViewDto {
    pub id: String,
    pub name: String,
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
    let sheet = load_sheet(&mut tx, workspace, table, view).await?;
    commit(tx).await?;
    Ok(sheet)
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

pub async fn rename_field(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    field: &str,
    name: &str,
) -> Result<FieldDto, StoreError> {
    let name = validate::clean_name(name).map_err(StoreError::Invalid)?;
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
        .bind(name)
        .fetch_optional(pool)
        .await
        .map_err(|err| map_write(err, "请重试重命名字段"))?
        .ok_or(StoreError::NotFound("找不到这个字段"))?;
    field_from_row(&row)
}

pub async fn create_record(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    values: &BTreeMap<String, Value>,
) -> Result<RecordDto, StoreError> {
    let mut tx = begin(pool).await?;
    lock_table(&mut tx, workspace, table).await?;
    let fields = load_fields(&mut tx, workspace, table).await?;
    let normalized = normalize_values(&fields, values)?;
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
        .fetch_one(&mut *tx)
        .await
        .map_err(failed)?;
    let id: String = row.get(0);
    write_cells(&mut tx, workspace, &id, &normalized).await?;
    let record = record_from_parts(&row, &fields, &normalized);
    commit(tx).await?;
    Ok(record)
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

pub struct ViewWrite<'a> {
    pub name: &'a str,
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
    let group = match write.group {
        GroupUpdate::Set(value) => value,
        GroupUpdate::Keep => None,
    };
    let view = normalize_write(write, group, &fields)?;
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
    ensure_view(&mut tx, workspace, table, view).await?;
    let fields = load_field_defs(&mut tx, workspace, table).await?;
    let group = match write.group {
        GroupUpdate::Set(value) => value.map(str::to_owned),
        GroupUpdate::Keep => load_group(&mut tx, workspace, table, view).await?,
    };
    let spec = normalize_write(write, group.as_deref(), &fields)?;
    let saved = update_view(&mut tx, workspace, table, view, &spec).await?;
    commit(tx).await?;
    Ok(saved)
}

fn normalize_write(
    write: &ViewWrite<'_>,
    group: Option<&str>,
    fields: &[FieldDef],
) -> Result<validate::StoredView, StoreError> {
    validate::normalize_view(
        write.name,
        write.columns,
        write.filter,
        write.sort,
        group,
        fields,
    )
    .map_err(StoreError::Invalid)
}

async fn load_group(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view: &str,
) -> Result<Option<String>, StoreError> {
    let row = sqlx::query(
        "SELECT group_field FROM generic_view WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid",
    )
    .bind(view)
    .bind(table)
    .bind(workspace)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?
    .ok_or(StoreError::NotFound("找不到这个视图"))?;
    Ok(row.get(0))
}

async fn load_sheet(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view_id: Option<&str>,
) -> Result<SheetDto, StoreError> {
    let table = load_table(tx, workspace, table).await?;
    let fields = load_fields(tx, workspace, &table.id).await?;
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

async fn next_position(
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
        "INSERT INTO generic_view (workspace_id, table_id, name, columns, filter, sort, group_field)
         VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5::jsonb, $6::jsonb, $7)
         RETURNING id::text, name, columns::text, filter::text, sort::text, group_field, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(workspace)
        .bind(table)
        .bind(&view.name)
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
         SET name = $4, columns = $5::jsonb, filter = $6::jsonb, sort = $7::jsonb, group_field = $8, updated_at = now()
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
         RETURNING id::text, name, columns::text, filter::text, sort::text, group_field, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(view)
        .bind(table)
        .bind(workspace)
        .bind(&spec.name)
        .bind(json_option(&spec.columns)?)
        .bind(json_option(&spec.filter)?)
        .bind(json_option(&spec.sort)?)
        .bind(&spec.group)
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?;
    view_from_row(&row)
}

async fn ensure_view(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view: &str,
) -> Result<(), StoreError> {
    let found = sqlx::query(
        "SELECT id::text FROM generic_view WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid FOR UPDATE",
    )
    .bind(view)
    .bind(table)
    .bind(workspace)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?;
    if found.is_none() {
        return Err(StoreError::NotFound("找不到这个视图"));
    }
    Ok(())
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
        "SELECT id::text, name, columns::text, filter::text, sort::text, group_field, {created}, {updated}
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

async fn load_records(
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

fn record_from_parts(
    row: &sqlx::postgres::PgRow,
    fields: &[FieldDto],
    values: &BTreeMap<String, Option<Value>>,
) -> RecordDto {
    let mut map = HashMap::new();
    for (id, value) in values {
        map.insert(id.clone(), value.clone().unwrap_or(Value::Null));
    }
    record_from_maps(row, fields, &map)
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

fn field_from_row(row: &sqlx::postgres::PgRow) -> Result<FieldDto, StoreError> {
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
    let columns: Option<String> = row.get(2);
    let filter: Option<String> = row.get(3);
    let sort: Option<String> = row.get(4);
    Ok(ViewDto {
        id: row.get(0),
        name: row.get(1),
        columns: parse_json(columns)?,
        filter: parse_json(filter)?,
        sort: parse_json(sort)?,
        group: row.get(5),
        created_at: row.get(6),
        updated_at: row.get(7),
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

fn map_write(err: sqlx::Error, retry: &'static str) -> StoreError {
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
