//! Manual source-context child creates: trusted Go snapshot + typed DB projection.

use chrono::{DateTime, Utc};
use serde::Deserialize;
use sqlx::{PgConnection, Postgres, Row, Transaction};
use std::collections::HashSet;

use super::create_write::{IssueCreateError, IssueCreateRequest};
use super::{CallerActor, is_uuid, resolve_caller_access_on};

pub const SOURCE_CONTEXT_VERSION: i16 = 1;
pub const SOURCE_CONTEXT_MAX_COMMENTS: i32 = 256;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SourceContextCreateTrust {
    pub context_id: String,
    pub source_issue_id: String,
    pub anchor_comment_id: String,
    pub capture_digest: String,
    pub captured_by_user_id: String,
    pub captured_at: String,
    pub snapshot: serde_json::Value,
}

#[derive(Debug, Deserialize)]
struct TrustSnapshot {
    version: i16,
    captured_by_user_id: String,
    captured_at: String,
    source_issue: TrustSourceIssue,
    comment_thread: Vec<TrustComment>,
    anchor_comment_id: String,
}

#[derive(Debug, Deserialize)]
struct TrustSourceIssue {
    id: String,
    identifier: String,
    number: i32,
    title: String,
    description: Option<String>,
    created_at: String,
    #[serde(default)]
    attachments: Vec<serde_json::Value>,
}

#[derive(Debug, Deserialize)]
struct TrustComment {
    id: String,
    parent_id: Option<String>,
    #[serde(rename = "type")]
    comment_type: String,
    content: String,
    author: TrustAuthor,
    created_at: String,
    #[serde(default)]
    attachments: Vec<serde_json::Value>,
    #[serde(default)]
    deleted: bool,
}

#[derive(Debug, Deserialize)]
struct TrustAuthor {
    #[serde(rename = "type")]
    author_type: String,
    id: String,
}

struct LiveCommentRow {
    id: String,
    parent_id: Option<String>,
    comment_type: String,
    content: String,
    author_type: String,
    author_id: String,
    created_at: String,
    deleted: bool,
}

struct LiveSourceIssue {
    title: String,
    number: i32,
    description: Option<String>,
    created_at: String,
}

fn validate_capture_digest(digest: &str) -> Result<(), IssueCreateError> {
    if digest.len() != 64
        || !digest
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return Err(IssueCreateError::InvalidInput);
    }
    Ok(())
}

fn parse_utc_timestamp(raw: &str) -> Result<DateTime<Utc>, IssueCreateError> {
    DateTime::parse_from_rfc3339(raw)
        .map(|ts| ts.with_timezone(&Utc))
        .map_err(|_| IssueCreateError::InvalidInput)
}

fn trust_timestamps_equal(a: &str, b: &str) -> Result<bool, IssueCreateError> {
    Ok(parse_utc_timestamp(a)? == parse_utc_timestamp(b)?)
}

fn parse_trust_snapshot(
    trust: &SourceContextCreateTrust,
) -> Result<TrustSnapshot, IssueCreateError> {
    serde_json::from_value(trust.snapshot.clone()).map_err(|_| IssueCreateError::InvalidInput)
}

fn validate_trust_snapshot_shape(
    trust: &SourceContextCreateTrust,
    snap: &TrustSnapshot,
) -> Result<(), IssueCreateError> {
    validate_capture_digest(&trust.capture_digest)?;
    if snap.version != SOURCE_CONTEXT_VERSION {
        return Err(IssueCreateError::InvalidInput);
    }
    if snap.captured_by_user_id != trust.captured_by_user_id
        || snap.source_issue.id != trust.source_issue_id
        || snap.anchor_comment_id != trust.anchor_comment_id
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if !trust_timestamps_equal(&snap.captured_at, &trust.captured_at)? {
        return Err(IssueCreateError::InvalidInput);
    }
    if !snap.source_issue.attachments.is_empty()
        || snap
            .comment_thread
            .iter()
            .any(|c| !c.attachments.is_empty())
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if snap
        .comment_thread
        .first()
        .is_none_or(|comment| comment.parent_id.is_some())
        || snap.comment_thread.last().map(|c| c.id.as_str())
            != Some(trust.anchor_comment_id.as_str())
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if snap.comment_thread.len() > SOURCE_CONTEXT_MAX_COMMENTS as usize {
        return Err(IssueCreateError::InvalidInput);
    }
    if snap
        .comment_thread
        .iter()
        .any(|comment| !is_uuid(&comment.id))
    {
        return Err(IssueCreateError::InvalidInput);
    }
    Ok(())
}

