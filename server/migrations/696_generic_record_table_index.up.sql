-- Records of one sheet.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_generic_record_table ON generic_record (workspace_id, table_id, created_at, id);
