use std::collections::{BTreeMap, BTreeSet};

use serde::Serialize;
use serde_json::Value;
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::relation;
use super::store::{
    FieldDto, RecordDto, StoreError, begin, commit, failed, load_fields, load_record, lock_record,
    normalize_values, ts, write_cells,
};

const CONFLICT: &str = "这条记录已在其他窗口更新，刚才的修改没有写入";
const PAGE: i64 = 200;

#[derive(Clone, Debug)]
pub struct Actor {
    pub source: String,
    pub id: String,
    pub task_id: Option<String>,
    pub label: String,
}

#[derive(Clone, Serialize)]
pub struct ActorDto {
    pub source: String,
    pub id: String,
    pub task_id: Option<String>,
    pub label: String,
    pub color: String,
}

#[derive(Clone, Serialize)]
pub struct ChangeDto {
    pub seq: i64,
    pub event_type: String,
    pub topic: String,
    pub entity_id: String,
    pub record_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub batch_id: Option<String>,
    pub base_revision: i64,
    pub client_revision: i64,
    pub record_revision: i64,
    pub fields: BTreeMap<String, Value>,
    pub actor: ActorDto,
    pub created_at: String,
}

#[derive(Clone, Serialize)]
pub struct PresenceDto {
    pub actor: ActorDto,
    pub record_id: Option<String>,
    pub field_id: Option<String>,
    pub online: bool,
}

