use std::collections::{BTreeMap, BTreeSet};

use serde_json::{Map, Value};
use sqlx::{Postgres, Row, Transaction};

use super::collab::{self, Actor, ChangeWrite};
use super::store::{
    FieldDto, StoreError, failed, field_from_row, lock_record, map_write, next_position, ts,
    write_cells,
};
use super::validate;

#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct RelationConfig {
    pub target_table_id: String,
    pub bidirectional: bool,
    pub reverse_field_id: Option<String>,
}

impl RelationConfig {
    pub fn parse_input(config: &Value) -> Result<Self, &'static str> {
        let Some(object) = config.as_object() else {
            return Err("关联字段需要指定目标表");
        };
        for key in object.keys() {
            if key != "target_table_id" && key != "bidirectional" && key != "reverse_field_id" {
                return Err("关联字段配置包含不支持的参数");
            }
        }
        if object
            .get("reverse_field_id")
            .is_some_and(|value| !value.is_null())
        {
            return Err("反向关联字段由系统自动生成");
        }
        let target_table_id = object
            .get("target_table_id")
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|id| validate::is_uuid(id))
            .ok_or("关联字段需要有效的目标表标识")?
            .to_ascii_lowercase();
        let bidirectional = match object.get("bidirectional") {
            None => false,
            Some(value) => value.as_bool().ok_or("双向关联开关需要是布尔值")?,
        };
        Ok(Self {
            target_table_id,
            bidirectional,
            reverse_field_id: None,
        })
    }

    pub fn from_stored(config: &Value) -> Result<Self, StoreError> {
        let Some(object) = config.as_object() else {
            return Err(StoreError::Failed);
        };
        let target_table_id = object
            .get("target_table_id")
            .and_then(Value::as_str)
            .filter(|id| validate::is_uuid(id))
            .ok_or(StoreError::Failed)?
            .to_ascii_lowercase();
        let bidirectional = object
            .get("bidirectional")
            .and_then(Value::as_bool)
            .unwrap_or(false);
        let reverse_field_id = match object.get("reverse_field_id") {
            None | Some(Value::Null) => None,
            Some(value) => {
                let id = value
                    .as_str()
                    .filter(|id| validate::is_uuid(id))
                    .ok_or(StoreError::Failed)?;
                Some(id.to_ascii_lowercase())
            }
        };
        Ok(Self {
            target_table_id,
            bidirectional,
            reverse_field_id,
        })
    }

    pub fn to_json(&self) -> Value {
        let mut object = Map::new();
        object.insert(
            "target_table_id".to_owned(),
            Value::String(self.target_table_id.clone()),
        );
        object.insert("bidirectional".to_owned(), Value::Bool(self.bidirectional));
        if let Some(reverse_id) = &self.reverse_field_id {
            object.insert(
                "reverse_field_id".to_owned(),
                Value::String(reverse_id.clone()),
            );
        }
        Value::Object(object)
    }
}

/// Normalizes a relation cell value to an ordered, unique list of lowercase UUIDs.
/// `null` and `[]` return `Ok(None)` to clear the cell.
pub(super) fn normalize_cell(value: &Value) -> Result<Option<Value>, &'static str> {
    if value.is_null() {
        return Ok(None);
    }
    let Some(items) = value.as_array() else {
        return Err("关联字段需要记录标识列表");
    };
    if items.is_empty() {
        return Ok(None);
    }
    let mut seen = BTreeSet::new();
    let mut out = Vec::with_capacity(items.len());
    for item in items {
        let raw = item
            .as_str()
            .map(str::trim)
            .filter(|id| validate::is_uuid(id))
            .ok_or("关联记录标识无效")?;
        let normalized = raw.to_ascii_lowercase();
        if seen.insert(normalized.clone()) {
            out.push(Value::String(normalized));
        }
    }
    Ok(Some(Value::Array(out)))
}

pub(super) fn extract_ids(value: Option<&Value>) -> Vec<String> {
    let Some(Value::Array(items)) = value else {
        return Vec::new();
    };
    items
        .iter()
        .filter_map(Value::as_str)
        .map(str::to_ascii_lowercase)
        .collect()
}

pub(super) fn validate_target_table(
    source_table: &str,
    target_table: &str,
    bidirectional: bool,
) -> Result<(), StoreError> {
    if bidirectional && source_table.eq_ignore_ascii_case(target_table) {
        return Err(StoreError::Invalid("双向关联字段不能指向当前表"));
    }
    Ok(())
}

pub(super) fn sorted_unique_tables(tables: &[&str]) -> Vec<String> {
    let mut set = BTreeSet::new();
    for table in tables {
        set.insert(table.to_ascii_lowercase());
    }
    set.into_iter().collect()
}

