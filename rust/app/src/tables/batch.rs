use std::collections::{BTreeMap, BTreeSet};

use serde::Serialize;
use serde_json::{Value, json};
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::collab::{self, Actor};
use super::relation;
use super::store::{
    self, DeletedDto, RecordDto, StoreError, begin, commit, failed, load_fields, normalize_values,
};
use super::validate::is_uuid;

pub const MAX_BATCH_RECORDS: usize = 10;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BatchOperation {
    Create,
    Update,
    Delete,
}

impl BatchOperation {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Create => "create",
            Self::Update => "update",
            Self::Delete => "delete",
        }
    }

    pub fn parse(raw: &str) -> Option<Self> {
        match raw {
            "create" => Some(Self::Create),
            "update" => Some(Self::Update),
            "delete" => Some(Self::Delete),
            _ => None,
        }
    }
}

#[derive(Clone, Debug, Serialize)]
pub struct BatchCreateItem {
    pub values: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Serialize)]
pub struct BatchUpdateItem {
    pub id: String,
    pub expected_revision: i64,
    pub values: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Serialize)]
pub struct BatchDeleteItem {
    pub id: String,
    pub expected_revision: i64,
}

#[derive(Clone, Debug)]
pub enum BatchItems {
    Create(Vec<BatchCreateItem>),
    Update(Vec<BatchUpdateItem>),
    Delete(Vec<BatchDeleteItem>),
}

#[derive(Clone, Debug)]
pub struct BatchRequest {
    pub idempotency_key: String,
    pub items: BatchItems,
}

#[derive(Debug)]
pub enum BatchErrorKind {
    Store(StoreError),
    IdempotencyConflict(&'static str),
}

#[derive(Debug)]
pub struct BatchError {
    pub index: Option<usize>,
    pub record_id: Option<String>,
    pub kind: BatchErrorKind,
}

impl BatchError {
    pub fn top(err: StoreError) -> Self {
        Self {
            index: None,
            record_id: None,
            kind: BatchErrorKind::Store(err),
        }
    }

    pub fn item(index: usize, record_id: Option<String>, err: StoreError) -> Self {
        Self {
            index: Some(index),
            record_id,
            kind: BatchErrorKind::Store(err),
        }
    }

    pub fn idempotency_conflict(message: &'static str) -> Self {
        Self {
            index: None,
            record_id: None,
            kind: BatchErrorKind::IdempotencyConflict(message),
        }
    }
}

impl BatchRequest {
    pub fn operation(&self) -> BatchOperation {
        match &self.items {
            BatchItems::Create(_) => BatchOperation::Create,
            BatchItems::Update(_) => BatchOperation::Update,
            BatchItems::Delete(_) => BatchOperation::Delete,
        }
    }

    pub fn parse(raw: &Value) -> Result<Self, BatchError> {
        let Some(object) = raw.as_object() else {
            return Err(BatchError::top(StoreError::Invalid("请求格式不正确")));
        };
        for key in object.keys() {
            if key != "idempotency_key" && key != "operation" && key != "records" {
                return Err(BatchError::top(StoreError::Invalid(
                    "批量请求包含不支持的参数",
                )));
            }
        }
        let idempotency_key = object
            .get("idempotency_key")
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|id| is_uuid(id))
            .ok_or_else(|| BatchError::top(StoreError::Invalid("idempotency_key 必须是有效的 UUID")))?
            .to_ascii_lowercase();

        let operation = object
            .get("operation")
            .and_then(Value::as_str)
            .and_then(BatchOperation::parse)
            .ok_or_else(|| {
                BatchError::top(StoreError::Invalid(
                    "operation 必须是 create、update 或 delete",
                ))
            })?;

        let Some(records) = object.get("records").and_then(Value::as_array) else {
            return Err(BatchError::top(StoreError::Invalid(
                "records 必须是非空数组",
            )));
        };
        if records.is_empty() || records.len() > MAX_BATCH_RECORDS {
            return Err(BatchError::top(StoreError::Invalid(
                "批量记录数量必须在 1 到 10 条之间",
            )));
        }

