//! Member work-method template adoption persistence for the method-adopt pilot.
//! Go validates the public change and supplies the catalog/run plan; this module
//! locks workspace templates, the scoped project, then the issue before applying
//! the planned run.

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgPool, Row};

use super::{
    CallerActor, IssueReadError, is_uuid,
    ordinary::{self, ISSUE_ROW_SNAPSHOT},
    resolve_caller_access_on,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueMethodAdoptWrite {
    issue_revision: i64,
    workspace_templates: Box<RawValue>,
    before_run: Box<RawValue>,
    after_run: Box<RawValue>,
    write_run: bool,
    #[serde(default)]
    expected_project_id: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct IssueMethodAdoptWriteResult {
    pub before: Box<RawValue>,
    pub after: Box<RawValue>,
}

#[derive(Debug)]
pub enum IssueMethodAdoptWriteError {
    Access(IssueReadError),
    CatalogConflict,
    ProjectConflict,
    RunConflict,
    RevisionConflict,
    Database(sqlx::Error),
    Snapshot(serde_json::Error),
    InvalidCommitProof,
}

impl From<sqlx::Error> for IssueMethodAdoptWriteError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssueMethodAdoptWriteError {
    fn from(error: serde_json::Error) -> Self {
        Self::Snapshot(error)
    }
}

fn require_member(actor: &CallerActor) -> Result<(), IssueMethodAdoptWriteError> {
    match actor {
        CallerActor::Member { .. } => Ok(()),
        CallerActor::Agent { .. } => Err(IssueMethodAdoptWriteError::Access(
            IssueReadError::Forbidden("只有成员能主动更换正在执行的方法版本"),
        )),
    }
}

fn validate_snapshots(
    before_text: &str,
    after_text: &str,
    issue_id: &str,
    workspace_id: &str,
    issue_revision: i64,
    write_run: bool,
    after_run: &RawValue,
) -> Result<(Box<RawValue>, Box<RawValue>), IssueMethodAdoptWriteError> {
    let before: serde_json::Value = serde_json::from_str(before_text)?;
    let after: serde_json::Value = serde_json::from_str(after_text)?;
    for snapshot in [&before, &after] {
        let saved_id = snapshot
            .get("id")
            .and_then(serde_json::Value::as_str)
            .ok_or(IssueMethodAdoptWriteError::InvalidCommitProof)?;
        let saved_ws = snapshot
            .get("workspace_id")
            .and_then(serde_json::Value::as_str)
            .ok_or(IssueMethodAdoptWriteError::InvalidCommitProof)?;
        if saved_id != issue_id || saved_ws != workspace_id {
            return Err(IssueMethodAdoptWriteError::InvalidCommitProof);
        }
    }
    let before_revision = before
        .get("revision")
        .and_then(serde_json::Value::as_i64)
        .ok_or(IssueMethodAdoptWriteError::InvalidCommitProof)?;
    let after_revision = after
        .get("revision")
        .and_then(serde_json::Value::as_i64)
        .ok_or(IssueMethodAdoptWriteError::InvalidCommitProof)?;
    if before_revision != issue_revision {
        return Err(IssueMethodAdoptWriteError::InvalidCommitProof);
    }
    let expected_after_revision = if write_run {
        issue_revision + 1
    } else {
        issue_revision
    };
    if after_revision != expected_after_revision {
        return Err(IssueMethodAdoptWriteError::InvalidCommitProof);
    }
    for key in ["status", "assignee_type", "assignee_id", "position"] {
        if before.get(key) != after.get(key) {
            return Err(IssueMethodAdoptWriteError::InvalidCommitProof);
        }
    }
    if before.get("acceptance_criteria") != after.get("acceptance_criteria") {
        return Err(IssueMethodAdoptWriteError::InvalidCommitProof);
    }
    let saved_run = after
        .get("work_method_run")
        .ok_or(IssueMethodAdoptWriteError::InvalidCommitProof)?;
    let expected_run: serde_json::Value = serde_json::from_str(after_run.get())?;
    if saved_run != &expected_run {
        return Err(IssueMethodAdoptWriteError::InvalidCommitProof);
    }
    if write_run {
        if after.get("updated_at").is_none() || after.get("last_activity_at").is_none() {
            return Err(IssueMethodAdoptWriteError::InvalidCommitProof);
        }
    } else if before.get("work_method_run") != Some(saved_run) {
        return Err(IssueMethodAdoptWriteError::InvalidCommitProof);
    }
    Ok((
        RawValue::from_string(before_text.to_owned())?,
        RawValue::from_string(after_text.to_owned())?,
    ))
}

async fn issue_snapshot(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    issue_id: &str,
    workspace_id: &str,
) -> Result<String, sqlx::Error> {
    sqlx::query_scalar(&format!(
        "SELECT {ISSUE_ROW_SNAPSHOT} FROM issue i WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_one(&mut **tx)
    .await
}

pub async fn write_issue_method_adopt(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    request: IssueMethodAdoptWrite,
) -> Result<IssueMethodAdoptWriteResult, IssueMethodAdoptWriteError> {
    require_member(actor)?;
    let expected_project = request.expected_project_id.as_deref();
    if expected_project.is_some_and(|project_id| !is_uuid(project_id)) {
        return Err(IssueMethodAdoptWriteError::ProjectConflict);
    }
    let mut tx = pool.begin().await?;

    let templates_match = sqlx::query_scalar::<_, bool>(
        "SELECT (work_method_templates IS NOT DISTINCT FROM $2::jsonb)
         FROM workspace WHERE id = $1::uuid FOR UPDATE",
    )
    .bind(workspace_id)
    .bind(request.workspace_templates.get())
    .fetch_optional(&mut *tx)
    .await?
    .unwrap_or(false);
    if !templates_match {
        return Err(IssueMethodAdoptWriteError::CatalogConflict);
    }

    if let Some(project_id) = expected_project {
        sqlx::query(
            "SELECT 1 FROM project WHERE id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
        )
        .bind(project_id)
        .bind(workspace_id)
        .fetch_optional(&mut *tx)
        .await?
        .ok_or(IssueMethodAdoptWriteError::ProjectConflict)?;
    }

    let locked = sqlx::query(
        "SELECT project_id::text, revision,
                (work_method_run IS NOT DISTINCT FROM $3::jsonb) AS run_matches
         FROM issue
         WHERE id = $1::uuid AND workspace_id = $2::uuid
         FOR UPDATE",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .bind(request.before_run.get())
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueMethodAdoptWriteError::Access(
        IssueReadError::NotFound("issue not found"),
    ))?;
    let project_id: Option<String> = locked.try_get(0)?;
    let actual_project = project_id.as_deref();
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueMethodAdoptWriteError::Access)?;
    if !hidden.can_see(actual_project) {
        return Err(IssueMethodAdoptWriteError::Access(
            IssueReadError::NotFound("issue not found"),
        ));
    }
    if actual_project != expected_project {
        return Err(IssueMethodAdoptWriteError::ProjectConflict);
    }
    let revision: i64 = locked.try_get(1)?;
    let run_matches: bool = locked.try_get(2)?;
    if request.issue_revision != revision || !run_matches {
        return Err(IssueMethodAdoptWriteError::RunConflict);
    }

    ordinary::set_write_actor(&mut tx, actor, None).await?;

    let before_text = issue_snapshot(&mut tx, issue_id, workspace_id).await?;

    let after_text = if request.write_run {
        sqlx::query_scalar(&format!(
            "UPDATE issue AS i
                SET work_method_run = $3::jsonb,
                    revision = i.revision + 1,
                    updated_at = now(),
                    last_activity_at = GREATEST(COALESCE(i.last_activity_at, i.updated_at), now())
              WHERE i.id = $1::uuid
                AND i.workspace_id = $2::uuid
                AND i.revision = $4
              RETURNING (SELECT {ISSUE_ROW_SNAPSHOT})"
        ))
        .bind(issue_id)
        .bind(workspace_id)
        .bind(request.after_run.get())
        .bind(request.issue_revision)
        .fetch_optional(&mut *tx)
        .await?
        .ok_or(IssueMethodAdoptWriteError::RevisionConflict)?
    } else {
        before_text.clone()
    };

    let (before, after) = validate_snapshots(
        &before_text,
        &after_text,
        issue_id,
        workspace_id,
        request.issue_revision,
        request.write_run,
        &request.after_run,
    )?;
    tx.commit().await?;
    Ok(IssueMethodAdoptWriteResult { before, after })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::value::RawValue;

    #[test]
    fn wire_accepts_method_adopt_plan() {
        let request: IssueMethodAdoptWrite = serde_json::from_str(
            r#"{
                "issue_revision": 3,
                "workspace_templates": null,
                "before_run": {"revision":1,"state":"working"},
                "after_run": {"revision":2,"state":"working"},
                "write_run": true,
                "expected_project_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
            }"#,
        )
        .unwrap();
        assert_eq!(request.issue_revision, 3);
        assert!(request.write_run);
    }

    #[test]
    fn validate_snapshots_rejects_revision_drift() {
        let after_run =
            RawValue::from_string(r#"{"revision":2,"state":"working"}"#.to_owned()).unwrap();
        let before = r#"{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","workspace_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","revision":3,"status":"todo","work_method_run":{"revision":1}}"#
            .to_owned();
        let after = r#"{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","workspace_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","revision":3,"status":"todo","work_method_run":{"revision":2,"state":"working"}}"#
            .to_owned();
        let err = validate_snapshots(
            &before,
            &after,
            "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
            3,
            true,
            &after_run,
        )
        .unwrap_err();
        assert!(matches!(
            err,
            IssueMethodAdoptWriteError::InvalidCommitProof
        ));
    }
}
