-- name: ListIssueTypes :many
SELECT *
FROM issue_type
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
ORDER BY position ASC, LOWER(name) ASC;

-- name: GetIssueType :one
SELECT *
FROM issue_type
WHERE id = $1 AND workspace_id = $2;

-- name: CountActiveIssueTypes :one
SELECT COUNT(*)
FROM issue_type
WHERE workspace_id = $1 AND archived_at IS NULL;

-- name: LockWorkspaceForIssueTypeCreate :one
SELECT id FROM workspace WHERE id = $1 FOR KEY SHARE;

-- name: CreateIssueType :one
INSERT INTO issue_type (workspace_id, name, fields, position)
SELECT sqlc.arg('workspace_id')::uuid,
       sqlc.arg('name')::text,
       sqlc.arg('fields')::jsonb,
       COALESCE((SELECT MAX(position) FROM issue_type WHERE workspace_id = sqlc.arg('workspace_id')::uuid), 0) + 1
RETURNING *;

-- name: UpdateIssueType :one
UPDATE issue_type SET
    name = COALESCE(sqlc.narg('name'), name),
    fields = COALESCE(sqlc.narg('fields'), fields),
    archived_at = CASE WHEN sqlc.arg('archived_set')::bool THEN sqlc.narg('archived_at') ELSE archived_at END,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;
