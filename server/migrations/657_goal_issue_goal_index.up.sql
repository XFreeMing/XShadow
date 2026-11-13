-- Serves the goal detail issue list (ordered by link time) and goal deletion.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_goal_issue_workspace_goal
    ON goal_issue (workspace_id, goal_id, created_at, issue_id);
