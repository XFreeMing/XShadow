-- Backing index for generic_presence's primary key, attached in 713.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_presence_actor_uidx
    ON generic_presence (workspace_id, table_id, actor_id);
