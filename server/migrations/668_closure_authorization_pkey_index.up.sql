-- Backing index for closure_authorization's primary key, attached in 669.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS closure_authorization_pkey_uidx
    ON closure_authorization (id);