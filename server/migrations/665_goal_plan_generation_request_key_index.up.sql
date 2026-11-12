-- request_key dedupe: a retried generation request resolves to the generation
-- already recorded for this goal. Unique per goal, not per workspace, so two
-- goals may legitimately reuse a client key.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_goal_plan_generation_request_key
    ON goal_plan_generation (workspace_id, goal_id, request_key);