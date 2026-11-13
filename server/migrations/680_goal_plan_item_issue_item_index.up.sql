-- One issue per (goal, plan item). This is the idempotency key every
-- materialize trigger contends on; the second writer's insert is a no-op and
-- its caller reads the row the first writer committed.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_goal_plan_item_issue
    ON goal_plan_item_issue (goal_id, item_id);