use std::collections::BTreeSet;

use serde_json::{Value, value::RawValue};
use sqlx::{Error as SqlxError, PgConnection};

use super::is_uuid;

const MAX_RECORD_RELATION_VALUES: usize = 50;

#[derive(Debug)]
pub(super) enum RecordRelationError {
    Invalid,
    Database(SqlxError),
}

pub(super) async fn validate_record_relation_value(
    conn: &mut PgConnection,
    workspace_id: &str,
    config_text: &str,
    value: &RawValue,
) -> Result<(), RecordRelationError> {
    let config: Value =
        serde_json::from_str(config_text).map_err(|_| RecordRelationError::Invalid)?;
    let target_table_id = config
        .get("target_table_id")
        .and_then(Value::as_str)
        .filter(|id| is_uuid(id))
        .ok_or(RecordRelationError::Invalid)?;
    let record_ids: Vec<String> =
        serde_json::from_str(value.get()).map_err(|_| RecordRelationError::Invalid)?;
    if record_ids.is_empty() || record_ids.len() > MAX_RECORD_RELATION_VALUES {
        return Err(RecordRelationError::Invalid);
    }
    let mut seen = BTreeSet::new();
    if record_ids.iter().any(|id| {
        !is_uuid(id) || id.bytes().any(|byte| byte.is_ascii_uppercase()) || !seen.insert(id)
    }) {
        return Err(RecordRelationError::Invalid);
    }

    let count: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM (
            SELECT r.id FROM generic_record r
            JOIN generic_table t ON t.id = r.table_id AND t.workspace_id = r.workspace_id
            WHERE r.workspace_id = $1::uuid AND r.table_id = $2::uuid
              AND r.id = ANY($3::text[]::uuid[])
            ORDER BY r.id FOR KEY SHARE OF r
         ) linked",
    )
    .bind(workspace_id)
    .bind(target_table_id)
    .bind(&record_ids)
    .fetch_one(&mut *conn)
    .await
    .map_err(RecordRelationError::Database)?;
    if count as usize != record_ids.len() {
        return Err(RecordRelationError::Invalid);
    }

    Ok(())
}
