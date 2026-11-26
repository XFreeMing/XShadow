//! Shared derived-value helpers for native task properties (lookup / formula).
//!
//! Task formulas reuse the generic-table expression compiler; property refs are
//! stored as definition ids in the AST.

use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};

use serde_json::{Value, json};
use sqlx::{Postgres, Row, Transaction};

use super::formula::{self, Expr};
use super::projection;
use super::store::{RecordDto, StoreError, failed, load_fields};
use super::validate;

pub(crate) const MAX_ISSUE_BATCH: usize = 200;

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct IssueLookupConfig {
    pub relation_property_id: String,
    pub target_field_id: String,
}

impl IssueLookupConfig {
    pub(crate) fn parse_input(config: &Value) -> Result<Self, &'static str> {
        let Some(object) = config.as_object() else {
            return Err("查找属性需要关联字段与目标字段");
        };
        for key in object.keys() {
            if key != "relation_property_id" && key != "target_field_id" {
                return Err("查找属性配置包含不支持的参数");
            }
        }
        let relation_property_id = object
            .get("relation_property_id")
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|id| validate::is_uuid(id))
            .ok_or("查找属性需要有效的 record_relation 属性")?
            .to_ascii_lowercase();
        let target_field_id = object
            .get("target_field_id")
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|id| validate::is_uuid(id))
            .ok_or("查找属性需要有效的目标字段")?
            .to_ascii_lowercase();
        Ok(Self {
            relation_property_id,
            target_field_id,
        })
    }

    pub(crate) fn from_stored(config: &Value) -> Result<Self, &'static str> {
        let Some(object) = config.as_object() else {
            return Err("查找属性配置无效");
        };
        let relation_property_id = object
            .get("relation_property_id")
            .and_then(Value::as_str)
            .filter(|id| validate::is_uuid(id))
            .ok_or("查找属性配置无效")?
            .to_ascii_lowercase();
        let target_field_id = object
            .get("target_field_id")
            .and_then(Value::as_str)
            .filter(|id| validate::is_uuid(id))
            .ok_or("查找属性配置无效")?
            .to_ascii_lowercase();
        Ok(Self {
            relation_property_id,
            target_field_id,
        })
    }

    pub(crate) fn to_json(&self) -> Value {
        json!({
            "relation_property_id": self.relation_property_id,
            "target_field_id": self.target_field_id,
        })
    }
}

pub(crate) fn parse_formula_input(config: &Value) -> Result<String, &'static str> {
    let Some(object) = config.as_object() else {
        return Err("公式属性需要表达式");
    };
    for key in object.keys() {
        if key != "expression" {
            return Err("公式属性配置包含不支持的参数");
        }
    }
    let expression = object
        .get("expression")
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|expr| !expr.is_empty())
        .ok_or("公式表达式不能为空")?;
    Ok(expression.to_owned())
}

fn parse_stored_ast(config: &Value) -> Result<Expr, &'static str> {
    let raw = config.get("ast").ok_or("公式配置缺少语法树")?;
    serde_json::from_value(raw.clone()).map_err(|_| "公式语法树无效")
}

pub(crate) fn stored_formula_config(expr: &Expr, rendered: &str) -> Result<Value, StoreError> {
    let ast = serde_json::to_value(expr).map_err(|_| StoreError::Failed)?;
    let mut refs = Vec::new();
    expr.refs(&mut refs);
    refs.sort();
    refs.dedup();
    Ok(json!({
        "expression": rendered,
        "ast": ast,
        "refs": refs,
    }))
}

#[derive(Clone)]
pub(crate) struct PropertyDef {
    pub id: String,
    pub name: String,
    pub kind: String,
    pub config: Value,
}