        let items = match operation {
            BatchOperation::Create => {
                let mut parsed = Vec::with_capacity(records.len());
                for (idx, entry) in records.iter().enumerate() {
                    parsed.push(parse_create_item(idx, entry)?);
                }
                BatchItems::Create(parsed)
            }
            BatchOperation::Update => {
                let mut seen = BTreeSet::new();
                let mut parsed = Vec::with_capacity(records.len());
                for (idx, entry) in records.iter().enumerate() {
                    let item = parse_update_item(idx, entry)?;
                    if !seen.insert(item.id.clone()) {
                        return Err(BatchError::item(
                            idx,
                            Some(item.id),
                            StoreError::Invalid("批量请求中存在重复的记录标识"),
                        ));
                    }
                    parsed.push(item);
                }
                BatchItems::Update(parsed)
            }
            BatchOperation::Delete => {
                let mut seen = BTreeSet::new();
                let mut parsed = Vec::with_capacity(records.len());
                for (idx, entry) in records.iter().enumerate() {
                    let item = parse_delete_item(idx, entry)?;
                    if !seen.insert(item.id.clone()) {
                        return Err(BatchError::item(
                            idx,
                            Some(item.id),
                            StoreError::Invalid("批量请求中存在重复的记录标识"),
                        ));
                    }
                    parsed.push(item);
                }
                BatchItems::Delete(parsed)
            }
        };

        Ok(Self {
            idempotency_key,
            items,
        })
    }

    pub fn canonical_request_bytes(&self) -> Result<Vec<u8>, StoreError> {
        let canonical = match &self.items {
            BatchItems::Create(items) => json!({
                "operation": "create",
                "records": items,
            }),
            BatchItems::Update(items) => json!({
                "operation": "update",
                "records": items,
            }),
            BatchItems::Delete(items) => json!({
                "operation": "delete",
                "records": items,
            }),
        };
        serde_json::to_vec(&canonical).map_err(|_| StoreError::Failed)
    }
}

fn parse_create_item(idx: usize, entry: &Value) -> Result<BatchCreateItem, BatchError> {
    let Some(obj) = entry.as_object() else {
        return Err(BatchError::item(
            idx,
            None,
            StoreError::Invalid("批量记录项必须是 JSON 对象"),
        ));
    };
    for key in obj.keys() {
        if key != "values" {
            return Err(BatchError::item(
                idx,
                None,
                StoreError::Invalid("创建记录项包含不支持的参数"),
            ));
        }
    }
    let values = match obj.get("values") {
        None => BTreeMap::new(),
        Some(raw_values) => parse_values_map(idx, None, raw_values, false)?,
    };
    Ok(BatchCreateItem { values })
}

fn parse_update_item(idx: usize, entry: &Value) -> Result<BatchUpdateItem, BatchError> {
    let Some(obj) = entry.as_object() else {
        return Err(BatchError::item(
            idx,
            None,
            StoreError::Invalid("批量记录项必须是 JSON 对象"),
        ));
    };
    for key in obj.keys() {
        if key != "id" && key != "expected_revision" && key != "values" {
            return Err(BatchError::item(
                idx,
                None,
                StoreError::Invalid("更新记录项包含不支持的参数"),
            ));
        }
    }
    let id = parse_item_record_id(idx, obj)?;
    let expected_revision = parse_item_expected_revision(idx, &id, obj)?;
    let Some(raw_values) = obj.get("values") else {
        return Err(BatchError::item(
            idx,
            Some(id),
            StoreError::Invalid("没有要保存的字段"),
        ));
    };
    let values = parse_values_map(idx, Some(&id), raw_values, true)?;
    Ok(BatchUpdateItem {
        id,
        expected_revision,
        values,
    })
}