pub(super) async fn lock_tables_sorted(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    primary_table: &str,
    extra_tables: &[&str],
) -> Result<Vec<String>, StoreError> {
    let primary = primary_table.to_ascii_lowercase();
    let mut all = Vec::with_capacity(extra_tables.len() + 1);
    all.push(primary.as_str());
    all.extend_from_slice(extra_tables);
    let ordered = sorted_unique_tables(&all);
    for table_id in &ordered {
        let found = sqlx::query(
            "SELECT id::text FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE",
        )
        .bind(table_id)
        .bind(workspace)
        .fetch_optional(&mut **tx)
        .await
        .map_err(failed)?;
        if found.is_none() {
            if table_id == &primary {
                return Err(StoreError::NotFound("找不到这张表"));
            }
            return Err(StoreError::Invalid("目标表不存在或不属于当前工作区"));
        }
    }
    Ok(ordered)
}

pub(super) fn pick_reverse_field_name(
    source_table_name: &str,
    source_field_name: &str,
    existing_names: &BTreeSet<String>,
) -> String {
    let base = truncate_chars(source_table_name.trim(), 80);
    if !base.is_empty() && !existing_names.contains(&base) {
        return base;
    }
    let with_field =
        format_with_suffix(source_table_name.trim(), &format!(" ({source_field_name})"));
    if !with_field.is_empty() && !existing_names.contains(&with_field) {
        return with_field;
    }
    for index in 2..10_000 {
        let candidate = format_with_suffix(source_table_name.trim(), &format!(" ({index})"));
        if !candidate.is_empty() && !existing_names.contains(&candidate) {
            return candidate;
        }
    }
    truncate_chars(&format!("{source_field_name} 反向"), 80)
}

fn format_with_suffix(base: &str, suffix: &str) -> String {
    let suffix_len = suffix.chars().count();
    if suffix_len >= 80 {
        return truncate_chars(suffix, 80);
    }
    let prefix = truncate_chars(base, 80 - suffix_len);
    format!("{prefix}{suffix}")
}

fn truncate_chars(text: &str, max_chars: usize) -> String {
    text.chars().take(max_chars).collect()
}