pub(crate) fn compile_issue_derived_config(
    defs: &[PropertyDef],
    property_id: &str,
    kind: &str,
    raw_config: &Value,
) -> Result<Value, StoreError> {
    let id_lower = property_id.to_ascii_lowercase();
    let stored = match kind {
        "lookup" => {
            let lookup = IssueLookupConfig::parse_input(raw_config).map_err(StoreError::Invalid)?;
            let relation = defs
                .iter()
                .find(|def| def.id == lookup.relation_property_id && def.kind == "record_relation")
                .ok_or(StoreError::Invalid("查找引用的 record_relation 不存在"))?;
            let _target_table = relation
                .config
                .get("target_table_id")
                .and_then(Value::as_str)
                .filter(|id| validate::is_uuid(id))
                .ok_or(StoreError::Invalid("record_relation 缺少目标表"))?;
            lookup.to_json()
        }
        "formula" => {
            let source = parse_formula_input(raw_config).map_err(StoreError::Invalid)?;
            let name_map: BTreeMap<String, String> = defs
                .iter()
                .filter(|def| def.id != id_lower)
                .map(|def| (def.id.clone(), def.name.clone()))
                .collect();
            let expr = formula::compile(&source, |name| {
                defs.iter()
                    .find(|def| def.name == name)
                    .or_else(|| defs.iter().find(|def| def.id.eq_ignore_ascii_case(name)))
                    .filter(|def| def.id != id_lower)
                    .map(|def| def.id.clone())
            })
            .map_err(StoreError::Invalid)?;
            let rendered = expr.source(&|id| name_map.get(id).cloned());
            stored_formula_config(&expr, &rendered)?
        }
        _ => return Err(StoreError::Invalid("不支持的属性类型")),
    };

    let mut graph = property_dependency_graph(defs);
    let new_deps = property_dependencies(kind, &stored);
    graph.insert(id_lower.clone(), new_deps);
    if has_cycle_from(&graph, &id_lower) {
        return Err(StoreError::Invalid("属性存在循环引用"));
    }
    Ok(stored)
}

pub(crate) fn property_dependencies(kind: &str, config: &Value) -> Vec<String> {
    match kind {
        "lookup" => IssueLookupConfig::from_stored(config)
            .map(|lookup| vec![lookup.relation_property_id])
            .unwrap_or_default(),
        "formula" => parse_stored_ast(config)
            .map(|expr| {
                let mut refs = Vec::new();
                expr.refs(&mut refs);
                refs.into_iter().map(|id| id.to_ascii_lowercase()).collect()
            })
            .unwrap_or_default(),
        _ => Vec::new(),
    }
}

fn property_dependency_graph(defs: &[PropertyDef]) -> BTreeMap<String, Vec<String>> {
    let mut graph = BTreeMap::new();
    for def in defs {
        if def.kind == "lookup" || def.kind == "formula" {
            graph.insert(
                def.id.to_ascii_lowercase(),
                property_dependencies(&def.kind, &def.config),
            );
        }
    }
    graph
}

fn has_cycle_from(graph: &BTreeMap<String, Vec<String>>, start: &str) -> bool {
    fn dfs(
        graph: &BTreeMap<String, Vec<String>>,
        node: &str,
        visiting: &mut HashSet<String>,
        visited: &mut HashSet<String>,
    ) -> bool {
        if !visiting.insert(node.to_owned()) {
            return true;
        }
        if let Some(deps) = graph.get(node) {
            for dep in deps {
                if visiting.contains(dep) {
                    return true;
                }
                if !visited.contains(dep) && dfs(graph, dep, visiting, visited) {
                    return true;
                }
            }
        }
        visiting.remove(node);
        visited.insert(node.to_owned());
        false
    }
    let mut visiting = HashSet::new();
    let mut visited = HashSet::new();
    dfs(graph, start, &mut visiting, &mut visited)
}

pub(crate) async fn verify_lookup_target_field(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    relation_property_id: &str,
    target_field_id: &str,
) -> Result<(), StoreError> {
    let row = sqlx::query(
        "SELECT config::text FROM issue_property
         WHERE id = $1::uuid AND workspace_id = $2::uuid AND type = 'record_relation'",
    )
    .bind(relation_property_id)
    .bind(workspace)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?;
    let Some(row) = row else {
        return Err(StoreError::Invalid("查找引用的 record_relation 不存在"));
    };
    let config: String = row.get(0);
    let raw: Value = serde_json::from_str(&config).map_err(|_| StoreError::Failed)?;
    let target_table = raw
        .get("target_table_id")
        .and_then(Value::as_str)
        .filter(|id| validate::is_uuid(id))
        .ok_or(StoreError::Invalid("record_relation 缺少目标表"))?;
    let exists = sqlx::query_scalar::<_, bool>(
        "SELECT EXISTS(
            SELECT 1 FROM generic_field
            WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
        )",
    )
    .bind(target_field_id)
    .bind(target_table)
    .bind(workspace)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    if !exists {
        return Err(StoreError::Invalid("查找的目标字段不存在"));
    }
    Ok(())
}

