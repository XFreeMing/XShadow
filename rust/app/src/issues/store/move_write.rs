use std::collections::BTreeMap;

use serde::Deserialize;
use serde_json::value::RawValue;
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, HiddenProjects, IssueReadError, is_uuid,
    ordinary::{ISSUE_ROW_SNAPSHOT, set_write_actor},
    resolve_caller_access_on,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueRelativeMove {
    pub expected_revision: Option<i64>,
    pub before_id: Option<String>,
    pub after_id: Option<String>,
    pub source_task_id: Option<String>,
}

#[derive(serde::Serialize)]
pub struct IssueRelativeMoveWrite {
    pub before: Box<RawValue>,
    pub after: Box<RawValue>,
    pub changed: bool,
}

#[derive(Debug)]
pub enum IssueRelativeMoveError {
    Access(IssueReadError),
    InvalidInput(&'static str),
    Conflict(&'static str),
    RevisionConflict(i64),
    Database(sqlx::Error),
    Snapshot(serde_json::Error),
}

impl From<sqlx::Error> for IssueRelativeMoveError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

impl From<serde_json::Error> for IssueRelativeMoveError {
    fn from(error: serde_json::Error) -> Self {
        Self::Snapshot(error)
    }
}

fn require_canonical_uuid(
    field: &'static str,
    raw: &str,
) -> Result<String, IssueRelativeMoveError> {
    if !is_uuid(raw) {
        return Err(IssueRelativeMoveError::InvalidInput(match field {
            "before_id" => "before_id must be a UUID or null",
            "after_id" => "after_id must be a UUID or null",
            "source_task_id" => "invalid source_task_id",
            _ => "invalid issue id",
        }));
    }
    if raw.bytes().any(|byte| byte.is_ascii_uppercase()) {
        return Err(IssueRelativeMoveError::InvalidInput(match field {
            "before_id" => "invalid before_id",
            "after_id" => "invalid after_id",
            "source_task_id" => "invalid source_task_id",
            _ => "issue not found",
        }));
    }
    Ok(raw.to_owned())
}

fn optional_canonical_uuid(
    field: &'static str,
    raw: &Option<String>,
) -> Result<Option<String>, IssueRelativeMoveError> {
    match raw {
        None => Ok(None),
        Some(value) => require_canonical_uuid(field, value).map(Some),
    }
}

pub fn issue_move_position(
    current: f64,
    before: Option<f64>,
    after: Option<f64>,
) -> Result<f64, IssueRelativeMoveError> {
    match (before, after) {
        (Some(before), Some(after)) => {
            if before.partial_cmp(&after) != Some(std::cmp::Ordering::Less) {
                return Err(IssueRelativeMoveError::Conflict(
                    "move anchors are stale or out of order",
                ));
            }
            let position = before + (after - before) / 2.0;
            if !(position > before && position < after)
                || position.is_infinite()
                || position.is_nan()
            {
                return Err(IssueRelativeMoveError::Conflict(
                    "move anchors are too close; refresh and retry",
                ));
            }
            Ok(position)
        }
        (Some(before), None) => {
            let position = before + 1.0;
            if position.is_infinite() || position.is_nan() {
                return Err(IssueRelativeMoveError::Conflict(
                    "move position is out of range",
                ));
            }
            Ok(position)
        }
        (None, Some(after)) => {
            let position = after - 1.0;
            if position.is_infinite() || position.is_nan() {
                return Err(IssueRelativeMoveError::Conflict(
                    "move position is out of range",
                ));
            }
            Ok(position)
        }
        (None, None) => Ok(current),
    }
}

struct LockedIssueRow {
    position: f64,
    project_id: Option<String>,
    revision: i64,
}

async fn lock_workspace_issues(
    tx: &mut PgConnection,
    workspace_id: &str,
    moved_issue_id: &str,
    lock_ids: &[String],
) -> Result<BTreeMap<String, LockedIssueRow>, IssueRelativeMoveError> {
    if lock_ids.is_empty() {
        return Ok(BTreeMap::new());
    }
    let rows = sqlx::query(
        "SELECT id::text, position, project_id::text, revision
         FROM issue
         WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])
         ORDER BY id
         FOR UPDATE",
    )
    .bind(workspace_id)
    .bind(lock_ids)
    .fetch_all(&mut *tx)
    .await?;
    if rows.len() != lock_ids.len() {
        if lock_ids.len() == 1 && lock_ids[0] == moved_issue_id {
            return Err(IssueRelativeMoveError::Access(IssueReadError::NotFound(
                "issue not found",
            )));
        }
        return Err(IssueRelativeMoveError::InvalidInput(
            "move anchor not found in this workspace",
        ));
    }
    let mut locked = BTreeMap::new();
    for row in rows {
        let id: String = row.try_get(0)?;
        locked.insert(
            id,
            LockedIssueRow {
                position: row.try_get(1)?,
                project_id: row.try_get(2)?,
                revision: row.try_get(3)?,
            },
        );
    }
    Ok(locked)
}

