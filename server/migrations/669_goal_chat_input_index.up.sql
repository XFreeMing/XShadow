CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_goal_chat_input ON goal (source_chat_input_id) WHERE source_chat_input_id IS NOT NULL;