async fn source_context_attachments_exist(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    comment_ids: &[String],
) -> Result<bool, IssueCreateError> {
    let found = sqlx::query_scalar::<_, i32>(
        "SELECT 1 FROM attachment
         WHERE workspace_id = $1::uuid
           AND issue_id = $2::uuid
           AND source_context_id IS NULL
           AND (
                comment_id IS NULL
                OR comment_id = ANY($3::text[]::uuid[])
           )
         LIMIT 1",
    )
    .bind(workspace_id)
    .bind(issue_id)
    .bind(comment_ids)
    .fetch_optional(&mut *conn)
    .await
    .map_err(IssueCreateError::Database)?;
    Ok(found.is_some())
}

async fn lock_and_load_source_issue(
    conn: &mut PgConnection,
    workspace_id: &str,
    member_id: &str,
    source_issue_id: &str,
) -> Result<LiveSourceIssue, IssueCreateError> {
    let row = sqlx::query(
        "SELECT i.title, i.number, i.description,
                to_char(i.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
                i.project_id::text
         FROM issue i
         WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid
         FOR UPDATE OF i",
    )
    .bind(source_issue_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await
    .map_err(IssueCreateError::Database)?;
    let Some(row) = row else {
        return Err(IssueCreateError::SourceIssueDeleted);
    };
    let project_id: Option<String> = row.try_get(4)?;
    if let Some(project_id) = project_id.as_deref() {
        let actor = CallerActor::Member {
            user_id: member_id.to_owned(),
        };
        let hidden = resolve_caller_access_on(conn, workspace_id, &actor)
            .await
            .map_err(IssueCreateError::Access)?;
        if !hidden.can_see(Some(project_id)) {
            return Err(IssueCreateError::AnchorCommentDeleted);
        }
        return Err(IssueCreateError::SourceContextChanged);
    }
    Ok(LiveSourceIssue {
        title: row.try_get(0)?,
        number: row.try_get(1)?,
        description: row.try_get(2)?,
        created_at: row.try_get(3)?,
    })
}

async fn load_anchor(
    conn: &mut PgConnection,
    workspace_id: &str,
    anchor_comment_id: &str,
) -> Result<(String, String), IssueCreateError> {
    let row = sqlx::query(
        "SELECT issue_id::text, to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
                type, deleted_at IS NOT NULL
         FROM comment
         WHERE id = $1::uuid AND workspace_id = $2::uuid",
    )
    .bind(anchor_comment_id)
    .bind(workspace_id)
    .fetch_optional(&mut *conn)
    .await
    .map_err(IssueCreateError::Database)?;
    let Some(row) = row else {
        return Err(IssueCreateError::AnchorCommentDeleted);
    };
    if row.try_get::<bool, _>(3)? {
        return Err(IssueCreateError::AnchorCommentDeleted);
    }
    if row.try_get::<&str, _>(2)? != "comment" {
        return Err(IssueCreateError::SourceContextInvalidPath);
    }
    let issue_id: String = row.try_get(0)?;
    let anchor_created_at: String = row.try_get(1)?;
    Ok((issue_id, anchor_created_at))
}

async fn validate_ancestor_path(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    anchor_comment_id: &str,
) -> Result<String, IssueCreateError> {
    let ancestors = sqlx::query(
        "WITH RECURSIVE ancestor_path AS (
            SELECT c.id, c.parent_id, 1::integer AS depth, ARRAY[c.id]::uuid[] AS visited_ids, false AS cycle
            FROM comment c
            WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid AND c.issue_id = $3::uuid
            UNION ALL
            SELECT parent.id, parent.parent_id, path.depth + 1, path.visited_ids || parent.id,
                   parent.id = ANY(path.visited_ids)
            FROM ancestor_path path
            JOIN comment parent ON parent.id = path.parent_id
            WHERE parent.workspace_id = $2::uuid AND parent.issue_id = $3::uuid
              AND path.depth <= $4 AND NOT path.cycle
        )
        SELECT id::text, parent_id::text, cycle
        FROM ancestor_path
        ORDER BY depth DESC",
    )
    .bind(anchor_comment_id)
    .bind(workspace_id)
    .bind(issue_id)
    .bind(SOURCE_CONTEXT_MAX_COMMENTS)
    .fetch_all(&mut *conn)
    .await
    .map_err(IssueCreateError::Database)?;
    if ancestors.is_empty()
        || ancestors
            .last()
            .and_then(|r| r.try_get::<String, _>(0).ok())
            != Some(anchor_comment_id.to_owned())
        || ancestors
            .first()
            .and_then(|r| r.try_get::<Option<String>, _>(1).ok())
            .flatten()
            .is_some()
    {
        return Err(IssueCreateError::SourceContextInvalidPath);
    }
    let mut seen = HashSet::new();
    for row in &ancestors {
        if row.try_get::<bool, _>(2)? {
            return Err(IssueCreateError::SourceContextInvalidPath);
        }
        let id: String = row.try_get(0)?;
        if !seen.insert(id) {
            return Err(IssueCreateError::SourceContextInvalidPath);
        }
    }
    ancestors[0]
        .try_get::<String, _>(0)
        .map_err(|_| IssueCreateError::SourceContextInvalidPath)
}

async fn lock_and_load_thread(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    root_id: &str,
    anchor_id: &str,
    anchor_created_at: &str,
) -> Result<Vec<LiveCommentRow>, IssueCreateError> {
    let rows = sqlx::query(
        "WITH RECURSIVE thread_history AS (
            SELECT root.*
            FROM comment root
            WHERE root.id = $3::uuid
              AND root.workspace_id = $1::uuid
              AND root.issue_id = $2::uuid
              AND root.parent_id IS NULL
              AND (root.created_at, root.id) <= ($5::text::timestamptz, $4::uuid)
            UNION ALL
            SELECT child.*
            FROM comment child
            JOIN thread_history parent ON child.parent_id = parent.id
            WHERE child.workspace_id = $1::uuid
              AND child.issue_id = $2::uuid
              AND (child.created_at, child.id) <= ($5::text::timestamptz, $4::uuid)
        )
        SELECT c.id::text, c.parent_id::text, c.type, c.content, c.author_type, c.author_id::text,
               to_char(c.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
               c.deleted_at IS NOT NULL
        FROM comment c
        WHERE c.id IN (SELECT id FROM thread_history)
        ORDER BY c.created_at, c.id
        LIMIT $6
        FOR UPDATE OF c",
    )
    .bind(workspace_id)
    .bind(issue_id)
    .bind(root_id)
    .bind(anchor_id)
    .bind(anchor_created_at)
    .bind(i64::from(SOURCE_CONTEXT_MAX_COMMENTS + 1))
    .fetch_all(&mut *conn)
    .await
    .map_err(IssueCreateError::Database)?;
    if rows.is_empty() {
        return Err(IssueCreateError::AnchorCommentDeleted);
    }
    if rows.last().map(|r| r.try_get::<String, _>(0).ok()) != Some(Some(anchor_id.to_owned())) {
        return Err(IssueCreateError::SourceContextInvalidPath);
    }
    if rows.len() > SOURCE_CONTEXT_MAX_COMMENTS as usize {
        return Err(IssueCreateError::SourceContextInvalidPath);
    }
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(LiveCommentRow {
            id: row.try_get(0)?,
            parent_id: row.try_get(1)?,
            comment_type: row.try_get(2)?,
            content: row.try_get(3)?,
            author_type: row.try_get(4)?,
            author_id: row.try_get(5)?,
            created_at: row.try_get(6)?,
            deleted: row.try_get(7)?,
        });
    }
    Ok(out)
}

