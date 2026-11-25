CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_type_workspace
    ON issue_type (workspace_id, position, LOWER(name));
