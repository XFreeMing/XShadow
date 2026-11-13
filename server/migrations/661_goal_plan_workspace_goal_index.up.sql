-- One current plan per goal, and the lookup used by every plan request.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_goal_plan_workspace_goal
    ON goal_plan (workspace_id, goal_id);