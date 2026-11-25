-- Replay reads one record's changes in base-revision order.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_generic_change_record
    ON generic_change (workspace_id, table_id, record_id, base_revision);
