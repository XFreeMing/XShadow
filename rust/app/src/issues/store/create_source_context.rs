//! Manual source-context child creates: trusted Go snapshot + typed DB projection.

use chrono::{DateTime, Utc};
use serde::Deserialize;
use sqlx::{PgConnection, Postgres, Row, Transaction};
use std::collections::{HashMap, HashSet};

use super::create_write::{IssueCreateError, IssueCreateRequest};
use super::is_uuid;

pub const SOURCE_CONTEXT_VERSION: i16 = 1;
pub const SOURCE_CONTEXT_MAX_COMMENTS: i32 = 256;

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SourceContextCloneTrust {
    pub clone_id: String,
    pub source_attachment_id: String,
    pub filename: String,
    pub url: String,
    pub content_type: String,
    pub size_bytes: i64,
    pub storage_key: String,
}

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
    #[serde(default)]
    pub source_compare_snapshot: Option<serde_json::Value>,
    #[serde(default)]
    pub expected_source_project_id: Option<String>,
    #[serde(default)]
    pub clones: Vec<SourceContextCloneTrust>,
}

#[derive(Debug, Deserialize)]
struct SnapshotBody {
    source_issue: SnapshotIssue,
    comment_thread: Vec<SnapshotComment>,
    anchor_comment_id: String,
}

#[derive(Debug, Deserialize)]
struct TrustSnapshot {
    version: i16,
    captured_by_user_id: String,
    captured_at: String,
    #[serde(flatten)]
    body: SnapshotBody,
}

#[derive(Clone, Debug, Deserialize)]
struct SnapshotIssue {
    id: String,
    #[serde(default)]
    identifier: String,
    #[serde(default)]
    number: i32,
    title: String,
    description: Option<String>,
    created_at: String,
    #[serde(default)]
    attachments: Vec<SourceAttachment>,
}

#[derive(Clone, Debug, Deserialize)]
struct SnapshotComment {
    id: String,
    parent_id: Option<String>,
    #[serde(rename = "type")]
    comment_type: String,
    content: String,
    author: SnapshotAuthor,
    created_at: String,
    #[serde(default)]
    attachments: Vec<SourceAttachment>,
    #[serde(default)]
    deleted: bool,
}

#[derive(Debug, Deserialize, Clone)]
struct SourceAttachment {
    id: String,
    #[serde(default)]
    source_attachment_id: String,
    owner_type: String,
    owner_id: String,
    filename: String,
    content_type: String,
    size_bytes: i64,
    created_at: String,
}

#[derive(Clone, Debug, Deserialize)]
struct SnapshotAuthor {
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

struct LiveSourceAttachment {
    comment_id: Option<String>,
    filename: String,
    content_type: String,
    size_bytes: i64,
    created_at: String,
}

pub(super) struct ParsedTrust<'a> {
    frozen: TrustSnapshot,
    compare: Option<SnapshotBody>,
    clones: &'a [SourceContextCloneTrust],
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

fn attachment_source_id(item: &SourceAttachment) -> &str {
    if !item.source_attachment_id.is_empty() {
        &item.source_attachment_id
    } else {
        &item.id
    }
}

fn canonical_clone_storage_key(workspace_id: &str, clone_id: &str, filename: &str) -> String {
    // Go path.Ext keeps the final dot and suffix, including for dotfiles.
    let basename = filename.rsplit('/').next().unwrap_or(filename);
    let ext = basename.rfind('.').map_or("", |index| &basename[index..]);
    format!("workspaces/{workspace_id}/source-context/{clone_id}{ext}")
}

fn parse_trust_snapshot(
    trust: &SourceContextCreateTrust,
) -> Result<TrustSnapshot, IssueCreateError> {
    TrustSnapshot::deserialize(&trust.snapshot).map_err(|_| IssueCreateError::InvalidInput)
}

fn parse_compare_body(raw: &serde_json::Value) -> Result<SnapshotBody, IssueCreateError> {
    SnapshotBody::deserialize(raw).map_err(|_| IssueCreateError::InvalidInput)
}

fn parse_trust_inputs(
    trust: &SourceContextCreateTrust,
) -> Result<ParsedTrust<'_>, IssueCreateError> {
    let frozen = parse_trust_snapshot(trust)?;
    let with_files = !trust.clones.is_empty();
    let compare = if with_files {
        Some(parse_compare_body(
            trust
                .source_compare_snapshot
                .as_ref()
                .ok_or(IssueCreateError::InvalidInput)?,
        )?)
    } else if trust.source_compare_snapshot.is_some() {
        return Err(IssueCreateError::InvalidInput);
    } else {
        None
    };
    Ok(ParsedTrust {
        frozen,
        compare,
        clones: &trust.clones,
    })
}