#[derive(Serialize)]
pub struct Snapshot {
    pub latest: i64,
    pub next_since: i64,
    pub changes: Vec<ChangeDto>,
    pub presence: Vec<PresenceDto>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Decision {
    Apply { base: i64 },
    Conflict,
}

struct Step {
    base: i64,
    result: i64,
    fields: BTreeSet<String>,
}

pub fn decide(
    current: i64,
    expected: i64,
    steps: &[(i64, i64, BTreeSet<String>)],
    patch: &BTreeSet<String>,
) -> Decision {
    if expected <= 0 || current <= 0 || expected > current {
        return Decision::Conflict;
    }
    if expected == current {
        return Decision::Apply { base: current };
    }
    let parsed: Vec<Step> = steps
        .iter()
        .map(|(base, result, fields)| Step {
            base: *base,
            result: *result,
            fields: fields.clone(),
        })
        .collect();
    let mut cursor = expected;
    let mut touched = BTreeSet::new();
    while cursor < current {
        let mut found: Vec<&Step> = parsed.iter().filter(|step| step.base == cursor).collect();
        if found.len() != 1 {
            return Decision::Conflict;
        }
        let step = found.pop().expect("one step");
        if step.result != cursor + 1 {
            return Decision::Conflict;
        }
        touched.extend(step.fields.iter().cloned());
        cursor = step.result;
    }
    if cursor != current || patch.iter().any(|field| touched.contains(field)) {
        return Decision::Conflict;
    }
    Decision::Apply { base: current }
}

pub fn digest_str(text: &str) -> String {
    let mut hash: u64 = 0xcbf29ce484222325;
    for byte in text.as_bytes() {
        hash ^= u64::from(*byte);
        hash = hash.wrapping_mul(0x100000001b3);
    }
    format!("{hash:016x}{:016x}", text.len() as u64)
}

pub fn digest(values: &BTreeMap<String, Option<Value>>) -> Result<String, StoreError> {
    let text = serde_json::to_string(values).map_err(|_| StoreError::Failed)?;
    Ok(digest_str(&text))
}

pub fn actor_color(id: &str) -> String {
    const COLORS: [&str; 6] = [
        "#7B67EE", "#FF8A00", "#30C28B", "#F25F8A", "#3B82F6", "#E6A100",
    ];
    let mut hash = 0u32;
    for byte in id.bytes() {
        hash = hash.wrapping_mul(31).wrapping_add(u32::from(byte));
    }
    COLORS[(hash as usize) % COLORS.len()].to_owned()
}

pub(super) fn actor_dto(source: &str, id: &str, task_id: Option<String>, label: &str) -> ActorDto {
    ActorDto {
        source: source.to_owned(),
        id: id.to_owned(),
        task_id,
        label: label.to_owned(),
        color: actor_color(id),
    }
}

pub async fn update_record(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    record: &str,
    expected_revision: i64,
    values: &BTreeMap<String, Value>,
    actor: &Actor,
) -> Result<RecordDto, StoreError> {
    if values.is_empty() || expected_revision <= 0 {
        return Err(StoreError::Invalid("没有要保存的字段"));
    }
    let mut tx = begin(pool).await?;
    let touched: Vec<String> = values.keys().cloned().collect();
    let locked_tables = relation::prepare_table_locks(&mut tx, workspace, table, &touched).await?;
    let fields = load_fields(&mut tx, workspace, table).await?;
    let saved = update_record_in_tx(
        &mut tx,
        workspace,
        table,
        &fields,
        &locked_tables,
        record,
        expected_revision,
        values,
        actor,
        None,
    )
    .await?;
    commit(tx).await?;
    Ok(saved)
}

#[allow(clippy::too_many_arguments)]
pub(super) async fn update_record_in_tx(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    fields: &[FieldDto],
    locked_tables: &[String],
    record: &str,
    expected_revision: i64,
    values: &BTreeMap<String, Value>,
    actor: &Actor,
    batch_id: Option<&str>,
) -> Result<RecordDto, StoreError> {
    if values.is_empty() || expected_revision <= 0 {
        return Err(StoreError::Invalid("没有要保存的字段"));
    }
    let current = lock_record(tx, workspace, table, record).await?;
    let normalized = normalize_values(fields, values)?;
    let digest = digest(&normalized)?;
    let fresh = load_record(tx, workspace, fields, record).await?;
    if batch_id.is_none()
        && same_write(
            tx,
            workspace,
            table,
            record,
            expected_revision,
            actor,
            &digest,
        )
        .await?
        && cells_match(&fresh, &normalized)
    {
        return Ok(fresh);
    }
    let steps = load_steps(tx, workspace, table, record).await?;
    let patch: BTreeSet<String> = normalized.keys().cloned().collect();
    let decision = decide(current.revision, expected_revision, &steps, &patch);
    let Decision::Apply { base } = decision else {
        return Err(StoreError::Conflict {
            message: CONFLICT,
            record: Box::new(fresh),
        });
    };
    let reverse_plan = relation::validate_and_plan_reverse(
        tx,
        workspace,
        fields,
        Some(&fresh.values),
        &normalized,
        locked_tables,
    )
    .await?;
    super::store::validate_attachment_refs(tx, workspace, table, fields, &normalized).await?;
    write_cells(tx, workspace, record, &normalized).await?;
    bump_revision(tx, workspace, table, record).await?;
    let saved_revision = current.revision + 1;
    insert_change(
        tx,
        &ChangeWrite {
            workspace,
            table,
            record,
            base,
            client_revision: expected_revision,
            record_revision: saved_revision,
            values: &normalized,
            actor,
            digest: &digest,
            batch_id,
        },
    )
    .await?;
    relation::apply_reverse_plan(tx, workspace, record, &reverse_plan, actor, batch_id).await?;
    load_record(tx, workspace, fields, record).await
}

fn cells_match(record: &RecordDto, patch: &BTreeMap<String, Option<Value>>) -> bool {
    patch.iter().all(|(id, value)| {
        let current = record.values.get(id).cloned().unwrap_or(Value::Null);
        current == value.clone().unwrap_or(Value::Null)
    })
}

async fn same_write(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    record: &str,
    client_revision: i64,
    actor: &Actor,
    digest: &str,
) -> Result<bool, StoreError> {
    let row = sqlx::query(
        "SELECT 1 FROM generic_change
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid
           AND event_type = 'record.updated'
           AND client_revision = $4 AND actor_id = $5::uuid AND op_digest = $6
         LIMIT 1",
    )
    .bind(workspace)
    .bind(table)
    .bind(record)
    .bind(client_revision)
    .bind(&actor.id)
    .bind(digest)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?;
    Ok(row.is_some())
}

async fn load_steps(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    record: &str,
) -> Result<Vec<(i64, i64, BTreeSet<String>)>, StoreError> {
    let rows = sqlx::query(
        "SELECT base_revision, record_revision, fields::text
         FROM generic_change
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid
           AND event_type = 'record.updated'
         ORDER BY base_revision",
    )
    .bind(workspace)
    .bind(table)
    .bind(record)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    let mut steps = Vec::new();
    for row in rows {
        let raw: String = row.get(2);
        let fields: BTreeMap<String, Value> =
            serde_json::from_str(&raw).map_err(|_| StoreError::Failed)?;
        steps.push((row.get(0), row.get(1), fields.keys().cloned().collect()));
    }
    Ok(steps)
}

pub(super) async fn bump_revision(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    record: &str,
) -> Result<(), StoreError> {
    let created = ts("created_at");
    let updated = ts("updated_at");
    let sql = format!(
        "UPDATE generic_record
         SET revision = revision + 1, updated_at = now()
         WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
         RETURNING id::text, revision, {created}, {updated}"
    );
    sqlx::query(&sql)
        .bind(record)
        .bind(table)
        .bind(workspace)
        .fetch_one(&mut **tx)
        .await
        .map_err(failed)?;
    Ok(())
}

async fn next_seq(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<i64, StoreError> {
    let row = sqlx::query(
        "SELECT COALESCE(MAX(seq), 0) + 1 FROM generic_change WHERE workspace_id = $1::uuid AND table_id = $2::uuid",
    )
    .bind(workspace)
    .bind(table)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    Ok(row.get(0))
}

pub(super) struct ChangeWrite<'a> {
    pub workspace: &'a str,
    pub table: &'a str,
    pub record: &'a str,
    pub base: i64,
    pub client_revision: i64,
    pub record_revision: i64,
    pub values: &'a BTreeMap<String, Option<Value>>,
    pub actor: &'a Actor,
    pub digest: &'a str,
    pub batch_id: Option<&'a str>,
}

pub(super) struct EventWrite<'a> {
    pub workspace: &'a str,
    pub table: &'a str,
    pub event_type: &'a str,
    pub topic: &'a str,
    pub entity_id: &'a str,
    pub record_id: Option<&'a str>,
    pub batch_id: Option<&'a str>,
    pub base_revision: i64,
    pub client_revision: i64,
    pub record_revision: i64,
    pub values: &'a BTreeMap<String, Option<Value>>,
    pub actor: &'a Actor,
    pub digest: &'a str,
}

