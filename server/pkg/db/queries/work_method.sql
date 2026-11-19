-- name: ListWorkMethodTemplates :one
SELECT work_method_templates FROM workspace WHERE id = $1;

-- name: LockWorkMethodTemplates :one
SELECT work_method_templates FROM workspace WHERE id = $1 FOR UPDATE;

-- name: SaveWorkMethodTemplates :exec
UPDATE workspace SET work_method_templates = sqlc.arg('templates')::jsonb
WHERE id = sqlc.arg('workspace_id')::uuid;

-- name: GetIssueMethodGoal :one
SELECT g.id, g.title, g.intent, g.acceptance_criteria
FROM goal_issue gi JOIN goal g ON g.id = gi.goal_id AND g.workspace_id = gi.workspace_id
JOIN issue i ON i.id = gi.issue_id AND i.workspace_id = gi.workspace_id
WHERE gi.workspace_id = $1 AND gi.issue_id = $2
  AND (g.project_id IS NULL OR g.project_id = i.project_id);

-- name: SaveIssueWorkMethodRun :one
UPDATE issue AS i SET work_method_run = sqlc.arg('run')::jsonb,
    acceptance_criteria = COALESCE(sqlc.narg('trace')::jsonb, i.acceptance_criteria),
    status = COALESCE(sqlc.narg('board_status')::text, i.status),
    position = CASE WHEN sqlc.narg('board_status')::text IS NOT NULL AND sqlc.narg('board_status')::text <> i.status THEN (
        SELECT COALESCE(MIN(target.position), 0) - 1 FROM issue AS target
        WHERE target.workspace_id = i.workspace_id AND target.status = sqlc.narg('board_status')::text
    ) ELSE i.position END,
    assignee_type = COALESCE(sqlc.narg('assignee_type')::text, assignee_type),
    assignee_id = COALESCE(sqlc.narg('assignee_id')::uuid, assignee_id),
    revision = revision + 1, updated_at = now(),
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
WHERE i.id = sqlc.arg('id') AND i.workspace_id = sqlc.arg('workspace_id')
  AND i.revision = sqlc.arg('issue_revision')
RETURNING *;
