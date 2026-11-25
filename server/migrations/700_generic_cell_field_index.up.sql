-- Values of one field, used when its options change.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_generic_cell_field ON generic_cell (workspace_id, field_id);
