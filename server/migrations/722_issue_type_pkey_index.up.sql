-- Backing index for issue_type's primary key, attached in 723.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS issue_type_pkey_uidx
    ON issue_type (id);
