-- Backing index for goal_plan_generation's primary key, attached in 664.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS goal_plan_generation_pkey_uidx
    ON goal_plan_generation (id);