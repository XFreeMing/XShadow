-- Serves the project goal view and the project-deletion sweep.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_goal_workspace_project_created
    ON goal (workspace_id, project_id, created_at DESC, id DESC);