fn ensure_visible(
    hidden: &HiddenProjects,
    project_id: Option<&str>,
) -> Result<(), IssueRelativeMoveError> {
    if hidden.can_see(project_id) {
        Ok(())
    } else {
        Err(IssueRelativeMoveError::Access(IssueReadError::NotFound(
            "issue not found",
        )))
    }
}

pub async fn move_issue_relative(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    request: IssueRelativeMove,
) -> Result<IssueRelativeMoveWrite, IssueRelativeMoveError> {
    require_canonical_uuid("issue_id", issue_id)?;
    let before_id = optional_canonical_uuid("before_id", &request.before_id)?;
    let after_id = optional_canonical_uuid("after_id", &request.after_id)?;
    if let Some(task_id) = request.source_task_id.as_deref() {
        require_canonical_uuid("source_task_id", task_id)?;
    }
    if let (Some(before), Some(after)) = (&before_id, &after_id)
        && before == after
    {
        return Err(IssueRelativeMoveError::InvalidInput(
            "move anchors must be distinct",
        ));
    }
    if request
        .expected_revision
        .is_some_and(|revision| revision < 1)
    {
        return Err(IssueRelativeMoveError::InvalidInput(
            "expected_revision must be a positive integer",
        ));
    }

    let mut lock_ids = Vec::new();
    if let Some(id) = before_id.as_deref() {
        if id == issue_id {
            return Err(IssueRelativeMoveError::InvalidInput(
                "before_id cannot be the moved issue",
            ));
        }
        lock_ids.push(id.to_owned());
    }
    if let Some(id) = after_id.as_deref() {
        if id == issue_id {
            return Err(IssueRelativeMoveError::InvalidInput(
                "after_id cannot be the moved issue",
            ));
        }
        lock_ids.push(id.to_owned());
    }
    lock_ids.push(issue_id.to_owned());
    lock_ids.sort();
    lock_ids.dedup();

    let mut tx = pool.begin().await?;
    let locked_rows = lock_workspace_issues(&mut tx, workspace_id, issue_id, &lock_ids).await?;
    let moved = locked_rows.get(issue_id).ok_or_else(|| {
        IssueRelativeMoveError::Access(IssueReadError::NotFound("issue not found"))
    })?;

    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueRelativeMoveError::Access)?;
    ensure_visible(&hidden, moved.project_id.as_deref())?;
    for row in locked_rows.values() {
        if !hidden.can_see(row.project_id.as_deref()) {
            return Err(IssueRelativeMoveError::InvalidInput(
                "move anchor not found in this workspace",
            ));
        }
    }

    if request
        .expected_revision
        .is_some_and(|expected| expected != moved.revision)
    {
        return Err(IssueRelativeMoveError::RevisionConflict(moved.revision));
    }

    let before_position = before_id
        .as_deref()
        .map(|id| locked_rows.get(id).expect("anchor row locked").position);
    let after_position = after_id
        .as_deref()
        .map(|id| locked_rows.get(id).expect("anchor row locked").position);
    let next_position = issue_move_position(moved.position, before_position, after_position)?;

    let before_snapshot: String = sqlx::query_scalar(&format!(
        "SELECT {ISSUE_ROW_SNAPSHOT}
         FROM issue i
         WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_one(&mut *tx)
    .await?;

    set_write_actor(&mut tx, actor, request.source_task_id.as_deref()).await?;

    let saved = sqlx::query(&format!(
        "WITH next AS (
            SELECT $3::double precision AS position
         ), changes AS (
            SELECT next.position,
                   next.position IS DISTINCT FROM i.position AS moved
            FROM issue i CROSS JOIN next
            WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
         )
         UPDATE issue i
            SET position = changes.position,
                status = i.status,
                revision = i.revision + changes.moved::int,
                updated_at = CASE WHEN changes.moved THEN now() ELSE i.updated_at END
           FROM changes
          WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
         RETURNING {ISSUE_ROW_SNAPSHOT}, changes.moved"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .bind(next_position)
    .fetch_one(&mut *tx)
    .await?;
    let changed: bool = saved.try_get(1)?;
    let write = IssueRelativeMoveWrite {
        before: serde_json::from_str(&before_snapshot)?,
        after: serde_json::from_str(&saved.try_get::<String, _>(0)?)?,
        changed,
    };
    tx.commit().await?;
    Ok(write)
}

#[cfg(test)]
mod tests {
    use super::issue_move_position;

    #[test]
    fn uses_relative_anchors() {
        let position = issue_move_position(3.0, Some(10.0), Some(20.0)).unwrap();
        assert_eq!(position, 15.0);
    }

    #[test]
    fn rejects_stale_anchors() {
        assert!(issue_move_position(3.0, Some(20.0), Some(10.0)).is_err());
    }

    #[test]
    fn rejects_exhausted_gap() {
        let before: f64 = 1.0;
        let after = f64::from_bits(before.to_bits() + 1);
        assert!(issue_move_position(3.0, Some(before), Some(after)).is_err());
    }

    #[test]
    fn keeps_position_without_anchors() {
        let position = issue_move_position(7.0, None, None).unwrap();
        assert_eq!(position, 7.0);
    }
}
