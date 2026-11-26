//! Read-only lookup and formula projection for native task properties.

use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::{CallerActor, IssueReadError, resolve_caller_access_on};
use crate::tables::FormulaExpr as Expr;
use crate::tables::issue_derived::{
    self, IssueLookupConfig, MAX_ISSUE_BATCH, PropertyDef, TableFieldKey,
    batch_lookup_target_values, record_ids_from_relation_value,
};

#[derive(Debug, Deserialize)]
pub struct IssuePropertyProjectRequest {
    pub issue_ids: Vec<String>,
}

#[derive(Debug, Serialize)]
pub struct IssuePropertyProjectResponse {
    pub properties: HashMap<String, Map<String, Value>>,
}

async fn merge_computed_properties(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    defs: &[PropertyDef],
    issue_ids: &[String],
    stored_by_issue: &HashMap<String, Map<String, Value>>,
) -> Result<HashMap<String, Map<String, Value>>, IssueReadError> {
    if issue_ids.is_empty() {
        return Ok(HashMap::new());
    }
    if issue_ids.len() > MAX_ISSUE_BATCH {
        return Err(IssueReadError::InvalidInput(
            "too many issues in projection batch",
        ));
    }
    let derived: Vec<_> = defs
        .iter()
        .filter(|def| def.kind == "lookup" || def.kind == "formula")
        .collect();
    if derived.is_empty() {
        return Ok(HashMap::new());
    }

    let derived_order = topo_derived(defs);

    let relation_meta: HashMap<String, (String, String)> = defs
        .iter()
        .filter(|def| def.kind == "record_relation")
        .filter_map(|def| {
            let table = def.config.get("target_table_id").and_then(Value::as_str)?;
            Some((def.id.clone(), (table.to_ascii_lowercase(), def.id.clone())))
        })
        .collect();

    let lookup_cfgs: HashMap<String, IssueLookupConfig> = derived
        .iter()
        .filter(|def| def.kind == "lookup")
        .filter_map(|def| {
            IssueLookupConfig::from_stored(&def.config)
                .ok()
                .map(|cfg| (def.id.clone(), cfg))
        })
        .collect();

    let mut batch_loads: BTreeMap<TableFieldKey, BTreeSet<String>> = BTreeMap::new();
    for issue_id in issue_ids {
        let Some(stored) = stored_by_issue.get(issue_id) else {
            continue;
        };
        for def in &derived {
            if def.kind != "lookup" {
                continue;
            }
            let Some(lookup) = lookup_cfgs.get(&def.id) else {
                continue;
            };
            let Some((target_table, _)) = relation_meta.get(&lookup.relation_property_id) else {
                continue;
            };
            let relation_value = stored
                .get(&lookup.relation_property_id)
                .cloned()
                .unwrap_or(Value::Null);
            let record_ids = record_ids_from_relation_value(&relation_value);
            if record_ids.is_empty() {
                continue;
            }
            let key = TableFieldKey {
                table_id: target_table.clone(),
                field_id: lookup.target_field_id.clone(),
            };
            batch_loads
                .entry(key)
                .or_default()
                .extend(record_ids.into_iter());
        }
    }

    let lookup_cache = batch_lookup_target_values(tx, workspace_id, &batch_loads)
        .await
        .map_err(|_| IssueReadError::DatabaseFailed)?;

    let mut result = HashMap::new();
    for issue_id in issue_ids {
        let Some(stored) = stored_by_issue.get(issue_id) else {
            continue;
        };
        let merged = project_computed_for_issue(
            defs,
            &derived_order,
            &lookup_cfgs,
            &relation_meta,
            &lookup_cache,
            stored,
        );
        if !merged.is_empty() {
            result.insert(issue_id.clone(), merged);
        }
    }
    Ok(result)
}

