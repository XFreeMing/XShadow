-- name: GetGoalProblemSolving :one
SELECT revision, state FROM goal_problem_solving
WHERE workspace_id = $1 AND goal_id = $2;

-- name: SaveGoalProblemSolving :exec
-- The caller holds the goal row lock and has checked the expected revision.
INSERT INTO goal_problem_solving (workspace_id, goal_id, revision, state)
VALUES ($1, $2, $3, $4)
ON CONFLICT (workspace_id, goal_id) DO UPDATE
SET revision = EXCLUDED.revision, state = EXCLUDED.state;

-- name: AdvanceGoalPlanForCharter :exec
-- Keep the same graph and issue bindings. Drafts, reviews, and execution
-- grants issued against the prior charter need to be evaluated again.
UPDATE goal_plan
SET revision = revision + 1, updated_at = now(), execution_scope = NULL
WHERE workspace_id = $1 AND goal_id = $2;
