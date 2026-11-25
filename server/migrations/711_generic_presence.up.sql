-- Short lease for who is active on one generic sheet. Expired rows stay
-- until the next heartbeat replaces them; readers filter on lease_until.
-- No foreign keys. The primary key is attached in 713.
CREATE TABLE IF NOT EXISTS generic_presence (
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    actor_id UUID NOT NULL,
    actor_source TEXT NOT NULL,
    task_id UUID,
    actor_label TEXT NOT NULL,
    record_id UUID,
    field_id UUID,
    lease_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT generic_presence_source_check CHECK (actor_source IN ('agent', 'member')),
    CONSTRAINT generic_presence_label_check CHECK (char_length(actor_label) BETWEEN 1 AND 40),
    CONSTRAINT generic_presence_position_check CHECK (field_id IS NULL OR record_id IS NOT NULL)
);
