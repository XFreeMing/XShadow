CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_wakeup_member_choice ON issue_wakeup (workspace_id, (condition->>'issue_id')) WHERE enabled AND condition ? 'member_option';
