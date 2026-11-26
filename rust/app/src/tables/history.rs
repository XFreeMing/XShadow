use std::collections::{BTreeMap, HashMap};

use chrono::{DateTime, SecondsFormat, Utc};
use serde::Serialize;
use serde_json::Value;
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::store::{FieldDto, RecordDto, TableDto, ViewDto, begin, commit, failed};
use super::validate::{StoredFilter, StoredSort, is_uuid};

const DEFAULT_LIMIT: i64 = 50;
const MAX_LIMIT: i64 = 200;
const COMPUTED_VALUES_MODE: &str = "stored_raw_only";
const HISTORY_TS: &str = "to_char(%s AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')";

fn history_ts(column: &str) -> String {
    HISTORY_TS.replace("%s", column)
}

#[derive(Debug)]
pub enum HistoryError {
    NotFound(&'static str),
    NotAvailable(&'static str),
    Invalid(&'static str),
    Failed,
}

impl From<super::store::StoreError> for HistoryError {
    fn from(err: super::store::StoreError) -> Self {
        match err {
            super::store::StoreError::NotFound(msg) => Self::NotFound(msg),
            super::store::StoreError::Invalid(msg) => Self::Invalid(msg),
            _ => Self::Failed,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SnapshotTarget {
    Revision(i64),
    Timestamp(String),
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct HistorySummaryDto {
    pub table_changes: i64,
    pub field_changes: i64,
    pub view_changes: i64,
    pub record_changes: i64,
    pub cell_changes: i64,
    pub total_changes: i64,
}

#[derive(Clone, Debug, Serialize)]
pub struct HistoryRevisionDto {
    pub revision: i64,
    pub recorded_at: String,
    pub baseline: bool,
    pub summary: HistorySummaryDto,
}

#[derive(Serialize)]
pub struct HistoryListDto {
    pub table_id: String,
    pub baseline_revision: Option<i64>,
    pub latest_revision: Option<i64>,
    pub revisions: Vec<HistoryRevisionDto>,
    pub limit: i64,
    pub has_more: bool,
    pub next_cursor: Option<String>,
}

#[derive(Serialize)]
pub struct HistorySnapshotDto {
    pub revision: i64,
    pub recorded_at: String,
    pub baseline: bool,
    pub computed_values_mode: &'static str,
    pub table: TableDto,
    pub fields: Vec<FieldDto>,
    pub views: Vec<ViewDto>,
    pub records: Vec<RecordDto>,
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct DeletedRecordHistoryDto {
    pub record_id: String,
    pub deleted_at_revision: i64,
    pub deleted_at: String,
}

#[derive(Serialize)]
pub struct HistoryDeletedListDto {
    pub table_id: String,
    pub deleted_records: Vec<DeletedRecordHistoryDto>,
    pub limit: i64,
    pub has_more: bool,
    pub next_cursor: Option<String>,
}

#[derive(Debug, PartialEq, Eq)]
enum DeletedCursor {
    RevisionOnly(i64),
    Composite(i64, String),
}

pub fn parse_limit(limit: Option<i64>) -> Result<i64, HistoryError> {
    match limit {
        None => Ok(DEFAULT_LIMIT),
        Some(n) if (1..=MAX_LIMIT).contains(&n) => Ok(n),
        Some(_) => Err(HistoryError::Invalid("limit 必须在 1 到 200 之间")),
    }
}

pub fn parse_revision_cursor(cursor: Option<&str>) -> Result<Option<i64>, HistoryError> {
    let Some(raw) = cursor.map(str::trim).filter(|s| !s.is_empty()) else {
        return Ok(None);
    };
    let parsed = raw
        .parse::<i64>()
        .map_err(|_| HistoryError::Invalid("cursor 无效"))?;
    if parsed <= 0 {
        return Err(HistoryError::Invalid("cursor 无效"));
    }
    Ok(Some(parsed))
}

fn parse_deleted_cursor(cursor: Option<&str>) -> Result<Option<DeletedCursor>, HistoryError> {
    let Some(raw) = cursor.map(str::trim).filter(|s| !s.is_empty()) else {
        return Ok(None);
    };
    if let Some((rev_part, id_part)) = raw.split_once(':') {
        let rev = rev_part
            .parse::<i64>()
            .map_err(|_| HistoryError::Invalid("cursor 无效"))?;
        if rev <= 0 || !is_uuid(id_part) {
            return Err(HistoryError::Invalid("cursor 无效"));
        }
        return Ok(Some(DeletedCursor::Composite(rev, id_part.to_owned())));
    }
    let rev = raw
        .parse::<i64>()
        .map_err(|_| HistoryError::Invalid("cursor 无效"))?;
    if rev <= 0 {
        return Err(HistoryError::Invalid("cursor 无效"));
    }
    Ok(Some(DeletedCursor::RevisionOnly(rev)))
}

pub fn parse_snapshot_target(
    revision: Option<i64>,
    timestamp: Option<&str>,
) -> Result<SnapshotTarget, HistoryError> {
    let ts_clean = timestamp.map(str::trim).filter(|s| !s.is_empty());
    match (revision, ts_clean) {
        (Some(rev), None) => {
            if rev <= 0 {
                return Err(HistoryError::Invalid("revision 必须是正整数"));
            }
            Ok(SnapshotTarget::Revision(rev))
        }
        (None, Some(raw_ts)) => {
            let parsed = DateTime::parse_from_rfc3339(raw_ts)
                .map_err(|_| HistoryError::Invalid("timestamp 必须是有效的 ISO-8601 时间"))?;
            Ok(SnapshotTarget::Timestamp(
                parsed
                    .with_timezone(&Utc)
                    .to_rfc3339_opts(SecondsFormat::Micros, true),
            ))
        }
        (Some(_), Some(_)) => Err(HistoryError::Invalid("revision 和 timestamp 不能同时提供")),
        (None, None) => Err(HistoryError::Invalid("必须提供 revision 或 timestamp")),
    }
}

async fn ensure_table(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<(), HistoryError> {
    let found =
        sqlx::query("SELECT 1 FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid")
            .bind(table)
            .bind(workspace)
            .fetch_optional(&mut **tx)
            .await
            .map_err(failed)?;
    if found.is_none() {
        return Err(HistoryError::NotFound("找不到这张表"));
    }
    Ok(())
}

async fn begin_repeatable_read(pool: &PgPool) -> Result<Transaction<'_, Postgres>, HistoryError> {
    let mut tx = begin(pool).await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
        .execute(&mut *tx)
        .await
        .map_err(failed)?;
    Ok(tx)
}

async fn revision_bounds(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<(Option<i64>, Option<i64>), HistoryError> {
    let row = sqlx::query(
        "SELECT MIN(revision), MAX(revision)
         FROM generic_history_revision
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid",
    )
    .bind(workspace)
    .bind(table)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    Ok((row.get(0), row.get(1)))
}

pub async fn list_history(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    limit: Option<i64>,
    cursor: Option<&str>,
) -> Result<HistoryListDto, HistoryError> {
    let limit = parse_limit(limit)?;
    let cursor_rev = parse_revision_cursor(cursor)?;

    let mut tx = begin_repeatable_read(pool).await?;
    ensure_table(&mut tx, workspace, table).await?;
    let (baseline_revision, latest_revision) = revision_bounds(&mut tx, workspace, table).await?;

    let recorded = history_ts("r.recorded_at");
    let fetch_limit = limit + 1;
    let sql = format!(
        "SELECT r.revision, {recorded}, r.baseline,
                COUNT(*) FILTER (WHERE v.kind = 'table')::bigint,
                COUNT(*) FILTER (WHERE v.kind = 'field')::bigint,
                COUNT(*) FILTER (WHERE v.kind = 'view')::bigint,
                COUNT(*) FILTER (WHERE v.kind = 'record')::bigint,
                COUNT(*) FILTER (WHERE v.kind = 'cell')::bigint,
                COUNT(v.revision)::bigint
         FROM generic_history_revision r
         LEFT JOIN generic_history_version v ON v.revision = r.revision
         WHERE r.workspace_id = $1::uuid
           AND r.table_id = $2::uuid
           AND ($3::bigint IS NULL OR r.revision < $3)
         GROUP BY r.revision, r.recorded_at, r.baseline
         ORDER BY r.revision DESC
         LIMIT $4"
    );
    let rows = sqlx::query(&sql)
        .bind(workspace)
        .bind(table)
        .bind(cursor_rev)
        .bind(fetch_limit)
        .fetch_all(&mut *tx)
        .await
        .map_err(failed)?;
    commit(tx).await?;

    let has_more = rows.len() > limit as usize;
    let revisions: Vec<HistoryRevisionDto> = rows
        .into_iter()
        .take(limit as usize)
        .map(|row| HistoryRevisionDto {
            revision: row.get(0),
            recorded_at: row.get(1),
            baseline: row.get(2),
            summary: HistorySummaryDto {
                table_changes: row.get(3),
                field_changes: row.get(4),
                view_changes: row.get(5),
                record_changes: row.get(6),
                cell_changes: row.get(7),
                total_changes: row.get(8),
            },
        })
        .collect();
    let next_cursor = if has_more {
        revisions.last().map(|item| item.revision.to_string())
    } else {
        None
    };

    Ok(HistoryListDto {
        table_id: table.to_owned(),
        baseline_revision,
        latest_revision,
        revisions,
        limit,
        has_more,
        next_cursor,
    })
}

pub async fn get_snapshot(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    target: SnapshotTarget,
) -> Result<HistorySnapshotDto, HistoryError> {
    let mut tx = begin_repeatable_read(pool).await?;
    ensure_table(&mut tx, workspace, table).await?;
    let (Some(min_rev), Some(max_rev)) = revision_bounds(&mut tx, workspace, table).await? else {
        return Err(HistoryError::NotAvailable("该表尚无可用历史基线"));
    };

    let recorded = history_ts("recorded_at");
    let resolved_row = match target {
        SnapshotTarget::Revision(rev) => {
            if rev < min_rev || rev > max_rev {
                return Err(HistoryError::NotAvailable(
                    "请求的修订版本不在该表的可用历史范围内",
                ));
            }
            let sql = format!(
                "SELECT revision, {recorded}, baseline
                 FROM generic_history_revision
                 WHERE workspace_id = $1::uuid
                   AND table_id = $2::uuid
                   AND revision = $3
                 LIMIT 1"
            );
            sqlx::query(&sql)
                .bind(workspace)
                .bind(table)
                .bind(rev)
                .fetch_optional(&mut *tx)
                .await
                .map_err(failed)?
        }
        SnapshotTarget::Timestamp(ts_str) => {
            let sql = format!(
                "SELECT revision, {recorded}, baseline
                 FROM generic_history_revision
                 WHERE workspace_id = $1::uuid
                   AND table_id = $2::uuid
                   AND recorded_at <= $3::text::timestamptz
                 ORDER BY revision DESC
                 LIMIT 1"
            );
            sqlx::query(&sql)
                .bind(workspace)
                .bind(table)
                .bind(ts_str)
                .fetch_optional(&mut *tx)
                .await
                .map_err(failed)?
        }
    };

    let Some(rev_row) = resolved_row else {
        return Err(HistoryError::NotAvailable(
            "请求的修订版本不存在或时间早于该表历史基线",
        ));
    };
    let resolved_revision: i64 = rev_row.get(0);
    let recorded_at: String = rev_row.get(1);
    let baseline: bool = rev_row.get(2);

    let rows = sqlx::query(
        "SELECT DISTINCT ON (v.kind, v.entity_id, v.secondary_id)
                v.kind,
                v.entity_id::text,
                v.secondary_id::text,
                v.payload::text
         FROM generic_history_version v
         JOIN generic_history_revision r ON r.revision = v.revision
         WHERE r.workspace_id = $1::uuid
           AND r.table_id = $2::uuid
           AND r.revision <= $3
         ORDER BY v.kind, v.entity_id, v.secondary_id, v.revision DESC",
    )
    .bind(workspace)
    .bind(table)
    .bind(resolved_revision)
    .fetch_all(&mut *tx)
    .await
    .map_err(failed)?;
    commit(tx).await?;

    let mut versions = Vec::with_capacity(rows.len());
    for row in rows {
        let kind: String = row.get(0);
        let entity_id: String = row.get(1);
        let secondary_id: String = row.get(2);
        let payload_text: Option<String> = row.get(3);
        let payload = match payload_text {
            Some(text) => Some(serde_json::from_str::<Value>(&text).map_err(|_| HistoryError::Failed)?),
            None => None,
        };
        versions.push(VersionEntry {
            kind,
            entity_id,
            secondary_id,
            payload,
        });
    }

    assemble_snapshot(resolved_revision, recorded_at, baseline, versions)
}

pub async fn list_deleted_records(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    limit: Option<i64>,
    cursor: Option<&str>,
) -> Result<HistoryDeletedListDto, HistoryError> {
    let limit = parse_limit(limit)?;
    let parsed_cursor = parse_deleted_cursor(cursor)?;

    let mut tx = begin_repeatable_read(pool).await?;
    ensure_table(&mut tx, workspace, table).await?;

    let recorded = history_ts("r.recorded_at");
    let fetch_limit = limit + 1;
    let rows = match parsed_cursor {
        None => {
            let sql = format!(
                "SELECT v.entity_id::text, v.revision, {recorded}
                 FROM generic_history_version v
                 JOIN generic_history_revision r ON r.revision = v.revision
                 WHERE r.workspace_id = $1::uuid
                   AND r.table_id = $2::uuid
                   AND v.kind = 'record'
                   AND v.payload IS NULL
                 ORDER BY v.revision DESC, v.entity_id DESC
                 LIMIT $3"
            );
            sqlx::query(&sql)
                .bind(workspace)
                .bind(table)
                .bind(fetch_limit)
                .fetch_all(&mut *tx)
                .await
                .map_err(failed)?
        }
        Some(DeletedCursor::RevisionOnly(rev)) => {
            let sql = format!(
                "SELECT v.entity_id::text, v.revision, {recorded}
                 FROM generic_history_version v
                 JOIN generic_history_revision r ON r.revision = v.revision
                 WHERE r.workspace_id = $1::uuid
                   AND r.table_id = $2::uuid
                   AND v.kind = 'record'
                   AND v.payload IS NULL
                   AND v.revision < $3
                 ORDER BY v.revision DESC, v.entity_id DESC
                 LIMIT $4"
            );
            sqlx::query(&sql)
                .bind(workspace)
                .bind(table)
                .bind(rev)
                .bind(fetch_limit)
                .fetch_all(&mut *tx)
                .await
                .map_err(failed)?
        }
        Some(DeletedCursor::Composite(rev, record_id)) => {
            let sql = format!(
                "SELECT v.entity_id::text, v.revision, {recorded}
                 FROM generic_history_version v
                 JOIN generic_history_revision r ON r.revision = v.revision
                 WHERE r.workspace_id = $1::uuid
                   AND r.table_id = $2::uuid
                   AND v.kind = 'record'
                   AND v.payload IS NULL
                   AND (v.revision, v.entity_id) < ($3, $4::uuid)
                 ORDER BY v.revision DESC, v.entity_id DESC
                 LIMIT $5"
            );
            sqlx::query(&sql)
                .bind(workspace)
                .bind(table)
                .bind(rev)
                .bind(record_id)
                .bind(fetch_limit)
                .fetch_all(&mut *tx)
                .await
                .map_err(failed)?
        }
    };
    commit(tx).await?;

    let has_more = rows.len() > limit as usize;
    let deleted_records: Vec<DeletedRecordHistoryDto> = rows
        .into_iter()
        .take(limit as usize)
        .map(|row| DeletedRecordHistoryDto {
            record_id: row.get(0),
            deleted_at_revision: row.get(1),
            deleted_at: row.get(2),
        })
        .collect();
    let next_cursor = if has_more {
        deleted_records
            .last()
            .map(|item| format!("{}:{}", item.deleted_at_revision, item.record_id))
    } else {
        None
    };

    Ok(HistoryDeletedListDto {
        table_id: table.to_owned(),
        deleted_records,
        limit,
        has_more,
        next_cursor,
    })
}

struct VersionEntry {
    kind: String,
    entity_id: String,
    secondary_id: String,
    payload: Option<Value>,
}

fn assemble_snapshot(
    revision: i64,
    recorded_at: String,
    baseline: bool,
    versions: Vec<VersionEntry>,
) -> Result<HistorySnapshotDto, HistoryError> {
    let mut table: Option<TableDto> = None;
    let mut fields: Vec<FieldDto> = Vec::new();
    let mut views: Vec<(String, ViewDto)> = Vec::new();
    let mut raw_records: Vec<(String, RecordDto)> = Vec::new();
    let mut cells_by_record: HashMap<String, HashMap<String, Value>> = HashMap::new();

    for entry in versions {
        let Some(payload) = entry.payload else {
            // Tombstone: entity was deleted at or before this revision.
            continue;
        };
        match entry.kind.as_str() {
            "table" => {
                table = Some(parse_table_payload(&payload)?);
            }
            "field" => {
                fields.push(parse_field_payload(&payload)?);
            }
            "view" => {
                let raw_created = str_field(&payload, "created_at")?.to_owned();
                views.push((raw_created, parse_view_payload(&payload)?));
            }
            "record" => {
                let raw_created = str_field(&payload, "created_at")?.to_owned();
                raw_records.push((raw_created, parse_record_payload(&payload)?));
            }
            "cell" => {
                let value = payload.get("value").cloned().unwrap_or(Value::Null);
                cells_by_record
                    .entry(entry.entity_id)
                    .or_default()
                    .insert(entry.secondary_id, value);
            }
            _ => {}
        }
    }

    let table = table.ok_or(HistoryError::NotAvailable("该修订版本中不存在表元数据"))?;

    fields.sort_by(|a, b| a.position.cmp(&b.position).then_with(|| a.id.cmp(&b.id)));
    views.sort_by(|(ca, a), (cb, b)| ca.cmp(cb).then_with(|| a.id.cmp(&b.id)));
    raw_records.sort_by(|(ca, a), (cb, b)| ca.cmp(cb).then_with(|| a.id.cmp(&b.id)));

    let views: Vec<ViewDto> = views.into_iter().map(|(_, v)| v).collect();
    let records: Vec<RecordDto> = raw_records
        .into_iter()
        .map(|(_, mut record)| {
            let cell_map = cells_by_record.remove(&record.id).unwrap_or_default();
            let mut values = BTreeMap::new();
            for field in &fields {
                let val = cell_map.get(&field.id).cloned().unwrap_or(Value::Null);
                values.insert(field.id.clone(), val);
            }
            record.values = values;
            record
        })
        .collect();

    Ok(HistorySnapshotDto {
        revision,
        recorded_at,
        baseline,
        computed_values_mode: COMPUTED_VALUES_MODE,
        table,
        fields,
        views,
        records,
    })
}

fn str_field<'a>(payload: &'a Value, key: &str) -> Result<&'a str, HistoryError> {
    payload
        .get(key)
        .and_then(Value::as_str)
        .ok_or(HistoryError::Failed)
}

fn normalize_timestamp(raw: &str) -> Result<String, HistoryError> {
    let parsed = DateTime::parse_from_rfc3339(raw).map_err(|_| HistoryError::Failed)?;
    Ok(parsed
        .with_timezone(&Utc)
        .format("%Y-%m-%dT%H:%M:%SZ")
        .to_string())
}

fn parse_table_payload(payload: &Value) -> Result<TableDto, HistoryError> {
    Ok(TableDto {
        id: str_field(payload, "id")?.to_owned(),
        name: str_field(payload, "name")?.to_owned(),
        created_at: normalize_timestamp(str_field(payload, "created_at")?)?,
        updated_at: normalize_timestamp(str_field(payload, "updated_at")?)?,
    })
}

fn parse_field_payload(payload: &Value) -> Result<FieldDto, HistoryError> {
    let position = payload
        .get("position")
        .and_then(Value::as_i64)
        .and_then(|v| i32::try_from(v).ok())
        .ok_or(HistoryError::Failed)?;
    Ok(FieldDto {
        id: str_field(payload, "id")?.to_owned(),
        name: str_field(payload, "name")?.to_owned(),
        kind: str_field(payload, "kind")?.to_owned(),
        config: payload
            .get("config")
            .cloned()
            .unwrap_or_else(|| Value::Object(serde_json::Map::new())),
        position,
        created_at: normalize_timestamp(str_field(payload, "created_at")?)?,
        updated_at: normalize_timestamp(str_field(payload, "updated_at")?)?,
    })
}

fn parse_view_payload(payload: &Value) -> Result<ViewDto, HistoryError> {
    let columns: Option<Vec<String>> = match payload.get("columns") {
        None | Some(Value::Null) => None,
        Some(v) => Some(serde_json::from_value(v.clone()).map_err(|_| HistoryError::Failed)?),
    };
    let filter: Option<StoredFilter> = match payload.get("filter") {
        None | Some(Value::Null) => None,
        Some(v) => Some(serde_json::from_value(v.clone()).map_err(|_| HistoryError::Failed)?),
    };
    let sort: Option<StoredSort> = match payload.get("sort") {
        None | Some(Value::Null) => None,
        Some(v) => Some(serde_json::from_value(v.clone()).map_err(|_| HistoryError::Failed)?),
    };
    let group = payload
        .get("group_field")
        .and_then(Value::as_str)
        .map(str::to_owned);
    Ok(ViewDto {
        id: str_field(payload, "id")?.to_owned(),
        name: str_field(payload, "name")?.to_owned(),
        kind: payload
            .get("kind")
            .and_then(Value::as_str)
            .unwrap_or("grid")
            .to_owned(),
        config: payload
            .get("config")
            .cloned()
            .unwrap_or_else(|| Value::Object(serde_json::Map::new())),
        columns,
        filter,
        sort,
        group,
        created_at: normalize_timestamp(str_field(payload, "created_at")?)?,
        updated_at: normalize_timestamp(str_field(payload, "updated_at")?)?,
    })
}

fn parse_record_payload(payload: &Value) -> Result<RecordDto, HistoryError> {
    let revision = payload
        .get("revision")
        .and_then(Value::as_i64)
        .ok_or(HistoryError::Failed)?;
    Ok(RecordDto {
        id: str_field(payload, "id")?.to_owned(),
        revision,
        created_at: normalize_timestamp(str_field(payload, "created_at")?)?,
        updated_at: normalize_timestamp(str_field(payload, "updated_at")?)?,
        values: BTreeMap::new(),
        errors: BTreeMap::new(),
    })
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn parse_limit_enforces_bounds() {
        assert_eq!(parse_limit(None).unwrap(), DEFAULT_LIMIT);
        assert_eq!(parse_limit(Some(1)).unwrap(), 1);
        assert_eq!(parse_limit(Some(200)).unwrap(), 200);
        assert!(matches!(parse_limit(Some(0)), Err(HistoryError::Invalid(_))));
        assert!(matches!(
            parse_limit(Some(201)),
            Err(HistoryError::Invalid(_))
        ));
    }

    #[test]
    fn parse_deleted_cursor_handles_composite_and_integer() {
        let id = "11111111-1111-4111-8111-111111111111";
        assert_eq!(
            parse_deleted_cursor(Some(&format!("42:{id}"))).unwrap(),
            Some(DeletedCursor::Composite(42, id.to_owned()))
        );
        assert_eq!(
            parse_deleted_cursor(Some("42")).unwrap(),
            Some(DeletedCursor::RevisionOnly(42))
        );
        assert!(parse_deleted_cursor(Some("0")).is_err());
        assert!(parse_deleted_cursor(Some("42:not-a-uuid")).is_err());
    }

    #[test]
    fn parse_snapshot_target_requires_exactly_one_valid_selector() {
        assert_eq!(
            parse_snapshot_target(Some(5), None).unwrap(),
            SnapshotTarget::Revision(5)
        );
        assert!(matches!(
            parse_snapshot_target(None, Some("2026-11-26T09:00:00.654321+08:00")).unwrap(),
            SnapshotTarget::Timestamp(ts) if ts == "2026-11-26T01:00:00.654321Z"
        ));
        assert!(matches!(
            parse_snapshot_target(None, Some("2026-11-26T09:00:00Z")).unwrap(),
            SnapshotTarget::Timestamp(ts) if ts == "2026-11-26T09:00:00.000000Z"
        ));
        assert!(parse_snapshot_target(None, None).is_err());
        assert!(parse_snapshot_target(Some(1), Some("2026-11-26T09:00:00Z")).is_err());
        assert!(parse_snapshot_target(Some(0), None).is_err());
        assert!(parse_snapshot_target(None, Some("not-a-timestamp")).is_err());
    }

    #[test]
    fn history_ts_uses_utc_microsecond_format() {
        assert_eq!(
            history_ts("r.recorded_at"),
            "to_char(r.recorded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')"
        );
    }

    #[test]
    fn assemble_snapshot_omits_tombstones_and_maps_cells_by_field_uuid() {
        let zero = "00000000-0000-0000-0000-000000000000".to_owned();
        let table_id = "11111111-1111-4111-8111-111111111111".to_owned();
        let field_text = "22222222-2222-4222-8222-222222222222".to_owned();
        let field_formula = "33333333-3333-4333-8333-333333333333".to_owned();
        let view_id = "44444444-4444-4444-8444-444444444444".to_owned();
        let rec_live = "55555555-5555-4555-8555-555555555555".to_owned();
        let rec_deleted = "66666666-6666-4666-8666-666666666666".to_owned();

        let versions = vec![
            VersionEntry {
                kind: "table".to_owned(),
                entity_id: table_id.clone(),
                secondary_id: zero.clone(),
                payload: Some(json!({
                    "id": table_id,
                    "name": "Orders",
                    "created_at": "2026-11-26T01:00:00.123456+00:00",
                    "updated_at": "2026-11-26T01:05:00+00:00"
                })),
            },
            VersionEntry {
                kind: "field".to_owned(),
                entity_id: field_formula.clone(),
                secondary_id: zero.clone(),
                payload: Some(json!({
                    "id": field_formula,
                    "name": "Double",
                    "kind": "formula",
                    "config": {"expression": "1 + 1"},
                    "position": 2,
                    "created_at": "2026-11-26T01:00:00Z",
                    "updated_at": "2026-11-26T01:00:00Z"
                })),
            },
            VersionEntry {
                kind: "field".to_owned(),
                entity_id: field_text.clone(),
                secondary_id: zero.clone(),
                payload: Some(json!({
                    "id": field_text,
                    "name": "Title Renamed",
                    "kind": "text",
                    "config": {},
                    "position": 1,
                    "created_at": "2026-11-26T01:00:00Z",
                    "updated_at": "2026-11-26T01:04:00Z"
                })),
            },
            VersionEntry {
                kind: "view".to_owned(),
                entity_id: view_id.clone(),
                secondary_id: zero.clone(),
                payload: Some(json!({
                    "id": view_id,
                    "name": "Edited Grid",
                    "kind": "grid",
                    "config": {},
                    "columns": [field_text],
                    "filter": null,
                    "sort": null,
                    "group_field": null,
                    "created_at": "2026-11-26T01:00:00Z",
                    "updated_at": "2026-11-26T01:05:00Z"
                })),
            },
            VersionEntry {
                kind: "record".to_owned(),
                entity_id: rec_live.clone(),
                secondary_id: zero.clone(),
                payload: Some(json!({
                    "id": rec_live,
                    "revision": 3,
                    "created_at": "2026-11-26T01:00:00Z",
                    "updated_at": "2026-11-26T01:03:00Z"
                })),
            },
            VersionEntry {
                kind: "record".to_owned(),
                entity_id: rec_deleted.clone(),
                secondary_id: zero.clone(),
                payload: None,
            },
            VersionEntry {
                kind: "cell".to_owned(),
                entity_id: rec_live.clone(),
                secondary_id: field_text.clone(),
                payload: Some(json!({
                    "record_id": rec_live,
                    "field_id": field_text,
                    "value": "v2"
                })),
            },
            VersionEntry {
                kind: "cell".to_owned(),
                entity_id: rec_deleted.clone(),
                secondary_id: field_text.clone(),
                payload: Some(json!({
                    "record_id": rec_deleted,
                    "field_id": field_text,
                    "value": "ghost"
                })),
            },
        ];

        let snap = assemble_snapshot(9, "2026-11-26T01:05:00Z".to_owned(), false, versions)
            .expect("snapshot");
        assert_eq!(snap.revision, 9);
        assert_eq!(snap.computed_values_mode, "stored_raw_only");
        assert_eq!(snap.table.created_at, "2026-11-26T01:00:00Z");
        assert_eq!(snap.fields.len(), 2);
        assert_eq!(snap.fields[0].id, field_text);
        assert_eq!(snap.fields[1].id, field_formula);
        assert_eq!(snap.views.len(), 1);
        assert_eq!(snap.views[0].name, "Edited Grid");
        assert_eq!(snap.records.len(), 1);
        assert_eq!(snap.records[0].id, rec_live);
        assert_eq!(snap.records[0].revision, 3);
        assert_eq!(snap.records[0].values.get(&field_text), Some(&json!("v2")));
        assert_eq!(
            snap.records[0].values.get(&field_formula),
            Some(&Value::Null)
        );
    }
}
