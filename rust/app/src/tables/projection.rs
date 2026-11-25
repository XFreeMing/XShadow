use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet, VecDeque};

use serde_json::{Map, Value, json};
use sqlx::{Postgres, Row, Transaction};

use super::formula::{self, Expr};
use super::relation::{self, RelationConfig};
use super::store::{
    FieldDto, RecordDto, StoreError, failed, field_from_row, load_fields, load_records, map_write,
    next_position, ts,
};
use super::validate;

const MAX_EVAL_DEPTH: usize = 32;
const MAX_TABLE_HOPS: usize = 16;

#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct LookupConfig {
    pub relation_field_id: String,
    pub target_field_id: String,
}

impl LookupConfig {
    pub fn parse_input(config: &Value) -> Result<Self, &'static str> {
        let Some(object) = config.as_object() else {
            return Err("查找字段需要指定关联字段和目标字段");
        };
        for key in object.keys() {
            if key != "relation_field_id" && key != "target_field_id" {
                return Err("查找字段配置包含不支持的参数");
            }
        }
        let relation_field_id = object
            .get("relation_field_id")
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|id| validate::is_uuid(id))
            .ok_or("查找字段需要有效的关联字段标识")?
            .to_ascii_lowercase();
        let target_field_id = object
            .get("target_field_id")
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|id| validate::is_uuid(id))
            .ok_or("查找字段需要有效的目标字段标识")?
            .to_ascii_lowercase();
        Ok(Self {
            relation_field_id,
            target_field_id,
        })
    }

    pub fn from_stored(config: &Value) -> Result<Self, &'static str> {
        let Some(object) = config.as_object() else {
            return Err("查找字段配置无效");
        };
        let relation_field_id = object
            .get("relation_field_id")
            .and_then(Value::as_str)
            .filter(|id| validate::is_uuid(id))
            .ok_or("查找字段配置无效")?
            .to_ascii_lowercase();
        let target_field_id = object
            .get("target_field_id")
            .and_then(Value::as_str)
            .filter(|id| validate::is_uuid(id))
            .ok_or("查找字段配置无效")?
            .to_ascii_lowercase();
        Ok(Self {
            relation_field_id,
            target_field_id,
        })
    }

    pub fn to_json(&self) -> Value {
        let mut object = Map::new();
        object.insert(
            "relation_field_id".to_owned(),
            Value::String(self.relation_field_id.clone()),
        );
        object.insert(
            "target_field_id".to_owned(),
            Value::String(self.target_field_id.clone()),
        );
        Value::Object(object)
    }
}

