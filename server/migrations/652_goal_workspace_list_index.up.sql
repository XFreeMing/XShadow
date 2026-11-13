-- Serves the workspace-wide goal list (created_at DESC, id DESC paging).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_goal_workspace_created
    ON goal (workspace_id, created_at DESC, id DESC);
