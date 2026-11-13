-- Backing index for goal_plan_item_issue's primary key, attached in 679.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS goal_plan_item_issue_pkey_uidx
    ON goal_plan_item_issue (id);