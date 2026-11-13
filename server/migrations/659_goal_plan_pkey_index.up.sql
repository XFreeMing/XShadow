-- Backing index for goal_plan's primary key, attached in 660.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS goal_plan_pkey_uidx
    ON goal_plan (id);