fn parse_delete_item(idx: usize, entry: &Value) -> Result<BatchDeleteItem, BatchError> {
    let Some(obj) = entry.as_object() else {
        return Err(BatchError::item(
            idx,
            None,
            StoreError::Invalid("批量记录项必须是 JSON 对象"),
        ));
    };
    for key in obj.keys() {
        if key != "id" && key != "expected_revision" {
            return Err(BatchError::item(
                idx,
                None,
                StoreError::Invalid("删除记录项包含不支持的参数"),
            ));
        }
    }
    let id = parse_item_record_id(idx, obj)?;
    let expected_revision = parse_item_expected_revision(idx, &id, obj)?;
    Ok(BatchDeleteItem {
        id,
        expected_revision,
    })
}

fn parse_item_record_id(
    idx: usize,
    obj: &serde_json::Map<String, Value>,
) -> Result<String, BatchError> {
    obj.get("id")
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|id| is_uuid(id))
        .map(str::to_ascii_lowercase)
        .ok_or_else(|| BatchError::item(idx, None, StoreError::Invalid("记录标识无效")))
}

fn parse_item_expected_revision(
    idx: usize,
    record_id: &str,
    obj: &serde_json::Map<String, Value>,
) -> Result<i64, BatchError> {
    let Some(raw) = obj.get("expected_revision") else {
        return Err(BatchError::item(
            idx,
            Some(record_id.to_owned()),
            StoreError::Invalid("记录版本号无效"),
        ));
    };
    let Some(rev) = raw.as_i64() else {
        return Err(BatchError::item(
            idx,
            Some(record_id.to_owned()),
            StoreError::Invalid("记录版本号无效"),
        ));
    };
    if rev <= 0 {
        return Err(BatchError::item(
            idx,
            Some(record_id.to_owned()),
            StoreError::Invalid("记录版本号无效"),
        ));
    }
    Ok(rev)
}

fn parse_values_map(
    idx: usize,
    record_id: Option<&str>,
    raw: &Value,
    require_non_empty: bool,
) -> Result<BTreeMap<String, Value>, BatchError> {
    let Some(map) = raw.as_object() else {
        return Err(BatchError::item(
            idx,
            record_id.map(str::to_owned),
            StoreError::Invalid("values 必须是 JSON 对象"),
        ));
    };
    if require_non_empty && map.is_empty() {
        return Err(BatchError::item(
            idx,
            record_id.map(str::to_owned),
            StoreError::Invalid("没有要保存的字段"),
        ));
    }
    let mut out = BTreeMap::new();
    for (field_id, value) in map {
        if !is_uuid(field_id) {
            return Err(BatchError::item(
                idx,
                record_id.map(str::to_owned),
                StoreError::Invalid("字段标识无效"),
            ));
        }
        out.insert(field_id.to_ascii_lowercase(), value.clone());
    }
    Ok(out)
}