fn project_computed_for_issue(
    defs: &[PropertyDef],
    derived_order: &[String],
    lookup_cfgs: &HashMap<String, IssueLookupConfig>,
    relation_meta: &HashMap<String, (String, String)>,
    lookup_cache: &HashMap<TableFieldKey, HashMap<String, Value>>,
    stored: &Map<String, Value>,
) -> Map<String, Value> {
    let mut out = Map::new();
    let mut cache = HashMap::new();
    for property_id in derived_order {
        let Some(def) = defs.iter().find(|def| def.id == *property_id) else {
            continue;
        };
        let result = match def.kind.as_str() {
            "lookup" => eval_lookup(def, stored, lookup_cfgs, relation_meta, lookup_cache),
            "formula" => eval_formula(defs, stored, def, &cache),
            _ => continue,
        };
        out.insert(
            property_id.clone(),
            result.as_ref().cloned().unwrap_or(Value::Null),
        );
        cache.insert(property_id.clone(), result);
    }
    out
}

async fn load_property_defs(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
) -> Result<Vec<PropertyDef>, IssueReadError> {
    let rows = sqlx::query(
        "SELECT id::text, name, type, config::text FROM issue_property
         WHERE workspace_id = $1::uuid AND archived_at IS NULL",
    )
    .bind(workspace_id)
    .fetch_all(&mut **tx)
    .await
    .map_err(|_| IssueReadError::DatabaseFailed)?;
    let mut defs = Vec::with_capacity(rows.len());
    for row in rows {
        let config: String = row.get(3);
        let config: Value =
            serde_json::from_str(&config).map_err(|_| IssueReadError::DatabaseFailed)?;
        defs.push(PropertyDef {
            id: row.get(0),
            name: row.get(1),
            kind: row.get(2),
            config,
        });
    }
    Ok(defs)
}

fn topo_derived(defs: &[PropertyDef]) -> Vec<String> {
    let derived: Vec<_> = defs
        .iter()
        .filter(|def| def.kind == "lookup" || def.kind == "formula")
        .map(|def| def.id.clone())
        .collect();
    let mut graph = BTreeMap::<String, Vec<String>>::new();
    for def in defs
        .iter()
        .filter(|def| def.kind == "lookup" || def.kind == "formula")
    {
        graph.insert(
            def.id.clone(),
            issue_derived::property_dependencies(&def.kind, &def.config),
        );
    }
    let mut order = Vec::new();
    let mut visited = HashSet::new();
    for seed in &derived {
        visit_topo(seed, &graph, &mut visited, &mut order);
    }
    order
}

fn visit_topo(
    node: &str,
    graph: &BTreeMap<String, Vec<String>>,
    visited: &mut HashSet<String>,
    order: &mut Vec<String>,
) {
    if !visited.insert(node.to_owned()) {
        return;
    }
    if let Some(deps) = graph.get(node) {
        for dep in deps {
            if graph.contains_key(dep) {
                visit_topo(dep, graph, visited, order);
            }
        }
    }
    order.push(node.to_owned());
}

fn eval_lookup(
    def: &PropertyDef,
    stored: &Map<String, Value>,
    lookup_cfgs: &HashMap<String, IssueLookupConfig>,
    relation_meta: &HashMap<String, (String, String)>,
    lookup_cache: &HashMap<TableFieldKey, HashMap<String, Value>>,
) -> Result<Value, &'static str> {
    let lookup = lookup_cfgs.get(&def.id).ok_or("查找属性配置无效")?;
    let (target_table, _) = relation_meta
        .get(&lookup.relation_property_id)
        .ok_or("查找引用的 record_relation 不存在")?;
    let relation_value = stored
        .get(&lookup.relation_property_id)
        .cloned()
        .unwrap_or(Value::Null);
    let record_ids = record_ids_from_relation_value(&relation_value);
    let key = TableFieldKey {
        table_id: target_table.clone(),
        field_id: lookup.target_field_id.clone(),
    };
    let values = lookup_cache.get(&key).ok_or("查找目标不可用")?;
    let mut ordered = Vec::with_capacity(record_ids.len());
    for record_id in record_ids {
        if let Some(value) = values.get(&record_id) {
            ordered.push(value.clone());
        }
    }
    Ok(Value::Array(ordered))
}

