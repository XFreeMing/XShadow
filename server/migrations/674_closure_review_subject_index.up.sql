-- Lookup used by every closure evaluation: the newest verdict for one subject.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_closure_review_subject
    ON closure_review (workspace_id, subject_kind, subject_id, recorded_at DESC);