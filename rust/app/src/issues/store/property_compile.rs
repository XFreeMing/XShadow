use serde::{Deserialize, Serialize};
use serde_json::Value;
use sqlx::{PgPool, Row};

use crate::tables::issue_derived::{
    self, PropertyDef, compile_issue_derived_config, verify_lookup_target_field,
};
use crate::tables::store::StoreError;

use super::IssueReadError;

#[derive(Debug, Deserialize)]
pub struct CompileIssuePropertyConfigRequest {
    pub property_id: Option<String>,
    pub kind: String,
    pub config: Value,
}

#[derive(Debug, Serialize)]
pub struct CompileIssuePropertyConfigResponse {
    pub config: Value,
}

pub async fn compile_issue_property_config(
    pool: &PgPool,
    workspace_id: &str,
    request: CompileIssuePropertyConfigRequest,
) -> Result<CompileIssuePropertyConfigResponse, IssueReadError> {
    if request.kind != "lookup" && request.kind != "formula" {
        return Err(IssueReadError::InvalidInput("unsupported derived kind"));
    }
    let mut tx = pool
        .begin()
        .await
        .map_err(|_| IssueReadError::DatabaseFailed)?;
    let rows = sqlx::query(
        "SELECT id::text, name, type, config::text FROM issue_property
         WHERE workspace_id = $1::uuid AND archived_at IS NULL",
    )
    .bind(workspace_id)
    .fetch_all(&mut *tx)
    .await
    .map_err(|_| IssueReadError::DatabaseFailed)?;
    let mut defs = Vec::with_capacity(rows.len());
    for row in rows {
        let config: String = row.get(3);
        let config: Value =
            serde_json::from_str(&config).map_err(|_| IssueReadError::DatabaseFailed)?;
        defs.push(PropertyDef {
            id: row.get(0),
            name: row.get(1),
            kind: row.get(2),
            config,
        });
    }
    let property_id = request
        .property_id
        .unwrap_or_else(|| "00000000-0000-4000-8000-000000000000".to_owned());
    let stored = compile_issue_derived_config(&defs, &property_id, &request.kind, &request.config)
        .map_err(map_store_error)?;
    if request.kind == "lookup" {
        let lookup = issue_derived::IssueLookupConfig::from_stored(&stored)
            .map_err(|_| IssueReadError::InvalidInput("lookup config invalid after compile"))?;
        verify_lookup_target_field(
            &mut tx,
            workspace_id,
            &lookup.relation_property_id,
            &lookup.target_field_id,
        )
        .await
        .map_err(map_store_error)?;
    }
    tx.commit()
        .await
        .map_err(|_| IssueReadError::DatabaseFailed)?;
    Ok(CompileIssuePropertyConfigResponse { config: stored })
}

fn map_store_error(error: StoreError) -> IssueReadError {
    match error {
        StoreError::Invalid(message) => IssueReadError::InvalidInput(message),
        _ => IssueReadError::DatabaseFailed,
    }
}
