//! Parent-only issue hierarchy edits for the native write pilot.

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError, is_uuid,
    ordinary::{self, ISSUE_ROW_SNAPSHOT},
    parent_link::{self, ParentLinkError},
    resolve_caller_access_on,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct OrdinaryBatchParentTrust {
    pub expected_project_id: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueParentEdit {
    pub expected_revision: Option<i64>,
    #[serde(default)]
    pub parent_clear: bool,
    pub parent_issue_id: Option<String>,
    pub source_task_id: Option<String>,
    #[serde(default)]
    pub ordinary_batch: Option<OrdinaryBatchParentTrust>,
}

#[derive(Debug, Serialize)]
pub struct IssueParentWrite {
    pub before: Box<RawValue>,
    pub after: Box<RawValue>,
    pub changed: bool,
}

#[derive(Debug)]
pub enum IssueParentEditError {
    Access(IssueReadError),
    InvalidInput(&'static str),
    RevisionConflict(i64),
    Parent(ParentLinkError),
    Database(sqlx::Error),
    Snapshot(serde_json::Error),
}

impl From<sqlx::Error> for IssueParentEditError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssueParentEditError {
    fn from(error: serde_json::Error) -> Self {
        Self::Snapshot(error)
    }
}

impl From<ParentLinkError> for IssueParentEditError {
    fn from(error: ParentLinkError) -> Self {
        Self::Parent(error)
    }
}

fn resolved_parent_target(
    edit: &IssueParentEdit,
) -> Result<Option<Option<&str>>, IssueParentEditError> {
    if edit.parent_clear {
        if edit.parent_issue_id.is_some() {
            return Err(IssueParentEditError::InvalidInput(
                "parent_issue_id conflicts with parent_clear",
            ));
        }
        return Ok(Some(None));
    }
    match edit.parent_issue_id.as_deref() {
        None => Err(IssueParentEditError::InvalidInput(
            "parent_issue_id or parent_clear is required",
        )),
        Some(raw) if !is_uuid(raw) => Err(IssueParentEditError::InvalidInput(
            "parent_issue_id must be a UUID",
        )),
        Some(raw) => Ok(Some(Some(raw))),
    }
}

pub async fn update_issue_parent(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    edit: IssueParentEdit,
) -> Result<IssueParentWrite, IssueParentEditError> {
    if !is_uuid(issue_id) {
        return Err(IssueParentEditError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    let next_parent = resolved_parent_target(&edit)?;

    let mut tx = pool.begin().await?;
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;

    parent_link::lock_workspace_parent_hierarchy(&mut tx, workspace_id).await?;

    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueParentEditError::Access)?;

    let snapshot_before_lock = sqlx::query(
        "SELECT parent_issue_id::text, project_id::text
         FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueParentEditError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let prev_parent: Option<String> = snapshot_before_lock.try_get(0)?;
    let issue_project: Option<String> = snapshot_before_lock.try_get(1)?;
    if !hidden.can_see(issue_project.as_deref()) {
        return Err(IssueParentEditError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    if let Some(trust) = edit.ordinary_batch.as_ref()
        && issue_project.as_deref() != trust.expected_project_id.as_deref()
    {
        return Err(IssueParentEditError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }

    let lock_ids = parent_link::parent_lock_ids(issue_id, prev_parent.as_deref(), next_parent.flatten());
    parent_link::lock_issue_rows_ordered(&mut tx, workspace_id, lock_ids).await?;

    let locked = sqlx::query(&format!(
        "SELECT i.project_id::text, i.revision, i.parent_issue_id::text,
                {ISSUE_ROW_SNAPSHOT} AS snapshot
         FROM issue i WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid FOR UPDATE"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueParentEditError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;

    let project_id: Option<String> = locked.try_get(0)?;
    if !hidden.can_see(project_id.as_deref()) {
        return Err(IssueParentEditError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }

    let revision: i64 = locked.try_get("revision")?;
    if let Some(trust) = edit.ordinary_batch.as_ref()
        && project_id.as_deref() != trust.expected_project_id.as_deref()
    {
        return Err(IssueParentEditError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    if let Some(expected) = edit.expected_revision
        && expected != revision
    {
        return Err(IssueParentEditError::RevisionConflict(revision));
    }

    let prev_parent_locked: Option<String> = locked.try_get(2)?;
    if prev_parent_locked.as_deref() == next_parent.flatten() {
        let before_text: String = locked.try_get("snapshot")?;
        let before: Box<RawValue> = serde_json::from_str(&before_text)?;
        return Ok(IssueParentWrite {
            before: before.clone(),
            after: before,
            changed: false,
        });
    }

    ordinary::set_write_actor(&mut tx, actor, edit.source_task_id.as_deref()).await?;

    if let Some(next) = next_parent.flatten() {
        parent_link::validate_parent_link(&mut tx, workspace_id, issue_id, Some(next), &hidden)
            .await?;
    }

    let (after_text, changed) =
        apply_parent_write(&mut tx, workspace_id, issue_id, next_parent).await?;

    let before_text: String = locked.try_get("snapshot")?;
    let write = IssueParentWrite {
        before: serde_json::from_str(&before_text)?,
        after: serde_json::from_str(&after_text)?,
        changed,
    };
    tx.commit().await?;
    Ok(write)
}

async fn apply_parent_write(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    next_parent: Option<Option<&str>>,
) -> Result<(String, bool), sqlx::Error> {
    let sql = format!(
        "WITH candidate AS (
            SELECT i.id,
                i.parent_issue_id IS DISTINCT FROM $3::uuid AS parent_changed,
                $3::uuid AS next_parent_issue_id
            FROM issue AS i
            WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
        ), updated AS (
            UPDATE issue AS i SET
                parent_issue_id = candidate.next_parent_issue_id,
                revision = i.revision + candidate.parent_changed::integer,
                last_activity_at = CASE
                    WHEN candidate.parent_changed
                        THEN GREATEST(COALESCE(i.last_activity_at, i.updated_at), now())
                    ELSE i.last_activity_at
                END,
                updated_at = CASE
                    WHEN candidate.parent_changed THEN now() ELSE i.updated_at
                END
            FROM candidate
            WHERE i.id = candidate.id AND i.workspace_id = $2::uuid
            RETURNING candidate.parent_changed, {ISSUE_ROW_SNAPSHOT} AS after_snapshot
        )
        SELECT updated.parent_changed, updated.after_snapshot FROM updated"
    );
    let row = sqlx::query(&sql)
        .bind(issue_id)
        .bind(workspace_id)
        .bind(match next_parent {
            None => None::<&str>,
            Some(None) => None::<&str>,
            Some(Some(id)) => Some(id),
        })
        .fetch_one(conn)
        .await?;
    let changed: bool = row.try_get(0)?;
    let after: String = row.try_get(1)?;
    Ok((after, changed))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parent_lock_ids_include_child_old_and_new_parents() {
        let ids = parent_link::parent_lock_ids(
            "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            Some("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
            Some("cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
        );
        assert_eq!(ids.len(), 3);
        assert!(ids.contains(&"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa".to_owned()));
    }

    #[test]
    fn clear_parent_requires_parent_clear_flag() {
        let edit = IssueParentEdit {
            expected_revision: Some(1),
            parent_clear: true,
            parent_issue_id: None,
            source_task_id: None,
            ordinary_batch: None,
        };
        assert!(resolved_parent_target(&edit).unwrap() == Some(None));
    }
}
