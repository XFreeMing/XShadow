use std::collections::BTreeMap;

use serde::Deserialize;
use serde_json::value::RawValue;
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, HiddenProjects, IssueReadError, is_uuid,
    ordinary::{ISSUE_ROW_SNAPSHOT, set_write_actor},
    parent_link::{self, ParentLinkError},
    resolve_caller_access_on,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueRelativeMove {
    pub expected_revision: Option<i64>,
    pub before_id: Option<String>,
    pub after_id: Option<String>,
    pub source_task_id: Option<String>,
    #[serde(default)]
    pub parent_touched: bool,
    #[serde(default)]
    pub parent_clear: bool,
    pub parent_issue_id: Option<String>,
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
    Parent(ParentLinkError),
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

impl From<ParentLinkError> for IssueRelativeMoveError {
    fn from(error: ParentLinkError) -> Self {
        Self::Parent(error)
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
            "parent_issue_id" => "parent_issue_id must be a UUID",
            _ => "invalid issue id",
        }));
    }
    if raw.bytes().any(|byte| byte.is_ascii_uppercase()) {
        return Err(IssueRelativeMoveError::InvalidInput(match field {
            "before_id" => "invalid before_id",
            "after_id" => "invalid after_id",
            "source_task_id" => "invalid source_task_id",
            "parent_issue_id" => "invalid parent_issue_id",
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

/// Parent tri-state: unchanged (`None`), clear (`Some(None)`), set (`Some(Some(canonical id))`).
fn resolved_move_parent(
    request: &IssueRelativeMove,
) -> Result<Option<Option<String>>, IssueRelativeMoveError> {
    if !request.parent_touched {
        return Ok(None);
    }
    if request.parent_clear {
        if request.parent_issue_id.is_some() {
            return Err(IssueRelativeMoveError::InvalidInput(
                "parent_issue_id conflicts with parent_clear",
            ));
        }
        return Ok(Some(None));
    }
    match request.parent_issue_id.as_deref() {
        None => Err(IssueRelativeMoveError::InvalidInput(
            "parent_issue_id or parent_clear is required when parent is touched",
        )),
        Some(raw) => {
            let id = require_canonical_uuid("parent_issue_id", raw)?;
            Ok(Some(Some(id)))
        }
    }
}

fn positions_distinct(current: f64, next: f64) -> bool {
    // `!=` matches PostgreSQL `IS DISTINCT FROM` for ±0 and aligns with Go `!=`.
    current != next
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
    parent_issue_id: Option<String>,
}

/// Locks every id in `all_ids` in one ordered pass; `required_ids` must all be present.
/// Parent rows used only for lock stability may be absent without error.
async fn lock_move_issue_rows(
    tx: &mut PgConnection,
    workspace_id: &str,
    moved_issue_id: &str,
    required_ids: &[String],
    all_ids: &[String],
) -> Result<BTreeMap<String, LockedIssueRow>, IssueRelativeMoveError> {
    if all_ids.is_empty() {
        return Ok(BTreeMap::new());
    }
    let rows = sqlx::query(
        "SELECT id::text, position, project_id::text, revision, parent_issue_id::text
         FROM issue
         WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])
         ORDER BY id
         FOR UPDATE",
    )
    .bind(workspace_id)
    .bind(all_ids)
    .fetch_all(&mut *tx)
    .await?;
    let mut locked = BTreeMap::new();
    for row in rows {
        let id: String = row.try_get(0)?;
        locked.insert(
            id,
            LockedIssueRow {
                position: row.try_get(1)?,
                project_id: row.try_get(2)?,
                revision: row.try_get(3)?,
                parent_issue_id: row.try_get(4)?,
            },
        );
    }
    for required in required_ids {
        if locked.contains_key(required) {
            continue;
        }
        if required == moved_issue_id {
            return Err(IssueRelativeMoveError::Access(IssueReadError::NotFound(
                "issue not found",
            )));
        }
        return Err(IssueRelativeMoveError::InvalidInput(
            "move anchor not found in this workspace",
        ));
    }
    Ok(locked)
}

fn ensure_anchor_visible(
    hidden: &HiddenProjects,
    anchor: &LockedIssueRow,
) -> Result<(), IssueRelativeMoveError> {
    if hidden.can_see(anchor.project_id.as_deref()) {
        Ok(())
    } else {
        Err(IssueRelativeMoveError::InvalidInput(
            "move anchor not found in this workspace",
        ))
    }
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

fn collect_move_lock_ids(
    issue_id: &str,
    before_id: &Option<String>,
    after_id: &Option<String>,
    prev_parent: Option<&str>,
    next_parent: Option<&str>,
    parent_touched: bool,
) -> (Vec<String>, Vec<String>) {
    let mut required = vec![issue_id.to_owned()];
    if let Some(id) = before_id {
        required.push(id.clone());
    }
    if let Some(id) = after_id {
        required.push(id.clone());
    }
    let mut all = required.clone();
    if parent_touched {
        for id in parent_link::parent_lock_ids(issue_id, prev_parent, next_parent) {
            if !all.iter().any(|existing| existing == &id) {
                all.push(id);
            }
        }
    }
    all.sort();
    all.dedup();
    required.sort();
    required.dedup();
    (required, all)
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
    if let Some(id) = before_id.as_deref()
        && id == issue_id
    {
        return Err(IssueRelativeMoveError::InvalidInput(
            "before_id cannot be the moved issue",
        ));
    }
    if let Some(id) = after_id.as_deref()
        && id == issue_id
    {
        return Err(IssueRelativeMoveError::InvalidInput(
            "after_id cannot be the moved issue",
        ));
    }

    let parent_target = resolved_move_parent(&request)?;

    let mut tx = pool.begin().await?;

    if request.parent_touched {
        sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
            .bind(workspace_id)
            .execute(&mut *tx)
            .await?;
        parent_link::lock_workspace_parent_hierarchy(&mut tx, workspace_id).await?;
    }

    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor)
        .await
        .map_err(IssueRelativeMoveError::Access)?;

    let prev_parent: Option<String> = if request.parent_touched {
        sqlx::query_scalar(
            "SELECT parent_issue_id::text FROM issue
             WHERE id = $1::uuid AND workspace_id = $2::uuid",
        )
        .bind(issue_id)
        .bind(workspace_id)
        .fetch_optional(&mut *tx)
        .await?
        .ok_or(IssueRelativeMoveError::Access(IssueReadError::NotFound(
            "issue not found",
        )))?
    } else {
        None
    };

    let issue_project: Option<String> = if request.parent_touched {
        sqlx::query_scalar(
            "SELECT project_id::text FROM issue
             WHERE id = $1::uuid AND workspace_id = $2::uuid",
        )
        .bind(issue_id)
        .bind(workspace_id)
        .fetch_optional(&mut *tx)
        .await?
        .flatten()
    } else {
        None
    };

    if request.parent_touched {
        ensure_visible(&hidden, issue_project.as_deref())?;
    }

    let next_parent_ref = parent_target.as_ref().and_then(|p| p.as_deref());
    let (required_lock_ids, all_lock_ids) = collect_move_lock_ids(
        issue_id,
        &before_id,
        &after_id,
        prev_parent.as_deref(),
        next_parent_ref,
        request.parent_touched,
    );

    let locked_rows = lock_move_issue_rows(
        &mut tx,
        workspace_id,
        issue_id,
        &required_lock_ids,
        &all_lock_ids,
    )
    .await?;
    let moved = locked_rows
        .get(issue_id)
        .expect("required issue row missing after lock");

    ensure_visible(&hidden, moved.project_id.as_deref())?;
    if let Some(id) = before_id.as_ref() {
        let anchor = locked_rows
            .get(id)
            .expect("required before anchor missing after lock");
        ensure_anchor_visible(&hidden, anchor)?;
    }
    if let Some(id) = after_id.as_ref() {
        let anchor = locked_rows
            .get(id)
            .expect("required after anchor missing after lock");
        ensure_anchor_visible(&hidden, anchor)?;
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

    let position_changed = positions_distinct(moved.position, next_position);
    let parent_changed = if let Some(ref next) = parent_target {
        moved.parent_issue_id.as_deref() != next.as_deref()
    } else {
        false
    };

    if let Some(Some(parent_id)) = parent_target.as_ref() {
        parent_link::validate_parent_link(
            &mut tx,
            workspace_id,
            issue_id,
            Some(parent_id.as_str()),
            &hidden,
        )
        .await?;
    }

    let before_snapshot: String = sqlx::query_scalar(&format!(
        "SELECT {ISSUE_ROW_SNAPSHOT}
         FROM issue i
         WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_one(&mut *tx)
    .await?;

    let before: Box<RawValue> = serde_json::from_str(&before_snapshot)?;
    if !position_changed && !parent_changed {
        return Ok(IssueRelativeMoveWrite {
            before: before.clone(),
            after: before,
            changed: false,
        });
    }

    set_write_actor(&mut tx, actor, request.source_task_id.as_deref()).await?;

    let next_parent_bind: Option<&str> = match &parent_target {
        None => None,
        Some(None) => None,
        Some(Some(id)) => Some(id.as_str()),
    };

    let saved = sqlx::query(&format!(
        "WITH next AS (
            SELECT $3::double precision AS position,
                   $4::uuid AS next_parent_issue_id,
                   $5::boolean AS parent_touched
         ), changes AS (
            SELECT next.position,
                   next.next_parent_issue_id,
                   next.parent_touched,
                   next.position IS DISTINCT FROM i.position AS position_changed,
                   CASE
                       WHEN next.parent_touched
                           THEN i.parent_issue_id IS DISTINCT FROM next.next_parent_issue_id
                       ELSE false
                   END AS parent_changed
            FROM issue i CROSS JOIN next
            WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
         ), flags AS (
            SELECT changes.*,
                   (changes.position_changed OR changes.parent_changed) AS any_changed
            FROM changes
         )
         UPDATE issue i
            SET position = flags.position,
                parent_issue_id = CASE
                    WHEN flags.parent_touched THEN flags.next_parent_issue_id
                    ELSE i.parent_issue_id
                END,
                revision = i.revision + flags.any_changed::int,
                last_activity_at = CASE
                    WHEN flags.parent_changed
                        THEN GREATEST(COALESCE(i.last_activity_at, i.updated_at), now())
                    ELSE i.last_activity_at
                END,
                updated_at = CASE WHEN flags.any_changed THEN now() ELSE i.updated_at END
           FROM flags
          WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
         RETURNING {ISSUE_ROW_SNAPSHOT}, flags.any_changed"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .bind(next_position)
    .bind(next_parent_bind)
    .bind(request.parent_touched)
    .fetch_one(&mut *tx)
    .await?;
    let changed: bool = saved.try_get(1)?;
    let write = IssueRelativeMoveWrite {
        before,
        after: serde_json::from_str(&saved.try_get::<String, _>(0)?)?,
        changed,
    };
    tx.commit().await?;
    Ok(write)
}

#[cfg(test)]
mod tests {
    use super::{IssueRelativeMove, issue_move_position, resolved_move_parent};

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

    #[test]
    fn parent_tri_state() {
        let unchanged = IssueRelativeMove {
            expected_revision: None,
            before_id: None,
            after_id: None,
            source_task_id: None,
            parent_touched: false,
            parent_clear: false,
            parent_issue_id: None,
        };
        assert!(resolved_move_parent(&unchanged).unwrap().is_none());

        let clear = IssueRelativeMove {
            expected_revision: None,
            before_id: None,
            after_id: None,
            source_task_id: None,
            parent_touched: true,
            parent_clear: true,
            parent_issue_id: None,
        };
        assert_eq!(resolved_move_parent(&clear).unwrap(), Some(None));

        let set = IssueRelativeMove {
            parent_touched: true,
            parent_issue_id: Some("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa".into()),
            ..unchanged
        };
        assert_eq!(
            resolved_move_parent(&set).unwrap(),
            Some(Some("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa".to_owned()))
        );
    }

    #[test]
    fn position_distinct_treats_signed_zero_as_equal() {
        assert!(!super::positions_distinct(0.0, -0.0));
        assert!(super::positions_distinct(0.0, 1.0));
        assert!(!super::positions_distinct(1.0, 1.0));
    }
}