pub(super) fn parse_formula_input(config: &Value) -> Result<String, &'static str> {
    let Some(object) = config.as_object() else {
        return Err("公式字段需要表达式");
    };
    for key in object.keys() {
        if key != "expression" {
            return Err("公式字段配置包含不支持的参数");
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

fn stored_formula_config(expr: &Expr, rendered: &str) -> Result<Value, StoreError> {
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

pub(super) fn hydrate_formula_fields(fields: &mut [FieldDto]) {
    let names: BTreeMap<String, String> = fields
        .iter()
        .map(|field| (field.id.clone(), field.name.clone()))
        .collect();
    for field in fields.iter_mut() {
        if field.kind == "formula" {
            hydrate_single_formula(field, &names);
        }
    }
}

pub(super) fn hydrate_single_formula(field: &mut FieldDto, names: &BTreeMap<String, String>) {
    if field.kind != "formula" {
        return;
    }
    let expression = match parse_stored_ast(&field.config) {
        Ok(expr) => expr.source(&|id| names.get(id).cloned()),
        Err(_) => field
            .config
            .get("expression")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_owned(),
    };
    field.config = json!({ "expression": expression });
}

#[derive(Clone)]
struct WorkspaceField {
    id: String,
    table_id: String,
    name: String,
    kind: String,
    config: Value,
}

async fn lock_workspace_tables_sorted(
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

async fn load_workspace_fields(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
) -> Result<Vec<WorkspaceField>, StoreError> {
    let rows = sqlx::query(
        "SELECT id::text, table_id::text, name, kind, config::text
         FROM generic_field
         WHERE workspace_id = $1::uuid
         ORDER BY table_id, position, id",
    )
    .bind(workspace)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let mut fields = Vec::with_capacity(rows.len());
    for row in rows {
        let raw: String = row.get(4);
        let config = serde_json::from_str(&raw).map_err(|_| StoreError::Failed)?;
        fields.push(WorkspaceField {
            id: row.get(0),
            table_id: row.get(1),
            name: row.get(2),
            kind: row.get(3),
            config,
        });
    }
    Ok(fields)
}

fn compile_derived_config(
    workspace_fields: &[WorkspaceField],
    table_id: &str,
    field_id: &str,
    kind: &str,
    raw_config: &Value,
) -> Result<Value, StoreError> {
    let table_lower = table_id.to_ascii_lowercase();
    let stored = match kind {
        "lookup" => {
            let lookup = LookupConfig::parse_input(raw_config).map_err(StoreError::Invalid)?;
            let relation_field = workspace_fields
                .iter()
                .find(|field| {
                    field.table_id == table_lower
                        && field.id == lookup.relation_field_id
                        && field.kind == "relation"
                })
                .ok_or(StoreError::Invalid("查找字段引用的关联字段不存在"))?;
            let rel = RelationConfig::from_stored(&relation_field.config)?;
            let target_exists = workspace_fields.iter().any(|field| {
                field.table_id == rel.target_table_id && field.id == lookup.target_field_id
            });
            if !target_exists {
                return Err(StoreError::Invalid("查找的目标字段不存在或不属于目标表"));
            }
            lookup.to_json()
        }
        "formula" => {
            let source = parse_formula_input(raw_config).map_err(StoreError::Invalid)?;
            let table_fields: Vec<&WorkspaceField> = workspace_fields
                .iter()
                .filter(|field| field.table_id == table_lower)
                .collect();
            let expr = formula::compile(&source, |name| {
                table_fields
                    .iter()
                    .find(|field| field.name == name)
                    .or_else(|| {
                        table_fields
                            .iter()
                            .find(|field| field.id.eq_ignore_ascii_case(name))
                    })
                    .map(|field| field.id.clone())
            })
            .map_err(StoreError::Invalid)?;
            let names: BTreeMap<String, String> = table_fields
                .iter()
                .map(|field| (field.id.clone(), field.name.clone()))
                .collect();
            let rendered = expr.source(&|id| names.get(id).cloned());
            stored_formula_config(&expr, &rendered)?
        }
        _ => return Err(StoreError::Invalid("不支持的字段类型")),
    };

    let mut graph = build_dependency_graph(workspace_fields);
    let new_deps = field_dependencies(kind, &stored);
    graph.insert(field_id.to_ascii_lowercase(), new_deps);
    if has_cycle_from(&graph, &field_id.to_ascii_lowercase()) {
        return Err(StoreError::Invalid("字段存在循环引用"));
    }

    Ok(stored)
}

fn build_dependency_graph(fields: &[WorkspaceField]) -> BTreeMap<String, Vec<String>> {
    let mut graph = BTreeMap::new();
    for field in fields {
        graph.insert(
            field.id.to_ascii_lowercase(),
            field_dependencies(&field.kind, &field.config),
        );
    }
    graph
}

fn field_dependencies(kind: &str, config: &Value) -> Vec<String> {
    match kind {
        "lookup" => match LookupConfig::from_stored(config) {
            Ok(lookup) => vec![lookup.relation_field_id, lookup.target_field_id],
            Err(_) => Vec::new(),
        },
        "formula" => match parse_stored_ast(config) {
            Ok(expr) => {
                let mut refs = Vec::new();
                expr.refs(&mut refs);
                refs.into_iter().map(|id| id.to_ascii_lowercase()).collect()
            }
            Err(_) => Vec::new(),
        },
        _ => Vec::new(),
    }
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

pub(super) async fn create_derived_field(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    name: &str,
    kind: &str,
    raw_config: &Value,
) -> Result<FieldDto, StoreError> {
    lock_workspace_tables_sorted(tx, workspace, table).await?;
    let workspace_fields = load_workspace_fields(tx, workspace).await?;
    let field_id: String = sqlx::query("SELECT gen_random_uuid()::text")
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?
        .get(0);
    let stored = compile_derived_config(&workspace_fields, table, &field_id, kind, raw_config)?;
    let position = next_position(tx, workspace, table).await?;
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "INSERT INTO generic_field (id, workspace_id, table_id, name, kind, config, position)
         VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::jsonb, $7)
         RETURNING id::text, name, kind, config::text, position, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(&field_id)
        .bind(workspace)
        .bind(table)
        .bind(name)
        .bind(kind)
        .bind(stored.to_string())
        .bind(position)
        .fetch_one(&mut **tx)
        .await
        .map_err(|err| map_write(err, "请重试添加字段"))?;
    let mut field = field_from_row(&row)?;
    if field.kind == "formula" {
        let names: BTreeMap<String, String> = workspace_fields
            .iter()
            .filter(|item| item.table_id.eq_ignore_ascii_case(table))
            .map(|item| (item.id.clone(), item.name.clone()))
            .collect();
        hydrate_single_formula(&mut field, &names);
    }
    Ok(field)
}

pub(super) async fn update_field_config(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    field_id: &str,
    name: Option<&str>,
    raw_config: &Value,
) -> Result<FieldDto, StoreError> {
    lock_workspace_tables_sorted(tx, workspace, table).await?;
    let workspace_fields = load_workspace_fields(tx, workspace).await?;
    let table_lower = table.to_ascii_lowercase();
    let field_lower = field_id.to_ascii_lowercase();
    let existing = workspace_fields
        .iter()
        .find(|item| item.table_id == table_lower && item.id == field_lower)
        .ok_or(StoreError::NotFound("找不到这个字段"))?;
    if existing.kind != "lookup" && existing.kind != "formula" {
        return Err(StoreError::Invalid("不支持修改该字段类型的配置"));
    }
    let stored = compile_derived_config(
        &workspace_fields,
        table,
        &existing.id,
        &existing.kind,
        raw_config,
    )?;
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "UPDATE generic_field
         SET name = COALESCE($4, name), config = $5::jsonb, updated_at = now()
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
         RETURNING id::text, name, kind, config::text, position, {created}, {updated}"
    );
    let row = sqlx::query(&sql)
        .bind(field_id)
        .bind(table)
        .bind(workspace)
        .bind(name)
        .bind(stored.to_string())
        .fetch_optional(&mut **tx)
        .await
        .map_err(|err| map_write(err, "请重试更新字段"))?
        .ok_or(StoreError::NotFound("找不到这个字段"))?;
    let mut field = field_from_row(&row)?;
    if field.kind == "formula" {
        let mut names: BTreeMap<String, String> = workspace_fields
            .iter()
            .filter(|item| item.table_id == table_lower)
            .map(|item| (item.id.clone(), item.name.clone()))
            .collect();
        names.insert(field.id.clone(), field.name.clone());
        hydrate_single_formula(&mut field, &names);
    }
    Ok(field)
}

struct TableSnapshot {
    fields: BTreeMap<String, FieldDto>,
    formulas: BTreeMap<String, Result<Expr, &'static str>>,
    lookups: BTreeMap<String, Result<LookupConfig, &'static str>>,
    records: BTreeMap<String, BTreeMap<String, Value>>,
}

impl TableSnapshot {
    fn from_parts(fields: &[FieldDto], records: &[RecordDto]) -> Self {
        let mut field_map = BTreeMap::new();
        let mut formulas = BTreeMap::new();
        let mut lookups = BTreeMap::new();
        for field in fields {
            if field.kind == "formula" {
                formulas.insert(field.id.clone(), parse_stored_ast(&field.config));
            } else if field.kind == "lookup" {
                lookups.insert(field.id.clone(), LookupConfig::from_stored(&field.config));
            }
            field_map.insert(field.id.clone(), field.clone());
        }
        let mut record_map = BTreeMap::new();
        for record in records {
            record_map.insert(record.id.clone(), record.values.clone());
        }
        Self {
            fields: field_map,
            formulas,
            lookups,
            records: record_map,
        }
    }

    fn referenced_targets(&self, only_fields: Option<&BTreeSet<String>>) -> Vec<(String, String)> {
        let mut reachable = BTreeSet::new();
        let mut queue = VecDeque::new();
        match only_fields {
            Some(seeds) => {
                for id in seeds {
                    if reachable.insert(id.clone()) {
                        queue.push_back(id.clone());
                    }
                }
            }
            None => {
                for field in self.fields.values() {
                    if (field.kind == "lookup" || field.kind == "formula")
                        && reachable.insert(field.id.clone())
                    {
                        queue.push_back(field.id.clone());
                    }
                }
            }
        }
        let mut targets = Vec::new();
        while let Some(field_id) = queue.pop_front() {
            let Some(field) = self.fields.get(&field_id) else {
                continue;
            };
            match field.kind.as_str() {
                "formula" => {
                    if let Some(Ok(expr)) = self.formulas.get(&field_id) {
                        let mut refs = Vec::new();
                        expr.refs(&mut refs);
                        for dep in refs {
                            if reachable.insert(dep.clone()) {
                                queue.push_back(dep);
                            }
                        }
                    }
                }
                "lookup" => {
                    if let Some(Ok(lookup)) = self.lookups.get(&field_id)
                        && let Some(rel_field) = self.fields.get(&lookup.relation_field_id)
                        && rel_field.kind == "relation"
                        && let Ok(rel) = RelationConfig::from_stored(&rel_field.config)
                    {
                        targets.push((rel.target_table_id, lookup.target_field_id.clone()));
                    }
                }
                _ => {}
            }
        }
        targets
    }
}

struct Evaluator {
    tables: HashMap<String, TableSnapshot>,
    cache: HashMap<(String, String, String), Result<Value, &'static str>>,
    visiting: HashSet<(String, String, String)>,
}

impl Evaluator {
    fn new(tables: HashMap<String, TableSnapshot>) -> Self {
        Self {
            tables,
            cache: HashMap::new(),
            visiting: HashSet::new(),
        }
    }

    fn eval_cell(
        &mut self,
        table_id: &str,
        record_id: &str,
        field_id: &str,
        depth: usize,
    ) -> Result<Value, &'static str> {
        if depth > MAX_EVAL_DEPTH {
            return Err("计算嵌套过深");
        }
        let key = (
            table_id.to_owned(),
            record_id.to_owned(),
            field_id.to_owned(),
        );
        if let Some(cached) = self.cache.get(&key) {
            return cached.clone();
        }
        if self.visiting.contains(&key) {
            return Err("检测到循环引用");
        }

        let Some(table) = self.tables.get(table_id) else {
            return Ok(Value::Null);
        };
        let Some(field) = table.fields.get(field_id) else {
            return Ok(Value::Null);
        };
        let kind = field.kind.clone();
        if kind != "lookup" && kind != "formula" {
            let value = table
                .records
                .get(record_id)
                .and_then(|values| values.get(field_id))
                .cloned()
                .unwrap_or(Value::Null);
            return Ok(value);
        }

        self.visiting.insert(key.clone());
        let result = match kind.as_str() {
            "lookup" => self.eval_lookup(table_id, record_id, field_id, depth),
            "formula" => self.eval_formula(table_id, record_id, field_id, depth),
            _ => Ok(Value::Null),
        };
        self.visiting.remove(&key);
        self.cache.insert(key, result.clone());
        result
    }

    fn eval_lookup(
        &mut self,
        table_id: &str,
        record_id: &str,
        field_id: &str,
        depth: usize,
    ) -> Result<Value, &'static str> {
        let (target_table_id, target_field_id, linked_ids) = {
            let table = self.tables.get(table_id).ok_or("查找表不存在")?;
            let lookup = table
                .lookups
                .get(field_id)
                .ok_or("查找字段配置无效")?
                .clone()?;
            let rel_field = table
                .fields
                .get(&lookup.relation_field_id)
                .filter(|field| field.kind == "relation")
                .ok_or("查找字段引用的关联字段不存在")?;
            let rel =
                RelationConfig::from_stored(&rel_field.config).map_err(|_| "关联字段配置无效")?;
            let linked = table
                .records
                .get(record_id)
                .and_then(|values| values.get(&lookup.relation_field_id))
                .map(|value| relation::extract_ids(Some(value)))
                .unwrap_or_default();
            (rel.target_table_id, lookup.target_field_id, linked)
        };

        let mut values = Vec::with_capacity(linked_ids.len());
        for target_record_id in linked_ids {
            let exists = self
                .tables
                .get(&target_table_id)
                .is_some_and(|target| target.records.contains_key(&target_record_id));
            if !exists {
                continue;
            }
            let value = self.eval_cell(
                &target_table_id,
                &target_record_id,
                &target_field_id,
                depth + 1,
            )?;
            values.push(value);
        }
        Ok(Value::Array(values))
    }

    fn eval_formula(
        &mut self,
        table_id: &str,
        record_id: &str,
        field_id: &str,
        depth: usize,
    ) -> Result<Value, &'static str> {
        let expr = {
            let table = self.tables.get(table_id).ok_or("公式表不存在")?;
            table
                .formulas
                .get(field_id)
                .ok_or("公式配置无效")?
                .clone()?
        };
        expr.evaluate(&mut |ref_field_id| {
            self.eval_cell(table_id, record_id, ref_field_id, depth + 1)
        })
    }
}