pub(super) async fn create_relation_field(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    name: &str,
    config: &Value,
) -> Result<FieldDto, StoreError> {
    let rel = RelationConfig::from_stored(config)?;
    validate_target_table(table, &rel.target_table_id, rel.bidirectional)?;
    lock_tables_sorted(tx, workspace, table, &[rel.target_table_id.as_str()]).await?;

    let created = ts("created_at");
    let updated = ts("updated_at");

    if !rel.bidirectional {
        let position = next_position(tx, workspace, table).await?;
        let sql = format!(
            "INSERT INTO generic_field (workspace_id, table_id, name, kind, config, position)
             VALUES ($1::uuid, $2::uuid, $3, 'relation', $4::jsonb, $5)
             RETURNING id::text, name, kind, config::text, position, {created}, {updated}"
        );
        let row = sqlx::query(&sql)
            .bind(workspace)
            .bind(table)
            .bind(name)
            .bind(rel.to_json().to_string())
            .bind(position)
            .fetch_one(&mut **tx)
            .await
            .map_err(|err| map_write(err, "请重试添加字段"))?;
        return field_from_row(&row);
    }

    let id_row = sqlx::query("SELECT gen_random_uuid()::text, gen_random_uuid()::text")
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?;
    let source_field_id: String = id_row.get(0);
    let reverse_field_id: String = id_row.get(1);

    let source_table_name: String = sqlx::query(
        "SELECT name FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(table)
    .bind(workspace)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?
    .get(0);

    let existing_rows = sqlx::query(
        "SELECT name FROM generic_field WHERE table_id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(&rel.target_table_id)
    .bind(workspace)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let existing_names: BTreeSet<String> = existing_rows
        .iter()
        .map(|row| row.get::<String, _>(0))
        .collect();
    let reverse_name = pick_reverse_field_name(&source_table_name, name, &existing_names);

    let source_config = RelationConfig {
        target_table_id: rel.target_table_id.clone(),
        bidirectional: true,
        reverse_field_id: Some(reverse_field_id.clone()),
    };
    let reverse_config = RelationConfig {
        target_table_id: table.to_ascii_lowercase(),
        bidirectional: true,
        reverse_field_id: Some(source_field_id.clone()),
    };

    let source_position = next_position(tx, workspace, table).await?;
    let insert_sql = format!(
        "INSERT INTO generic_field (id, workspace_id, table_id, name, kind, config, position)
         VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'relation', $5::jsonb, $6)
         RETURNING id::text, name, kind, config::text, position, {created}, {updated}"
    );
    let source_row = sqlx::query(&insert_sql)
        .bind(&source_field_id)
        .bind(workspace)
        .bind(table)
        .bind(name)
        .bind(source_config.to_json().to_string())
        .bind(source_position)
        .fetch_one(&mut **tx)
        .await
        .map_err(|err| map_write(err, "请重试添加字段"))?;

    let reverse_position = next_position(tx, workspace, &rel.target_table_id).await?;
    sqlx::query(&insert_sql)
        .bind(&reverse_field_id)
        .bind(workspace)
        .bind(&rel.target_table_id)
        .bind(&reverse_name)
        .bind(reverse_config.to_json().to_string())
        .bind(reverse_position)
        .fetch_one(&mut **tx)
        .await
        .map_err(|err| map_write(err, "请重试添加反向关联字段"))?;

    field_from_row(&source_row)
}

pub(super) async fn prepare_table_locks(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    touched_field_ids: &[String],
) -> Result<Vec<String>, StoreError> {
    let mut targets = Vec::new();
    if !touched_field_ids.is_empty() {
        let rows = sqlx::query(
            "SELECT config::text
             FROM generic_field
             WHERE workspace_id = $1::uuid
               AND table_id = $2::uuid
               AND kind = 'relation'
               AND id = ANY($3::text[]::uuid[])",
        )
        .bind(workspace)
        .bind(table)
        .bind(touched_field_ids)
        .fetch_all(&mut **tx)
        .await
        .map_err(failed)?;
        for row in rows {
            let raw: String = row.get(0);
            let value: Value = serde_json::from_str(&raw).map_err(|_| StoreError::Failed)?;
            let rel = RelationConfig::from_stored(&value)?;
            if rel.bidirectional {
                targets.push(rel.target_table_id);
            }
        }
    }
    let refs: Vec<&str> = targets.iter().map(String::as_str).collect();
    lock_tables_sorted(tx, workspace, table, &refs).await
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum ReverseOp {
    Add,
    Remove,
}

#[derive(Default)]
pub(super) struct ReversePlan {
    // Keyed by (target_table_id, target_record_id) for deterministic lock/write order.
    ops: BTreeMap<(String, String), Vec<(String, ReverseOp)>>,
}

impl ReversePlan {
    pub fn has_writes(&self) -> bool {
        !self.ops.is_empty()
    }
}

pub(super) async fn validate_and_plan_reverse(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    fields: &[FieldDto],
    old_values: Option<&BTreeMap<String, Value>>,
    normalized: &BTreeMap<String, Option<Value>>,
    locked_tables: &[String],
) -> Result<ReversePlan, StoreError> {
    let mut plan = ReversePlan::default();
    for (field_id, next_val) in normalized {
        let Some(field) = fields.iter().find(|item| &item.id == field_id) else {
            continue;
        };
        if field.kind != "relation" {
            continue;
        }
        let rel = RelationConfig::from_stored(&field.config)?;
        if rel.bidirectional {
            if !locked_tables.iter().any(|id| id == &rel.target_table_id) {
                return Err(StoreError::Invalid("关联字段定义已变更，请重试"));
            }
            let Some(reverse_field_id) = rel.reverse_field_id.as_deref() else {
                return Err(StoreError::Failed);
            };
            ensure_reverse_field(tx, workspace, &rel.target_table_id, reverse_field_id).await?;
        } else {
            ensure_target_table(tx, workspace, &rel.target_table_id).await?;
        }

        let new_ids = extract_ids(next_val.as_ref());
        if !new_ids.is_empty() {
            ensure_target_records(tx, workspace, &rel.target_table_id, &new_ids).await?;
        }

        if rel.bidirectional {
            let reverse_field_id = rel.reverse_field_id.expect("checked above");
            let old_ids = old_values
                .and_then(|values| values.get(field_id))
                .map(|value| extract_ids(Some(value)))
                .unwrap_or_default();
            let (added, removed) = diff_ids(&old_ids, &new_ids);
            for target_record_id in added {
                plan.ops
                    .entry((rel.target_table_id.clone(), target_record_id))
                    .or_default()
                    .push((reverse_field_id.clone(), ReverseOp::Add));
            }
            for target_record_id in removed {
                plan.ops
                    .entry((rel.target_table_id.clone(), target_record_id))
                    .or_default()
                    .push((reverse_field_id.clone(), ReverseOp::Remove));
            }
        }
    }
    Ok(plan)
}

pub(super) fn diff_ids(old_ids: &[String], new_ids: &[String]) -> (Vec<String>, Vec<String>) {
    let old_set: BTreeSet<&str> = old_ids.iter().map(String::as_str).collect();
    let new_set: BTreeSet<&str> = new_ids.iter().map(String::as_str).collect();
    let added = new_ids
        .iter()
        .filter(|id| !old_set.contains(id.as_str()))
        .cloned()
        .collect();
    let removed = old_ids
        .iter()
        .filter(|id| !new_set.contains(id.as_str()))
        .cloned()
        .collect();
    (added, removed)
}

pub(super) async fn apply_reverse_plan(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    source_record_id: &str,
    plan: &ReversePlan,
    actor: &Actor,
    batch_id: Option<&str>,
) -> Result<(), StoreError> {
    let source_id = source_record_id.to_ascii_lowercase();
    for ((target_table_id, target_record_id), ops) in &plan.ops {
        let current = match lock_record(tx, workspace, target_table_id, target_record_id).await {
            Ok(record) => record,
            Err(StoreError::NotFound(_)) if ops.iter().all(|(_, op)| *op == ReverseOp::Remove) => {
                continue;
            }
            Err(StoreError::NotFound(_)) => {
                return Err(StoreError::Invalid("关联的目标记录不存在或不属于目标表"));
            }
            Err(err) => return Err(err),
        };

        let mut patch: BTreeMap<String, Option<Value>> = BTreeMap::new();
        for (reverse_field_id, op) in ops {
            let mut list = load_cell_ids(tx, workspace, target_record_id, reverse_field_id).await?;
            let changed = match op {
                ReverseOp::Add => {
                    if list.iter().any(|id| id == &source_id) {
                        false
                    } else {
                        list.push(source_id.clone());
                        true
                    }
                }
                ReverseOp::Remove => {
                    let before = list.len();
                    list.retain(|id| id != &source_id);
                    list.len() != before
                }
            };
            if changed {
                let stored = if list.is_empty() {
                    None
                } else {
                    Some(Value::Array(list.into_iter().map(Value::String).collect()))
                };
                patch.insert(reverse_field_id.clone(), stored);
            }
        }

        if patch.is_empty() {
            continue;
        }

        write_cells(tx, workspace, target_record_id, &patch).await?;
        collab::bump_revision(tx, workspace, target_table_id, target_record_id).await?;
        let next_revision = current.revision + 1;
        let rev_digest = collab::digest(&patch)?;
        collab::insert_change(
            tx,
            &ChangeWrite {
                workspace,
                table: target_table_id,
                record: target_record_id,
                base: current.revision,
                client_revision: current.revision,
                record_revision: next_revision,
                values: &patch,
                actor,
                digest: &rev_digest,
                batch_id,
            },
        )
        .await?;
    }
    Ok(())
}

async fn ensure_target_table(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    target_table_id: &str,
) -> Result<(), StoreError> {
    let found =
        sqlx::query("SELECT 1 FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid")
            .bind(target_table_id)
            .bind(workspace)
            .fetch_optional(&mut **tx)
            .await
            .map_err(failed)?;
    if found.is_none() {
        return Err(StoreError::Invalid("目标表不存在或不属于当前工作区"));
    }
    Ok(())
}

async fn ensure_reverse_field(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    target_table_id: &str,
    reverse_field_id: &str,
) -> Result<(), StoreError> {
    let found = sqlx::query(
        "SELECT 1
         FROM generic_field
         WHERE id = $1::uuid
           AND table_id = $2::uuid
           AND workspace_id = $3::uuid
           AND kind = 'relation'",
    )
    .bind(reverse_field_id)
    .bind(target_table_id)
    .bind(workspace)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?;
    if found.is_none() {
        return Err(StoreError::Invalid("反向关联字段不存在"));
    }
    Ok(())
}

async fn ensure_target_records(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    target_table_id: &str,
    record_ids: &[String],
) -> Result<(), StoreError> {
    let row = sqlx::query(
        "SELECT COUNT(*)::bigint
         FROM generic_record
         WHERE workspace_id = $1::uuid
           AND table_id = $2::uuid
           AND id = ANY($3::text[]::uuid[])",
    )
    .bind(workspace)
    .bind(target_table_id)
    .bind(record_ids)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    let count: i64 = row.get(0);
    if count as usize != record_ids.len() {
        return Err(StoreError::Invalid("关联的目标记录不存在或不属于目标表"));
    }
    Ok(())
}

async fn load_cell_ids(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    record_id: &str,
    field_id: &str,
) -> Result<Vec<String>, StoreError> {
    let row = sqlx::query(
        "SELECT value::text
         FROM generic_cell
         WHERE workspace_id = $1::uuid AND record_id = $2::uuid AND field_id = $3::uuid",
    )
    .bind(workspace)
    .bind(record_id)
    .bind(field_id)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?;
    let Some(row) = row else {
        return Ok(Vec::new());
    };
    let raw: Option<String> = row.get(0);
    let Some(text) = raw else {
        return Ok(Vec::new());
    };
    let value: Value = serde_json::from_str(&text).map_err(|_| StoreError::Failed)?;
    Ok(extract_ids(Some(&value)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const T1: &str = "11111111-1111-4111-8111-111111111111";
    const T2: &str = "22222222-2222-4222-8222-222222222222";
    const R1: &str = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    const R2: &str = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";

    #[test]
    fn relation_config_validates_target_and_rejects_client_reverse_id() {
        let one_way = RelationConfig::parse_input(&json!({ "target_table_id": T1 })).unwrap();
        assert_eq!(one_way.target_table_id, T1);
        assert!(!one_way.bidirectional);
        assert_eq!(one_way.reverse_field_id, None);
        assert_eq!(
            one_way.to_json(),
            json!({ "target_table_id": T1, "bidirectional": false })
        );

        let two_way = RelationConfig::parse_input(&json!({
            "target_table_id": T1.to_ascii_uppercase(),
            "bidirectional": true
        }))
        .unwrap();
        assert_eq!(two_way.target_table_id, T1);
        assert!(two_way.bidirectional);

        assert!(RelationConfig::parse_input(&json!({})).is_err());
        assert!(RelationConfig::parse_input(&json!({ "target_table_id": "bad" })).is_err());
        assert!(
            RelationConfig::parse_input(&json!({
                "target_table_id": T1,
                "bidirectional": true,
                "reverse_field_id": R1
            }))
            .is_err()
        );
    }

    #[test]
    fn relation_cell_normalizes_ordered_unique_uuids_and_clears_empty() {
        assert_eq!(normalize_cell(&Value::Null).unwrap(), None);
        assert_eq!(normalize_cell(&json!([])).unwrap(), None);
        assert_eq!(
            normalize_cell(&json!([R2.to_ascii_uppercase(), R1, R2])).unwrap(),
            Some(json!([R2, R1]))
        );
        assert!(normalize_cell(&json!(R1)).is_err());
        assert!(normalize_cell(&json!(["not-a-uuid"])).is_err());
    }

    #[test]
    fn sorted_unique_tables_orders_both_sides_identically() {
        assert_eq!(
            sorted_unique_tables(&[T2, T1]),
            sorted_unique_tables(&[T1, T2])
        );
        assert_eq!(
            sorted_unique_tables(&[T2, T1, T2]),
            vec![T1.to_owned(), T2.to_owned()]
        );
    }

    #[test]
    fn reverse_field_name_disambiguates_collisions() {
        let mut existing = BTreeSet::new();
        assert_eq!(
            pick_reverse_field_name("项目", "关联任务", &existing),
            "项目"
        );
        existing.insert("项目".to_owned());
        assert_eq!(
            pick_reverse_field_name("项目", "关联任务", &existing),
            "项目 (关联任务)"
        );
        existing.insert("项目 (关联任务)".to_owned());
        assert_eq!(
            pick_reverse_field_name("项目", "关联任务", &existing),
            "项目 (2)"
        );
    }

    #[test]
    fn diff_ids_computes_added_and_removed() {
        let old = vec![R1.to_owned()];
        let new = vec![R2.to_owned()];
        assert_eq!(
            diff_ids(&old, &new),
            (vec![R2.to_owned()], vec![R1.to_owned()])
        );
        assert_eq!(
            diff_ids(
                &[R1.to_owned(), R2.to_owned()],
                &[R2.to_owned(), R1.to_owned()]
            ),
            (Vec::new(), Vec::new())
        );
    }

    #[test]
    fn target_table_validation_allows_same_table_one_way_and_rejects_bidirectional() {
        assert!(validate_target_table(T1, T1, false).is_ok());
        assert!(validate_target_table(T1, &T1.to_ascii_uppercase(), false).is_ok());
        assert!(matches!(
            validate_target_table(T1, T1, true),
            Err(StoreError::Invalid(_))
        ));
        assert!(matches!(
            validate_target_table(T1, &T1.to_ascii_uppercase(), true),
            Err(StoreError::Invalid(_))
        ));
        assert!(validate_target_table(T1, T2, false).is_ok());
        assert!(validate_target_table(T1, T2, true).is_ok());
    }
}
