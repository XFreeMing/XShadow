-- Views of one sheet.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_generic_view_table ON generic_view (workspace_id, table_id, created_at, id);
