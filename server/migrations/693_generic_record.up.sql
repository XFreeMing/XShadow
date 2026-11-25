-- One row shared by every view of a generic sheet.
-- `revision` is the optimistic concurrency token for cell writes.
-- No foreign keys. The primary key is attached in 695.
CREATE TABLE IF NOT EXISTS generic_record (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT generic_record_revision_check CHECK (revision > 0)
);
