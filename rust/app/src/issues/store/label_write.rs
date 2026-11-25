//! Issue↔label attach/detach mirror `AttachLabelToIssue` / `DetachLabelFromIssue`
//! after catalog and issue row locks. Lock order matches Go attach/detach and
//! `DeleteLabel`: `issue_label` (`FOR KEY SHARE` on link, row deletion on delete)
//! before `issue` (`FOR UPDATE`).
//!
//! `DeleteLabel` deletes the catalog row before junction cleanup in one transaction,
//! so attach/detach block on that lock and cannot insert after cleanup begins.

use serde::{Deserialize, Serialize};
use sqlx::{PgConnection, PgPool, Row};

use super::{CallerActor, IssueReadError, ordinary::set_write_actor, resolve_caller_access_on};

#[derive(Debug, Deserialize)]
pub struct IssueLabelWrite {
    source_task_id: Option<String>,
    pub detach: bool,
}

#[derive(Debug, Serialize, PartialEq, Eq)]
pub struct IssueLabelMutationResult {
    pub changed: bool,
    pub issue_revision: i64,
}

#[derive(Debug)]
pub enum IssueLabelWriteError {
    Access(IssueReadError),
    LabelNotFound,
    Database(sqlx::Error),
}

impl From<sqlx::Error> for IssueLabelWriteError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

async fn lock_issue_label_for_link(
    tx: &mut PgConnection,
    label_id: &str,
    workspace_id: &str,
) -> Result<(), IssueLabelWriteError> {
    sqlx::query(
        "SELECT id FROM issue_label
         WHERE id = $1::uuid AND workspace_id = $2::uuid AND resource_type = 'issue'
         FOR KEY SHARE",
    )
    .bind(label_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueLabelWriteError::LabelNotFound)?;
    Ok(())
}

async fn lock_issue_for_label_write(
    tx: &mut PgConnection,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
) -> Result<(), IssueLabelWriteError> {
    let locked = sqlx::query(
        "SELECT project_id::text FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueLabelWriteError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let hidden = resolve_caller_access_on(tx, workspace_id, actor)
        .await
        .map_err(IssueLabelWriteError::Access)?;
    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssueLabelWriteError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    Ok(())
}

async fn run_attach_cte(
    tx: &mut PgConnection,
    issue_id: &str,
    label_id: &str,
    workspace_id: &str,
) -> Result<IssueLabelMutationResult, sqlx::Error> {
    let row = sqlx::query(
        "WITH inserted AS (
            INSERT INTO issue_to_label (issue_id, label_id)
            VALUES ($1::uuid, $2::uuid)
            ON CONFLICT DO NOTHING
            RETURNING issue_id
        ), bumped AS (
            UPDATE issue
            SET revision = revision + 1,
                last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
            WHERE id IN (SELECT issue_id FROM inserted)
              AND workspace_id = $3::uuid
            RETURNING revision
        )
        SELECT EXISTS(SELECT 1 FROM inserted) AS changed,
               COALESCE((SELECT revision FROM bumped), 0)::bigint AS issue_revision",
    )
    .bind(issue_id)
    .bind(label_id)
    .bind(workspace_id)
    .fetch_one(&mut *tx)
    .await?;
    Ok(IssueLabelMutationResult {
        changed: row.try_get(0)?,
        issue_revision: row.try_get(1)?,
    })
}

async fn run_detach_cte(
    tx: &mut PgConnection,
    issue_id: &str,
    label_id: &str,
    workspace_id: &str,
) -> Result<IssueLabelMutationResult, sqlx::Error> {
    let row = sqlx::query(
        "WITH deleted AS (
            DELETE FROM issue_to_label
            WHERE issue_id = $1::uuid AND label_id = $2::uuid
            RETURNING issue_id
        ), bumped AS (
            UPDATE issue
            SET revision = revision + 1,
                last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
            WHERE id IN (SELECT issue_id FROM deleted)
              AND workspace_id = $3::uuid
            RETURNING revision
        )
        SELECT EXISTS(SELECT 1 FROM deleted) AS changed,
               COALESCE((SELECT revision FROM bumped), 0)::bigint AS issue_revision",
    )
    .bind(issue_id)
    .bind(label_id)
    .bind(workspace_id)
    .fetch_one(&mut *tx)
    .await?;
    Ok(IssueLabelMutationResult {
        changed: row.try_get(0)?,
        issue_revision: row.try_get(1)?,
    })
}

pub async fn write_issue_label(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    label_id: &str,
    request: IssueLabelWrite,
) -> Result<IssueLabelMutationResult, IssueLabelWriteError> {
    let mut tx = pool.begin().await?;
    lock_issue_label_for_link(&mut tx, label_id, workspace_id).await?;
    lock_issue_for_label_write(&mut tx, workspace_id, actor, issue_id).await?;
    set_write_actor(&mut tx, actor, request.source_task_id.as_deref()).await?;
    let result = if request.detach {
        run_detach_cte(&mut tx, issue_id, label_id, workspace_id).await?
    } else {
        run_attach_cte(&mut tx, issue_id, label_id, workspace_id).await?
    };
    tx.commit().await?;
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wire_accepts_attach_and_detach() {
        let attach: IssueLabelWrite = serde_json::from_str(
            r#"{"detach":false,"source_task_id":"11111111-1111-4111-8111-111111111111"}"#,
        )
        .unwrap();
        assert!(!attach.detach);
        let detach: IssueLabelWrite = serde_json::from_str(r#"{"detach":true}"#).unwrap();
        assert!(detach.detach);
    }
}