pub async fn execute_batch(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    actor: &Actor,
    req: &BatchRequest,
) -> Result<Value, BatchError> {
    let table_lower = table.to_ascii_lowercase();
    let canonical_bytes = req.canonical_request_bytes().map_err(BatchError::top)?;
    let operation = req.operation().as_str();

    let mut tx = begin(pool).await.map_err(BatchError::top)?;

    let request_hash: String = sqlx::query_scalar("SELECT encode(sha256($1::bytea), 'hex')")
        .bind(canonical_bytes.as_slice())
        .fetch_one(&mut *tx)
        .await
        .map_err(|err| BatchError::top(failed(err)))?;

    // Acquire table and related table locks once in canonical order before
    // idempotency lookup or any mutation.
    let locked_tables = match &req.items {
        BatchItems::Create(items) => {
            let touched = collect_touched_fields(items.iter().map(|item| &item.values));
            relation::prepare_table_locks(&mut tx, workspace, &table_lower, &touched)
                .await
                .map_err(BatchError::top)?
        }
        BatchItems::Update(items) => {
            let touched = collect_touched_fields(items.iter().map(|item| &item.values));
            relation::prepare_table_locks(&mut tx, workspace, &table_lower, &touched)
                .await
                .map_err(BatchError::top)?
        }
        BatchItems::Delete(_) => {
            store::lock_workspace_tables_sorted(&mut tx, workspace, &table_lower)
                .await
                .map_err(BatchError::top)?;
            Vec::new()
        }
    };

    if let Some(replayed) = check_idempotency(
        &mut tx,
        workspace,
        &table_lower,
        actor,
        &req.idempotency_key,
        operation,
        &request_hash,
    )
    .await?
    {
        commit(tx).await.map_err(BatchError::top)?;
        return Ok(replayed);
    }

    let batch_id: String = sqlx::query_scalar("SELECT gen_random_uuid()::text")
        .fetch_one(&mut *tx)
        .await
        .map_err(|err| BatchError::top(failed(err)))?;

    let response = match &req.items {
        BatchItems::Create(items) => {
            let fields = load_fields(&mut tx, workspace, &table_lower)
                .await
                .map_err(BatchError::top)?;
            let mut results: Vec<RecordDto> = Vec::with_capacity(items.len());
            for (idx, item) in items.iter().enumerate() {
                let normalized = normalize_values(&fields, &item.values)
                    .map_err(|err| BatchError::item(idx, None, err))?;
                let created = store::create_record_in_tx(
                    &mut tx,
                    workspace,
                    &table_lower,
                    &fields,
                    &normalized,
                    &locked_tables,
                    Some(actor),
                    Some(&batch_id),
                )
                .await
                .map_err(|err| BatchError::item(idx, None, err))?;
                results.push(created);
            }
            json!({
                "operation": operation,
                "idempotency_key": req.idempotency_key,
                "batch_id": batch_id,
                "records": results,
            })
        }
        BatchItems::Update(items) => {
            let fields = load_fields(&mut tx, workspace, &table_lower)
                .await
                .map_err(BatchError::top)?;
            let mut results: Vec<RecordDto> = Vec::with_capacity(items.len());
            for (idx, item) in items.iter().enumerate() {
                let updated = collab::update_record_in_tx(
                    &mut tx,
                    workspace,
                    &table_lower,
                    &fields,
                    &locked_tables,
                    &item.id,
                    item.expected_revision,
                    &item.values,
                    actor,
                    Some(&batch_id),
                )
                .await
                .map_err(|err| BatchError::item(idx, Some(item.id.clone()), err))?;
                results.push(updated);
            }
            json!({
                "operation": operation,
                "idempotency_key": req.idempotency_key,
                "batch_id": batch_id,
                "records": results,
            })
        }
        BatchItems::Delete(items) => {
            let mut results: Vec<DeletedDto> = Vec::with_capacity(items.len());
            for (idx, item) in items.iter().enumerate() {
                let deleted = store::delete_record_in_tx(
                    &mut tx,
                    workspace,
                    &table_lower,
                    &item.id,
                    item.expected_revision,
                    actor,
                    Some(&batch_id),
                )
                .await
                .map_err(|err| BatchError::item(idx, Some(item.id.clone()), err))?;
                results.push(deleted);
            }
            json!({
                "operation": operation,
                "idempotency_key": req.idempotency_key,
                "batch_id": batch_id,
                "records": results,
            })
        }
    };

    let encoded_response =
        serde_json::to_string(&response).map_err(|_| BatchError::top(StoreError::Failed))?;
    sqlx::query(
        "INSERT INTO generic_record_batch_idempotency (
            workspace_id, table_id, actor_source, actor_id, idempotency_key,
            operation, request_hash, batch_id, response_json
         ) VALUES (
            $1::uuid, $2::uuid, $3, $4::uuid, $5::uuid,
            $6, $7, $8::uuid, $9::jsonb
         )",
    )
    .bind(workspace)
    .bind(&table_lower)
    .bind(&actor.source)
    .bind(&actor.id)
    .bind(&req.idempotency_key)
    .bind(operation)
    .bind(&request_hash)
    .bind(&batch_id)
    .bind(encoded_response)
    .execute(&mut *tx)
    .await
    .map_err(|err| BatchError::top(failed(err)))?;

    commit(tx).await.map_err(BatchError::top)?;
    Ok(response)
}

fn collect_touched_fields<'a>(
    maps: impl Iterator<Item = &'a BTreeMap<String, Value>>,
) -> Vec<String> {
    let mut set = BTreeSet::new();
    for map in maps {
        for field_id in map.keys() {
            set.insert(field_id.clone());
        }
    }
    set.into_iter().collect()
}