fn validate_thread_boundaries(
    body: &SnapshotBody,
    anchor_id: &str,
) -> Result<(), IssueCreateError> {
    if body
        .comment_thread
        .first()
        .is_none_or(|comment| comment.parent_id.is_some())
        || body.comment_thread.last().map(|c| c.id.as_str()) != Some(anchor_id)
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if body.comment_thread.len() > SOURCE_CONTEXT_MAX_COMMENTS as usize {
        return Err(IssueCreateError::InvalidInput);
    }
    if body
        .comment_thread
        .iter()
        .any(|comment| !is_uuid(&comment.id))
    {
        return Err(IssueCreateError::InvalidInput);
    }
    Ok(())
}

fn index_attachments_by_source(
    body: &SnapshotBody,
    frozen: bool,
) -> Result<HashMap<&str, &SourceAttachment>, IssueCreateError> {
    let mut out = HashMap::new();
    let stacks = std::iter::once((
        "issue",
        body.source_issue.id.as_str(),
        body.source_issue.attachments.as_slice(),
    ))
    .chain(body.comment_thread.iter().map(|comment| {
        (
            "comment",
            comment.id.as_str(),
            comment.attachments.as_slice(),
        )
    }));
    for (owner_type, owner_id, attachments) in stacks {
        for item in attachments {
            let source_id = attachment_source_id(item);
            if !is_uuid(source_id)
                || !is_uuid(&item.id)
                || item.owner_type != owner_type
                || item.owner_id != owner_id
                || frozen == item.source_attachment_id.is_empty()
            {
                return Err(IssueCreateError::InvalidInput);
            }
            if out.insert(source_id, item).is_some() {
                return Err(IssueCreateError::InvalidInput);
            }
        }
    }
    Ok(out)
}

fn validate_frozen_shape(
    workspace_id: &str,
    trust: &SourceContextCreateTrust,
    parsed: &ParsedTrust<'_>,
) -> Result<(), IssueCreateError> {
    validate_capture_digest(&trust.capture_digest)?;
    let snap = &parsed.frozen;
    if snap.version != SOURCE_CONTEXT_VERSION {
        return Err(IssueCreateError::InvalidInput);
    }
    if snap.captured_by_user_id != trust.captured_by_user_id
        || snap.body.source_issue.id != trust.source_issue_id
        || snap.body.anchor_comment_id != trust.anchor_comment_id
        || !trust_timestamps_equal(&snap.captured_at, &trust.captured_at)?
    {
        return Err(IssueCreateError::InvalidInput);
    }
    validate_thread_boundaries(&snap.body, &trust.anchor_comment_id)?;
    if parsed.clones.is_empty() {
        if !snap.body.source_issue.attachments.is_empty()
            || snap
                .body
                .comment_thread
                .iter()
                .any(|c| !c.attachments.is_empty())
        {
            return Err(IssueCreateError::InvalidInput);
        }
        return Ok(());
    }
    let compare = parsed
        .compare
        .as_ref()
        .ok_or(IssueCreateError::InvalidInput)?;
    if compare.anchor_comment_id != trust.anchor_comment_id
        || compare.source_issue.id != trust.source_issue_id
        || compare.source_issue.identifier != snap.body.source_issue.identifier
    {
        return Err(IssueCreateError::InvalidInput);
    }
    validate_thread_boundaries(compare, &trust.anchor_comment_id)?;
    validate_clone_provenance(workspace_id, &snap.body, compare, parsed.clones)
}

