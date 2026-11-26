use std::collections::BTreeSet;

use serde_json::{Value, value::RawValue};
use sqlx::{Error as SqlxError, PgConnection};

use super::is_uuid;

const MAX_RECORD_RELATION_VALUES: usize = 50;

pub(super) async fn is_active_status_value(
    conn: &mut PgConnection,
    workspace_id: &str,
    value: &RawValue,
) -> Result<bool, SqlxError> {
    let Ok(key) = serde_json::from_str::<String>(value.get()) else {
        return Ok(false);
    };
    sqlx::query("SELECT pg_advisory_xact_lock_shared(hashtextextended($1::uuid::text || ':issue_status', 0))")
        .bind(workspace_id)
        .execute(&mut *conn)
        .await?;
    sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM issue_status
         WHERE workspace_id = $1::uuid AND key = $2 AND archived_at IS NULL)",
    )
    .bind(workspace_id)
    .bind(key)
    .fetch_one(conn)
    .await
}

#[derive(Debug)]
pub(super) enum RecordRelationError {
    Invalid(&'static str),
    Database(SqlxError),
}

#[derive(Debug, PartialEq, Eq)]
enum RecordRelationTarget {
    Table(String),
    Issues,
}

fn parse_record_relation_target(
    config_text: &str,
) -> Result<RecordRelationTarget, RecordRelationError> {
    let config: Value = serde_json::from_str(config_text)
        .map_err(|_| RecordRelationError::Invalid("invalid record_relation config"))?;
    let Some(object) = config.as_object() else {
        return Err(RecordRelationError::Invalid(
            "invalid record_relation config",
        ));
    };
    if object
        .keys()
        .any(|key| key != "target_table_id" && key != "target_kind")
    {
        return Err(RecordRelationError::Invalid(
            "invalid record_relation config",
        ));
    }
    let target_table_id = match object.get("target_table_id") {
        None => None,
        Some(Value::String(id)) if !id.trim().is_empty() => Some(id.trim()),
        Some(_) => {
            return Err(RecordRelationError::Invalid(
                "invalid record_relation config",
            ));
        }
    };
    let target_kind = match object.get("target_kind") {
        None => None,
        Some(Value::String(kind)) if !kind.trim().is_empty() => Some(kind.trim()),
        Some(_) => {
            return Err(RecordRelationError::Invalid(
                "invalid record_relation config",
            ));
        }
    };
    match (target_table_id, target_kind) {
        (Some(table_id), None) if is_uuid(table_id) => {
            Ok(RecordRelationTarget::Table(table_id.to_owned()))
        }
        (None, Some("issues")) => Ok(RecordRelationTarget::Issues),
        _ => Err(RecordRelationError::Invalid(
            "invalid record_relation config",
        )),
    }
}

pub(super) async fn validate_record_relation_value(
    conn: &mut PgConnection,
    workspace_id: &str,
    config_text: &str,
    value: &RawValue,
    source_issue_id: Option<&str>,
) -> Result<(), RecordRelationError> {
    let target = parse_record_relation_target(config_text)?;
    let record_ids: Vec<String> = serde_json::from_str(value.get()).map_err(|_| {
        RecordRelationError::Invalid("value must be a non-empty array of record id strings")
    })?;
    if record_ids.is_empty() || record_ids.len() > MAX_RECORD_RELATION_VALUES {
        return Err(RecordRelationError::Invalid(
            "value must be a non-empty array of record id strings",
        ));
    }
    let mut seen = BTreeSet::new();
    if record_ids.iter().any(|id| {
        !is_uuid(id) || id.bytes().any(|byte| byte.is_ascii_uppercase()) || !seen.insert(id)
    }) {
        return Err(RecordRelationError::Invalid(
            "each record id must be a canonical lowercase UUID without duplicates",
        ));
    }

    match target {
        RecordRelationTarget::Table(target_table_id) => {
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
            .bind(&target_table_id)
            .bind(&record_ids)
            .fetch_one(&mut *conn)
            .await
            .map_err(RecordRelationError::Database)?;
            if count as usize != record_ids.len() {
                return Err(RecordRelationError::Invalid(
                    "one or more record ids are invalid for the configured target table",
                ));
            }
        }
        RecordRelationTarget::Issues => {
            if let Some(source_id) = source_issue_id {
                if record_ids
                    .iter()
                    .any(|id| id.eq_ignore_ascii_case(source_id))
                {
                    return Err(RecordRelationError::Invalid(
                        "an issue cannot reference itself",
                    ));
                }
            }
            let count: i64 = sqlx::query_scalar(
                "SELECT COUNT(*) FROM (
                    SELECT i.id FROM issue i
                    LEFT JOIN project p ON p.id = i.project_id AND p.workspace_id = i.workspace_id
                    WHERE i.workspace_id = $1::uuid
                      AND i.id = ANY($2::text[]::uuid[])
                      AND (i.project_id IS NULL OR (p.id IS NOT NULL AND p.visibility = 'workspace'))
                    ORDER BY i.id FOR KEY SHARE OF i
                 ) linked",
            )
            .bind(workspace_id)
            .bind(&record_ids)
            .fetch_one(&mut *conn)
            .await
            .map_err(RecordRelationError::Database)?;
            if count as usize != record_ids.len() {
                return Err(RecordRelationError::Invalid(
                    "one or more issue ids are invalid or not workspace-visible",
                ));
            }
        }
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_record_relation_target_accepts_table_or_issues_exclusively() {
        let table_id = "11111111-1111-4111-8111-111111111111";
        assert_eq!(
            parse_record_relation_target(&format!(r#"{{"target_table_id":"{table_id}"}}"#))
                .unwrap(),
            RecordRelationTarget::Table(table_id.to_owned())
        );
        assert_eq!(
            parse_record_relation_target(r#"{"target_kind":"issues"}"#).unwrap(),
            RecordRelationTarget::Issues
        );
        assert!(
            parse_record_relation_target(&format!(
                r#"{{"target_table_id":"{table_id}","target_kind":"issues"}}"#
            ))
            .is_err()
        );
        assert!(parse_record_relation_target(r#"{"target_kind":"projects"}"#).is_err());
        assert!(parse_record_relation_target(r#"{}"#).is_err());
        assert!(parse_record_relation_target(r#"{"target_kind":"issues","extra":1}"#).is_err());
    }
}
