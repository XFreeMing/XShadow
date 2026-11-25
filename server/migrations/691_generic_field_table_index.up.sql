-- Fields of one sheet in creation order.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_generic_field_table ON generic_field (workspace_id, table_id, position, id);