fn validate_clone_provenance(
    workspace_id: &str,
    frozen: &SnapshotBody,
    compare: &SnapshotBody,
    clones: &[SourceContextCloneTrust],
) -> Result<(), IssueCreateError> {
    let frozen_by_source = index_attachments_by_source(frozen, true)?;
    let compare_by_source = index_attachments_by_source(compare, false)?;
    if frozen_by_source.len() != clones.len() || compare_by_source.len() != clones.len() {
        return Err(IssueCreateError::InvalidInput);
    }
    let mut seen_sources = HashSet::new();
    let mut seen_clones = HashSet::new();
    for clone in clones {
        if !is_uuid(&clone.clone_id)
            || !is_uuid(&clone.source_attachment_id)
            || clone.storage_key.is_empty()
        {
            return Err(IssueCreateError::InvalidInput);
        }
        if canonical_clone_storage_key(workspace_id, &clone.clone_id, &clone.filename)
            != clone.storage_key
        {
            return Err(IssueCreateError::InvalidInput);
        }
        if !seen_sources.insert(clone.source_attachment_id.clone())
            || !seen_clones.insert(clone.clone_id.clone())
        {
            return Err(IssueCreateError::InvalidInput);
        }
        let Some(frozen_att) = frozen_by_source.get(clone.source_attachment_id.as_str()) else {
            return Err(IssueCreateError::InvalidInput);
        };
        if frozen_att.id != clone.clone_id
            || frozen_att.filename != clone.filename
            || frozen_att.content_type != clone.content_type
            || frozen_att.size_bytes != clone.size_bytes
        {
            return Err(IssueCreateError::InvalidInput);
        }
        let Some(compare_att) = compare_by_source.get(clone.source_attachment_id.as_str()) else {
            return Err(IssueCreateError::InvalidInput);
        };
        if compare_att.filename != clone.filename
            || compare_att.content_type != clone.content_type
            || compare_att.size_bytes != clone.size_bytes
            || compare_att.owner_type != frozen_att.owner_type
            || compare_att.owner_id != frozen_att.owner_id
            || !trust_timestamps_equal(&compare_att.created_at, &frozen_att.created_at)?
        {
            return Err(IssueCreateError::InvalidInput);
        }
    }
    Ok(())
}

