-- Backing index for closure_review's primary key, attached in 673.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS closure_review_pkey_uidx
    ON closure_review (id);