fn compare_live_to_trust(
    snap: &TrustSnapshot,
    issue: &LiveSourceIssue,
    live: &[LiveCommentRow],
) -> Result<(), IssueCreateError> {
    if matches!(live.first(), Some(row) if row.parent_id.is_some()) {
        return Err(IssueCreateError::SourceContextInvalidPath);
    }
    if snap.source_issue.title != issue.title
        || snap.source_issue.number != issue.number
        || snap.source_issue.description != issue.description
        || !trust_timestamps_equal(&snap.source_issue.created_at, &issue.created_at)?
        || live.len() != snap.comment_thread.len()
    {
        return Err(IssueCreateError::SourceContextChanged);
    }
    for (trust, row) in snap.comment_thread.iter().zip(live.iter()) {
        if trust.id != row.id
            || trust.parent_id != row.parent_id
            || trust.comment_type != row.comment_type
            || trust.content != row.content
            || trust.author.author_type != row.author_type
            || trust.author.id != row.author_id
            || trust.deleted != row.deleted
            || !trust_timestamps_equal(&trust.created_at, &row.created_at)?
        {
            return Err(IssueCreateError::SourceContextChanged);
        }
    }
    Ok(())
}

fn validate_trust_ids(
    user_id: &str,
    request: &IssueCreateRequest,
    trust: &SourceContextCreateTrust,
) -> Result<(), IssueCreateError> {
    for id in [
        &trust.context_id,
        &trust.source_issue_id,
        &trust.anchor_comment_id,
        &trust.captured_by_user_id,
    ] {
        if !is_uuid(id) {
            return Err(IssueCreateError::InvalidInput);
        }
    }
    if request.creator_type != "member" || request.creator_id != user_id {
        return Err(IssueCreateError::InvalidInput);
    }
    if trust.captured_by_user_id != user_id {
        return Err(IssueCreateError::InvalidInput);
    }
    if request.parent_issue_id.as_deref() != Some(trust.source_issue_id.as_str()) {
        return Err(IssueCreateError::InvalidInput);
    }
    if request.project_id.is_some()
        || request.assignee_type.is_some()
        || request.assignee_id.is_some()
        || !request.attachment_ids.is_empty()
        || request.prepared_enqueue.is_some()
        || request.squad_create_trust.is_some()
    {
        return Err(IssueCreateError::InvalidInput);
    }
    Ok(())
}

