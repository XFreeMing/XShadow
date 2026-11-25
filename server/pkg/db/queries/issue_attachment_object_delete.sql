-- name: AckIssueAttachmentObjectDeleteOutbox :execrows
DELETE FROM issue_attachment_object_delete_outbox
WHERE attachment_id = $1 AND delivery_lease_token = sqlc.arg(lease_token);

-- name: DeleteIssueAttachmentObjectDeleteOutboxByID :execrows
DELETE FROM issue_attachment_object_delete_outbox WHERE attachment_id = $1;

-- name: ClaimIssueAttachmentObjectDeleteLease :one
UPDATE issue_attachment_object_delete_outbox
SET delivery_lease_token = sqlc.arg(lease_token),
    next_attempt_at = now() + sqlc.arg(lease_seconds)::integer * interval '1 second'
WHERE attachment_id = (
    SELECT pending.attachment_id
    FROM issue_attachment_object_delete_outbox AS pending
    WHERE pending.attachment_id = sqlc.arg(attachment_id) AND pending.next_attempt_at <= now()
    FOR UPDATE SKIP LOCKED
)
RETURNING attachment_id, workspace_id, storage_key, object_url;

-- name: HasIssueAttachmentOnIssue :one
SELECT EXISTS (
    SELECT 1
    FROM attachment a
    WHERE a.workspace_id = sqlc.arg(workspace_id)
      AND a.issue_id = sqlc.arg(issue_id)
      AND a.comment_id IS NULL
      AND a.chat_session_id IS NULL
      AND a.chat_message_id IS NULL
      AND a.task_id IS NULL
      AND a.source_context_id IS NULL
) AS present;

-- name: HasCommentAttachmentsOnIssue :one
SELECT EXISTS (
    SELECT 1
    FROM attachment a
    INNER JOIN comment c ON c.id = a.comment_id
    WHERE c.issue_id = sqlc.arg(issue_id)
      AND a.workspace_id = sqlc.arg(workspace_id)
) AS present;

-- name: ListDirectIssueAttachmentsForDelete :many
SELECT * FROM attachment
WHERE workspace_id = sqlc.arg(workspace_id)
  AND issue_id = sqlc.arg(issue_id)
  AND comment_id IS NULL
  AND chat_session_id IS NULL
  AND chat_message_id IS NULL
  AND task_id IS NULL
  AND source_context_id IS NULL
ORDER BY id ASC;

-- name: ListDueIssueAttachmentObjectDeletes :many
SELECT attachment_id
FROM issue_attachment_object_delete_outbox
WHERE next_attempt_at <= now()
ORDER BY next_attempt_at, created_at, attachment_id
LIMIT sqlc.arg(batch_limit);

-- name: ReleaseIssueAttachmentObjectDeleteLease :exec
UPDATE issue_attachment_object_delete_outbox
SET delivery_lease_token = NULL,
    attempt_count = LEAST(attempt_count, 2147483646) + 1,
    next_attempt_at = now() +
        LEAST(attempt_count::bigint + 1, sqlc.arg(retry_cap_seconds)::integer) * interval '1 second',
    last_error = sqlc.narg(last_error)
WHERE attachment_id = $1 AND delivery_lease_token = sqlc.arg(lease_token);