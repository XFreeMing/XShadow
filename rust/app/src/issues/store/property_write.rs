use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgPool, Row};

use super::{CallerActor, IssueReadError, ordinary::set_write_actor, resolve_caller_access_on};

#[derive(Debug, Deserialize)]
pub struct IssuePropertyWrite {
    source_task_id: Option<String>,
    operation: IssuePropertyOperation,
}

impl IssuePropertyWrite {
    pub fn is_unset(&self) -> bool {
        matches!(self.operation, IssuePropertyOperation::Unset)
    }
}

// The external tag lets serde_json pass RawValue through untouched.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "snake_case")]
enum IssuePropertyOperation {
    Set {
        property_type: String,
        config: Box<RawValue>,
        value: Box<RawValue>,
    },
    Unset,
}

#[derive(Debug, Serialize)]
pub struct IssuePropertyWriteResult {
    pub properties: Box<RawValue>,
    pub issue_revision: i64,
}

#[derive(Debug)]
pub enum IssuePropertyWriteError {
    Access(IssueReadError),
    PropertyNotFound,
    Archived(String),
    Required(String),
    DefinitionChanged,
    SizeLimit,
    Database(sqlx::Error),
    Json(serde_json::Error),
}

impl From<sqlx::Error> for IssuePropertyWriteError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssuePropertyWriteError {
    fn from(error: serde_json::Error) -> Self {
        Self::Json(error)
    }
}

#[derive(Deserialize)]
struct IssueTypeField {
    property_id: Option<String>,
    required: Option<bool>,
}

fn is_properties_size_violation(error: &sqlx::Error) -> bool {
    match error {
        sqlx::Error::Database(db) => db.code().as_deref() == Some("23514"),
        _ => false,
    }
}

async fn definitions_match(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    stored_config: &str,
    property_type: &str,
    stored_type: &str,
    supplied_config: &RawValue,
) -> Result<bool, sqlx::Error> {
    if stored_type != property_type {
        return Ok(false);
    }
    sqlx::query_scalar::<_, bool>("SELECT $1::jsonb IS NOT DISTINCT FROM $2::jsonb")
        .bind(stored_config)
        .bind(supplied_config.get())
        .fetch_one(&mut **tx)
        .await
}

pub async fn write_issue_property(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    property_id: &str,
    request: IssuePropertyWrite,
) -> Result<IssuePropertyWriteResult, IssuePropertyWriteError> {
    let mut tx = pool.begin().await?;
    let lock_key = format!("prop:{property_id}");
    sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))")
        .bind(&lock_key)
        .execute(&mut *tx)
        .await?;

    let locked = sqlx::query(
        "SELECT project_id::text, issue_type_id::text FROM issue
         WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssuePropertyWriteError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssuePropertyWriteError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssuePropertyWriteError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }

    let definition = sqlx::query(
        "SELECT name, type, config::text, archived_at IS NOT NULL
         FROM issue_property WHERE id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(property_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssuePropertyWriteError::PropertyNotFound)?;

    let value = match request.operation {
        IssuePropertyOperation::Set {
            property_type,
            config,
            value,
        } => {
            if definition.try_get::<bool, _>(3)? {
                return Err(IssuePropertyWriteError::Archived(definition.try_get(0)?));
            }
            if !definitions_match(
                &mut tx,
                definition.try_get(2)?,
                &property_type,
                definition.try_get(1)?,
                &config,
            )
            .await?
            {
                return Err(IssuePropertyWriteError::DefinitionChanged);
            }
            Some(value)
        }
        IssuePropertyOperation::Unset => {
            if let Some(issue_type_id) = locked.try_get::<Option<&str>, _>(1)?
                && let Some((name, fields)) = sqlx::query_as::<_, (String, String)>(
                    "SELECT name, fields::text FROM issue_type
                     WHERE id = $1::uuid AND workspace_id = $2::uuid",
                )
                .bind(issue_type_id)
                .bind(workspace_id)
                .fetch_optional(&mut *tx)
                .await?
                && required_for_type(&fields, property_id)
            {
                return Err(IssuePropertyWriteError::Required(name));
            }
            None
        }
    };

    set_write_actor(&mut tx, actor, request.source_task_id.as_deref()).await?;
    let (properties, issue_revision) = sqlx::query_as::<_, (String, i64)>(
        "WITH next AS (
            SELECT CASE WHEN $4::jsonb IS NULL THEN properties - $3::text
                        ELSE jsonb_set(properties, ARRAY[$3::text], $4::jsonb, true) END AS properties
            FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid
         )
         UPDATE issue i SET
             properties = next.properties,
             revision = i.revision + (i.properties IS DISTINCT FROM next.properties)::int,
             last_activity_at = CASE WHEN i.properties IS DISTINCT FROM next.properties
                 THEN GREATEST(COALESCE(i.last_activity_at, i.updated_at), now()) ELSE i.last_activity_at END,
             updated_at = CASE WHEN i.properties IS DISTINCT FROM next.properties THEN now() ELSE i.updated_at END
         FROM next WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
         RETURNING i.properties::text, i.revision",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .bind(property_id)
    .bind(value.as_deref().map(RawValue::get))
    .fetch_one(&mut *tx)
    .await
    .map_err(|error| {
        if value.is_some() && is_properties_size_violation(&error) {
            IssuePropertyWriteError::SizeLimit
        } else {
            IssuePropertyWriteError::Database(error)
        }
    })?;
    let result = IssuePropertyWriteResult {
        properties: RawValue::from_string(properties)?,
        issue_revision,
    };
    tx.commit().await?;
    Ok(result)
}

// Go treats missing, null and malformed legacy type definitions as having no
// required fields. The whole array must decode before applying requiredness.
fn required_for_type(fields: &str, id: &str) -> bool {
    serde_json::from_str::<Option<Vec<Option<IssueTypeField>>>>(fields)
        .ok()
        .flatten()
        .is_some_and(|fields| {
            fields.into_iter().flatten().any(|field| {
                field.required == Some(true) && field.property_id.as_deref() == Some(id)
            })
        })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wire_preserves_numeric_lexical_value() {
        let raw = r#"{
            "source_task_id":"11111111-1111-4111-8111-111111111111",
            "operation":{"set":{"property_type":"number","config":{},"value":1.25e2}}
        }"#;
        let request: IssuePropertyWrite = serde_json::from_str(raw).unwrap();
        assert!(!request.is_unset());
        let IssuePropertyOperation::Set { config, value, .. } = request.operation else {
            panic!("expected set operation");
        };
        assert_eq!(value.get(), "1.25e2");
        assert_eq!(config.get(), "{}");
        assert!(
            serde_json::from_str::<IssuePropertyWrite>(r#"{"operation":"unset"}"#)
                .unwrap()
                .is_unset()
        );
    }
}
