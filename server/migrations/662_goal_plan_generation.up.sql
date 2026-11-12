-- One planning run request. A generation is the durable link between a chat
-- session (the existing planning-run chain), the agent task it created, and
-- the goal plan it may produce. It never creates or assigns a product issue.
--
-- request_key is the client-generated retry key: the unique index in 665 makes
-- a retried request return the original generation instead of starting a
-- second run. `candidate` holds a plan that arrived after the draft moved on
-- (base_revision no longer current) so the user can compare it instead of
-- losing it or silently overwriting newer edits.
--
-- The primary key is built CONCURRENTLY in 663/664.
CREATE TABLE IF NOT EXISTS goal_plan_generation (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    goal_id UUID NOT NULL,
    project_id UUID NOT NULL,
    request_key TEXT NOT NULL,
    intent TEXT NOT NULL,
    agent_id UUID NOT NULL,
    base_revision INTEGER NOT NULL DEFAULT 0 CHECK (base_revision >= 0),
    state TEXT NOT NULL DEFAULT 'preparing'
        CHECK (state IN ('preparing', 'queued', 'succeeded', 'failed')),
    chat_session_id UUID,
    task_id UUID,
    input_context JSONB NOT NULL DEFAULT '{}'::jsonb,
    candidate JSONB,
    applied BOOLEAN NOT NULL DEFAULT false,
    applied_revision INTEGER,
    observed_revision INTEGER,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);