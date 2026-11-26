-- Atomic generic-table record batch idempotency ledger and batch grouping id on
-- generic_change. No foreign keys; primary key is inline on the new table.
CREATE TABLE IF NOT EXISTS generic_record_batch_idempotency (
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    actor_source TEXT NOT NULL,
    actor_id UUID NOT NULL,
    idempotency_key UUID NOT NULL,
    operation TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    batch_id UUID NOT NULL,
    response_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, table_id, actor_source, actor_id, idempotency_key),
    CONSTRAINT generic_record_batch_idempotency_source_check CHECK (actor_source IN ('agent', 'member')),
    CONSTRAINT generic_record_batch_idempotency_op_check CHECK (operation IN ('create', 'update', 'delete')),
    CONSTRAINT generic_record_batch_idempotency_hash_check CHECK (char_length(request_hash) = 64)
);

ALTER TABLE generic_change
    ADD COLUMN IF NOT EXISTS batch_id UUID;
