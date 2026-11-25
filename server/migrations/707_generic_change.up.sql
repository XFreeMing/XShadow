-- Per-table change log for generic sheets. `seq` is assigned in the same
-- transaction as the cell write, while the table row lock is held, so a
-- later commit cannot publish a lower sequence that a cursor would skip.
-- No foreign keys. The primary key is attached in 709.
CREATE TABLE IF NOT EXISTS generic_change (
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    seq BIGINT NOT NULL,
    record_id UUID NOT NULL,
    base_revision BIGINT NOT NULL,
    client_revision BIGINT NOT NULL,
    record_revision BIGINT NOT NULL,
    fields JSONB NOT NULL,
    actor_source TEXT NOT NULL,
    actor_id UUID NOT NULL,
    task_id UUID,
    actor_label TEXT NOT NULL,
    op_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT generic_change_seq_check CHECK (seq > 0),
    CONSTRAINT generic_change_revision_check CHECK (
        base_revision > 0 AND client_revision > 0 AND record_revision = base_revision + 1
    ),
    CONSTRAINT generic_change_source_check CHECK (actor_source IN ('agent', 'member')),
    CONSTRAINT generic_change_label_check CHECK (char_length(actor_label) BETWEEN 1 AND 40),
    CONSTRAINT generic_change_digest_check CHECK (char_length(op_digest) = 32)
);
