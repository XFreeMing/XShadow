-- Backing index for generic_change's primary key, attached in 709.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_change_seq_uidx
    ON generic_change (workspace_id, table_id, seq);
