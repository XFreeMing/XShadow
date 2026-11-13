-- A repeated review request (same idempotency key) returns the original row
-- instead of recording a second verdict.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_closure_review_idempotency
    ON closure_review (workspace_id, idempotency_key)
    WHERE idempotency_key <> '';