pub(super) async fn insert_change(
    tx: &mut Transaction<'_, Postgres>,
    write: &ChangeWrite<'_>,
) -> Result<(), StoreError> {
    insert_event(
        tx,
        &EventWrite {
            workspace: write.workspace,
            table: write.table,
            event_type: "record.updated",
            topic: "record",
            entity_id: write.record,
            record_id: Some(write.record),
            batch_id: write.batch_id,
            base_revision: write.base,
            client_revision: write.client_revision,
            record_revision: write.record_revision,
            values: write.values,
            actor: write.actor,
            digest: write.digest,
        },
    )
    .await
}

pub(super) async fn insert_event(
    tx: &mut Transaction<'_, Postgres>,
    write: &EventWrite<'_>,
) -> Result<(), StoreError> {
    let seq = next_seq(tx, write.workspace, write.table).await?;
    let mut fields = BTreeMap::new();
    for (id, value) in write.values {
        fields.insert(id.clone(), value.clone().unwrap_or(Value::Null));
    }
    let encoded = serde_json::to_string(&fields).map_err(|_| StoreError::Failed)?;
    sqlx::query(
        "INSERT INTO generic_change (
            workspace_id, table_id, seq, event_type, topic, entity_id, record_id, batch_id,
            base_revision, client_revision, record_revision,
            fields, actor_source, actor_id, task_id, actor_label, op_digest
         ) VALUES (
            $1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7::uuid, $8::uuid,
            $9, $10, $11, $12::jsonb, $13, $14::uuid, $15::uuid, $16, $17
         )",
    )
    .bind(write.workspace)
    .bind(write.table)
    .bind(seq)
    .bind(write.event_type)
    .bind(write.topic)
    .bind(write.entity_id)
    .bind(write.record_id)
    .bind(write.batch_id)
    .bind(write.base_revision)
    .bind(write.client_revision)
    .bind(write.record_revision)
    .bind(encoded)
    .bind(&write.actor.source)
    .bind(&write.actor.id)
    .bind(&write.actor.task_id)
    .bind(&write.actor.label)
    .bind(write.digest)
    .execute(&mut **tx)
    .await
    .map_err(failed)?;
    Ok(())
}