async fn check_idempotency(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    actor: &Actor,
    idempotency_key: &str,
    operation: &str,
    request_hash: &str,
) -> Result<Option<Value>, BatchError> {
    let row = sqlx::query(
        "SELECT operation, request_hash, response_json::text
         FROM generic_record_batch_idempotency
         WHERE workspace_id = $1::uuid
           AND table_id = $2::uuid
           AND actor_source = $3
           AND actor_id = $4::uuid
           AND idempotency_key = $5::uuid",
    )
    .bind(workspace)
    .bind(table)
    .bind(&actor.source)
    .bind(&actor.id)
    .bind(idempotency_key)
    .fetch_optional(&mut **tx)
    .await
    .map_err(|err| BatchError::top(failed(err)))?;

    let Some(row) = row else {
        return Ok(None);
    };
    let stored_op: String = row.get(0);
    let stored_hash: String = row.get(1);
    let stored_response: String = row.get(2);
    if stored_op != operation || stored_hash != request_hash {
        return Err(BatchError::idempotency_conflict(
            "同一个 idempotency_key 不能用于不同的批量请求内容",
        ));
    }
    let parsed: Value =
        serde_json::from_str(&stored_response).map_err(|_| BatchError::top(StoreError::Failed))?;
    Ok(Some(parsed))
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    const KEY: &str = "11111111-1111-4111-8111-111111111111";
    const FIELD_A: &str = "22222222-2222-4222-8222-222222222222";
    const REC_1: &str = "33333333-3333-4333-8333-333333333333";
    const REC_2: &str = "44444444-4444-4444-8444-444444444444";

    #[test]
    fn rejects_empty_or_over_limit_batches() {
        let empty = json!({
            "idempotency_key": KEY,
            "operation": "create",
            "records": []
        });
        assert!(BatchRequest::parse(&empty).is_err());

        let too_many = json!({
            "idempotency_key": KEY,
            "operation": "create",
            "records": (0..11).map(|_| json!({"values": {}})).collect::<Vec<_>>()
        });
        assert!(BatchRequest::parse(&too_many).is_err());
    }

    #[test]
    fn rejects_duplicate_record_ids_with_zero_based_index() {
        let dup_update = json!({
            "idempotency_key": KEY,
            "operation": "update",
            "records": [
                {"id": REC_1, "expected_revision": 1, "values": {FIELD_A: "a"}},
                {"id": REC_1.to_uppercase(), "expected_revision": 1, "values": {FIELD_A: "b"}}
            ]
        });
        let err = BatchRequest::parse(&dup_update).unwrap_err();
        assert_eq!(err.index, Some(1));
        assert_eq!(err.record_id.as_deref(), Some(REC_1));

        let dup_delete = json!({
            "idempotency_key": KEY,
            "operation": "delete",
            "records": [
                {"id": REC_1, "expected_revision": 1},
                {"id": REC_2, "expected_revision": 2},
                {"id": REC_1, "expected_revision": 1}
            ]
        });
        let err = BatchRequest::parse(&dup_delete).unwrap_err();
        assert_eq!(err.index, Some(2));
        assert_eq!(err.record_id.as_deref(), Some(REC_1));
    }

    #[test]
    fn canonical_request_bytes_match_across_key_order_and_uuid_case() {
        let a = BatchRequest::parse(&json!({
            "idempotency_key": KEY,
            "operation": "update",
            "records": [
                {"id": REC_1, "expected_revision": 2, "values": {FIELD_A: "ok"}}
            ]
        }))
        .unwrap();
        let b = BatchRequest::parse(&json!({
            "idempotency_key": KEY.to_uppercase(),
            "operation": "update",
            "records": [
                {"values": {FIELD_A.to_uppercase(): "ok"}, "expected_revision": 2, "id": REC_1.to_uppercase()}
            ]
        }))
        .unwrap();
        assert_eq!(
            a.canonical_request_bytes().unwrap(),
            b.canonical_request_bytes().unwrap()
        );
    }
}