pub(super) async fn project_sheet(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table_id: &str,
    fields: &mut [FieldDto],
    records: &mut [RecordDto],
) -> Result<(), StoreError> {
    let has_derived = fields
        .iter()
        .any(|field| field.kind == "lookup" || field.kind == "formula");
    if !has_derived {
        return Ok(());
    }

    let primary_snapshot = TableSnapshot::from_parts(fields, records);
    let initial_targets = primary_snapshot.referenced_targets(None);

    let mut tables = HashMap::new();
    tables.insert(table_id.to_owned(), primary_snapshot);

    let mut pending: VecDeque<(String, String, usize)> = initial_targets
        .into_iter()
        .map(|(target_table, target_field)| (target_table, target_field, 1))
        .collect();
    let mut visited_targets: HashSet<(String, String)> = HashSet::new();

    while let Some((target_table, target_field, hop)) = pending.pop_front() {
        if hop > MAX_TABLE_HOPS
            || !visited_targets.insert((target_table.clone(), target_field.clone()))
        {
            continue;
        }
        if !tables.contains_key(&target_table) {
            let target_fields = load_fields(tx, workspace, &target_table).await?;
            let target_records = load_records(tx, workspace, &target_table, &target_fields).await?;
            tables.insert(
                target_table.clone(),
                TableSnapshot::from_parts(&target_fields, &target_records),
            );
        }
        if let Some(snapshot) = tables.get(&target_table) {
            let mut seed = BTreeSet::new();
            seed.insert(target_field);
            for (next_table, next_field) in snapshot.referenced_targets(Some(&seed)) {
                pending.push_back((next_table, next_field, hop + 1));
            }
        }
    }

    let derived_ids: Vec<String> = fields
        .iter()
        .filter(|field| field.kind == "lookup" || field.kind == "formula")
        .map(|field| field.id.clone())
        .collect();

    let mut evaluator = Evaluator::new(tables);
    for record in records.iter_mut() {
        for field_id in &derived_ids {
            match evaluator.eval_cell(table_id, &record.id, field_id, 0) {
                Ok(value) => {
                    record.values.insert(field_id.clone(), value);
                }
                Err(message) => {
                    record.values.insert(field_id.clone(), Value::Null);
                    record.errors.insert(field_id.clone(), message.to_owned());
                }
            }
        }
    }

    hydrate_lookup_fields(&evaluator.tables, table_id, fields);
    hydrate_formula_fields(fields);
    Ok(())
}

