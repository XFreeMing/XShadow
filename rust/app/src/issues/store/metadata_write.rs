use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgPool, Row};

use super::{CallerActor, IssueReadError, ordinary::set_write_actor, resolve_caller_access_on};

const MAX_ISSUE_METADATA_KEYS: i64 = 50;

#[derive(Debug, Deserialize)]
pub struct IssueMetadataWrite {
    source_task_id: Option<String>,
    #[serde(default)]
    value: Option<Box<RawValue>>,
}

impl IssueMetadataWrite {
    pub fn is_delete(&self) -> bool {
        self.value.is_none()
    }
}

#[derive(Debug, Serialize)]
pub struct IssueMetadataWriteResult {
    pub metadata: Box<RawValue>,
    pub issue_revision: i64,
    pub changed: bool,
}

#[derive(Debug)]
pub enum IssueMetadataWriteError {
    Access(IssueReadError),
    KeyLimit,
    SizeLimit,
    Database(sqlx::Error),
    Json(serde_json::Error),
}

impl From<sqlx::Error> for IssueMetadataWriteError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssueMetadataWriteError {
    fn from(error: serde_json::Error) -> Self {
        Self::Json(error)
    }
}

fn is_metadata_size_violation(error: &sqlx::Error) -> bool {
    match error {
        sqlx::Error::Database(db) => db.code().as_deref() == Some("23514"),
        _ => false,
    }
}

pub async fn write_issue_metadata(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    key: &str,
    request: IssueMetadataWrite,
) -> Result<IssueMetadataWriteResult, IssueMetadataWriteError> {
    let mut tx = pool.begin().await?;
    let locked = sqlx::query(
        "SELECT project_id::text, metadata::text, revision,
                (metadata ? $3::text) AS has_key,
                (SELECT count(*)::bigint FROM jsonb_object_keys(metadata)) AS key_count
         FROM issue
         WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .bind(key)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueMetadataWriteError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueMetadataWriteError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssueMetadataWriteError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    let old_metadata: String = locked.try_get(1)?;
    let old_revision: i64 = locked.try_get(2)?;
    if !request.is_delete()
        && !locked.try_get::<bool, _>(3)?
        && locked.try_get::<i64, _>(4)? >= MAX_ISSUE_METADATA_KEYS
    {
        return Err(IssueMetadataWriteError::KeyLimit);
    }

    set_write_actor(&mut tx, actor, request.source_task_id.as_deref()).await?;
    let row = sqlx::query(
        "WITH next AS (
            SELECT CASE WHEN $4::jsonb IS NULL THEN metadata - $3::text
                        ELSE jsonb_set(metadata, ARRAY[$3::text], $4::jsonb, true) END AS metadata
            FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid
         )
         UPDATE issue i SET
             metadata = next.metadata,
             revision = i.revision + 1,
             last_activity_at = GREATEST(COALESCE(i.last_activity_at, i.updated_at), now()),
             updated_at = now()
         FROM next
         WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
           AND i.metadata IS DISTINCT FROM next.metadata
         RETURNING i.metadata::text, i.revision",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .bind(key)
    .bind(request.value.as_deref().map(RawValue::get))
    .fetch_optional(&mut *tx)
    .await
    .map_err(|error| {
        if !request.is_delete() && is_metadata_size_violation(&error) {
            IssueMetadataWriteError::SizeLimit
        } else {
            IssueMetadataWriteError::Database(error)
        }
    })?;
    let result = match row {
        None => IssueMetadataWriteResult {
            metadata: RawValue::from_string(old_metadata)?,
            issue_revision: old_revision,
            changed: false,
        },
        Some(row) => IssueMetadataWriteResult {
            metadata: RawValue::from_string(row.try_get(0)?)?,
            issue_revision: row.try_get(1)?,
            changed: true,
        },
    };
    tx.commit().await?;
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wire_preserves_numeric_lexical_value() {
        let raw = r#"{
            "source_task_id":"11111111-1111-4111-8111-111111111111",
            "value":1.25e2
        }"#;
        let request: IssueMetadataWrite = serde_json::from_str(raw).unwrap();
        assert!(!request.is_delete());
        assert_eq!(request.value.as_ref().unwrap().get(), "1.25e2");
        assert!(
            serde_json::from_str::<IssueMetadataWrite>("{}")
                .unwrap()
                .is_delete()
        );
    }
}
