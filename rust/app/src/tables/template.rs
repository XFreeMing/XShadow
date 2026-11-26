use std::collections::{BTreeMap, BTreeSet};

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::collab::Actor;
use super::formula;
use super::projection::{self, LookupConfig};
use super::relation::{self, RelationConfig};
use super::store::{
    self, FieldDto, RecordDto, StoreError, TableDto, ViewDto, def_of, failed, field_from_row,
    map_write, ts,
};
use super::validate::{self, FieldDef, StoredFilter, StoredSort, StoredView};

pub const TEMPLATE_VERSION: u32 = 1;
const MAX_TEMPLATE_FIELDS: usize = 200;
const MAX_TEMPLATE_VIEWS: usize = 100;
const MAX_SAMPLE_RECORDS: usize = 50;

const VIEW_CONFIG_SINGLE_FIELD_KEYS: &[&str] = &[
    "title_field_id",
    "group_field_id",
    "date_field_id",
    "end_date_field_id",
    "parent_field_id",
];

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct UnsupportedDependencyDto {
    pub field_id: String,
    pub field_name: String,
    pub kind: String,
    pub code: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target_table_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub relation_field_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target_field_id: Option<String>,
}

#[derive(Debug)]
pub enum TemplateError {
    NotFound(&'static str),
    Invalid {
        code: &'static str,
        message: String,
    },
    UnsupportedDependency {
        message: String,
        dependencies: Vec<UnsupportedDependencyDto>,
    },
    Store(StoreError),
}

impl From<StoreError> for TemplateError {
    fn from(err: StoreError) -> Self {
        match err {
            StoreError::NotFound(msg) => Self::NotFound(msg),
            other => Self::Store(other),
        }
    }
}

impl TemplateError {
    fn invalid(code: &'static str, message: impl Into<String>) -> Self {
        Self::Invalid {
            code,
            message: message.into(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct TableTemplateDto {
    pub template_version: u32,
    pub source_table_id: String,
    pub name: String,
    pub fields: Vec<TemplateFieldDto>,
    pub views: Vec<TemplateViewDto>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub sample_records: Vec<TemplateSampleRecordDto>,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct TemplateFieldDto {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub position: i32,
    pub config: Value,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct TemplateViewDto {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub config: Value,
    pub columns: Option<Vec<String>>,
    pub filter: Option<StoredFilter>,
    pub sort: Option<StoredSort>,
    pub group: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct TemplateSampleRecordDto {
    pub values: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TemplateImportRequest {
    pub template_version: u32,
    #[serde(default)]
    pub source_table_id: Option<String>,
    pub name: String,
    #[serde(default)]
    pub fields: Vec<TemplateFieldInput>,
    #[serde(default)]
    pub views: Vec<TemplateViewInput>,
    #[serde(default)]
    pub sample_records: Vec<TemplateSampleRecordInput>,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TemplateFieldInput {
    #[serde(default)]
    pub id: Option<String>,
    pub name: String,
    pub kind: String,
    #[serde(default)]
    pub position: Option<i32>,
    #[serde(default)]
    pub config: Value,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TemplateViewInput {
    #[serde(default)]
    pub id: Option<String>,
    pub name: String,
    #[serde(default)]
    pub kind: Option<String>,
    #[serde(default)]
    pub config: Value,
    #[serde(default)]
    pub columns: Option<Vec<String>>,
    #[serde(default)]
    pub filter: Option<StoredFilter>,
    #[serde(default)]
    pub sort: Option<StoredSort>,
    #[serde(default)]
    pub group: Option<String>,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TemplateSampleRecordInput {
    #[serde(default)]
    pub values: BTreeMap<String, Value>,
}

#[derive(Serialize)]
pub struct TemplateImportResponse {
    pub table: TableDto,
    pub fields: Vec<FieldDto>,
    pub views: Vec<ViewDto>,
    pub records: Vec<RecordDto>,
    pub active_view_id: Option<String>,
    pub id_map: TemplateIdMap,
}

#[derive(Serialize)]
pub struct TemplateIdMap {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source_table_id: Option<String>,
    pub table_id: String,
    pub fields: BTreeMap<String, String>,
    pub views: BTreeMap<String, String>,
}

#[derive(Serialize)]
pub struct TableCreatedWithSchema {
    pub table: TableDto,
    pub view: ViewDto,
    pub fields: Vec<FieldDto>,
}

pub fn parse_sample_record_ids_query(raw: Option<&str>) -> Result<Vec<String>, TemplateError> {
    let Some(raw) = raw else {
        return Ok(Vec::new());
    };
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return Ok(Vec::new());
    }
    let mut seen = BTreeSet::new();
    let mut ids = Vec::new();
    for part in trimmed.split(',') {
        let candidate = part.trim();
        if !validate::is_uuid(candidate) {
            return Err(TemplateError::invalid(
                "invalid_value",
                "sample_record_ids 包含无效的记录标识",
            ));
        }
        let lower = candidate.to_ascii_lowercase();
        if seen.insert(lower.clone()) {
            ids.push(lower);
        }
        if ids.len() > MAX_SAMPLE_RECORDS {
            return Err(TemplateError::invalid(
                "invalid_value",
                format!("最多导出 {MAX_SAMPLE_RECORDS} 条示例记录"),
            ));
        }
    }
    Ok(ids)
}

pub async fn export_template(
    pool: &PgPool,
    workspace: &str,
    table_id: &str,
    sample_record_ids: &[String],
) -> Result<TableTemplateDto, TemplateError> {
    if sample_record_ids.len() > MAX_SAMPLE_RECORDS {
        return Err(TemplateError::invalid(
            "invalid_value",
            format!("最多导出 {MAX_SAMPLE_RECORDS} 条示例记录"),
        ));
    }
    let mut tx = store::begin(pool).await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
        .execute(&mut *tx)
        .await
        .map_err(failed)?;

    let table = store::load_table(&mut tx, workspace, table_id).await?;
    let mut fields = store::load_fields(&mut tx, workspace, &table.id).await?;
    let views = store::load_views(&mut tx, workspace, &table.id).await?;

    let unsupported = check_export_dependencies(&table.id, &fields)?;
    if !unsupported.is_empty() {
        return Err(TemplateError::UnsupportedDependency {
            message: "模板导出不支持跨表关联或外部查找依赖".to_owned(),
            dependencies: unsupported,
        });
    }
    for view in &views {
        check_portable_view_filter(view.filter.as_ref(), |id| {
            fields
                .iter()
                .find(|field| field.id == id)
                .map(|field| field.kind.as_str())
        })?;
    }

    projection::hydrate_formula_fields(&mut fields);

    let sample_records = if sample_record_ids.is_empty() {
        Vec::new()
    } else {
        export_sample_records(&mut tx, workspace, &table.id, &fields, sample_record_ids).await?
    };

    store::commit(tx).await?;

    let export_fields = fields
        .into_iter()
        .map(|field| {
            let config = clean_export_field_config(&field, &table.id);
            TemplateFieldDto {
                id: field.id,
                name: field.name,
                kind: field.kind,
                position: field.position,
                config,
            }
        })
        .collect();

    let export_views = views
        .into_iter()
        .map(|view| TemplateViewDto {
            id: view.id,
            name: view.name,
            kind: view.kind,
            config: view.config,
            columns: view.columns,
            filter: view.filter,
            sort: view.sort,
            group: view.group,
        })
        .collect();

    Ok(TableTemplateDto {
        template_version: TEMPLATE_VERSION,
        source_table_id: table.id,
        name: table.name,
        fields: export_fields,
        views: export_views,
        sample_records,
    })
}

fn check_export_dependencies(
    table_id: &str,
    fields: &[FieldDto],
) -> Result<Vec<UnsupportedDependencyDto>, TemplateError> {
    let by_id: BTreeMap<String, &FieldDto> = fields
        .iter()
        .map(|field| (field.id.to_ascii_lowercase(), field))
        .collect();
    let mut unsupported = Vec::new();
    for field in fields {
        match field.kind.as_str() {
            "relation" => {
                let rel = RelationConfig::from_stored(&field.config)?;
                if rel.bidirectional || rel.reverse_field_id.is_some() {
                    unsupported.push(UnsupportedDependencyDto {
                        field_id: field.id.clone(),
                        field_name: field.name.clone(),
                        kind: field.kind.clone(),
                        code: "bidirectional_relation_unsupported",
                        target_table_id: Some(rel.target_table_id),
                        relation_field_id: None,
                        target_field_id: None,
                    });
                } else if !rel.target_table_id.eq_ignore_ascii_case(table_id) {
                    unsupported.push(UnsupportedDependencyDto {
                        field_id: field.id.clone(),
                        field_name: field.name.clone(),
                        kind: field.kind.clone(),
                        code: "external_relation_unsupported",
                        target_table_id: Some(rel.target_table_id),
                        relation_field_id: None,
                        target_field_id: None,
                    });
                }
            }
            "lookup" => {
                let lookup = LookupConfig::from_stored(&field.config)
                    .map_err(|msg| TemplateError::invalid("invalid_template", msg))?;
                let rel_field = by_id.get(&lookup.relation_field_id).copied();
                let rel_is_self = rel_field.is_some_and(|rf| {
                    rf.kind == "relation"
                        && RelationConfig::from_stored(&rf.config).is_ok_and(|rel| {
                            !rel.bidirectional
                                && rel.reverse_field_id.is_none()
                                && rel.target_table_id.eq_ignore_ascii_case(table_id)
                        })
                });
                let target_is_local = by_id.contains_key(&lookup.target_field_id);
                if !rel_is_self || !target_is_local {
                    let target_table_id = rel_field.and_then(|rf| {
                        RelationConfig::from_stored(&rf.config)
                            .ok()
                            .map(|rel| rel.target_table_id)
                    });
                    unsupported.push(UnsupportedDependencyDto {
                        field_id: field.id.clone(),
                        field_name: field.name.clone(),
                        kind: field.kind.clone(),
                        code: "external_lookup_unsupported",
                        target_table_id,
                        relation_field_id: Some(lookup.relation_field_id),
                        target_field_id: Some(lookup.target_field_id),
                    });
                }
            }
            _ => {}
        }
    }
    Ok(unsupported)
}

fn clean_export_field_config(field: &FieldDto, table_id: &str) -> Value {
    match field.kind.as_str() {
        "relation" => RelationConfig {
            target_table_id: table_id.to_ascii_lowercase(),
            bidirectional: false,
            reverse_field_id: None,
        }
        .to_json(),
        "lookup" => match LookupConfig::from_stored(&field.config) {
            Ok(lookup) => lookup.to_json(),
            Err(_) => field.config.clone(),
        },
        _ => field.config.clone(),
    }
}

fn is_portable_sample_kind(kind: &str) -> bool {
    matches!(
        kind,
        "text" | "number" | "date" | "single_select" | "checkbox"
    )
}

async fn export_sample_records(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table_id: &str,
    fields: &[FieldDto],
    requested_ids: &[String],
) -> Result<Vec<TemplateSampleRecordDto>, TemplateError> {
    let portable_fields: Vec<String> = fields
        .iter()
        .filter(|field| is_portable_sample_kind(&field.kind))
        .map(|field| field.id.clone())
        .collect();
    let cells = sqlx::query(
        "SELECT r.id::text, c.field_id::text, c.value::text
         FROM generic_record r
         LEFT JOIN generic_cell c ON c.record_id = r.id AND c.workspace_id = r.workspace_id
           AND c.field_id IN (SELECT unnest($4::text[])::uuid)
         WHERE r.workspace_id = $1::uuid AND r.table_id = $2::uuid
           AND r.id IN (SELECT unnest($3::text[])::uuid)",
    )
    .bind(workspace)
    .bind(table_id)
    .bind(requested_ids)
    .bind(portable_fields)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let mut by_record: BTreeMap<String, BTreeMap<String, Value>> = BTreeMap::new();
    for cell in cells {
        let values = by_record.entry(cell.get(0)).or_default();
        if let (Some(field), Some(raw)) = (cell.get(1), cell.get::<Option<String>, _>(2)) {
            let value: Value = serde_json::from_str(&raw).map_err(|_| StoreError::Failed)?;
            if !value.is_null() {
                values.insert(field, value);
            }
        }
    }

    let mut out = Vec::with_capacity(requested_ids.len());
    for req_id in requested_ids {
        let Some(values) = by_record.remove(req_id) else {
            return Err(TemplateError::NotFound("找不到指定的示例记录"));
        };
        out.push(TemplateSampleRecordDto { values });
    }
    Ok(out)
}

pub async fn create_with_fields(
    pool: &PgPool,
    workspace: &str,
    name: String,
    fields: Vec<TemplateFieldInput>,
) -> Result<TableCreatedWithSchema, TemplateError> {
    let request = TemplateImportRequest {
        template_version: TEMPLATE_VERSION,
        source_table_id: None,
        name,
        fields,
        views: Vec::new(),
        sample_records: Vec::new(),
    };
    let mut created = import_template(pool, workspace, &request, None).await?;
    let view = created.views.pop().ok_or(StoreError::Failed)?;
    Ok(TableCreatedWithSchema {
        table: created.table,
        view,
        fields: created.fields,
    })
}

pub async fn import_template(
    pool: &PgPool,
    workspace: &str,
    req: &TemplateImportRequest,
    actor: Option<&Actor>,
) -> Result<TemplateImportResponse, TemplateError> {
    if req.template_version != TEMPLATE_VERSION {
        return Err(TemplateError::invalid(
            "unsupported_template_version",
            format!(
                "不支持的模板版本：{}（当前仅支持版本 {TEMPLATE_VERSION}）",
                req.template_version
            ),
        ));
    }
    let table_name = validate::clean_name(&req.name)
        .map_err(|msg| TemplateError::invalid("invalid_value", msg))?;
    let source_table_id = match req.source_table_id.as_deref() {
        None => None,
        Some(raw) => {
            let trimmed = raw.trim();
            if trimmed.is_empty() {
                None
            } else if validate::is_uuid(trimmed) {
                Some(trimmed.to_ascii_lowercase())
            } else {
                return Err(TemplateError::invalid(
                    "invalid_template",
                    "source_table_id 必须是有效的 UUID",
                ));
            }
        }
    };
    if req.fields.len() > MAX_TEMPLATE_FIELDS {
        return Err(TemplateError::invalid(
            "invalid_template",
            format!("模板字段数量不能超过 {MAX_TEMPLATE_FIELDS} 个"),
        ));
    }
    if req.views.len() > MAX_TEMPLATE_VIEWS {
        return Err(TemplateError::invalid(
            "invalid_template",
            format!("模板视图数量不能超过 {MAX_TEMPLATE_VIEWS} 个"),
        ));
    }
    if !req.views.is_empty()
        && !req
            .views
            .iter()
            .any(|view| view.kind.as_deref().unwrap_or("grid") == "grid")
    {
        return Err(TemplateError::invalid(
            "invalid_template",
            "模板必须保留至少一个网格视图",
        ));
    }
    if req.sample_records.len() > MAX_SAMPLE_RECORDS {
        return Err(TemplateError::invalid(
            "invalid_template",
            format!("模板示例记录数量不能超过 {MAX_SAMPLE_RECORDS} 条"),
        ));
    }

    let prepared_fields = prepare_field_metadata(&req.fields)?;

    let mut tx = store::begin(pool).await?;
    let table = store::insert_table(&mut tx, workspace, &table_name).await?;

    let mut new_field_ids = Vec::with_capacity(prepared_fields.len());
    for _ in 0..prepared_fields.len() {
        let id: String = sqlx::query("SELECT gen_random_uuid()::text")
            .fetch_one(&mut *tx)
            .await
            .map_err(failed)?
            .get(0);
        new_field_ids.push(id.to_ascii_lowercase());
    }

    let mut field_id_map: BTreeMap<String, String> = BTreeMap::new();
    let mut public_field_id_map: BTreeMap<String, String> = BTreeMap::new();
    for (spec, new_id) in prepared_fields.iter().zip(new_field_ids.iter()) {
        if let Some((canonical_old, display_old)) = &spec.source_id {
            field_id_map.insert(canonical_old.clone(), new_id.clone());
            public_field_id_map.insert(display_old.clone(), new_id.clone());
        }
    }

    let resolved_configs = resolve_and_validate_fields(
        &table.id,
        source_table_id.as_deref(),
        &prepared_fields,
        &new_field_ids,
        &field_id_map,
    )?;

    let created = ts("created_at");
    let updated = ts("updated_at");
    let insert_field_sql = format!(
        "INSERT INTO generic_field (id, workspace_id, table_id, name, kind, config, position)
         VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::jsonb, $7)
         RETURNING id::text, name, kind, config::text, position, {created}, {updated}"
    );
    let mut inserted_fields = Vec::with_capacity(prepared_fields.len());
    for (idx, ((spec, new_id), stored_config)) in prepared_fields
        .iter()
        .zip(new_field_ids.iter())
        .zip(resolved_configs.iter())
        .enumerate()
    {
        let position = (idx + 1) as i32;
        let row = sqlx::query(&insert_field_sql)
            .bind(new_id)
            .bind(workspace)
            .bind(&table.id)
            .bind(&spec.name)
            .bind(&spec.kind)
            .bind(stored_config.to_string())
            .bind(position)
            .fetch_one(&mut *tx)
            .await
            .map_err(|err| map_write(err, "请重试导入字段"))?;
        inserted_fields.push(field_from_row(&row)?);
    }

    let field_defs: Vec<FieldDef> = inserted_fields.iter().map(def_of).collect();
    let mut inserted_views = Vec::new();
    let mut view_id_map: BTreeMap<String, String> = BTreeMap::new();

    if req.views.is_empty() {
        let default_view = store::insert_view(
            &mut tx,
            workspace,
            &table.id,
            &StoredView {
                name: "全部".to_owned(),
                kind: "grid".to_owned(),
                config: Value::Object(Map::new()),
                columns: None,
                filter: None,
                sort: None,
                group: None,
            },
            0,
        )
        .await?;
        inserted_views.push(default_view);
    } else {
        let mut seen_view_ids = BTreeSet::new();
        for (order, view) in req.views.iter().enumerate() {
            let source_view_id = match view.id.as_deref() {
                None => None,
                Some(raw) => {
                    let trimmed = raw.trim();
                    if trimmed.is_empty() {
                        return Err(TemplateError::invalid(
                            "invalid_template",
                            "模板视图标识不能为空",
                        ));
                    }
                    let canonical = canonical_ref_key(trimmed);
                    if !seen_view_ids.insert(canonical) {
                        return Err(TemplateError::invalid(
                            "invalid_template",
                            format!("模板中存在重复的视图标识：{trimmed}"),
                        ));
                    }
                    Some(trimmed.to_owned())
                }
            };
            let stored_view =
                rebind_and_validate_view(&table.id, view, &field_id_map, &field_defs)?;
            let created_view =
                store::insert_view(&mut tx, workspace, &table.id, &stored_view, order as i32)
                    .await?;
            if let Some(old_id) = source_view_id {
                view_id_map.insert(old_id, created_view.id.clone());
            }
            inserted_views.push(created_view);
        }
    }

    let mut records = Vec::with_capacity(req.sample_records.len());
    if !req.sample_records.is_empty() {
        let by_new_id: BTreeMap<&str, &FieldDto> = inserted_fields
            .iter()
            .map(|field| (field.id.as_str(), field))
            .collect();
        let locked = vec![table.id.clone()];
        for sample in &req.sample_records {
            let rebound_values =
                rebind_sample_record_values(&sample.values, &field_id_map, &by_new_id)?;
            let normalized = store::normalize_values(&inserted_fields, &rebound_values)?;
            let record = store::create_record_in_tx(
                &mut tx,
                workspace,
                &table.id,
                &inserted_fields,
                &normalized,
                &locked,
                actor,
                None,
            )
            .await?;
            records.push(record);
        }
    }

    let mut response_fields = inserted_fields;
    if !records.is_empty()
        && response_fields
            .iter()
            .any(|field| field.kind == "lookup" || field.kind == "formula")
    {
        projection::project_sheet(
            &mut tx,
            workspace,
            &table.id,
            &mut response_fields,
            &mut records,
            projection::PrimaryRows::All,
        )
        .await?;
    } else {
        projection::hydrate_formula_fields(&mut response_fields);
    }

    store::commit(tx).await?;

    let active_view_id = inserted_views.first().map(|view| view.id.clone());
    Ok(TemplateImportResponse {
        id_map: TemplateIdMap {
            source_table_id,
            table_id: table.id.clone(),
            fields: public_field_id_map,
            views: view_id_map,
        },
        table,
        fields: response_fields,
        views: inserted_views,
        records,
        active_view_id,
    })
}

struct PreparedField {
    source_id: Option<(String, String)>,
    name: String,
    kind: String,
    config: Value,
}

fn canonical_ref_key(raw: &str) -> String {
    let trimmed = raw.trim();
    if validate::is_uuid(trimmed) {
        trimmed.to_ascii_lowercase()
    } else {
        trimmed.to_owned()
    }
}

fn prepare_field_metadata(
    fields: &[TemplateFieldInput],
) -> Result<Vec<PreparedField>, TemplateError> {
    let mut seen_names = BTreeSet::new();
    let mut seen_ids = BTreeSet::new();
    let mut out = Vec::with_capacity(fields.len());
    for field in fields {
        let name = validate::clean_name(&field.name)
            .map_err(|msg| TemplateError::invalid("invalid_value", msg))?;
        if !seen_names.insert(name.clone()) {
            return Err(TemplateError::invalid(
                "duplicate_field_name",
                format!("模板中存在重复的字段名称：{name}"),
            ));
        }
        if !validate::KINDS.contains(&field.kind.as_str()) {
            return Err(TemplateError::invalid(
                "invalid_value",
                format!("不支持的字段类型：{}", field.kind),
            ));
        }
        if let Some(position) = field.position
            && position <= 0
        {
            return Err(TemplateError::invalid(
                "invalid_template",
                "字段位置必须是正整数",
            ));
        }
        let source_id = match field.id.as_deref() {
            None => None,
            Some(raw) => {
                let trimmed = raw.trim();
                if trimmed.is_empty() {
                    return Err(TemplateError::invalid(
                        "invalid_template",
                        "模板字段标识不能为空",
                    ));
                }
                let canonical = canonical_ref_key(trimmed);
                if !seen_ids.insert(canonical.clone()) {
                    return Err(TemplateError::invalid(
                        "duplicate_field_id",
                        format!("模板中存在重复的字段标识：{trimmed}"),
                    ));
                }
                Some((canonical, trimmed.to_owned()))
            }
        };
        out.push(PreparedField {
            source_id,
            name,
            kind: field.kind.clone(),
            config: field.config.clone(),
        });
    }
    Ok(out)
}

fn resolve_and_validate_fields(
    new_table_id: &str,
    source_table_id: Option<&str>,
    prepared: &[PreparedField],
    new_field_ids: &[String],
    field_id_map: &BTreeMap<String, String>,
) -> Result<Vec<Value>, TemplateError> {
    let mut resolved: Vec<Option<Value>> = vec![None; prepared.len()];
    let mut unsupported: Vec<UnsupportedDependencyDto> = Vec::new();
    let mut kind_by_new_id: BTreeMap<&str, &str> = BTreeMap::new();
    let mut name_by_new_id: BTreeMap<String, String> = BTreeMap::new();
    for (spec, new_id) in prepared.iter().zip(new_field_ids.iter()) {
        kind_by_new_id.insert(new_id.as_str(), spec.kind.as_str());
        name_by_new_id.insert(new_id.clone(), spec.name.clone());
    }

    // Pass A: non-derived fields (base fields and self-relations).
    for (idx, spec) in prepared.iter().enumerate() {
        if spec.kind == "lookup" || spec.kind == "formula" {
            continue;
        }
        if spec.kind == "relation" {
            let field_label_id = spec
                .source_id
                .as_ref()
                .map(|(_, display)| display.clone())
                .unwrap_or_else(|| spec.name.clone());
            match rebind_relation_config(
                new_table_id,
                source_table_id,
                &field_label_id,
                &spec.name,
                &spec.config,
            ) {
                Ok(cfg) => resolved[idx] = Some(cfg),
                Err(RelationRebindError::Unsupported(dep)) => unsupported.push(dep),
                Err(RelationRebindError::Invalid(msg)) => {
                    return Err(TemplateError::invalid("invalid_value", msg));
                }
            }
            continue;
        }
        let normalized = validate::normalize_config(&spec.kind, &spec.config)
            .map_err(|msg| TemplateError::invalid("invalid_value", msg))?;
        resolved[idx] = Some(normalized);
    }

    // Pass B: lookup fields (rebind relation_field_id and target_field_id).
    for (idx, spec) in prepared.iter().enumerate() {
        if spec.kind != "lookup" {
            continue;
        }
        let field_label_id = spec
            .source_id
            .as_ref()
            .map(|(_, display)| display.clone())
            .unwrap_or_else(|| spec.name.clone());
        let (raw_rel, raw_target) = parse_template_lookup_keys(&spec.config)?;
        let mapped_rel = field_id_map.get(&canonical_ref_key(&raw_rel)).cloned();
        let mapped_target = field_id_map.get(&canonical_ref_key(&raw_target)).cloned();

        let rel_is_local_relation = mapped_rel
            .as_deref()
            .is_some_and(|new_rel_id| kind_by_new_id.get(new_rel_id) == Some(&"relation"));
        if mapped_rel.is_none() || mapped_target.is_none() || !rel_is_local_relation {
            unsupported.push(UnsupportedDependencyDto {
                field_id: field_label_id,
                field_name: spec.name.clone(),
                kind: spec.kind.clone(),
                code: "external_lookup_unsupported",
                target_table_id: None,
                relation_field_id: Some(raw_rel),
                target_field_id: Some(raw_target),
            });
            continue;
        }

        let rebound = LookupConfig {
            relation_field_id: mapped_rel.expect("checked"),
            target_field_id: mapped_target.expect("checked"),
        }
        .to_json();
        let normalized = validate::normalize_config("lookup", &rebound)
            .map_err(|msg| TemplateError::invalid("invalid_value", msg))?;
        resolved[idx] = Some(normalized);
    }

    if !unsupported.is_empty() {
        return Err(TemplateError::UnsupportedDependency {
            message: "模板包含不支持的外部关联或跨表查找依赖".to_owned(),
            dependencies: unsupported,
        });
    }

    // Pass C: formula fields (compile against new field IDs).
    for (idx, spec) in prepared.iter().enumerate() {
        if spec.kind != "formula" {
            continue;
        }
        let source = projection::parse_formula_input(&spec.config)
            .map_err(|msg| TemplateError::invalid("invalid_value", msg))?;
        let expr = formula::compile(&source, |token| {
            prepared
                .iter()
                .zip(new_field_ids.iter())
                .find(|(candidate, _)| candidate.name == token)
                .map(|(_, new_id)| new_id.clone())
                .or_else(|| field_id_map.get(&canonical_ref_key(token)).cloned())
        })
        .map_err(|msg| TemplateError::invalid("invalid_value", msg))?;
        let rendered = expr.source(&|id| name_by_new_id.get(id).cloned());
        let stored = projection::stored_formula_config(&expr, &rendered)?;
        resolved[idx] = Some(stored);
    }

    // Cycle detection across all derived fields in the new table.
    let mut graph: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for ((spec, new_id), cfg) in prepared
        .iter()
        .zip(new_field_ids.iter())
        .zip(resolved.iter())
    {
        let stored = cfg.as_ref().expect("every field resolved");
        graph.insert(
            new_id.clone(),
            projection::field_dependencies(&spec.kind, stored),
        );
    }
    for (spec, new_id) in prepared.iter().zip(new_field_ids.iter()) {
        if (spec.kind == "lookup" || spec.kind == "formula")
            && projection::has_cycle_from(&graph, new_id)
        {
            return Err(TemplateError::invalid("invalid_value", "字段存在循环引用"));
        }
    }

    Ok(resolved.into_iter().map(|v| v.expect("resolved")).collect())
}

#[derive(Debug)]
enum RelationRebindError {
    Unsupported(UnsupportedDependencyDto),
    Invalid(&'static str),
}

fn rebind_relation_config(
    new_table_id: &str,
    source_table_id: Option<&str>,
    field_id: &str,
    field_name: &str,
    raw: &Value,
) -> Result<Value, RelationRebindError> {
    let Some(object) = raw.as_object() else {
        return Err(RelationRebindError::Invalid("关联字段需要指定目标表"));
    };
    for key in object.keys() {
        if key != "target_table_id" && key != "bidirectional" && key != "reverse_field_id" {
            return Err(RelationRebindError::Invalid("关联字段配置包含不支持的参数"));
        }
    }
    let raw_target = object
        .get("target_table_id")
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .ok_or(RelationRebindError::Invalid("关联字段需要有效的目标表标识"))?;
    let bidirectional = match object.get("bidirectional") {
        None | Some(Value::Null) => false,
        Some(value) => value
            .as_bool()
            .ok_or(RelationRebindError::Invalid("双向关联开关需要是布尔值"))?,
    };
    let has_reverse = object
        .get("reverse_field_id")
        .is_some_and(|value| !value.is_null());

    if bidirectional || has_reverse {
        return Err(RelationRebindError::Unsupported(UnsupportedDependencyDto {
            field_id: field_id.to_owned(),
            field_name: field_name.to_owned(),
            kind: "relation".to_owned(),
            code: "bidirectional_relation_unsupported",
            target_table_id: Some(raw_target.to_ascii_lowercase()),
            relation_field_id: None,
            target_field_id: None,
        }));
    }

    let is_self = raw_target.eq_ignore_ascii_case("self")
        || source_table_id.is_some_and(|src| raw_target.eq_ignore_ascii_case(src));
    if !is_self {
        return Err(RelationRebindError::Unsupported(UnsupportedDependencyDto {
            field_id: field_id.to_owned(),
            field_name: field_name.to_owned(),
            kind: "relation".to_owned(),
            code: "external_relation_unsupported",
            target_table_id: Some(raw_target.to_ascii_lowercase()),
            relation_field_id: None,
            target_field_id: None,
        }));
    }

    let rebound = RelationConfig {
        target_table_id: new_table_id.to_ascii_lowercase(),
        bidirectional: false,
        reverse_field_id: None,
    }
    .to_json();
    let normalized =
        validate::normalize_config("relation", &rebound).map_err(RelationRebindError::Invalid)?;
    relation::validate_target_table(new_table_id, new_table_id, false)
        .map_err(|_| RelationRebindError::Invalid("关联字段配置无效"))?;
    Ok(normalized)
}

fn parse_template_lookup_keys(config: &Value) -> Result<(String, String), TemplateError> {
    let Some(object) = config.as_object() else {
        return Err(TemplateError::invalid(
            "invalid_value",
            "查找字段需要指定关联字段和目标字段",
        ));
    };
    for key in object.keys() {
        if key != "relation_field_id" && key != "target_field_id" {
            return Err(TemplateError::invalid(
                "invalid_value",
                "查找字段配置包含不支持的参数",
            ));
        }
    }
    let relation_field_id = object
        .get("relation_field_id")
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|id| !id.is_empty())
        .ok_or_else(|| TemplateError::invalid("invalid_value", "查找字段需要有效的关联字段标识"))?
        .to_owned();
    let target_field_id = object
        .get("target_field_id")
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|id| !id.is_empty())
        .ok_or_else(|| TemplateError::invalid("invalid_value", "查找字段需要有效的目标字段标识"))?
        .to_owned();
    Ok((relation_field_id, target_field_id))
}

fn rebind_field_ref(
    field_id_map: &BTreeMap<String, String>,
    raw: &str,
    context: &str,
) -> Result<String, TemplateError> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return Err(TemplateError::invalid(
            "unknown_field_reference",
            format!("{context} 引用了空的字段标识"),
        ));
    }
    field_id_map
        .get(&canonical_ref_key(trimmed))
        .cloned()
        .ok_or_else(|| {
            TemplateError::invalid(
                "unknown_field_reference",
                format!("{context} 引用了未知字段：{trimmed}"),
            )
        })
}

fn rebind_and_validate_view(
    new_table_id: &str,
    view: &TemplateViewInput,
    field_id_map: &BTreeMap<String, String>,
    defs: &[FieldDef],
) -> Result<StoredView, TemplateError> {
    let kind = view.kind.as_deref().unwrap_or("grid");
    let columns = match &view.columns {
        None => None,
        Some(ids) => {
            let mut mapped = Vec::with_capacity(ids.len());
            for id in ids {
                mapped.push(rebind_field_ref(field_id_map, id, "视图可见列")?);
            }
            Some(mapped)
        }
    };
    let filter = match &view.filter {
        None => None,
        Some(item) => Some(StoredFilter {
            field_id: rebind_field_ref(field_id_map, &item.field_id, "视图筛选")?,
            op: item.op.clone(),
            value: item.value.clone(),
        }),
    };
    let sort = match &view.sort {
        None => None,
        Some(item) => Some(StoredSort {
            field_id: rebind_field_ref(field_id_map, &item.field_id, "视图排序")?,
            direction: item.direction.clone(),
        }),
    };
    let group = match &view.group {
        None => None,
        Some(id) => Some(rebind_field_ref(field_id_map, id, "视图分组")?),
    };
    let config = rebind_view_config(&view.config, field_id_map)?;
    check_portable_view_filter(filter.as_ref(), |id| {
        defs.iter()
            .find(|field| field.id == id)
            .map(|field| field.kind.as_str())
    })?;

    validate::normalize_view(
        new_table_id,
        &view.name,
        kind,
        &config,
        columns.as_deref(),
        filter.as_ref(),
        sort.as_ref(),
        group.as_deref(),
        defs,
    )
    .map_err(|msg| TemplateError::invalid("invalid_value", msg))
}

fn check_portable_view_filter<'a>(
    filter: Option<&StoredFilter>,
    field_kind: impl FnOnce(&str) -> Option<&'a str>,
) -> Result<(), TemplateError> {
    if let Some(filter) = filter
        && filter.op == "eq"
        && matches!(
            field_kind(&filter.field_id),
            Some("relation" | "attachment")
        )
    {
        return Err(TemplateError::invalid(
            "unportable_view_filter",
            "模板视图不能按附件或关联记录的标识筛选",
        ));
    }
    Ok(())
}

fn rebind_view_config(
    raw: &Value,
    field_id_map: &BTreeMap<String, String>,
) -> Result<Value, TemplateError> {
    if raw.is_null() {
        return Ok(Value::Object(Map::new()));
    }
    let Some(object) = raw.as_object() else {
        return Err(TemplateError::invalid(
            "invalid_value",
            "视图配置必须是对象",
        ));
    };
    let mut rebound = Map::with_capacity(object.len());
    for (key, value) in object {
        if VIEW_CONFIG_SINGLE_FIELD_KEYS.contains(&key.as_str()) {
            if value.is_null() {
                rebound.insert(key.clone(), Value::Null);
            } else if let Some(raw_id) = value.as_str() {
                let mapped = rebind_field_ref(field_id_map, raw_id, &format!("视图配置 {key}"))?;
                rebound.insert(key.clone(), Value::String(mapped));
            } else {
                return Err(TemplateError::invalid(
                    "invalid_value",
                    "视图配置中的字段标识无效",
                ));
            }
        } else if key == "required_field_ids" {
            if value.is_null() {
                rebound.insert(key.clone(), Value::Null);
            } else if let Some(items) = value.as_array() {
                let mut mapped_items = Vec::with_capacity(items.len());
                for item in items {
                    let Some(raw_id) = item.as_str() else {
                        return Err(TemplateError::invalid("invalid_value", "必填字段标识无效"));
                    };
                    let mapped =
                        rebind_field_ref(field_id_map, raw_id, "视图配置 required_field_ids")?;
                    mapped_items.push(Value::String(mapped));
                }
                rebound.insert(key.clone(), Value::Array(mapped_items));
            } else {
                return Err(TemplateError::invalid(
                    "invalid_value",
                    "必填字段列表格式不正确",
                ));
            }
        } else {
            // Preserve unknown keys so validate::normalize_view rejects them consistently.
            rebound.insert(key.clone(), value.clone());
        }
    }
    Ok(Value::Object(rebound))
}

fn rebind_sample_record_values(
    raw_values: &BTreeMap<String, Value>,
    field_id_map: &BTreeMap<String, String>,
    fields_by_new_id: &BTreeMap<&str, &FieldDto>,
) -> Result<BTreeMap<String, Value>, TemplateError> {
    let mut rebound = BTreeMap::new();
    for (raw_key, value) in raw_values {
        let new_id = rebind_field_ref(field_id_map, raw_key, "示例记录字段")?;
        if let Some(field) = fields_by_new_id.get(new_id.as_str())
            && !value.is_null()
            && (field.kind == "attachment" || field.kind == "relation")
        {
            return Err(TemplateError::UnsupportedDependency {
                message: "便携模板示例记录不支持包含附件或关联记录引用".to_owned(),
                dependencies: vec![UnsupportedDependencyDto {
                    field_id: raw_key.clone(),
                    field_name: field.name.clone(),
                    kind: field.kind.clone(),
                    code: if field.kind == "attachment" {
                        "sample_attachment_unsupported"
                    } else {
                        "sample_relation_unsupported"
                    },
                    target_table_id: None,
                    relation_field_id: None,
                    target_field_id: None,
                }],
            });
        }
        rebound.insert(new_id, value.clone());
    }
    Ok(rebound)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const SRC_TABLE: &str = "11111111-1111-4111-8111-111111111111";
    const NEW_TABLE: &str = "22222222-2222-4222-8222-222222222222";
    const EXT_TABLE: &str = "33333333-3333-4333-8333-333333333333";
    const F_OLD_1: &str = "aaaa1111-1111-4111-8111-111111111111";
    const F_OLD_2: &str = "aaaa2222-2222-4222-8222-222222222222";
    const F_OLD_3: &str = "aaaa3333-3333-4333-8333-333333333333";
    const F_OLD_4: &str = "aaaa4444-4444-4444-8444-444444444444";
    const F_NEW_1: &str = "bbbb1111-1111-4111-8111-111111111111";
    const F_NEW_2: &str = "bbbb2222-2222-4222-8222-222222222222";
    const F_NEW_3: &str = "bbbb3333-3333-4333-8333-333333333333";
    const F_NEW_4: &str = "bbbb4444-4444-4444-8444-444444444444";

    #[test]
    fn rebind_relation_supports_self_and_rejects_external_or_bidirectional() {
        let rebound = rebind_relation_config(
            NEW_TABLE,
            Some(SRC_TABLE),
            F_OLD_1,
            "Parent",
            &json!({ "target_table_id": SRC_TABLE, "bidirectional": false }),
        )
        .unwrap();
        assert_eq!(
            rebound,
            json!({ "target_table_id": NEW_TABLE, "bidirectional": false })
        );

        let err = rebind_relation_config(
            NEW_TABLE,
            Some(SRC_TABLE),
            F_OLD_1,
            "External",
            &json!({ "target_table_id": EXT_TABLE, "bidirectional": false }),
        )
        .unwrap_err();
        match err {
            RelationRebindError::Unsupported(dep) => {
                assert_eq!(dep.code, "external_relation_unsupported");
                assert_eq!(dep.target_table_id.as_deref(), Some(EXT_TABLE));
            }
            RelationRebindError::Invalid(msg) => panic!("expected unsupported, got {msg}"),
        }

        let err = rebind_relation_config(
            NEW_TABLE,
            Some(SRC_TABLE),
            F_OLD_1,
            "TwoWay",
            &json!({ "target_table_id": EXT_TABLE, "bidirectional": true }),
        )
        .unwrap_err();
        match err {
            RelationRebindError::Unsupported(dep) => {
                assert_eq!(dep.code, "bidirectional_relation_unsupported");
            }
            RelationRebindError::Invalid(msg) => panic!("expected unsupported, got {msg}"),
        }
    }

    #[test]
    fn resolve_fields_rebinds_self_relation_lookup_and_formula_and_detects_cycles() {
        let fields = vec![
            TemplateFieldInput {
                id: Some(F_OLD_1.to_owned()),
                name: "Price".to_owned(),
                kind: "number".to_owned(),
                position: Some(1),
                config: json!({}),
            },
            TemplateFieldInput {
                id: Some(F_OLD_2.to_owned()),
                name: "Parent".to_owned(),
                kind: "relation".to_owned(),
                position: Some(2),
                config: json!({ "target_table_id": SRC_TABLE, "bidirectional": false }),
            },
            TemplateFieldInput {
                id: Some(F_OLD_3.to_owned()),
                name: "ParentPrice".to_owned(),
                kind: "lookup".to_owned(),
                position: Some(3),
                config: json!({ "relation_field_id": F_OLD_2, "target_field_id": F_OLD_1 }),
            },
            TemplateFieldInput {
                id: Some(F_OLD_4.to_owned()),
                name: "Total".to_owned(),
                kind: "formula".to_owned(),
                position: Some(4),
                config: json!({ "expression": "{Price} + SUM({ParentPrice})" }),
            },
        ];
        let prepared = prepare_field_metadata(&fields).unwrap();
        let new_ids = vec![
            F_NEW_1.to_owned(),
            F_NEW_2.to_owned(),
            F_NEW_3.to_owned(),
            F_NEW_4.to_owned(),
        ];
        let mut id_map = BTreeMap::new();
        id_map.insert(F_OLD_1.to_owned(), F_NEW_1.to_owned());
        id_map.insert(F_OLD_2.to_owned(), F_NEW_2.to_owned());
        id_map.insert(F_OLD_3.to_owned(), F_NEW_3.to_owned());
        id_map.insert(F_OLD_4.to_owned(), F_NEW_4.to_owned());

        let resolved =
            resolve_and_validate_fields(NEW_TABLE, Some(SRC_TABLE), &prepared, &new_ids, &id_map)
                .unwrap();
        assert_eq!(
            resolved[1],
            json!({ "target_table_id": NEW_TABLE, "bidirectional": false })
        );
        assert_eq!(
            resolved[2],
            json!({ "relation_field_id": F_NEW_2, "target_field_id": F_NEW_1 })
        );
        let formula_refs = resolved[3].get("refs").unwrap().as_array().unwrap();
        assert!(formula_refs.contains(&json!(F_NEW_1)));
        assert!(formula_refs.contains(&json!(F_NEW_3)));
        assert!(!formula_refs.contains(&json!(F_OLD_1)));

        // Cycle check: A -> B -> A
        let cyclic = vec![
            TemplateFieldInput {
                id: Some(F_OLD_1.to_owned()),
                name: "A".to_owned(),
                kind: "formula".to_owned(),
                position: None,
                config: json!({ "expression": "{B} + 1" }),
            },
            TemplateFieldInput {
                id: Some(F_OLD_2.to_owned()),
                name: "B".to_owned(),
                kind: "formula".to_owned(),
                position: None,
                config: json!({ "expression": "{A} + 1" }),
            },
        ];
        let prepared_cyclic = prepare_field_metadata(&cyclic).unwrap();
        let err = resolve_and_validate_fields(
            NEW_TABLE,
            None,
            &prepared_cyclic,
            &[F_NEW_1.to_owned(), F_NEW_2.to_owned()],
            &id_map,
        )
        .unwrap_err();
        assert!(matches!(err, TemplateError::Invalid { .. }));
    }

    #[test]
    fn rebind_view_remaps_columns_filter_sort_group_and_config_preserving_literals() {
        let mut id_map = BTreeMap::new();
        id_map.insert(F_OLD_1.to_owned(), F_NEW_1.to_owned());
        id_map.insert(F_OLD_2.to_owned(), F_NEW_2.to_owned());
        id_map.insert(F_OLD_3.to_owned(), F_NEW_3.to_owned());

        let defs = vec![
            FieldDef {
                id: F_NEW_1.to_owned(),
                kind: "text".to_owned(),
                config: json!({}),
            },
            FieldDef {
                id: F_NEW_2.to_owned(),
                kind: "single_select".to_owned(),
                config: json!({ "options": [{ "id": "open", "label": "Open" }] }),
            },
            FieldDef {
                id: F_NEW_3.to_owned(),
                kind: "relation".to_owned(),
                config: json!({ "target_table_id": NEW_TABLE, "bidirectional": false }),
            },
        ];

        let kanban = TemplateViewInput {
            id: None,
            name: "Board View".to_owned(),
            kind: Some("kanban".to_owned()),
            config: json!({ "group_field_id": F_OLD_2, "title_field_id": F_OLD_1 }),
            columns: Some(vec![F_OLD_1.to_owned(), F_OLD_2.to_owned()]),
            filter: Some(StoredFilter {
                field_id: F_OLD_2.to_owned(),
                op: "eq".to_owned(),
                value: Some(json!("open")),
            }),
            sort: Some(StoredSort {
                field_id: F_OLD_1.to_owned(),
                direction: "desc".to_owned(),
            }),
            group: Some(F_OLD_2.to_owned()),
        };

        let stored = rebind_and_validate_view(NEW_TABLE, &kanban, &id_map, &defs).unwrap();
        assert_eq!(stored.name, "Board View");
        assert_eq!(
            stored.columns,
            Some(vec![F_NEW_1.to_owned(), F_NEW_2.to_owned()])
        );
        assert_eq!(
            stored.filter,
            Some(StoredFilter {
                field_id: F_NEW_2.to_owned(),
                op: "eq".to_owned(),
                value: Some(json!("open")),
            })
        );
        assert_eq!(
            stored.sort,
            Some(StoredSort {
                field_id: F_NEW_1.to_owned(),
                direction: "desc".to_owned(),
            })
        );
        assert_eq!(stored.group.as_deref(), Some(F_NEW_2));
        assert_eq!(
            stored.config,
            json!({ "group_field_id": F_NEW_2, "title_field_id": F_NEW_1 })
        );

        let org = TemplateViewInput {
            id: None,
            name: "Org".to_owned(),
            kind: Some("org_chart".to_owned()),
            config: json!({ "parent_field_id": F_OLD_3, "title_field_id": F_OLD_1 }),
            columns: None,
            filter: None,
            sort: None,
            group: None,
        };
        let stored_org = rebind_and_validate_view(NEW_TABLE, &org, &id_map, &defs).unwrap();
        assert_eq!(
            stored_org.config,
            json!({ "parent_field_id": F_NEW_3, "title_field_id": F_NEW_1 })
        );

        let form = TemplateViewInput {
            id: None,
            name: "Intake".to_owned(),
            kind: Some("form".to_owned()),
            config: json!({ "required_field_ids": [F_OLD_1] }),
            columns: Some(vec![F_OLD_1.to_owned()]),
            filter: None,
            sort: None,
            group: None,
        };
        let stored_form = rebind_and_validate_view(NEW_TABLE, &form, &id_map, &defs).unwrap();
        assert_eq!(
            stored_form.config,
            json!({ "required_field_ids": [F_NEW_1] })
        );

        // Unknown field reference fails.
        let bad = TemplateViewInput {
            id: None,
            name: "Broken".to_owned(),
            kind: Some("grid".to_owned()),
            config: json!({}),
            columns: Some(vec![F_OLD_4.to_owned()]),
            filter: None,
            sort: None,
            group: None,
        };
        let err = rebind_and_validate_view(NEW_TABLE, &bad, &id_map, &defs).unwrap_err();
        assert!(matches!(
            err,
            TemplateError::Invalid {
                code: "unknown_field_reference",
                ..
            }
        ));
    }

    #[test]
    fn parse_sample_record_ids_validates_and_deduplicates() {
        assert!(parse_sample_record_ids_query(None).unwrap().is_empty());
        assert!(
            parse_sample_record_ids_query(Some("  "))
                .unwrap()
                .is_empty()
        );
        let ids = parse_sample_record_ids_query(Some(&format!(
            "{}, {}",
            F_OLD_1.to_ascii_uppercase(),
            F_OLD_1
        )))
        .unwrap();
        assert_eq!(ids, vec![F_OLD_1.to_owned()]);
        assert!(parse_sample_record_ids_query(Some("not-a-uuid")).is_err());
    }
}