fn compare_live_content(
    body: &SnapshotBody,
    issue: &LiveSourceIssue,
    live: &[LiveCommentRow],
) -> Result<(), IssueCreateError> {
    if matches!(live.first(), Some(row) if row.parent_id.is_some()) {
        return Err(IssueCreateError::SourceContextInvalidPath);
    }
    if body.source_issue.title != issue.title
        || body.source_issue.number != issue.number
        || body.source_issue.description != issue.description
        || !trust_timestamps_equal(&body.source_issue.created_at, &issue.created_at)?
        || live.len() != body.comment_thread.len()
    {
        return Err(IssueCreateError::SourceContextChanged);
    }
    for (trust, row) in body.comment_thread.iter().zip(live.iter()) {
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

async fn load_live_source_attachments(
    conn: &mut PgConnection,
    workspace_id: &str,
    issue_id: &str,
    comment_ids: &[String],
) -> Result<HashMap<String, LiveSourceAttachment>, IssueCreateError> {
    let rows = sqlx::query(
        "SELECT a.id::text, a.comment_id::text, a.filename, a.content_type, a.size_bytes,
                to_char(a.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')
         FROM attachment a
         WHERE a.workspace_id = $1::uuid
           AND a.issue_id = $2::uuid
           AND a.source_context_id IS NULL
           AND (
                a.comment_id IS NULL
                OR a.comment_id = ANY($3::text[]::uuid[])
           )",
    )
    .bind(workspace_id)
    .bind(issue_id)
    .bind(comment_ids)
    .fetch_all(&mut *conn)
    .await
    .map_err(IssueCreateError::Database)?;
    let mut out = HashMap::with_capacity(rows.len());
    for row in rows {
        let id: String = row.try_get(0)?;
        if out.contains_key(&id) {
            return Err(IssueCreateError::SourceContextChanged);
        }
        out.insert(
            id,
            LiveSourceAttachment {
                comment_id: row.try_get(1)?,
                filename: row.try_get(2)?,
                content_type: row.try_get(3)?,
                size_bytes: row.try_get(4)?,
                created_at: row.try_get(5)?,
            },
        );
    }
    Ok(out)
}

fn compare_attachments_by_source_id(
    expected: &HashMap<&str, &SourceAttachment>,
    live: &HashMap<String, LiveSourceAttachment>,
) -> Result<(), IssueCreateError> {
    if expected.len() != live.len() {
        return Err(IssueCreateError::SourceContextChanged);
    }
    for (source_id, trust) in expected {
        let Some(row) = live.get(*source_id) else {
            return Err(IssueCreateError::SourceContextChanged);
        };
        if trust.filename != row.filename
            || trust.content_type != row.content_type
            || trust.size_bytes != row.size_bytes
            || !trust_timestamps_equal(&trust.created_at, &row.created_at)?
        {
            return Err(IssueCreateError::SourceContextChanged);
        }
        let owner_ok = match trust.owner_type.as_str() {
            "issue" => row.comment_id.is_none(),
            "comment" => row.comment_id.as_deref() == Some(trust.owner_id.as_str()),
            _ => false,
        };
        if !owner_ok {
            return Err(IssueCreateError::SourceContextChanged);
        }
    }
    Ok(())
}

async fn load_locked_source_issue(
    conn: &mut PgConnection,
    workspace_id: &str,
    source_issue_id: &str,
    effective_project: Option<&str>,
) -> Result<LiveSourceIssue, IssueCreateError> {
    let row = sqlx::query(
        "SELECT i.title, i.number, i.description,
                to_char(i.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
                i.project_id::text
         FROM issue i
         WHERE i.id = $1::uuid AND i.workspace_id = $2::uuid",
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
    if project_id.as_deref() != effective_project {
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
    Ok((row.try_get(0)?, row.try_get(1)?))
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
    if request.project_id.is_some() {
        return Err(IssueCreateError::InvalidInput);
    }
    if request.assignee_type.is_some()
        || request.assignee_id.is_some()
        || !request.attachment_ids.is_empty()
        || request.prepared_enqueue.is_some()
        || request.squad_create_trust.is_some()
    {
        return Err(IssueCreateError::InvalidInput);
    }
    if !trust.clones.is_empty() {
        for clone in &trust.clones {
            if !is_uuid(&clone.clone_id) || !is_uuid(&clone.source_attachment_id) {
                return Err(IssueCreateError::InvalidInput);
            }
        }
    } else if trust.source_compare_snapshot.is_some() {
        return Err(IssueCreateError::InvalidInput);
    }
    Ok(())
}

pub fn prepare_source_context_create_trust<'a>(
    workspace_id: &str,
    user_id: &str,
    request: &IssueCreateRequest,
    trust: &'a SourceContextCreateTrust,
) -> Result<ParsedTrust<'a>, IssueCreateError> {
    validate_trust_ids(user_id, request, trust)?;
    let parsed = parse_trust_inputs(trust)?;
    validate_frozen_shape(workspace_id, trust, &parsed)?;
    Ok(parsed)
}

pub fn reconcile_source_context_project(
    trust: &SourceContextCreateTrust,
    effective_project: Option<&str>,
) -> Result<(), IssueCreateError> {
    if trust.expected_source_project_id.as_deref() != effective_project {
        return Err(IssueCreateError::SourceContextChanged);
    }
    Ok(())
}

pub async fn validate_source_context_create_trust(
    conn: &mut PgConnection,
    workspace_id: &str,
    trust: &SourceContextCreateTrust,
    parsed: &ParsedTrust<'_>,
    effective_project: Option<&str>,
) -> Result<String, IssueCreateError> {
    let issue = load_locked_source_issue(
        conn,
        workspace_id,
        &trust.source_issue_id,
        effective_project,
    )
    .await?;

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

    let content_body = parsed.compare.as_ref().unwrap_or(&parsed.frozen.body);
    let comment_ids: Vec<String> = content_body
        .comment_thread
        .iter()
        .map(|c| c.id.clone())
        .collect();

    if !parsed.clones.is_empty() {
        let compare = parsed
            .compare
            .as_ref()
            .ok_or(IssueCreateError::InvalidInput)?;
        let expected = index_attachments_by_source(compare, false)?;
        let live_attachments =
            load_live_source_attachments(conn, workspace_id, &trust.source_issue_id, &comment_ids)
                .await?;
        compare_attachments_by_source_id(&expected, &live_attachments)?;
    } else {
        let live_attachments =
            load_live_source_attachments(conn, workspace_id, &trust.source_issue_id, &comment_ids)
                .await?;
        if !live_attachments.is_empty() {
            return Err(IssueCreateError::SourceContextChanged);
        }
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

    compare_live_content(content_body, &issue, &live)?;

    let suffix = format!("-{}", issue.number);
    let prefix = parsed
        .frozen
        .body
        .source_issue
        .identifier
        .strip_suffix(&suffix)
        .map(str::to_owned)
        .ok_or(IssueCreateError::InvalidInput)?;
    Ok(prefix)
}

async fn consume_source_context_intents(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    context_id: &str,
    clones: &[SourceContextCloneTrust],
) -> Result<(), IssueCreateError> {
    if clones.is_empty() {
        return Ok(());
    }
    // Inspect the actual consumed set. Any mismatch rolls back this delete
    // along with the child, context, and cloned attachment rows.
    let rows = sqlx::query(
        "DELETE FROM issue_source_context_object_intent
         WHERE workspace_id = $1::uuid AND source_context_id = $2::uuid
         RETURNING storage_key, attachment_id::text, object_url, state,
                   lease_token IS NOT NULL OR lease_expires_at IS NOT NULL",
    )
    .bind(workspace_id)
    .bind(context_id)
    .fetch_all(&mut **tx)
    .await?;
    if rows.len() != clones.len() {
        return Err(IssueCreateError::SourceContextChanged);
    }
    let expected: HashMap<_, _> = clones
        .iter()
        .map(|clone| (clone.storage_key.as_str(), clone))
        .collect();
    for row in rows {
        let key: &str = row.try_get(0)?;
        let attachment_id: &str = row.try_get(1)?;
        let url: &str = row.try_get(2)?;
        let state: &str = row.try_get(3)?;
        let leased: bool = row.try_get(4)?;
        if !expected
            .get(key)
            .is_some_and(|clone| clone.clone_id == attachment_id && clone.url == url)
            || state != "pending"
            || leased
        {
            return Err(IssueCreateError::SourceContextChanged);
        }
    }
    Ok(())
}

async fn insert_source_context_clones(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    context_id: &str,
    captured_by_user_id: &str,
    clones: &[SourceContextCloneTrust],
) -> Result<(), IssueCreateError> {
    for clone in clones {
        let result = sqlx::query(
            "INSERT INTO attachment (
                id, workspace_id, source_context_id, uploader_type, uploader_id,
                filename, url, content_type, size_bytes
             ) VALUES (
                $1::uuid, $2::uuid, $3::uuid, 'member', $4::uuid,
                $5, $6, $7, $8
             )",
        )
        .bind(&clone.clone_id)
        .bind(workspace_id)
        .bind(context_id)
        .bind(captured_by_user_id)
        .bind(&clone.filename)
        .bind(&clone.url)
        .bind(&clone.content_type)
        .bind(clone.size_bytes)
        .execute(&mut **tx)
        .await
        .map_err(IssueCreateError::Database)?;
        if result.rows_affected() != 1 {
            return Err(IssueCreateError::SourceContextChanged);
        }
    }
    Ok(())
}

pub async fn insert_attached_source_context(
    tx: &mut Transaction<'_, Postgres>,
    workspace_id: &str,
    child_issue_id: &str,
    trust: &SourceContextCreateTrust,
    expected_issue_prefix: &str,
) -> Result<(), IssueCreateError> {
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

    insert_source_context_clones(
        tx,
        workspace_id,
        &trust.context_id,
        &trust.captured_by_user_id,
        &trust.clones,
    )
    .await?;
    consume_source_context_intents(tx, workspace_id, &trust.context_id, &trust.clones).await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn clone_storage_key_keeps_dotfile_suffix() {
        assert_eq!(
            canonical_clone_storage_key("workspace", "clone", ".source-snapshot"),
            "workspaces/workspace/source-context/clone.source-snapshot"
        );
    }

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
            source_compare_snapshot: None,
            expected_source_project_id: None,
            clones: Vec::new(),
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
        let parsed = parse_trust_inputs(&trust).unwrap();
        assert!(matches!(
            validate_frozen_shape("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", &trust, &parsed),
            Err(IssueCreateError::InvalidInput)
        ));
    }
}
