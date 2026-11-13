-- Lookup used by every closure evaluation: active grants for one subject.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_closure_authorization_subject
    ON closure_authorization (workspace_id, subject_kind, subject_id, state);