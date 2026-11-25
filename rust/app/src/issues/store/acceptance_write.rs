//! Native transactional persistence for `UpdateIssueAcceptanceTrace`. Go validates
//! the public change and supplies the encoded trace; this module mirrors the sqlc
//! mutation under a task row lock and project visibility recheck.

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgPool, Row};

use super::{
    CallerActor, IssueReadError,
    ordinary::{self, ISSUE_ROW_SNAPSHOT},
    resolve_caller_access_on,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueAcceptanceWrite {
    issue_revision: i64,
    source_task_id: Option<String>,
    trace: Box<RawValue>,
}

#[derive(Debug, Serialize)]
pub struct IssueAcceptanceWriteResult {
    pub after: Box<RawValue>,
}

#[derive(Debug)]
pub enum IssueAcceptanceWriteError {
    Access(IssueReadError),
    RevisionConflict,
    Database(sqlx::Error),
    Snapshot(serde_json::Error),
    InvalidCommitProof,
}

impl From<sqlx::Error> for IssueAcceptanceWriteError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssueAcceptanceWriteError {
    fn from(error: serde_json::Error) -> Self {
        Self::Snapshot(error)
    }
}

fn validate_after_snapshot(
    after_text: String,
    issue_id: &str,
    workspace_id: &str,
    issue_revision: i64,
    trace: &RawValue,
) -> Result<Box<RawValue>, IssueAcceptanceWriteError> {
    let snapshot: serde_json::Value = serde_json::from_str(&after_text)?;
    let saved_id = snapshot
        .get("id")
        .and_then(serde_json::Value::as_str)
        .ok_or(IssueAcceptanceWriteError::InvalidCommitProof)?;
    let saved_ws = snapshot
        .get("workspace_id")
        .and_then(serde_json::Value::as_str)
        .ok_or(IssueAcceptanceWriteError::InvalidCommitProof)?;
    if saved_id != issue_id || saved_ws != workspace_id {
        return Err(IssueAcceptanceWriteError::InvalidCommitProof);
    }
    let saved_revision = snapshot
        .get("revision")
        .and_then(serde_json::Value::as_i64)
        .ok_or(IssueAcceptanceWriteError::InvalidCommitProof)?;
    if Some(saved_revision) != issue_revision.checked_add(1) {
        return Err(IssueAcceptanceWriteError::InvalidCommitProof);
    }
    let saved_trace = snapshot
        .get("acceptance_criteria")
        .ok_or(IssueAcceptanceWriteError::InvalidCommitProof)?;
    let expected: serde_json::Value = serde_json::from_str(trace.get())?;
    if saved_trace != &expected {
        return Err(IssueAcceptanceWriteError::InvalidCommitProof);
    }
    RawValue::from_string(after_text).map_err(IssueAcceptanceWriteError::Snapshot)
}

pub async fn write_issue_acceptance_trace(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    request: IssueAcceptanceWrite,
) -> Result<IssueAcceptanceWriteResult, IssueAcceptanceWriteError> {
    let mut tx = pool.begin().await?;
    let locked = sqlx::query(
        "SELECT project_id::text, revision FROM issue
         WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueAcceptanceWriteError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueAcceptanceWriteError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssueAcceptanceWriteError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    let revision: i64 = locked.try_get(1)?;
    if request.issue_revision != revision {
        return Err(IssueAcceptanceWriteError::RevisionConflict);
    }

    ordinary::set_write_actor(&mut tx, actor, request.source_task_id.as_deref()).await?;

    let saved = sqlx::query(&format!(
        "UPDATE issue i
            SET acceptance_criteria = $3::jsonb,
                revision = i.revision + 1,
                updated_at = now(),
                last_activity_at = GREATEST(COALESCE(i.last_activity_at, i.updated_at), now())
          WHERE i.id = $1::uuid
            AND i.workspace_id = $2::uuid
            AND i.revision = $4
          RETURNING {ISSUE_ROW_SNAPSHOT}"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .bind(request.trace.get())
    .bind(request.issue_revision)
    .fetch_optional(&mut *tx)
    .await?;
    let after_text = match saved {
        Some(row) => row.try_get::<String, _>(0)?,
        None => return Err(IssueAcceptanceWriteError::RevisionConflict),
    };
    let after = validate_after_snapshot(
        after_text,
        issue_id,
        workspace_id,
        request.issue_revision,
        &request.trace,
    )?;
    tx.commit().await?;
    Ok(IssueAcceptanceWriteResult { after })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wire_accepts_trace_and_issue_revision() {
        let request: IssueAcceptanceWrite = serde_json::from_str(
            r#"{
                "issue_revision": 3,
                "source_task_id": "11111111-1111-4111-8111-111111111111",
                "trace": {"revision":4,"criteria":[]}
            }"#,
        )
        .unwrap();
        assert_eq!(request.issue_revision, 3);
        assert_eq!(request.trace.get(), r#"{"revision":4,"criteria":[]}"#);
    }

    #[test]
    fn validate_after_rejects_partial_snapshot() {
        let trace = RawValue::from_string(r#"{"revision":2,"criteria":[{"id":"C1","versions":["v1"],"cases":[],"implementations":[]}]}"#.to_owned()).unwrap();
        let partial = r#"{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","workspace_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}"#;
        let err = validate_after_snapshot(
            partial.to_owned(),
            "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
            4,
            &trace,
        )
        .unwrap_err();
        assert!(matches!(err, IssueAcceptanceWriteError::InvalidCommitProof));
    }
}
