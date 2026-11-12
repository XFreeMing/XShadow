-- Backing index for goal_issue's primary key, attached in 656.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS goal_issue_pkey_uidx
    ON goal_issue (workspace_id, issue_id);
