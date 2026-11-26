use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde::Serialize;
use serde_json::Value;
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::collab::{self, Actor, EventWrite};
use super::validate::{self, FieldDef, StoredFilter, StoredSort, StoredView};
use super::{projection, relation};

#[derive(Debug)]
pub enum StoreError {
    NotFound(&'static str),
    Conflict {
        message: &'static str,
        record: Box<RecordDto>,
    },
    ReferenceConflict {
        code: &'static str,
        message: String,
        hint: &'static str,
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

#[derive(Clone, Debug, Serialize)]
pub struct DeletedDto {
    pub deleted: bool,
    pub id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub revision: Option<i64>,
}

#[derive(Serialize)]
pub struct RecordListDto {
    pub records: Vec<RecordDto>,
    pub total: i64,
    pub limit: i64,
    pub offset: i64,
    pub has_more: bool,
    pub next_offset: Option<i64>,
    pub active_view_id: Option<String>,
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
        0,
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
    actor: Option<&Actor>,
) -> Result<TableDto, StoreError> {
    let name = validate::clean_name(name).map_err(StoreError::Invalid)?;
    let mut tx = begin(pool).await?;
    lock_table(&mut tx, workspace, table).await?;
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
        .fetch_optional(&mut *tx)
        .await
        .map_err(failed)?
        .ok_or(StoreError::NotFound("找不到这张表"))?;
    let renamed = table_from_row(&row)?;
    if let Some(actor) = actor {
        publish_metadata_event(
            &mut tx,
            workspace,
            &renamed.id,
            "table.renamed",
            "table",
            &renamed.id,
            actor,
        )
        .await?;
    }
    commit(tx).await?;
    Ok(renamed)
}

pub async fn delete_table(
    pool: &PgPool,
    workspace: &str,
    table: &str,
) -> Result<DeletedDto, StoreError> {
    let table_lower = table.to_ascii_lowercase();
    let mut tx = begin(pool).await?;
    lock_workspace_tables_sorted(&mut tx, workspace, &table_lower).await?;

    if let Some(prop_name) = sqlx::query_scalar::<_, String>(
        "SELECT name FROM issue_property
         WHERE workspace_id = $1::uuid
           AND type = 'record_relation'
           AND lower(COALESCE(config->>'target_table_id', '')) = $2
         ORDER BY name
         LIMIT 1",
    )
    .bind(workspace)
    .bind(&table_lower)
    .fetch_optional(&mut *tx)
    .await
    .map_err(failed)?
    {
        return Err(StoreError::ReferenceConflict {
            code: "table_in_use",
            message: format!("无法删除表格：工作区任务属性「{prop_name}」正在关联这张表"),
            hint: "当前存在指向该表的任务 record_relation 属性定义；在相关任务属性不再引用该表前无法删除表格",
        });
    }

    if let Some((other_table_name, field_name)) = sqlx::query_as::<_, (String, String)>(
        "SELECT t.name, f.name
         FROM generic_field f
         JOIN generic_table t ON t.id = f.table_id AND t.workspace_id = f.workspace_id
         WHERE f.workspace_id = $1::uuid
           AND f.table_id <> $2::uuid
           AND f.kind = 'relation'
           AND lower(COALESCE(f.config->>'target_table_id', '')) = $3
         ORDER BY t.name, f.name
         LIMIT 1",
    )
    .bind(workspace)
    .bind(&table_lower)
    .bind(&table_lower)
    .fetch_optional(&mut *tx)
    .await
    .map_err(failed)?
    {
        return Err(StoreError::ReferenceConflict {
            code: "table_in_use",
            message: format!(
                "无法删除表格：表格「{other_table_name}」的关联字段「{field_name}」指向当前表"
            ),
            hint: "请先删除或解除其他表中指向当前表的关联字段后再删除表格",
        });
    }

    if let Some((other_table_name, field_name)) = sqlx::query_as::<_, (String, String)>(
        "SELECT t.name, f.name
         FROM generic_field f
         JOIN generic_table t ON t.id = f.table_id AND t.workspace_id = f.workspace_id
         WHERE f.workspace_id = $1::uuid
           AND f.table_id <> $2::uuid
           AND f.kind = 'lookup'
           AND lower(COALESCE(f.config->>'target_field_id', '')) IN (
               SELECT lower(id::text)
               FROM generic_field
               WHERE workspace_id = $1::uuid AND table_id = $2::uuid
           )
         ORDER BY t.name, f.name
         LIMIT 1",
    )
    .bind(workspace)
    .bind(&table_lower)
    .fetch_optional(&mut *tx)
    .await
    .map_err(failed)?
    {
        return Err(StoreError::ReferenceConflict {
            code: "table_in_use",
            message: format!(
                "无法删除表格：表格「{other_table_name}」的查找字段「{field_name}」依赖当前表的字段"
            ),
            hint: "请先删除其他表中引用当前表字段的查找字段后再删除表格",
        });
    }

    sqlx::query(
        "DELETE FROM generic_cell
         WHERE workspace_id = $1::uuid
           AND record_id IN (
               SELECT id FROM generic_record WHERE workspace_id = $1::uuid AND table_id = $2::uuid
           )",
    )
    .bind(workspace)
    .bind(&table_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "DELETE FROM generic_record_comment WHERE workspace_id = $1::uuid AND table_id = $2::uuid",
    )
    .bind(workspace)
    .bind(&table_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "DELETE FROM generic_presence WHERE workspace_id = $1::uuid AND table_id = $2::uuid",
    )
    .bind(workspace)
    .bind(&table_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "INSERT INTO issue_attachment_object_delete_outbox (
            attachment_id, workspace_id, storage_key, object_url
         )
         SELECT id, workspace_id, storage_key, object_url
         FROM generic_table_attachment
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid
         ON CONFLICT (attachment_id) DO NOTHING",
    )
    .bind(workspace)
    .bind(&table_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "DELETE FROM generic_table_attachment
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid",
    )
    .bind(workspace)
    .bind(&table_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "DELETE FROM generic_record_batch_idempotency
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid",
    )
    .bind(workspace)
    .bind(&table_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    sqlx::query("DELETE FROM generic_change WHERE workspace_id = $1::uuid AND table_id = $2::uuid")
        .bind(workspace)
        .bind(&table_lower)
        .execute(&mut *tx)
        .await
        .map_err(failed)?;

    sqlx::query("DELETE FROM generic_record WHERE workspace_id = $1::uuid AND table_id = $2::uuid")
        .bind(workspace)
        .bind(&table_lower)
        .execute(&mut *tx)
        .await
        .map_err(failed)?;

    sqlx::query("DELETE FROM generic_view WHERE workspace_id = $1::uuid AND table_id = $2::uuid")
        .bind(workspace)
        .bind(&table_lower)
        .execute(&mut *tx)
        .await
        .map_err(failed)?;

    sqlx::query("DELETE FROM generic_field WHERE workspace_id = $1::uuid AND table_id = $2::uuid")
        .bind(workspace)
        .bind(&table_lower)
        .execute(&mut *tx)
        .await
        .map_err(failed)?;

    sqlx::query("DELETE FROM generic_table WHERE workspace_id = $1::uuid AND id = $2::uuid")
        .bind(workspace)
        .bind(&table_lower)
        .execute(&mut *tx)
        .await
        .map_err(failed)?;

    commit(tx).await?;
    Ok(DeletedDto {
        deleted: true,
        id: table_lower,
        revision: None,
    })
}

pub async fn list_fields(
    pool: &PgPool,
    workspace: &str,
    table: &str,
) -> Result<Vec<FieldDto>, StoreError> {
    let mut tx = begin(pool).await?;
    let table = load_table(&mut tx, workspace, table).await?;
    let mut fields = load_fields(&mut tx, workspace, &table.id).await?;
    projection::hydrate_formula_fields(&mut fields);
    commit(tx).await?;
    Ok(fields)
}

pub async fn get_field(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    field_id: &str,
) -> Result<FieldDto, StoreError> {
    let fields = list_fields(pool, workspace, table).await?;
    fields
        .into_iter()
        .find(|item| item.id.eq_ignore_ascii_case(field_id))
        .ok_or(StoreError::NotFound("找不到这个字段"))
}

pub async fn list_records(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    view: Option<&str>,
    limit: Option<i64>,
    offset: Option<i64>,
) -> Result<RecordListDto, StoreError> {
    let limit = limit.unwrap_or(100);
    if !(1..=200).contains(&limit) {
        return Err(StoreError::Invalid("记录分页数量需要在 1 到 200 之间"));
    }
    let offset = offset.unwrap_or(0);
    if offset < 0 {
        return Err(StoreError::Invalid("记录偏移量不能为负数"));
    }
    let mut tx = begin(pool).await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
        .execute(&mut *tx)
        .await
        .map_err(failed)?;
    let sheet = load_sheet(&mut tx, workspace, table, view).await?;
    commit(tx).await?;

    let total = sheet.records.len() as i64;
    let start = usize::try_from(offset)
        .unwrap_or(usize::MAX)
        .min(sheet.records.len());
    let take = usize::try_from(limit).unwrap_or(0);
    let end = start.saturating_add(take).min(sheet.records.len());
    let page: Vec<RecordDto> = sheet.records[start..end].to_vec();
    let has_more = (end as i64) < total;
    let next_offset = if has_more { Some(end as i64) } else { None };
    Ok(RecordListDto {
        records: page,
        total,
        limit,
        offset,
        has_more,
        next_offset,
        active_view_id: sheet.active_view_id,
    })
}

pub async fn get_record(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    record_id: &str,
) -> Result<RecordDto, StoreError> {
    let mut tx = begin(pool).await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
        .execute(&mut *tx)
        .await
        .map_err(failed)?;
    let table = load_table(&mut tx, workspace, table).await?;
    let mut fields = load_fields(&mut tx, workspace, &table.id).await?;
    let record = load_record_in_table(&mut tx, workspace, &table.id, &fields, record_id).await?;
    let mut records = vec![record];
    if fields
        .iter()
        .any(|field| field.kind == "lookup" || field.kind == "formula")
    {
        projection::project_sheet(
            &mut tx,
            workspace,
            &table.id,
            &mut fields,
            &mut records,
            projection::PrimaryRows::Subset,
        )
        .await?;
    }
    commit(tx).await?;
    Ok(records.pop().expect("one record"))
}

pub async fn list_views(
    pool: &PgPool,
    workspace: &str,
    table: &str,
) -> Result<Vec<ViewDto>, StoreError> {
    let mut tx = begin(pool).await?;
    let table = load_table(&mut tx, workspace, table).await?;
    let views = load_views(&mut tx, workspace, &table.id).await?;
    commit(tx).await?;
    Ok(views)
}

pub async fn get_view(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    view_id: &str,
) -> Result<ViewDto, StoreError> {
    let mut tx = begin(pool).await?;
    let table = load_table(&mut tx, workspace, table).await?;
    let view = load_view(&mut tx, workspace, &table.id, view_id).await?;
    commit(tx).await?;
    Ok(view)
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
    actor: Option<&Actor>,
) -> Result<FieldDto, StoreError> {
    let name = validate::clean_name(name).map_err(StoreError::Invalid)?;
    let config = validate::normalize_config(kind, config).map_err(StoreError::Invalid)?;
    let mut tx = begin(pool).await?;
    if kind == "relation" {
        let field =
            relation::create_relation_field(&mut tx, workspace, table, &name, &config).await?;
        if let Some(actor) = actor {
            publish_metadata_event(
                &mut tx,
                workspace,
                table,
                "field.created",
                "field",
                &field.id,
                actor,
            )
            .await?;
            if let Ok(rel) = relation::RelationConfig::from_stored(&field.config)
                && rel.bidirectional
                && let Some(reverse_id) = rel.reverse_field_id.as_deref()
            {
                publish_metadata_event(
                    &mut tx,
                    workspace,
                    &rel.target_table_id,
                    "field.created",
                    "field",
                    reverse_id,
                    actor,
                )
                .await?;
            }
        }
        commit(tx).await?;
        return Ok(field);
    }
    if kind == "lookup" || kind == "formula" {
        let field =
            projection::create_derived_field(&mut tx, workspace, table, &name, kind, &config)
                .await?;
        if let Some(actor) = actor {
            publish_metadata_event(
                &mut tx,
                workspace,
                table,
                "field.created",
                "field",
                &field.id,
                actor,
            )
            .await?;
        }
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
    if let Some(actor) = actor {
        publish_metadata_event(
            &mut tx,
            workspace,
            table,
            "field.created",
            "field",
            &field.id,
            actor,
        )
        .await?;
    }
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
    actor: Option<&Actor>,
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
        if let Some(actor) = actor {
            publish_metadata_event(
                &mut tx,
                workspace,
                table,
                "field.updated",
                "field",
                &updated.id,
                actor,
            )
            .await?;
        }
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
    if let Some(actor) = actor {
        publish_metadata_event(
            &mut tx,
            workspace,
            table,
            "field.updated",
            "field",
            &updated_field.id,
            actor,
        )
        .await?;
    }
    commit(tx).await?;
    Ok(updated_field)
}

pub async fn delete_field(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    field_id: &str,
    actor: Option<&Actor>,
) -> Result<DeletedDto, StoreError> {
    let table_lower = table.to_ascii_lowercase();
    let field_lower = field_id.to_ascii_lowercase();
    let mut tx = begin(pool).await?;
    lock_workspace_tables_sorted(&mut tx, workspace, &table_lower).await?;

    let ws_rows = sqlx::query(
        "SELECT id::text, table_id::text, name, kind, config::text
         FROM generic_field
         WHERE workspace_id = $1::uuid
         ORDER BY table_id, position, id",
    )
    .bind(workspace)
    .fetch_all(&mut *tx)
    .await
    .map_err(failed)?;

    struct WsField {
        id: String,
        table_id: String,
        name: String,
        kind: String,
        config: Value,
    }
    let mut all_fields = Vec::with_capacity(ws_rows.len());
    for row in ws_rows {
        let raw: String = row.get(4);
        let config = serde_json::from_str(&raw).map_err(|_| StoreError::Failed)?;
        all_fields.push(WsField {
            id: row.get::<String, _>(0).to_ascii_lowercase(),
            table_id: row.get::<String, _>(1).to_ascii_lowercase(),
            name: row.get(2),
            kind: row.get(3),
            config,
        });
    }

    let target = all_fields
        .iter()
        .find(|item| item.table_id == table_lower && item.id == field_lower)
        .ok_or(StoreError::NotFound("找不到这个字段"))?;

    if target.kind == "relation"
        && let Ok(rel) = relation::RelationConfig::from_stored(&target.config)
        && (rel.bidirectional || rel.reverse_field_id.is_some())
    {
        return Err(StoreError::ReferenceConflict {
            code: "field_in_use",
            message: format!(
                "无法删除双向关联字段「{}」：该字段与目标表反向字段成对绑定",
                target.name
            ),
            hint: "当前模型阻断单独删除双向关联字段以防止反向关联悬空；请保留成对结构或改用单向关联字段",
        });
    }
    for other in &all_fields {
        if other.id == field_lower {
            continue;
        }
        if other.kind == "relation"
            && let Ok(rel) = relation::RelationConfig::from_stored(&other.config)
            && rel.reverse_field_id.as_deref() == Some(field_lower.as_str())
        {
            return Err(StoreError::ReferenceConflict {
                code: "field_in_use",
                message: format!(
                    "无法删除字段「{}」：它是关联字段「{}」的反向关联字段",
                    target.name, other.name
                ),
                hint: "当前模型阻断单独删除双向关联字段以防止成对关联悬空",
            });
        }
    }

    let views = load_views(&mut tx, workspace, &table_lower).await?;
    for view in &views {
        if view_references_field(view, &field_lower) {
            return Err(StoreError::ReferenceConflict {
                code: "field_in_use",
                message: format!(
                    "无法删除字段：视图「{}」正在引用该字段（列、筛选、排序、分组或视图配置）",
                    view.name
                ),
                hint: "请先在对应视图中移除该字段的列、筛选、排序、分组或视图配置后再删除字段",
            });
        }
    }

    for other in &all_fields {
        if other.id == field_lower {
            continue;
        }
        let deps = projection::field_dependencies(&other.kind, &other.config);
        if deps
            .iter()
            .any(|dep| dep.eq_ignore_ascii_case(&field_lower))
        {
            let kind_label = if other.kind == "formula" {
                "公式"
            } else {
                "查找"
            };
            return Err(StoreError::ReferenceConflict {
                code: "field_in_use",
                message: format!("无法删除字段：{kind_label}字段「{}」依赖该字段", other.name),
                hint: "请先修改或删除依赖该字段的公式或查找字段后再删除",
            });
        }
    }

    if let Some(prop_name) = sqlx::query_scalar::<_, String>(
        "SELECT name FROM issue_property
         WHERE workspace_id = $1::uuid
           AND type = 'lookup'
           AND lower(COALESCE(config->>'target_field_id', '')) = $2
         ORDER BY name
         LIMIT 1",
    )
    .bind(workspace)
    .bind(&field_lower)
    .fetch_optional(&mut *tx)
    .await
    .map_err(failed)?
    {
        return Err(StoreError::ReferenceConflict {
            code: "field_in_use",
            message: format!("无法删除字段：工作区任务查找属性「{prop_name}」依赖该字段"),
            hint: "当前存在引用该字段的任务 lookup 属性定义；在相关任务属性解除对该字段的引用前无法删除字段",
        });
    }

    sqlx::query(
        "DELETE FROM generic_cell
         WHERE workspace_id = $1::uuid
           AND field_id = $2::uuid
           AND record_id IN (
               SELECT id FROM generic_record WHERE workspace_id = $1::uuid AND table_id = $3::uuid
           )",
    )
    .bind(workspace)
    .bind(&field_lower)
    .bind(&table_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "UPDATE generic_presence
         SET field_id = NULL, updated_at = now()
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND field_id = $3::uuid",
    )
    .bind(workspace)
    .bind(&table_lower)
    .bind(&field_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "DELETE FROM generic_field
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND id = $3::uuid",
    )
    .bind(workspace)
    .bind(&table_lower)
    .bind(&field_lower)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;

    if let Some(actor) = actor {
        publish_metadata_event(
            &mut tx,
            workspace,
            &table_lower,
            "field.deleted",
            "field",
            &field_lower,
            actor,
        )
        .await?;
    }
    commit(tx).await?;
    Ok(DeletedDto {
        deleted: true,
        id: field_lower,
        revision: None,
    })
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
        None,
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
        None,
    )
    .await?;
    commit(tx).await?;
    Ok(record)
}

pub(super) async fn validate_attachment_refs(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    fields: &[FieldDto],
    normalized: &BTreeMap<String, Option<Value>>,
) -> Result<(), StoreError> {
    let attachment_field_ids: BTreeSet<&str> = fields
        .iter()
        .filter(|field| field.kind == "attachment")
        .map(|field| field.id.as_str())
        .collect();
    if attachment_field_ids.is_empty() {
        return Ok(());
    }
    let mut referenced = BTreeSet::new();
    for (field_id, cell) in normalized {
        if !attachment_field_ids.contains(field_id.as_str()) {
            continue;
        }
        let Some(Value::Array(items)) = cell else {
            continue;
        };
        for item in items {
            let Some(id) = item.as_str() else {
                return Err(StoreError::Invalid("附件标识必须是有效的 UUID"));
            };
            referenced.insert(id.to_ascii_lowercase());
        }
    }
    if referenced.is_empty() {
        return Ok(());
    }
    let ids: Vec<String> = referenced.iter().cloned().collect();
    let ready: Vec<String> = sqlx::query_scalar(
        "SELECT lower(id::text)
         FROM generic_table_attachment
         WHERE workspace_id = $1::uuid
           AND table_id = $2::uuid
           AND status = 'ready'
           AND id = ANY($3::text[]::uuid[])",
    )
    .bind(workspace)
    .bind(table)
    .bind(&ids)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let ready_set: BTreeSet<String> = ready.into_iter().collect();
    if ready_set.len() != referenced.len() {
        return Err(StoreError::Invalid("附件不存在、尚未就绪或不属于当前表"));
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
pub(super) async fn create_record_in_tx(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    fields: &[FieldDto],
    normalized: &BTreeMap<String, Option<Value>>,
    locked_tables: &[String],
    actor: Option<&super::collab::Actor>,
    batch_id: Option<&str>,
) -> Result<RecordDto, StoreError> {
    validate_attachment_refs(tx, workspace, table, fields, normalized).await?;
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
    if let Some(actor) = actor {
        let digest = collab::digest(normalized)?;
        collab::insert_event(
            tx,
            &EventWrite {
                workspace,
                table,
                event_type: "record.created",
                topic: "record",
                entity_id: &id,
                record_id: Some(&id),
                batch_id,
                base_revision: 0,
                client_revision: 0,
                record_revision: 1,
                values: normalized,
                actor,
                digest: &digest,
            },
        )
        .await?;
        if reverse_plan.has_writes() {
            relation::apply_reverse_plan(tx, workspace, &id, &reverse_plan, actor, batch_id)
                .await?;
        }
    }
    load_record(tx, workspace, fields, &id).await
}

pub async fn delete_record(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    record_id: &str,
    expected_revision: i64,
    actor: &Actor,
) -> Result<DeletedDto, StoreError> {
    if expected_revision <= 0 {
        return Err(StoreError::Invalid("记录版本号无效"));
    }
    let table_lower = table.to_ascii_lowercase();
    let record_lower = record_id.to_ascii_lowercase();
    let mut tx = begin(pool).await?;
    lock_workspace_tables_sorted(&mut tx, workspace, &table_lower).await?;
    let deleted = delete_record_in_tx(
        &mut tx,
        workspace,
        &table_lower,
        &record_lower,
        expected_revision,
        actor,
        None,
    )
    .await?;
    commit(tx).await?;
    Ok(deleted)
}

pub(super) async fn delete_record_in_tx(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table_lower: &str,
    record_id: &str,
    expected_revision: i64,
    actor: &Actor,
    batch_id: Option<&str>,
) -> Result<DeletedDto, StoreError> {
    if expected_revision <= 0 {
        return Err(StoreError::Invalid("记录版本号无效"));
    }
    let record_lower = record_id.to_ascii_lowercase();
    let current = lock_record(tx, workspace, table_lower, &record_lower).await?;
    if current.revision != expected_revision {
        let fields = load_fields(tx, workspace, table_lower).await?;
        let fresh = load_record(tx, workspace, &fields, &record_lower).await?;
        return Err(StoreError::Conflict {
            message: "这条记录已在其他窗口更新，无法按过期版本删除",
            record: Box::new(fresh),
        });
    }

    let referenced_by_issue = sqlx::query_scalar::<_, bool>(
        "SELECT EXISTS(
             SELECT 1
             FROM issue_property p
             JOIN issue i ON i.workspace_id = p.workspace_id
             WHERE p.workspace_id = $1::uuid
               AND p.type = 'record_relation'
               AND lower(COALESCE(p.config->>'target_table_id', '')) = $2
               AND i.properties ? p.id::text
               AND i.properties -> p.id::text @> jsonb_build_array($3::text)
         )",
    )
    .bind(workspace)
    .bind(table_lower)
    .bind(&record_lower)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    if referenced_by_issue {
        return Err(StoreError::ReferenceConflict {
            code: "record_in_use",
            message: "无法删除记录：仍有任务引用了这条记录".to_owned(),
            hint: "请先在引用该记录的任务中解除对应的 record_relation 关联后再删除记录",
        });
    }

    let referenced_by_record = sqlx::query_scalar::<_, bool>(
        "SELECT EXISTS(
             SELECT 1
             FROM generic_field f
             JOIN generic_cell c ON c.workspace_id = f.workspace_id AND c.field_id = f.id
             WHERE f.workspace_id = $1::uuid
               AND f.kind = 'relation'
               AND lower(COALESCE(f.config->>'target_table_id', '')) = $2
               AND c.record_id <> $3::uuid
               AND c.value @> jsonb_build_array($4::text)
         )",
    )
    .bind(workspace)
    .bind(table_lower)
    .bind(&record_lower)
    .bind(&record_lower)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    if referenced_by_record {
        return Err(StoreError::ReferenceConflict {
            code: "record_in_use",
            message: "无法删除记录：仍有其他记录通过关联字段引用了这条记录".to_owned(),
            hint: "请先清除其他记录中指向该记录的关联字段值后再删除记录",
        });
    }

    sqlx::query("DELETE FROM generic_cell WHERE workspace_id = $1::uuid AND record_id = $2::uuid")
        .bind(workspace)
        .bind(&record_lower)
        .execute(&mut **tx)
        .await
        .map_err(failed)?;

    sqlx::query(
        "DELETE FROM generic_record_comment
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid",
    )
    .bind(workspace)
    .bind(table_lower)
    .bind(&record_lower)
    .execute(&mut **tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "UPDATE generic_presence
         SET record_id = NULL, field_id = NULL, updated_at = now()
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid",
    )
    .bind(workspace)
    .bind(table_lower)
    .bind(&record_lower)
    .execute(&mut **tx)
    .await
    .map_err(failed)?;

    sqlx::query(
        "DELETE FROM generic_record
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND id = $3::uuid",
    )
    .bind(workspace)
    .bind(table_lower)
    .bind(&record_lower)
    .execute(&mut **tx)
    .await
    .map_err(failed)?;

    let empty = BTreeMap::new();
    let digest = collab::digest_str(&format!(
        "record.deleted:{record_lower}:{}",
        current.revision
    ));
    collab::insert_event(
        tx,
        &EventWrite {
            workspace,
            table: table_lower,
            event_type: "record.deleted",
            topic: "record",
            entity_id: &record_lower,
            record_id: Some(&record_lower),
            batch_id,
            base_revision: current.revision,
            client_revision: expected_revision,
            record_revision: current.revision,
            values: &empty,
            actor,
            digest: &digest,
        },
    )
    .await?;

    Ok(DeletedDto {
        deleted: true,
        id: record_lower,
        revision: Some(current.revision),
    })
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
    actor: Option<&Actor>,
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
    let saved = insert_view(&mut tx, workspace, table, &view, 0).await?;
    if let Some(actor) = actor {
        publish_metadata_event(
            &mut tx,
            workspace,
            table,
            "view.created",
            "view",
            &saved.id,
            actor,
        )
        .await?;
    }
    commit(tx).await?;
    Ok(saved)
}

pub async fn save_view(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    view: &str,
    write: &ViewWrite<'_>,
    actor: Option<&Actor>,
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
    if let Some(actor) = actor {
        publish_metadata_event(
            &mut tx,
            workspace,
            table,
            "view.updated",
            "view",
            &saved.id,
            actor,
        )
        .await?;
    }
    commit(tx).await?;
    Ok(saved)
}

pub async fn delete_view(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    view_id: &str,
    actor: Option<&Actor>,
) -> Result<DeletedDto, StoreError> {
    let table_lower = table.to_ascii_lowercase();
    let view_lower = view_id.to_ascii_lowercase();
    let mut tx = begin(pool).await?;
    lock_table(&mut tx, workspace, &table_lower).await?;
    let views = load_views(&mut tx, workspace, &table_lower).await?;
    let target = views
        .iter()
        .find(|item| item.id.eq_ignore_ascii_case(&view_lower))
        .ok_or(StoreError::NotFound("找不到这个视图"))?;
    if target.kind == "grid" {
        let grid_count = views.iter().filter(|item| item.kind == "grid").count();
        if grid_count <= 1 {
            return Err(StoreError::ReferenceConflict {
                code: "last_grid_view",
                message: "无法删除表格中最后一个网格视图".to_owned(),
                hint: "每张表至少需要保留一个网格视图；请先新建其他网格视图后再删除",
            });
        }
    }
    sqlx::query(
        "DELETE FROM generic_view
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid",
    )
    .bind(&view_lower)
    .bind(&table_lower)
    .bind(workspace)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;
    if let Some(actor) = actor {
        publish_metadata_event(
            &mut tx,
            workspace,
            &table_lower,
            "view.deleted",
            "view",
            &view_lower,
            actor,
        )
        .await?;
    }
    commit(tx).await?;
    Ok(DeletedDto {
        deleted: true,
        id: view_lower,
        revision: None,
    })
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
    projection::project_sheet(
        tx,
        workspace,
        &table.id,
        &mut fields,
        &mut records,
        projection::PrimaryRows::All,
    )
    .await?;
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

pub(super) fn def_of(field: &FieldDto) -> FieldDef {
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

pub(super) async fn lock_workspace_tables_sorted(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    primary_table: &str,
) -> Result<(), StoreError> {
    let primary = primary_table.to_ascii_lowercase();
    let rows = sqlx::query(
        "SELECT id::text FROM generic_table WHERE workspace_id = $1::uuid ORDER BY id FOR UPDATE",
    )
    .bind(workspace)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let exists = rows
        .iter()
        .any(|row| row.get::<String, _>(0).eq_ignore_ascii_case(&primary));
    if !exists {
        return Err(StoreError::NotFound("找不到这张表"));
    }
    Ok(())
}

async fn publish_metadata_event(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    event_type: &str,
    topic: &str,
    entity_id: &str,
    actor: &Actor,
) -> Result<(), StoreError> {
    let empty = BTreeMap::new();
    let digest = collab::digest_str(&format!("{event_type}:{entity_id}"));
    collab::insert_event(
        tx,
        &EventWrite {
            workspace,
            table,
            event_type,
            topic,
            entity_id,
            record_id: None,
            batch_id: None,
            base_revision: 0,
            client_revision: 0,
            record_revision: 0,
            values: &empty,
            actor,
            digest: &digest,
        },
    )
    .await
}

fn json_contains_uuid(value: &Value, target_lower: &str) -> bool {
    match value {
        Value::String(text) => text.eq_ignore_ascii_case(target_lower),
        Value::Array(items) => items
            .iter()
            .any(|item| json_contains_uuid(item, target_lower)),
        Value::Object(map) => map
            .values()
            .any(|item| json_contains_uuid(item, target_lower)),
        _ => false,
    }
}

pub(super) fn view_references_field(view: &ViewDto, field_id: &str) -> bool {
    let target = field_id.to_ascii_lowercase();
    if view
        .columns
        .as_ref()
        .is_some_and(|cols| cols.iter().any(|id| id.eq_ignore_ascii_case(&target)))
    {
        return true;
    }
    if view
        .filter
        .as_ref()
        .is_some_and(|filter| filter.field_id.eq_ignore_ascii_case(&target))
    {
        return true;
    }
    if view
        .sort
        .as_ref()
        .is_some_and(|sort| sort.field_id.eq_ignore_ascii_case(&target))
    {
        return true;
    }
    if view
        .group
        .as_deref()
        .is_some_and(|group| group.eq_ignore_ascii_case(&target))
    {
        return true;
    }
    json_contains_uuid(&view.config, &target)
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

pub(super) async fn insert_table(
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

pub(super) async fn insert_view(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    view: &StoredView,
    order: i32,
) -> Result<ViewDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "INSERT INTO generic_view (workspace_id, table_id, name, kind, config, columns, filter, sort, group_field, created_at, updated_at)
         VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9,
                 now() + $10::int * interval '1 microsecond', now() + $10::int * interval '1 microsecond')
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
        .bind(order)
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

pub(super) async fn load_table(
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

pub(super) async fn load_views(
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

async fn load_record_in_table(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    fields: &[FieldDto],
    record: &str,
) -> Result<RecordDto, StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "SELECT id::text, revision, {created}, {updated}
         FROM generic_record
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid"
    );
    let row = sqlx::query(&sql)
        .bind(record)
        .bind(table)
        .bind(workspace)
        .fetch_optional(&mut **tx)
        .await
        .map_err(failed)?
        .ok_or(StoreError::NotFound("找不到这条记录"))?;
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

    #[test]
    fn view_references_field_detects_columns_filter_sort_group_and_config() {
        let f1 = "11111111-1111-4111-8111-111111111111";
        let f2 = "22222222-2222-4222-8222-222222222222";
        let base = ViewDto {
            id: "55555555-5555-4555-8555-555555555555".to_owned(),
            name: "All".to_owned(),
            kind: "grid".to_owned(),
            config: json!({}),
            columns: None,
            filter: None,
            sort: None,
            group: None,
            created_at: String::new(),
            updated_at: String::new(),
        };
        assert!(!view_references_field(&base, f1));

        let mut with_cols = base.clone();
        with_cols.columns = Some(vec![f1.to_ascii_uppercase()]);
        assert!(view_references_field(&with_cols, f1));
        assert!(!view_references_field(&with_cols, f2));

        let mut with_filter = base.clone();
        with_filter.filter = Some(StoredFilter {
            field_id: f1.to_owned(),
            op: "eq".to_owned(),
            value: Some(json!("ok")),
        });
        assert!(view_references_field(&with_filter, f1));

        let mut with_sort = base.clone();
        with_sort.sort = Some(StoredSort {
            field_id: f1.to_owned(),
            direction: "asc".to_owned(),
        });
        assert!(view_references_field(&with_sort, f1));

        let mut with_group = base.clone();
        with_group.group = Some(f1.to_owned());
        assert!(view_references_field(&with_group, f1));

        let mut with_cfg = base.clone();
        with_cfg.config = json!({ "required_fields": [f1] });
        assert!(view_references_field(&with_cfg, f1));
    }
}
