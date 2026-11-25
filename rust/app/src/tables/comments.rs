use serde::Serialize;
use sqlx::{PgPool, Postgres, Row, Transaction};

use super::collab::{Actor, ActorDto, actor_dto};
use super::store::{StoreError, begin, commit, failed, lock_record, ts};

const DEFAULT_LIMIT: i64 = 50;
const MAX_LIMIT: i64 = 100;
const MAX_BODY_CHARS: usize = 4000;

#[derive(Clone, Serialize)]
pub struct CommentDto {
    pub id: String,
    pub record_id: String,
    pub parent_id: Option<String>,
    pub seq: i64,
    pub body: String,
    pub actor: ActorDto,
    pub created_at: String,
}

#[derive(Serialize)]
pub struct CommentListDto {
    pub comments: Vec<CommentDto>,
    pub has_more: bool,
    pub next_cursor: Option<i64>,
}

pub fn clean_body(raw: &str) -> Result<String, &'static str> {
    let trimmed = raw.trim();
    let len = trimmed.chars().count();
    if len == 0 {
        return Err("评论内容不能为空");
    }
    if len > MAX_BODY_CHARS {
        return Err("评论内容不能超过 4000 个字符");
    }
    Ok(trimmed.to_owned())
}

pub fn normalize_list_params(
    after: Option<i64>,
    limit: Option<i64>,
) -> Result<(i64, i64), &'static str> {
    let after = after.unwrap_or(0);
    if after < 0 {
        return Err("评论游标无效");
    }
    let limit = limit.unwrap_or(DEFAULT_LIMIT);
    if !(1..=MAX_LIMIT).contains(&limit) {
        return Err("评论分页数量需要在 1 到 100 之间");
    }
    Ok((after, limit))
}

pub async fn list_comments(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    record: &str,
    after: Option<i64>,
    limit: Option<i64>,
) -> Result<CommentListDto, StoreError> {
    let (after, limit) = normalize_list_params(after, limit).map_err(StoreError::Invalid)?;
    let mut tx = begin(pool).await?;
    ensure_table(&mut tx, workspace, table).await?;
    ensure_record(&mut tx, workspace, table, record).await?;
    let created = ts("created_at");
    let sql = format!(
        "SELECT id::text, record_id::text, parent_id::text, seq, body,
                actor_source, actor_id::text, task_id::text, actor_label, {created}
         FROM generic_record_comment
         WHERE workspace_id = $1::uuid
           AND table_id = $2::uuid
           AND record_id = $3::uuid
           AND seq > $4
         ORDER BY seq ASC
         LIMIT $5"
    );
    let rows = sqlx::query(&sql)
        .bind(workspace)
        .bind(table)
        .bind(record)
        .bind(after)
        .bind(limit + 1)
        .fetch_all(&mut *tx)
        .await
        .map_err(failed)?;
    let has_more = rows.len() as i64 > limit;
    let take_len = if has_more { limit as usize } else { rows.len() };
    let comments: Vec<CommentDto> = rows[..take_len].iter().map(comment_from_row).collect();
    let next_cursor = if has_more {
        comments.last().map(|item| item.seq)
    } else {
        None
    };
    commit(tx).await?;
    Ok(CommentListDto {
        comments,
        has_more,
        next_cursor,
    })
}

