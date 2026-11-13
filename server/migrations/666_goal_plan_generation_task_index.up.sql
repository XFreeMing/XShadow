-- A task token reports one result: a partial unique index keeps a duplicated
-- or retried daemon callback from attaching a second generation to the task.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_goal_plan_generation_task
    ON goal_plan_generation (task_id)
    WHERE task_id IS NOT NULL;