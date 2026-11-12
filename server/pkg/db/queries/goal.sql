-- name: ListGoals :many
-- Workspace-level goal list. project_id narrows to one project; standalone_only
-- narrows to goals with no project ("project_id=none" on the wire). Hidden
-- private projects are excluded the same way ListIssues excludes them.
SELECT * FROM goal
WHERE workspace_id = sqlc.arg('workspace_id')
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('standalone_only')::boolean IS NOT TRUE OR project_id IS NULL)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('hidden_project_ids')::uuid[] IS NULL
       OR project_id IS NULL
       OR NOT (project_id = ANY(sqlc.narg('hidden_project_ids')::uuid[])))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountGoals :one
-- Same filters as ListGoals, so a page's total never counts an invisible goal.
SELECT count(*) FROM goal
WHERE workspace_id = sqlc.arg('workspace_id')
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('standalone_only')::boolean IS NOT TRUE OR project_id IS NULL)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('hidden_project_ids')::uuid[] IS NULL
       OR project_id IS NULL
       OR NOT (project_id = ANY(sqlc.narg('hidden_project_ids')::uuid[])));

-- name: GetGoalInWorkspace :one
SELECT * FROM goal
WHERE id = $1 AND workspace_id = $2;

-- name: LockGoalForUpdate :one
-- Serializes goal deletion / project moves with relation writes, so a link
-- cannot commit against a goal that is going away and a project move cannot
-- leave a link behind.
SELECT * FROM goal
WHERE id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: LockGoalForLink :one
-- Relation writes take a shared lock on the goal row: concurrent links to the
-- same goal still run together, while goal deletion and project moves (FOR
-- UPDATE) serialize with them.
SELECT id, project_id FROM goal
WHERE id = $1 AND workspace_id = $2
FOR SHARE;

-- name: CreateGoal :one
INSERT INTO goal (
    workspace_id, project_id, title, intent, scope, acceptance_criteria,
    owner_type, owner_id, status, creator_type, creator_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
) RETURNING *;

-- name: UpdateGoal :one
UPDATE goal SET
    title = COALESCE(sqlc.narg('title')::text, title),
    intent = CASE WHEN sqlc.arg('intent_set')::boolean THEN sqlc.narg('intent')::text ELSE intent END,
    scope = CASE WHEN sqlc.arg('scope_set')::boolean THEN sqlc.narg('scope')::text ELSE scope END,
    acceptance_criteria = CASE WHEN sqlc.arg('acceptance_criteria_set')::boolean
        THEN sqlc.narg('acceptance_criteria')::text ELSE acceptance_criteria END,
    status = COALESCE(sqlc.narg('status')::text, status),
    owner_type = CASE WHEN sqlc.arg('owner_set')::boolean THEN sqlc.narg('owner_type')::text ELSE owner_type END,
    owner_id = CASE WHEN sqlc.arg('owner_set')::boolean THEN sqlc.narg('owner_id')::uuid ELSE owner_id END,
    project_id = CASE WHEN sqlc.arg('project_id_set')::boolean THEN sqlc.narg('project_id')::uuid ELSE project_id END,
    updated_at = now()
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id')
RETURNING *;

-- name: DeleteGoal :exec
DELETE FROM goal WHERE id = $1 AND workspace_id = $2;

-- name: DeleteGoalIssuesByGoal :exec
DELETE FROM goal_issue WHERE workspace_id = $1 AND goal_id = $2;

-- name: DeleteGoalIssuesByIssue :execrows
-- Issue deletion (single or batch) clears the task's link; the goal survives.
DELETE FROM goal_issue WHERE workspace_id = $1 AND issue_id = $2;

-- name: DeleteGoalIssuesByProject :exec
-- Project deletion clears the links of every goal in that project. Callers
-- lock the goals first (LockGoalsByProjectForDelete) so a concurrent link
-- cannot slip in between this statement and DeleteGoalsByProject.
DELETE FROM goal_issue
WHERE goal_issue.workspace_id = sqlc.arg('workspace_id')
  AND goal_id IN (
    SELECT id FROM goal
    WHERE goal.workspace_id = sqlc.arg('workspace_id') AND project_id = sqlc.arg('project_id')
  );

-- name: LockGoalsByProjectForDelete :many
SELECT id FROM goal
WHERE workspace_id = $1 AND project_id = $2
FOR UPDATE;

-- name: DeleteGoalsByProject :exec
DELETE FROM goal WHERE workspace_id = $1 AND project_id = $2;

-- name: CountGoalIssues :one
SELECT count(*) FROM goal_issue WHERE workspace_id = $1 AND goal_id = $2;

-- name: LockIssueForGoalLink :one
-- Same row lock family as issue updates / deletes: a concurrent project move
-- (FOR UPDATE in UpdateIssue) or delete serializes with the link write.
SELECT id, project_id FROM issue
WHERE id = $1 AND workspace_id = $2
FOR SHARE;

-- name: LinkGoalIssue :one
-- Idempotent: a repeat link of the same pair returns no row; the handler then
-- reads the existing link to distinguish "already this goal" (204) from
-- "belongs to another goal" (409).
INSERT INTO goal_issue (workspace_id, goal_id, issue_id)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, issue_id) DO NOTHING
RETURNING issue_id;

-- name: GetGoalIssueLink :one
SELECT * FROM goal_issue
WHERE workspace_id = $1 AND issue_id = $2;

-- name: UnlinkGoalIssue :execrows
DELETE FROM goal_issue
WHERE workspace_id = $1 AND goal_id = $2 AND issue_id = $3;

-- name: ListGoalIssues :many
-- The goal's linked issues in link order. Kept single-table so the row type is
-- db.Issue and the handler reuses issueToResponse.
SELECT i.* FROM issue AS i
WHERE i.workspace_id = sqlc.arg('workspace_id')
  AND EXISTS (
    SELECT 1 FROM goal_issue AS gi
    WHERE gi.workspace_id = i.workspace_id
      AND gi.goal_id = sqlc.arg('goal_id')
      AND gi.issue_id = i.id
  )
  AND (sqlc.narg('hidden_project_ids')::uuid[] IS NULL
       OR i.project_id IS NULL
       OR NOT (i.project_id = ANY(sqlc.narg('hidden_project_ids')::uuid[])))
ORDER BY (
    SELECT gi.created_at FROM goal_issue AS gi
    WHERE gi.workspace_id = i.workspace_id
      AND gi.goal_id = sqlc.arg('goal_id')
      AND gi.issue_id = i.id
) ASC, i.id ASC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountGoalIssuesVisible :one
SELECT count(*) FROM issue AS i
WHERE i.workspace_id = sqlc.arg('workspace_id')
  AND EXISTS (
    SELECT 1 FROM goal_issue AS gi
    WHERE gi.workspace_id = i.workspace_id
      AND gi.goal_id = sqlc.arg('goal_id')
      AND gi.issue_id = i.id
  )
  AND (sqlc.narg('hidden_project_ids')::uuid[] IS NULL
       OR i.project_id IS NULL
       OR NOT (i.project_id = ANY(sqlc.narg('hidden_project_ids')::uuid[])));