async fn values_for_table_records(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table_id: &str,
    record_ids: &[String],
    field_id: &str,
) -> Result<HashMap<String, Value>, StoreError> {
    let mut fields = load_fields(tx, workspace, table_id).await?;
    let Some(target) = fields.iter().find(|field| field.id == field_id) else {
        return Ok(HashMap::new());
    };
    let derived = matches!(target.kind.as_str(), "lookup" | "formula");
    let rows = sqlx::query(
        "SELECT r.id::text, r.revision, r.created_at::text, r.updated_at::text,
                c.field_id::text, c.value::text
         FROM generic_record r
         LEFT JOIN generic_cell c ON c.workspace_id = r.workspace_id AND c.record_id = r.id
           AND ($4::bool OR c.field_id = $5::uuid)
         WHERE r.workspace_id = $1::uuid AND r.table_id = $2::uuid
           AND r.id = ANY($3::text[]::uuid[])",
    )
    .bind(workspace)
    .bind(table_id)
    .bind(record_ids)
    .bind(derived)
    .bind(field_id)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let mut records = BTreeMap::new();
    for row in rows {
        let id: String = row.get(0);
        let record = records.entry(id.clone()).or_insert_with(|| RecordDto {
            id,
            revision: row.get(1),
            created_at: row.get(2),
            updated_at: row.get(3),
            values: BTreeMap::new(),
            errors: BTreeMap::new(),
        });
        let key: Option<String> = row.get(4);
        let raw: Option<String> = row.get(5);
        if let (Some(key), Some(raw)) = (key, raw) {
            let value = serde_json::from_str(&raw).map_err(|_| StoreError::Failed)?;
            record.values.insert(key, value);
        }
    }
    let mut records: Vec<_> = records.into_values().collect();
    if derived {
        projection::project_sheet(tx, workspace, table_id, &mut fields, &mut records).await?;
    }
    Ok(records
        .into_iter()
        .map(|mut record| {
            let value = record.values.remove(field_id).unwrap_or(Value::Null);
            (record.id, value)
        })
        .collect())
}

#[derive(Clone, Debug, Hash, Eq, PartialEq, Ord, PartialOrd)]
pub(crate) struct TableFieldKey {
    pub table_id: String,
    pub field_id: String,
}

/// Batch-read lookup targets: one generic projection per distinct table/field pair.
pub(crate) async fn batch_lookup_target_values(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    loads: &BTreeMap<TableFieldKey, BTreeSet<String>>,
) -> Result<HashMap<TableFieldKey, HashMap<String, Value>>, StoreError> {
    let mut out = HashMap::new();
    for (key, record_ids) in loads {
        if record_ids.is_empty() {
            out.insert(key.clone(), HashMap::new());
            continue;
        }
        let list: Vec<String> = record_ids.iter().cloned().collect();
        let values =
            values_for_table_records(tx, workspace, &key.table_id, &list, &key.field_id).await?;
        out.insert(key.clone(), values);
    }
    Ok(out)
}

pub(crate) fn record_ids_from_relation_value(value: &Value) -> Vec<String> {
    let Some(items) = value.as_array() else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for item in items {
        if let Some(id) = item.as_str().filter(|id| validate::is_uuid(id)) {
            out.push(id.to_ascii_lowercase());
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cycle_detection_rejects_formula_loops() {
        let mut graph = BTreeMap::new();
        graph.insert("a".to_owned(), vec!["b".to_owned()]);
        graph.insert("b".to_owned(), vec!["a".to_owned()]);
        assert!(has_cycle_from(&graph, "a"));
    }
}
