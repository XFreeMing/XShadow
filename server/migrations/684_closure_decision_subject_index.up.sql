-- Newest-first read of one subject's decision history.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_closure_decision_subject
    ON closure_decision (workspace_id, subject_kind, subject_id, created_at DESC);