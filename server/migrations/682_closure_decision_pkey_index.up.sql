-- Backing index for closure_decision's primary key, attached in 683.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS closure_decision_pkey_uidx
    ON closure_decision (id);