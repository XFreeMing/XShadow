ALTER TABLE chat_session ADD COLUMN IF NOT EXISTS is_goal_conversation boolean NOT NULL DEFAULT false;
ALTER TABLE goal ADD COLUMN IF NOT EXISTS source_chat_input_id uuid;