fn hydrate_lookup_fields(
    tables: &HashMap<String, TableSnapshot>,
    table_id: &str,
    fields: &mut [FieldDto],
) {
    for field in fields.iter_mut() {
        if field.kind != "lookup" {
            continue;
        }
        let Some(target) = resolve_lookup_target_field(tables, table_id, &field.id) else {
            continue;
        };
        let Some(config) = field.config.as_object_mut() else {
            continue;
        };
        if target.kind == "single_select"
            && let Some(options) = target.config.get("options")
        {
            config.insert("target_options".to_owned(), options.clone());
        } else if target.kind == "relation"
            && let Ok(rel) = RelationConfig::from_stored(&target.config)
        {
            config.insert(
                "target_relation_table_id".to_owned(),
                Value::String(rel.target_table_id),
            );
        }
    }
}

fn resolve_lookup_target_field<'a>(
    tables: &'a HashMap<String, TableSnapshot>,
    start_table_id: &str,
    start_field_id: &str,
) -> Option<&'a FieldDto> {
    let mut current_table = start_table_id.to_owned();
    let mut current_field = start_field_id.to_owned();
    let mut seen = HashSet::new();
    for _ in 0..MAX_TABLE_HOPS {
        if !seen.insert((current_table.clone(), current_field.clone())) {
            return None;
        }
        let snapshot = tables.get(&current_table)?;
        let lookup = snapshot.lookups.get(&current_field)?.as_ref().ok()?;
        let rel_field = snapshot
            .fields
            .get(&lookup.relation_field_id)
            .filter(|item| item.kind == "relation")?;
        let rel = RelationConfig::from_stored(&rel_field.config).ok()?;
        let target_snapshot = tables.get(&rel.target_table_id)?;
        let target_field = target_snapshot.fields.get(&lookup.target_field_id)?;
        if target_field.kind != "lookup" {
            return Some(target_field);
        }
        current_table = rel.target_table_id;
        current_field = lookup.target_field_id.clone();
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    const T_A: &str = "11111111-1111-4111-8111-111111111111";
    const T_B: &str = "22222222-2222-4222-8222-222222222222";
    const F_REL: &str = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    const F_LKP: &str = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const F_FRM: &str = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
    const F_ERR: &str = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
    const F_B_VAL: &str = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee";

    fn field_dto(id: &str, name: &str, kind: &str, config: Value) -> FieldDto {
        FieldDto {
            id: id.to_owned(),
            name: name.to_owned(),
            kind: kind.to_owned(),
            config,
            position: 1,
            created_at: String::new(),
            updated_at: String::new(),
        }
    }

    fn record_dto(id: &str, values: BTreeMap<String, Value>) -> RecordDto {
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
    fn cycle_detection_rejects_self_indirect_and_cross_table_cycles() {
        let mut graph = BTreeMap::new();
        // Self cycle: a -> a
        graph.insert("a".to_owned(), vec!["a".to_owned()]);
        assert!(has_cycle_from(&graph, "a"));

        // Indirect / cross-table cycle: a -> b -> c -> a
        let mut cross = BTreeMap::new();
        cross.insert("a".to_owned(), vec!["b".to_owned()]);
        cross.insert("b".to_owned(), vec!["c".to_owned()]);
        cross.insert("c".to_owned(), vec!["a".to_owned()]);
        assert!(has_cycle_from(&cross, "a"));

        // Diamond DAG (acyclic): a -> b, a -> c, b -> d, c -> d
        let mut dag = BTreeMap::new();
        dag.insert("a".to_owned(), vec!["b".to_owned(), "c".to_owned()]);
        dag.insert("b".to_owned(), vec!["d".to_owned()]);
        dag.insert("c".to_owned(), vec!["d".to_owned()]);
        dag.insert("d".to_owned(), vec![]);
        assert!(!has_cycle_from(&dag, "a"));
    }

    #[test]
    fn lookup_preserves_link_order_zero_false_null_and_formula_isolates_errors() {
        let sum_expr = formula::compile("SUM({Lookup}) + 1", |name| {
            (name == "Lookup").then(|| F_LKP.to_owned())
        })
        .unwrap();
        let div0_expr = formula::compile("10 / 0", |_| None).unwrap();

        let fields_a = vec![
            field_dto(
                F_REL,
                "Items",
                "relation",
                json!({ "target_table_id": T_B, "bidirectional": false }),
            ),
            field_dto(
                F_LKP,
                "Lookup",
                "lookup",
                json!({ "relation_field_id": F_REL, "target_field_id": F_B_VAL }),
            ),
            field_dto(
                F_FRM,
                "Total",
                "formula",
                stored_formula_config(&sum_expr, "SUM({Lookup}) + 1").unwrap(),
            ),
            field_dto(
                F_ERR,
                "Broken",
                "formula",
                stored_formula_config(&div0_expr, "10 / 0").unwrap(),
            ),
        ];

        let mut rec_a_vals = BTreeMap::new();
        rec_a_vals.insert(
            F_REL.to_owned(),
            json!(["r-zero", "r-false", "r-null", "r-five"]),
        );
        let rec_a = record_dto("r-a", rec_a_vals);

        let fields_b = vec![field_dto(F_B_VAL, "Val", "number", json!({}))];
        let mut r_zero = BTreeMap::new();
        r_zero.insert(F_B_VAL.to_owned(), json!(0));
        let mut r_false = BTreeMap::new();
        r_false.insert(F_B_VAL.to_owned(), json!(false));
        let mut r_null = BTreeMap::new();
        r_null.insert(F_B_VAL.to_owned(), Value::Null);
        let mut r_five = BTreeMap::new();
        r_five.insert(F_B_VAL.to_owned(), json!(5));

        let records_b = vec![
            record_dto("r-zero", r_zero),
            record_dto("r-false", r_false),
            record_dto("r-null", r_null),
            record_dto("r-five", r_five),
        ];

        let mut tables = HashMap::new();
        tables.insert(
            T_A.to_owned(),
            TableSnapshot::from_parts(&fields_a, &[rec_a]),
        );
        tables.insert(
            T_B.to_owned(),
            TableSnapshot::from_parts(&fields_b, &records_b),
        );

        let mut evaluator = Evaluator::new(tables);
        assert_eq!(
            evaluator.eval_cell(T_A, "r-a", F_LKP, 0).unwrap(),
            json!([0, false, null, 5])
        );
        assert_eq!(
            evaluator.eval_cell(T_A, "r-a", F_FRM, 0).unwrap(),
            json!(6.0)
        );
        assert_eq!(
            evaluator.eval_cell(T_A, "r-a", F_ERR, 0).unwrap_err(),
            "公式不能除以零"
        );
    }

    #[test]
    fn hydrate_formula_fields_reflects_renamed_field_labels() {
        let expr = formula::compile("{旧名} + 1", |name| {
            (name == "旧名").then(|| F_B_VAL.to_owned())
        })
        .unwrap();
        let mut fields = vec![
            field_dto(F_B_VAL, "新名", "number", json!({})),
            field_dto(
                F_FRM,
                "结果",
                "formula",
                stored_formula_config(&expr, "({旧名} + 1)").unwrap(),
            ),
        ];
        hydrate_formula_fields(&mut fields);
        assert_eq!(fields[1].config, json!({ "expression": "({新名} + 1.0)" }));
    }

    #[test]
    fn hydrate_lookup_fields_attaches_target_select_options_and_relation_table_hint() {
        const T_C: &str = "33333333-3333-4333-8333-333333333333";
        const F_B_REL: &str = "ffffffff-ffff-4fff-8fff-ffffffffffff";
        const F_LKP_REL: &str = "99999999-9999-4999-8999-999999999999";

        let mut fields_a = vec![
            field_dto(
                F_REL,
                "Items",
                "relation",
                json!({ "target_table_id": T_B, "bidirectional": false }),
            ),
            field_dto(
                F_LKP,
                "StatusLookup",
                "lookup",
                json!({ "relation_field_id": F_REL, "target_field_id": F_B_VAL }),
            ),
            field_dto(
                F_LKP_REL,
                "OwnerLookup",
                "lookup",
                json!({ "relation_field_id": F_REL, "target_field_id": F_B_REL }),
            ),
        ];
        let fields_b = vec![
            field_dto(
                F_B_VAL,
                "Status",
                "single_select",
                json!({ "options": [{ "id": "opt1", "label": "高优" }] }),
            ),
            field_dto(
                F_B_REL,
                "Owner",
                "relation",
                json!({ "target_table_id": T_C, "bidirectional": false }),
            ),
        ];

        let mut tables = HashMap::new();
        tables.insert(T_A.to_owned(), TableSnapshot::from_parts(&fields_a, &[]));
        tables.insert(T_B.to_owned(), TableSnapshot::from_parts(&fields_b, &[]));

        hydrate_lookup_fields(&tables, T_A, &mut fields_a);
        assert_eq!(
            fields_a[1].config.get("target_options"),
            Some(&json!([{ "id": "opt1", "label": "高优" }]))
        );
        assert_eq!(
            fields_a[2].config.get("target_relation_table_id"),
            Some(&json!(T_C))
        );
    }
}
