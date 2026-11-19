-- name: ListWorkMethodTemplates :one
SELECT work_method_templates FROM workspace WHERE id = $1;

-- name: AddWorkMethodTemplate :execrows
UPDATE workspace SET work_method_templates = work_method_templates || jsonb_build_array(sqlc.arg('definition')::jsonb)
WHERE id = sqlc.arg('workspace_id')::uuid
  AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(work_method_templates) AS method
                  WHERE lower(method->>'name') = lower(sqlc.arg('name')::text));

-- name: GetIssueMethodGoal :one
SELECT g.id, g.title, g.intent, g.acceptance_criteria
FROM goal_issue gi JOIN goal g ON g.id = gi.goal_id AND g.workspace_id = gi.workspace_id
JOIN issue i ON i.id = gi.issue_id AND i.workspace_id = gi.workspace_id
WHERE gi.workspace_id = $1 AND gi.issue_id = $2
  AND (g.project_id IS NULL OR g.project_id = i.project_id);

-- name: SaveIssueWorkMethodRun :one
UPDATE issue SET work_method_run = sqlc.arg('run')::jsonb,
    revision = revision + 1, updated_at = now(),
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now())
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id')
  AND revision = sqlc.arg('issue_revision')
RETURNING *;
