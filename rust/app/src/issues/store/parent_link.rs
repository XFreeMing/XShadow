//! Shared parent-link validation and row locking for transactional writes that
//! can introduce hierarchy edges (parent edits and creation guards).

use std::collections::BTreeSet;

use sqlx::{PgConnection, Row};

use super::{HiddenProjects, IssueReadError, is_uuid};

pub const HIERARCHY_LOCK_SUFFIX: &str = ":issue_parent_hierarchy";

#[derive(Debug)]
pub enum ParentLinkError {
    SelfParent,
    ParentNotFound,
    Circular,
    Invisible,
}

pub async fn lock_workspace_parent_hierarchy(
    conn: &mut PgConnection,
    workspace_id: &str,
) -> Result<(), sqlx::Error> {
    let key = format!("{workspace_id}{HIERARCHY_LOCK_SUFFIX}");
    sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))")
        .bind(&key)
        .execute(conn)
        .await?;
    Ok(())
}

pub async fn lock_issue_rows_ordered(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_ids: impl IntoIterator<Item = String>,
) -> Result<(), sqlx::Error> {
    let unique: BTreeSet<String> = issue_ids.into_iter().filter(|id| is_uuid(id)).collect();
    for issue_id in unique {
        sqlx::query(
            "SELECT 1 FROM issue WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE",
        )
        .bind(&issue_id)
        .bind(workspace_id)
        .execute(&mut *conn)
        .await?;
    }
    Ok(())
}

async fn parent_link_would_cycle(
    conn: &mut PgConnection,
    workspace_id: &str,
    child_id: &str,
    parent_id: &str,
) -> Result<bool, ParentLinkError> {
    let rows = sqlx::query(
        "WITH RECURSIVE walk AS (
            SELECT i.id, i.parent_issue_id, ARRAY[i.id]::uuid[] AS visited, false AS cycle
            FROM issue i
            WHERE i.workspace_id = $1::uuid AND i.id = $2::uuid
            UNION ALL
            SELECT p.id, p.parent_issue_id, walk.visited || p.id, p.id = ANY(walk.visited)
            FROM walk
            JOIN issue p ON p.id = walk.parent_issue_id AND p.workspace_id = $1::uuid
            WHERE walk.parent_issue_id IS NOT NULL AND NOT walk.cycle
        )
        SELECT walk.id::text, walk.cycle FROM walk",
    )
    .bind(workspace_id)
    .bind(parent_id)
    .fetch_all(conn)
    .await
    .map_err(|_| ParentLinkError::ParentNotFound)?;

    for row in rows {
        let ancestor: String = row
            .try_get(0)
            .map_err(|_| ParentLinkError::ParentNotFound)?;
        let cycle: bool = row
            .try_get(1)
            .map_err(|_| ParentLinkError::ParentNotFound)?;
        if cycle || ancestor == child_id {
            return Ok(true);
        }
    }
    Ok(false)
}

/// Validates a proposed parent after the workspace hierarchy lock and row locks.
pub async fn validate_parent_link(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    new_parent_id: Option<&str>,
    hidden: &HiddenProjects,
) -> Result<(), ParentLinkError> {
    let Some(parent_id) = new_parent_id else {
        return Ok(());
    };
    if !is_uuid(parent_id) {
        return Err(ParentLinkError::ParentNotFound);
    }
    if parent_id == issue_id {
        return Err(ParentLinkError::SelfParent);
    }
    let parent_project: Option<String> = sqlx::query_scalar(
        "SELECT project_id::text FROM issue
         WHERE id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(parent_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await
    .map_err(|_| ParentLinkError::ParentNotFound)?
    .ok_or(ParentLinkError::ParentNotFound)?;
    if !hidden.can_see(parent_project.as_deref()) {
        return Err(ParentLinkError::Invisible);
    }
    if parent_link_would_cycle(conn, workspace_id, issue_id, parent_id).await? {
        return Err(ParentLinkError::Circular);
    }
    Ok(())
}

/// Issue row ids to lock for a parent change: the child, and distinct old/new parents.
pub fn parent_lock_ids(
    issue_id: &str,
    prev_parent: Option<&str>,
    next_parent: Option<&str>,
) -> Vec<String> {
    let mut ids = vec![issue_id.to_owned()];
    if let Some(parent) = prev_parent
        && parent != issue_id
    {
        ids.push(parent.to_owned());
    }
    if let Some(parent) = next_parent
        && parent != issue_id
        && ids.iter().all(|id| id != parent)
    {
        ids.push(parent.to_owned());
    }
    ids
}

pub fn map_parent_link_access(error: ParentLinkError) -> IssueReadError {
    match error {
        ParentLinkError::Invisible | ParentLinkError::ParentNotFound => {
            IssueReadError::InvalidInput("parent issue not found in this workspace")
        }
        ParentLinkError::SelfParent => {
            IssueReadError::InvalidInput("an issue cannot be its own parent")
        }
        ParentLinkError::Circular => {
            IssueReadError::InvalidInput("circular parent relationship detected")
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hierarchy_lock_suffix_matches_go() {
        assert_eq!(HIERARCHY_LOCK_SUFFIX, ":issue_parent_hierarchy");
    }
}