pub async fn create_comment(
    pool: &PgPool,
    workspace: &str,
    table: &str,
    record: &str,
    actor: &Actor,
    body: &str,
    parent_id: Option<&str>,
) -> Result<CommentDto, StoreError> {
    let body = clean_body(body).map_err(StoreError::Invalid)?;
    let mut tx = begin(pool).await?;
    ensure_table(&mut tx, workspace, table).await?;
    lock_record(&mut tx, workspace, table, record).await?;
    if let Some(parent) = parent_id {
        ensure_parent_comment(&mut tx, workspace, table, record, parent).await?;
    }
    let seq = next_seq(&mut tx, workspace, table, record).await?;
    let created = ts("created_at");
    let sql = format!(
        "INSERT INTO generic_record_comment (
            workspace_id, table_id, record_id, parent_id, seq, body,
            actor_source, actor_id, task_id, actor_label
         ) VALUES (
            $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8::uuid, $9::uuid, $10
         )
         RETURNING id::text, record_id::text, parent_id::text, seq, body,
                   actor_source, actor_id::text, task_id::text, actor_label, {created}"
    );
    let row = sqlx::query(&sql)
        .bind(workspace)
        .bind(table)
        .bind(record)
        .bind(parent_id)
        .bind(seq)
        .bind(&body)
        .bind(&actor.source)
        .bind(&actor.id)
        .bind(&actor.task_id)
        .bind(&actor.label)
        .fetch_one(&mut *tx)
        .await
        .map_err(failed)?;
    let comment = comment_from_row(&row);
    commit(tx).await?;
    Ok(comment)
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

async fn ensure_record(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    record: &str,
) -> Result<(), StoreError> {
    let found = sqlx::query(
        "SELECT 1 FROM generic_record WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid",
    )
    .bind(record)
    .bind(table)
    .bind(workspace)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?;
    if found.is_none() {
        return Err(StoreError::NotFound("找不到这条记录"));
    }
    Ok(())
}

async fn ensure_parent_comment(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    record: &str,
    parent: &str,
) -> Result<(), StoreError> {
    let found = sqlx::query(
        "SELECT 1
         FROM generic_record_comment
         WHERE id = $1::uuid
           AND workspace_id = $2::uuid
           AND table_id = $3::uuid
           AND record_id = $4::uuid",
    )
    .bind(parent)
    .bind(workspace)
    .bind(table)
    .bind(record)
    .fetch_optional(&mut **tx)
    .await
    .map_err(failed)?;
    if found.is_none() {
        return Err(StoreError::Invalid("回复的父评论不存在或不属于当前记录"));
    }
    Ok(())
}

async fn next_seq(
    tx: &mut Transaction<'_, Postgres>,
    workspace: &str,
    table: &str,
    record: &str,
) -> Result<i64, StoreError> {
    let row = sqlx::query(
        "SELECT COALESCE(MAX(seq), 0) + 1
         FROM generic_record_comment
         WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid",
    )
    .bind(workspace)
    .bind(table)
    .bind(record)
    .fetch_one(&mut **tx)
    .await
    .map_err(failed)?;
    Ok(row.get(0))
}

fn comment_from_row(row: &sqlx::postgres::PgRow) -> CommentDto {
    let source: String = row.get(5);
    let actor_id: String = row.get(6);
    let task_id: Option<String> = row.get(7);
    let label: String = row.get(8);
    CommentDto {
        id: row.get(0),
        record_id: row.get(1),
        parent_id: row.get(2),
        seq: row.get(3),
        body: row.get(4),
        actor: actor_dto(&source, &actor_id, task_id, &label),
        created_at: row.get(9),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn clean_body_enforces_nonempty_and_length_bound() {
        assert!(clean_body("").is_err());
        assert!(clean_body("   \n\t ").is_err());
        assert_eq!(clean_body("  你好\n世界  ").unwrap(), "你好\n世界");
        let max = "a".repeat(MAX_BODY_CHARS);
        assert_eq!(clean_body(&max).unwrap().len(), MAX_BODY_CHARS);
        let too_long = "a".repeat(MAX_BODY_CHARS + 1);
        assert!(clean_body(&too_long).is_err());
    }

    #[test]
    fn list_params_enforce_nonnegative_cursor_and_bounded_limit() {
        assert_eq!(
            normalize_list_params(None, None).unwrap(),
            (0, DEFAULT_LIMIT)
        );
        assert_eq!(
            normalize_list_params(Some(10), Some(100)).unwrap(),
            (10, 100)
        );
        assert!(normalize_list_params(Some(-1), None).is_err());
        assert!(normalize_list_params(None, Some(0)).is_err());
        assert!(normalize_list_params(None, Some(101)).is_err());
    }
}
