-- Goal plans are the editable draft produced from a one-sentence intent. They
-- are goal-scoped data, not a second execution queue: nothing here creates,
-- assigns or wakes an issue, and a draft node is only traceable through
-- goal_plan.content until DEV-7 converts it.

-- name: GetGoalPlan :one
SELECT * FROM goal_plan
WHERE workspace_id = $1 AND goal_id = $2;

-- name: GoalPlanExists :one
SELECT EXISTS (
    SELECT 1 FROM goal_plan
    WHERE workspace_id = $1 AND goal_id = $2
);

-- name: LockGoalPlanAdvisory :exec
-- Serializes plan edits, generation requests and generation results for one
-- goal. The key is namespaced so it cannot collide with unrelated advisory
-- locks; the hash keeps the two-argument form unnecessary.
SELECT pg_advisory_xact_lock(hashtextextended('goal_plan:' || sqlc.arg('goal_id')::text, 0));

-- name: CreateGoalPlan :one
-- The first save always starts at revision 1. The unique
-- (workspace_id, goal_id) index makes a concurrent create fail closed instead
-- of forking the draft.
INSERT INTO goal_plan (workspace_id, goal_id, project_id, revision, content, context_snapshot)
VALUES ($1, $2, $3, 1, $4, $5)
RETURNING *;

-- name: UpdateGoalPlanContent :one
-- Compare-and-swap on the revision: no row means the caller edited a stale
-- draft, and the handler answers 409 instead of overwriting newer input.
UPDATE goal_plan SET
    content = sqlc.arg('content')::jsonb,
    context_snapshot = sqlc.arg('context_snapshot')::jsonb,
    revision = revision + 1,
    updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND goal_id = sqlc.arg('goal_id')
  AND revision = sqlc.arg('expected_revision')
RETURNING *;

-- name: DeleteGoalPlansByGoal :exec
DELETE FROM goal_plan WHERE workspace_id = $1 AND goal_id = $2;

-- name: DeleteGoalPlansByProject :exec
DELETE FROM goal_plan WHERE workspace_id = $1 AND project_id = $2;

-- name: DeleteWorkspaceGoalPlans :exec
DELETE FROM goal_plan WHERE workspace_id = $1;

-- name: CreateGoalPlanGeneration :one
-- Every generation is one explicit user request: its request_key is unique per
-- goal, and base_revision records the draft it was allowed to build on.
INSERT INTO goal_plan_generation (
    workspace_id, goal_id, project_id, request_key, intent, agent_id,
    base_revision, state, input_context
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, 'preparing', $8
) RETURNING *;

-- name: GetGoalPlanGeneration :one
SELECT * FROM goal_plan_generation
WHERE id = $1 AND workspace_id = $2;

-- name: GetGoalPlanGenerationByRequestKey :one
SELECT * FROM goal_plan_generation
WHERE workspace_id = $1 AND goal_id = $2 AND request_key = $3;

-- name: GetGoalPlanGenerationByTask :one
-- The result endpoint resolves the generation from the authenticated task, so a
-- run can only ever write the plan it was started for.
SELECT * FROM goal_plan_generation
WHERE workspace_id = $1 AND task_id = $2;

-- name: SetGoalPlanGenerationRun :one
UPDATE goal_plan_generation SET
    chat_session_id = sqlc.arg('chat_session_id')::uuid,
    task_id = sqlc.arg('task_id')::uuid,
    state = 'queued',
    updated_at = now()
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id')
RETURNING *;

-- name: FailGoalPlanGeneration :exec
-- Only a generation that has not produced a result can be marked failed, so a
-- late failure report never erases a candidate that already arrived.
UPDATE goal_plan_generation SET
    state = 'failed',
    error = $3,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND state IN ('preparing', 'queued');

-- name: CompleteGoalPlanGeneration :one
-- Records the first result of a run. A candidate is kept even when the draft
-- moved on after the run started (base_revision < observed_revision): the
-- handler never auto-applies it, so newer edits are not overwritten. An empty
-- candidate records an explicit run failure instead. Only an unfinished
-- generation accepts a result, so a run that already reported stays reported
-- (a retry reads the stored row) and a failure is never silently upgraded.
UPDATE goal_plan_generation SET
    candidate = sqlc.narg('candidate')::jsonb,
    observed_revision = sqlc.arg('observed_revision'),
    state = CASE WHEN sqlc.narg('candidate')::jsonb IS NULL THEN 'failed' ELSE 'succeeded' END,
    error = sqlc.arg('error'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND state IN ('preparing', 'queued')
RETURNING *;

-- name: MarkGoalPlanGenerationApplied :exec
UPDATE goal_plan_generation SET
    applied = true,
    applied_revision = $3,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: CountActiveGoalPlanGenerations :one
-- A project move waits for in-flight planning runs: their input context names
-- the old project, so applying that candidate to a moved goal would mix bases.
SELECT count(*) FROM goal_plan_generation AS g
LEFT JOIN agent_task_queue AS t ON t.id = g.task_id
WHERE g.workspace_id = $1
  AND g.goal_id = $2
  AND g.state IN ('preparing', 'queued')
  AND (t.id IS NULL OR t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory'));

-- name: DeleteGoalPlanGenerationsByGoal :exec
DELETE FROM goal_plan_generation WHERE workspace_id = $1 AND goal_id = $2;

-- name: DeleteGoalPlanGenerationsByProject :exec
DELETE FROM goal_plan_generation WHERE workspace_id = $1 AND project_id = $2;

-- name: DeleteWorkspaceGoalPlanGenerations :exec
DELETE FROM goal_plan_generation WHERE workspace_id = $1;