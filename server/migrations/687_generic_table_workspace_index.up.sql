-- Workspace sheet list.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_generic_table_workspace ON generic_table (workspace_id, created_at DESC, id DESC);
