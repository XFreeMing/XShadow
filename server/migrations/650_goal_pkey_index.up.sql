-- Backing index for goal's primary key, attached in 651.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS goal_pkey_uidx
    ON goal (id);
