//! Member-authored batch issue deletes in one transaction. Mirrors Go
//! `deleteIssuesAndCollectAttachmentURLs` with excluded batch members.

use serde::Deserialize;
use sqlx::PgPool;

use super::{
    CallerActor, IssueReadError,
    delete_write::{
        ClearedDuplicateSnapshot, IssueDeletePlan, IssueDeleteWriteError,
        execute_issue_delete_steps, lock_issues_for_delete,
    },
    is_uuid, ordinary,
};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueBatchDeleteWrite {
    pub issue_ids: Vec<String>,
    pub source_task_id: Option<String>,
}

#[derive(Debug, serde::Serialize)]
pub struct IssueBatchDeleteWriteResult {
    pub deleted: u32,
    pub deleted_issue_ids: Vec<String>,
    pub detached_children: Vec<String>,
    pub cleared_duplicates: Vec<ClearedDuplicateSnapshot>,
    pub goal_links_changed: bool,
}

pub async fn batch_delete_issues(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    request: IssueBatchDeleteWrite,
) -> Result<IssueBatchDeleteWriteResult, IssueDeleteWriteError> {
    if !is_uuid(workspace_id) {
        return Err(IssueDeleteWriteError::Access(IssueReadError::NotFound(
            "issue not found",
        )));
    }
    let mut issue_ids: Vec<String> = request
        .issue_ids
        .into_iter()
        .filter(|id| is_uuid(id))
        .collect();
    if issue_ids.is_empty() {
        return Ok(IssueBatchDeleteWriteResult {
            deleted: 0,
            deleted_issue_ids: Vec::new(),
            detached_children: Vec::new(),
            cleared_duplicates: Vec::new(),
            goal_links_changed: false,
        });
    }
    let mut seen = std::collections::HashSet::new();
    issue_ids.retain(|id| seen.insert(id.clone()));
    issue_ids.sort();

    let mut tx = pool.begin().await?;
    ordinary::set_write_actor(&mut tx, actor, request.source_task_id.as_deref()).await?;
    lock_issues_for_delete(
        &mut tx,
        workspace_id,
        actor,
        &issue_ids,
        IssueDeletePlan::Ordinary,
    )
    .await?;

    let mut detached_children = Vec::new();
    let mut cleared_duplicates = Vec::new();
    let mut goal_links_changed = false;
    let excluded = issue_ids.clone();
    for issue_id in &issue_ids {
        let (detached, cleared, goal_changed, _) =
            execute_issue_delete_steps(&mut tx, workspace_id, issue_id, &excluded, None).await?;
        detached_children.extend(detached);
        cleared_duplicates.extend(cleared);
        goal_links_changed = goal_links_changed || goal_changed;
    }

    tx.commit().await?;
    Ok(IssueBatchDeleteWriteResult {
        deleted: issue_ids.len() as u32,
        deleted_issue_ids: issue_ids,
        detached_children,
        cleared_duplicates,
        goal_links_changed,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn batch_delete_wire_accepts_issue_ids() {
        let request: IssueBatchDeleteWrite = serde_json::from_str(
            r#"{"issue_ids":["11111111-1111-4111-8111-111111111111"],
              "source_task_id":"22222222-2222-4222-8222-222222222222"}"#,
        )
        .unwrap();
        assert_eq!(request.issue_ids.len(), 1);
        assert!(request.source_task_id.is_some());
    }
}