async fn ensure_table(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<(), StoreError> {
    let found =
        sqlx::query("SELECT 1 FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid")
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

pub async fn list_since(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    since: i64,
) -> Result<Snapshot, StoreError> {
    if since < 0 {
        return Err(StoreError::Invalid("变更序号无效"));
    }
    let mut tx = begin(pool).await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
        .execute(&mut *tx)
        .await
        .map_err(failed)?;
    ensure_table(&mut tx, workspace, table).await?;
    let latest = latest_seq(&mut tx, workspace, table).await?;
    let changes = load_changes(&mut tx, workspace, table, since).await?;
    let presence = load_presence(&mut tx, workspace, table).await?;
    commit(tx).await?;
    let next_since = changes.last().map(|change| change.seq).unwrap_or(since);
    Ok(Snapshot {
        latest,
        next_since,
        changes,
        presence,
    })
}

async fn latest_seq(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<i64, StoreError> {
    let row = sqlx::query(
        "SELECT COALESCE(MAX(seq), 0) FROM generic_change WHERE workspace_id = $1::uuid AND table_id = $2::uuid",
    )
    .bind(workspace)
    .bind(table)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    Ok(row.get(0))
}

async fn load_changes(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    since: i64,
) -> Result<Vec<ChangeDto>, StoreError> {
    let created = ts("created_at");
    let fetch_limit = PAGE + 1;
    let sql = format!(
        "SELECT seq, event_type, topic, COALESCE(entity_id, record_id)::text, record_id::text,
                batch_id::text, base_revision, client_revision, record_revision, fields::text,
                actor_source, actor_id::text, task_id::text, actor_label, {created}
         FROM generic_change
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND seq > $3
         ORDER BY seq
         LIMIT {fetch_limit}"
    );
    let rows = sqlx::query(&sql)
        .bind(workspace)
        .bind(table)
        .bind(since)
        .fetch_all(&mut **tx)
        .await
        .map_err(failed)?;
    let mut changes = Vec::with_capacity(rows.len());
    for row in rows {
        let raw: String = row.get(9);
        let fields = serde_json::from_str(&raw).map_err(|_| StoreError::Failed)?;
        let source: String = row.get(10);
        let id: String = row.get(11);
        let task_id: Option<String> = row.get(12);
        let label: String = row.get(13);
        let record_id: Option<String> = row.get(4);
        changes.push(ChangeDto {
            seq: row.get(0),
            event_type: row.get(1),
            topic: row.get(2),
            entity_id: row.get(3),
            record_id: record_id.unwrap_or_default(),
            batch_id: row.get(5),
            base_revision: row.get(6),
            client_revision: row.get(7),
            record_revision: row.get(8),
            fields,
            actor: actor_dto(&source, &id, task_id, &label),
            created_at: row.get(14),
        });
    }
    Ok(trim_split_batch_page(changes, PAGE as usize))
}

fn trim_split_batch_page(mut changes: Vec<ChangeDto>, page_limit: usize) -> Vec<ChangeDto> {
    if changes.len() <= page_limit {
        return changes;
    }
    let next_batch_id = changes[page_limit].batch_id.clone();
    changes.truncate(page_limit);
    if let Some(split_batch_id) = next_batch_id
        && changes
            .last()
            .and_then(|c| c.batch_id.as_deref())
            .is_some_and(|id| id == split_batch_id)
    {
        let keep_len = changes
            .iter()
            .rposition(|c| c.batch_id.as_deref() != Some(split_batch_id.as_str()))
            .map(|idx| idx + 1)
            .unwrap_or(0);
        if keep_len > 0 {
            changes.truncate(keep_len);
        }
    }
    changes
}

pub async fn get_presence(
    pool: &PgPool,
    workspace: &str,
    table: &str,
) -> Result<Snapshot, StoreError> {
    let mut tx = begin(pool).await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
        .execute(&mut *tx)
        .await
        .map_err(failed)?;
    ensure_table(&mut tx, workspace, table).await?;
    let latest = latest_seq(&mut tx, workspace, table).await?;
    let presence = load_presence(&mut tx, workspace, table).await?;
    commit(tx).await?;
    Ok(Snapshot {
        latest,
        next_since: latest,
        changes: Vec::new(),
        presence,
    })
}

pub async fn touch_presence(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    actor: &Actor,
    record_id: Option<&str>,
    field_id: Option<&str>,
) -> Result<Snapshot, StoreError> {
    if field_id.is_some() && record_id.is_none() {
        return Err(StoreError::Invalid("字段位置需要一条记录"));
    }
    let mut tx = begin(pool).await?;
    ensure_table(&mut tx, workspace, table).await?;
    if let Some(record) = record_id {
        let found = sqlx::query(
            "SELECT 1 FROM generic_record WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid",
        )
        .bind(record)
        .bind(table)
        .bind(workspace)
        .fetch_optional(&mut *tx)
        .await
        .map_err(failed)?;
        if found.is_none() {
            return Err(StoreError::NotFound("找不到这条记录"));
        }
    }
    if let Some(field) = field_id {
        let found = sqlx::query(
            "SELECT 1 FROM generic_field WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid",
        )
        .bind(field)
        .bind(table)
        .bind(workspace)
        .fetch_optional(&mut *tx)
        .await
        .map_err(failed)?;
        if found.is_none() {
            return Err(StoreError::NotFound("找不到这个字段"));
        }
    }
    sqlx::query(
        "INSERT INTO generic_presence (
            workspace_id, table_id, actor_id, actor_source, task_id, actor_label, record_id, field_id, lease_until
         ) VALUES (
            $1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6, $7::uuid, $8::uuid, now() + interval '8 seconds'
         )
         ON CONFLICT (workspace_id, table_id, actor_id)
         DO UPDATE SET actor_source = EXCLUDED.actor_source, task_id = EXCLUDED.task_id,
                       actor_label = EXCLUDED.actor_label, record_id = EXCLUDED.record_id,
                       field_id = EXCLUDED.field_id, lease_until = EXCLUDED.lease_until, updated_at = now()",
    )
    .bind(workspace)
    .bind(table)
    .bind(&actor.id)
    .bind(&actor.source)
    .bind(&actor.task_id)
    .bind(&actor.label)
    .bind(record_id)
    .bind(field_id)
    .execute(&mut *tx)
    .await
    .map_err(failed)?;
    let latest = latest_seq(&mut tx, workspace, table).await?;
    let presence = load_presence(&mut tx, workspace, table).await?;
    commit(tx).await?;
    Ok(Snapshot {
        latest,
        next_since: latest,
        changes: Vec::new(),
        presence,
    })
}

async fn load_presence(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
) -> Result<Vec<PresenceDto>, StoreError> {
    let rows = sqlx::query(
        "SELECT actor_source, actor_id::text, task_id::text, actor_label, record_id::text, field_id::text
         FROM generic_presence
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND lease_until > now()
         ORDER BY actor_label, actor_id",
    )
    .bind(workspace)
    .bind(table)
    .fetch_all(&mut **tx)
    .await
    .map_err(failed)?;
    Ok(rows
        .iter()
        .map(|row| {
            let source: String = row.get(0);
            let id: String = row.get(1);
            let task_id: Option<String> = row.get(2);
            let label: String = row.get(3);
            PresenceDto {
                actor: actor_dto(&source, &id, task_id, &label),
                record_id: row.get(4),
                field_id: row.get(5),
                online: true,
            }
        })
        .collect())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fields(names: &[&str]) -> BTreeSet<String> {
        names.iter().map(|name| (*name).to_owned()).collect()
    }

    #[test]
    fn same_revision_applies_without_history() {
        let patch = fields(&["a"]);
        assert_eq!(decide(4, 4, &[], &patch), Decision::Apply { base: 4 });
    }

    #[test]
    fn disjoint_replay_applies_when_the_chain_is_complete() {
        let steps = vec![(1, 2, fields(&["a"]))];
        assert_eq!(
            decide(2, 1, &steps, &fields(&["b"])),
            Decision::Apply { base: 2 }
        );
    }

    #[test]
    fn same_field_or_a_gap_does_not_guess() {
        let overlap = vec![(1, 2, fields(&["a"]))];
        assert_eq!(decide(2, 1, &overlap, &fields(&["a"])), Decision::Conflict);
        assert_eq!(decide(3, 1, &overlap, &fields(&["b"])), Decision::Conflict);
        assert_eq!(decide(2, 1, &[], &fields(&["b"])), Decision::Conflict);
    }

    #[test]
    fn digest_str_matches_32_hex_char_constraint() {
        let d = digest_str("record.deleted:11111111-1111-4111-8111-111111111111:2");
        assert_eq!(d.len(), 32);
        assert!(d.chars().all(|ch| ch.is_ascii_hexdigit()));
    }

    #[test]
    fn trim_split_batch_page_keeps_batch_atomic_across_page_boundary() {
        let make = |seq: i64, batch_id: Option<&str>| ChangeDto {
            seq,
            event_type: "record.created".to_owned(),
            topic: "record".to_owned(),
            entity_id: "11111111-1111-4111-8111-111111111111".to_owned(),
            record_id: "11111111-1111-4111-8111-111111111111".to_owned(),
            batch_id: batch_id.map(str::to_owned),
            base_revision: 0,
            client_revision: 0,
            record_revision: 1,
            fields: BTreeMap::new(),
            actor: actor_dto("member", "22222222-2222-4222-8222-222222222222", None, "M"),
            created_at: "2026-11-26T00:00:00Z".to_owned(),
        };
        let rows = vec![
            make(1, None),
            make(2, Some("batch-a")),
            make(3, Some("batch-a")),
            make(4, Some("batch-b")),
            make(5, Some("batch-b")),
            make(6, Some("batch-b")),
        ];
        // Page size 5 splits batch-b across seq 5 and seq 6 -> trims back to seq 3.
        let page = trim_split_batch_page(rows, 5);
        let seqs: Vec<i64> = page.into_iter().map(|c| c.seq).collect();
        assert_eq!(seqs, vec![1, 2, 3]);
    }
}
