CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_type_ws_name
    ON issue_type (workspace_id, LOWER(name))
    WHERE archived_at IS NULL;
