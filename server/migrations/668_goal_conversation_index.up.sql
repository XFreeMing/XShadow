CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_goal_conversation_member ON chat_session (workspace_id, creator_id, agent_id) WHERE is_goal_conversation;