pub async fn validate_source_context_create_trust(
    conn: &mut PgConnection,
    workspace_id: &str,
    user_id: &str,
    request: &IssueCreateRequest,
    trust: &SourceContextCreateTrust,
) -> Result<String, IssueCreateError> {
    validate_trust_ids(user_id, request, trust)?;
    let snap = parse_trust_snapshot(trust)?;
    validate_trust_snapshot_shape(trust, &snap)?;

    let issue =
        lock_and_load_source_issue(conn, workspace_id, user_id, &trust.source_issue_id).await?;

    let (anchor_issue_id, anchor_created_at) =
        load_anchor(conn, workspace_id, &trust.anchor_comment_id).await?;
    if anchor_issue_id != trust.source_issue_id {
        return Err(IssueCreateError::SourceContextInvalidPath);
    }

    let root_id = validate_ancestor_path(
        conn,
        workspace_id,
        &anchor_issue_id,
        &trust.anchor_comment_id,
    )
    .await?;

    let comment_ids: Vec<String> = snap.comment_thread.iter().map(|c| c.id.clone()).collect();
    if source_context_attachments_exist(conn, workspace_id, &trust.source_issue_id, &comment_ids)
        .await?
    {
        return Err(IssueCreateError::SourceContextChanged);
    }

    let live = lock_and_load_thread(
        conn,
        workspace_id,
        &anchor_issue_id,
        &root_id,
        &trust.anchor_comment_id,
        &anchor_created_at,
    )
    .await?;

    compare_live_to_trust(&snap, &issue, &live)?;
    let suffix = format!("-{}", issue.number);
    snap.source_issue
        .identifier
        .strip_suffix(&suffix)
        .map(str::to_owned)
        .ok_or(IssueCreateError::InvalidInput)
}

pub async fn insert_attached_source_context(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    child_issue_id: &str,
    trust: &SourceContextCreateTrust,
    expected_issue_prefix: &str,
) -> Result<(), IssueCreateError> {
    // The number allocator already holds the workspace row lock, so a prefix
    // change cannot invalidate the context between this check and commit.
    let result = sqlx::query(
        "INSERT INTO issue_source_context (
            id, workspace_id, issue_id, origin_task_id, source_issue_id,
            anchor_comment_id, captured_by_user_id, snapshot_version, snapshot,
            capture_digest, state, captured_at, attached_at
         ) SELECT
            $1::uuid, $2::uuid, $3::uuid, NULL, $4::uuid,
            $5::uuid, $6::uuid, $7, $8::jsonb, $9,
            'attached', $10::text::timestamptz, $10::text::timestamptz
         FROM workspace WHERE id = $2::uuid AND issue_prefix = $11",
    )
    .bind(&trust.context_id)
    .bind(workspace_id)
    .bind(child_issue_id)
    .bind(&trust.source_issue_id)
    .bind(&trust.anchor_comment_id)
    .bind(&trust.captured_by_user_id)
    .bind(SOURCE_CONTEXT_VERSION)
    .bind(&trust.snapshot)
    .bind(&trust.capture_digest)
    .bind(&trust.captured_at)
    .bind(expected_issue_prefix)
    .execute(&mut **tx)
    .await
    .map_err(|error| {
        if let sqlx::Error::Database(db) = &error
            && db.code().as_deref() == Some("23505")
        {
            return IssueCreateError::SourceContextAlreadyAttached;
        }
        IssueCreateError::Database(error)
    })?;
    if result.rows_affected() != 1 {
        return Err(IssueCreateError::SourceContextChanged);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn empty_captured_thread_is_rejected() {
        let mut trust = SourceContextCreateTrust {
            context_id: String::new(),
            source_issue_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa".to_owned(),
            anchor_comment_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb".to_owned(),
            capture_digest: "a".repeat(64),
            captured_by_user_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc".to_owned(),
            captured_at: "2026-11-26T03:00:00Z".to_owned(),
            snapshot: json!(null),
        };
        trust.snapshot = json!({
            "version": 1,
            "captured_by_user_id": trust.captured_by_user_id,
            "captured_at": trust.captured_at,
            "source_issue": {
                "id": trust.source_issue_id,
                "identifier": "TASK-1",
                "number": 1,
                "title": "Source task",
                "description": null,
                "created_at": trust.captured_at,
                "attachments": []
            },
            "anchor_comment_id": trust.anchor_comment_id,
            "comment_thread": []
        });
        let snapshot = parse_trust_snapshot(&trust).unwrap();
        assert!(matches!(
            validate_trust_snapshot_shape(&trust, &snapshot),
            Err(IssueCreateError::InvalidInput)
        ));
    }
}
