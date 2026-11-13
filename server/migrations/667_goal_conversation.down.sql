ALTER TABLE goal DROP COLUMN IF EXISTS source_chat_input_id;
ALTER TABLE chat_session DROP COLUMN IF EXISTS is_goal_conversation;