fn eval_formula(
    defs: &[PropertyDef],
    stored: &Map<String, Value>,
    def: &PropertyDef,
    cache: &HashMap<String, Result<Value, &'static str>>,
) -> Result<Value, &'static str> {
    let raw = def.config.get("ast").ok_or("公式配置无效")?;
    let expr: Expr = serde_json::from_value(raw.clone()).map_err(|_| "公式语法树无效")?;
    expr.evaluate(&mut |property_id| {
        if let Some(property) = defs.iter().find(|d| d.id == property_id)
            && (property.kind == "lookup" || property.kind == "formula")
        {
            return match cache.get(property_id) {
                Some(Ok(value)) => Ok(value.clone()),
                Some(Err(message)) => Err(message),
                None => Err("依赖尚未计算"),
            };
        }
        Ok(stored.get(property_id).cloned().unwrap_or(Value::Null))
    })
}

pub async fn project_issue_properties(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    request: IssuePropertyProjectRequest,
) -> Result<IssuePropertyProjectResponse, IssueReadError> {
    if request.issue_ids.len() > MAX_ISSUE_BATCH {
        return Err(IssueReadError::InvalidInput("too many issue ids"));
    }
    if request.issue_ids.is_empty() {
        return Ok(IssuePropertyProjectResponse {
            properties: HashMap::new(),
        });
    }

    let mut tx = pool
        .begin()
        .await
        .map_err(|_| IssueReadError::DatabaseFailed)?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
        .execute(&mut *tx)
        .await
        .map_err(|_| IssueReadError::DatabaseFailed)?;
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor).await?;
    let defs = load_property_defs(&mut tx, workspace_id).await?;
    let derived: Vec<_> = defs
        .iter()
        .filter(|def| def.kind == "lookup" || def.kind == "formula")
        .collect();
    if derived.is_empty() {
        return Ok(IssuePropertyProjectResponse {
            properties: HashMap::new(),
        });
    }

    let mut requested = HashSet::new();
    for raw in &request.issue_ids {
        let id = raw.trim().to_ascii_lowercase();
        if super::is_uuid(&id) {
            requested.insert(id);
        }
    }
    if requested.is_empty() {
        return Err(IssueReadError::InvalidInput("no valid issue ids"));
    }
    let id_list: Vec<String> = requested.iter().cloned().collect();

    let rows = sqlx::query(
        "SELECT id::text, project_id::text, properties::text FROM issue
         WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])",
    )
    .bind(workspace_id)
    .bind(&id_list)
    .fetch_all(&mut *tx)
    .await
    .map_err(|_| IssueReadError::DatabaseFailed)?;

    let mut stored_by_issue = HashMap::new();
    let mut authorized = Vec::new();
    for row in rows {
        let id: String = row.get(0);
        let project_id: Option<String> =
            row.try_get(1).map_err(|_| IssueReadError::DatabaseFailed)?;
        if !hidden.can_see(project_id.as_deref()) {
            continue;
        }
        if !requested.contains(&id) {
            continue;
        }
        authorized.push(id.clone());
        let props: String = row.get(2);
        let map: Map<String, Value> = serde_json::from_str(&props).unwrap_or_else(|_| Map::new());
        stored_by_issue.insert(id, map);
    }

    let computed =
        merge_computed_properties(&mut tx, workspace_id, &defs, &authorized, &stored_by_issue)
            .await?;
    tx.commit()
        .await
        .map_err(|_| IssueReadError::DatabaseFailed)?;

    Ok(IssuePropertyProjectResponse {
        properties: computed,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn topo_orders_dependencies_before_dependents() {
        let defs = vec![
            PropertyDef {
                id: "b".into(),
                name: "B".into(),
                kind: "formula".into(),
                config: serde_json::json!({"ast": {"Field": "a"}}),
            },
            PropertyDef {
                id: "a".into(),
                name: "A".into(),
                kind: "lookup".into(),
                config: serde_json::json!({
                    "relation_property_id": "rel",
                    "target_field_id": "fld"
                }),
            },
        ];
        let order = topo_derived(&defs);
        let a_pos = order.iter().position(|id| id == "a").unwrap();
        let b_pos = order.iter().position(|id| id == "b").unwrap();
        assert!(a_pos < b_pos);
    }
}
