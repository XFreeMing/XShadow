//! Member task reactions on projectless issues. Reaction row mutations are staged
//! before the issue row is locked for scope; `FOR NO KEY UPDATE` is taken only for
//! the post-mutation access check and optional `revision` bump, matching the legacy
//! Go lock order and avoiding mixed-version deadlocks on the same reaction key.

use serde::{Deserialize, Serialize};
use sqlx::{PgConnection, PgPool, Row};

use super::{
    CallerActor, IssueReadError, format_issue_timestamp, is_uuid, ordinary::set_write_actor,
    resolve_caller_access_on,
};

const DUPLICATE_LOOKUP_RETRIES: usize = 3;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IssueReactionWrite {
    pub emoji: String,
}

#[derive(Debug, Serialize, Clone, PartialEq, Eq)]
pub struct IssueReactionRow {
    pub id: String,
    pub issue_id: String,
    pub actor_type: String,
    pub actor_id: String,
    pub emoji: String,
    pub created_at: String,
}

#[derive(Debug, Serialize)]
pub struct IssueReactionWriteResult {
    pub changed: bool,
    pub reaction: IssueReactionRow,
    pub issue_revision: i64,
}

#[derive(Debug, Serialize)]
pub struct IssueReactionRemoveResult {
    pub changed: bool,
    pub issue_revision: i64,
}

#[derive(Debug)]
pub enum IssueReactionWriteError {
    Access(IssueReadError),
    InvalidInput,
    RouteChanged,
    Database(sqlx::Error),
}

impl From<sqlx::Error> for IssueReactionWriteError {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

async fn lock_workspace_and_member(
    tx: &mut PgConnection,
    workspace_id: &str,
    user_id: &str,
) -> Result<(), IssueReactionWriteError> {
    sqlx::query("SELECT 1 FROM workspace WHERE id = $1::uuid FOR KEY SHARE")
        .bind(workspace_id)
        .execute(&mut *tx)
        .await?;
    let member = sqlx::query_scalar::<_, i32>(
        "SELECT 1 FROM member WHERE user_id = $1::uuid AND workspace_id = $2::uuid FOR SHARE",
    )
    .bind(user_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?;
    if member.is_none() {
        return Err(IssueReactionWriteError::Access(IssueReadError::NotFound(
            "workspace not found",
        )));
    }
    Ok(())
}

/// Post-mutation scope: lock the issue, re-check membership visibility and
/// projectless routing. Any rejection rolls back staged reaction changes.
async fn recheck_projectless_issue_scope(
    tx: &mut PgConnection,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
) -> Result<i64, IssueReactionWriteError> {
    let locked = sqlx::query(
        "SELECT project_id::text, revision FROM issue
         WHERE id = $1::uuid AND workspace_id = $2::uuid
         FOR NO KEY UPDATE",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or(IssueReactionWriteError::Access(IssueReadError::NotFound(
        "issue not found",
    )))?;
    let project_id: Option<String> = locked.try_get(0)?;
    if project_id.is_some() {
        let hidden = resolve_caller_access_on(tx, workspace_id, actor)
            .await
            .map_err(IssueReactionWriteError::Access)?;
        if !hidden.can_see(project_id.as_deref()) {
            return Err(IssueReactionWriteError::Access(IssueReadError::NotFound(
                "issue not found",
            )));
        }
        return Err(IssueReactionWriteError::RouteChanged);
    }
    Ok(locked.try_get(1)?)
}

fn map_reaction_row(row: &sqlx::postgres::PgRow) -> Result<IssueReactionRow, sqlx::Error> {
    Ok(IssueReactionRow {
        id: row.try_get(0)?,
        issue_id: row.try_get(1)?,
        actor_type: row.try_get(2)?,
        actor_id: row.try_get(3)?,
        emoji: row.try_get(4)?,
        created_at: format_issue_timestamp(row.try_get(5)?),
    })
}

const REACTION_ROW_SQL: &str = "id::text, issue_id::text, actor_type, actor_id::text, emoji,
    to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')";

