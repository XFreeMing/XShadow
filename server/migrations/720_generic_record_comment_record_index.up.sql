-- Ordered per-record thread reads and monotonic sequence assignment.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_generic_record_comment_record
    ON generic_record_comment (workspace_id, table_id, record_id, seq);