async fn try_insert_reaction(
    tx: &mut PgConnection,
    issue_id: &str,
    workspace_id: &str,
    actor_id: &str,
    emoji: &str,
) -> Result<Option<IssueReactionRow>, sqlx::Error> {
    let row = sqlx::query(&format!(
        "INSERT INTO issue_reaction (issue_id, workspace_id, actor_type, actor_id, emoji)
         SELECT $1::uuid, $2::uuid, 'member', $3::uuid, $4
         FROM issue i
         WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
         ON CONFLICT (issue_id, actor_type, actor_id, emoji) DO NOTHING
         RETURNING {REACTION_ROW_SQL}"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .bind(actor_id)
    .bind(emoji)
    .fetch_optional(&mut *tx)
    .await?;
    row.as_ref().map(map_reaction_row).transpose()
}

async fn load_existing_reaction(
    tx: &mut PgConnection,
    issue_id: &str,
    workspace_id: &str,
    actor_id: &str,
    emoji: &str,
) -> Result<Option<IssueReactionRow>, sqlx::Error> {
    let row = sqlx::query(&format!(
        "SELECT {REACTION_ROW_SQL}
         FROM issue_reaction
         WHERE issue_id = $1::uuid AND workspace_id = $2::uuid
           AND actor_type = 'member' AND actor_id = $3::uuid AND emoji = $4
         FOR SHARE"
    ))
    .bind(issue_id)
    .bind(workspace_id)
    .bind(actor_id)
    .bind(emoji)
    .fetch_optional(&mut *tx)
    .await?;
    row.as_ref().map(map_reaction_row).transpose()
}

async fn bump_issue_revision(
    tx: &mut PgConnection,
    issue_id: &str,
    workspace_id: &str,
) -> Result<i64, sqlx::Error> {
    let row = sqlx::query(
        "UPDATE issue SET revision = revision + 1
         WHERE id = $1::uuid AND workspace_id = $2::uuid
         RETURNING revision",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .fetch_one(&mut *tx)
    .await?;
    row.try_get(0)
}

async fn stage_remove_reaction(
    tx: &mut PgConnection,
    issue_id: &str,
    workspace_id: &str,
    actor_id: &str,
    emoji: &str,
) -> Result<bool, sqlx::Error> {
    let removed = sqlx::query(
        "DELETE FROM issue_reaction
         WHERE issue_id = $1::uuid AND workspace_id = $2::uuid
           AND actor_type = 'member' AND actor_id = $3::uuid AND emoji = $4",
    )
    .bind(issue_id)
    .bind(workspace_id)
    .bind(actor_id)
    .bind(emoji)
    .execute(&mut *tx)
    .await?;
    Ok(removed.rows_affected() > 0)
}

pub async fn add_issue_reaction(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    request: IssueReactionWrite,
) -> Result<IssueReactionWriteResult, IssueReactionWriteError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(IssueReactionWriteError::InvalidInput);
    };
    if !is_uuid(issue_id) || request.emoji.is_empty() {
        return Err(IssueReactionWriteError::InvalidInput);
    }

    let mut tx = pool.begin().await?;
    lock_workspace_and_member(&mut tx, workspace_id, user_id).await?;
    set_write_actor(&mut tx, actor, None).await?;

    let mut outcome = None;
    for _ in 0..DUPLICATE_LOOKUP_RETRIES {
        if let Some(row) =
            try_insert_reaction(&mut tx, issue_id, workspace_id, user_id, &request.emoji).await?
        {
            outcome = Some((row, true));
            break;
        }
        if let Some(row) =
            load_existing_reaction(&mut tx, issue_id, workspace_id, user_id, &request.emoji).await?
        {
            outcome = Some((row, false));
            break;
        }
    }
    let (reaction, changed) = outcome.ok_or(IssueReactionWriteError::Access(
        IssueReadError::NotFound("issue not found"),
    ))?;

    let current_revision =
        recheck_projectless_issue_scope(&mut tx, workspace_id, actor, issue_id).await?;
    let issue_revision = if changed {
        bump_issue_revision(&mut tx, issue_id, workspace_id).await?
    } else {
        current_revision
    };

    tx.commit().await?;
    Ok(IssueReactionWriteResult {
        changed,
        reaction,
        issue_revision,
    })
}

pub async fn remove_issue_reaction(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    issue_id: &str,
    request: IssueReactionWrite,
) -> Result<IssueReactionRemoveResult, IssueReactionWriteError> {
    let CallerActor::Member { user_id } = actor else {
        return Err(IssueReactionWriteError::InvalidInput);
    };
    if !is_uuid(issue_id) || request.emoji.is_empty() {
        return Err(IssueReactionWriteError::InvalidInput);
    }

    let mut tx = pool.begin().await?;
    lock_workspace_and_member(&mut tx, workspace_id, user_id).await?;
    set_write_actor(&mut tx, actor, None).await?;

    let changed =
        stage_remove_reaction(&mut tx, issue_id, workspace_id, user_id, &request.emoji).await?;
    let current_revision =
        recheck_projectless_issue_scope(&mut tx, workspace_id, actor, issue_id).await?;
    let issue_revision = if changed {
        bump_issue_revision(&mut tx, issue_id, workspace_id).await?
    } else {
        current_revision
    };

    tx.commit().await?;
    Ok(IssueReactionRemoveResult {
        changed,
        issue_revision,
    })
}
