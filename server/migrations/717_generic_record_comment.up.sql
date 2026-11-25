-- Per-record discussion comments for generic tables.
-- `seq` is assigned per record while the record row lock is held, ensuring
-- stable monotonic ordering without cursor skips.
-- No foreign keys. The primary key is attached in 719.
CREATE TABLE IF NOT EXISTS generic_record_comment (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    record_id UUID NOT NULL,
    parent_id UUID,
    seq BIGINT NOT NULL,
    body TEXT NOT NULL,
    actor_source TEXT NOT NULL,
    actor_id UUID NOT NULL,
    task_id UUID,
    actor_label TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT generic_record_comment_seq_check CHECK (seq > 0),
    CONSTRAINT generic_record_comment_body_check CHECK (char_length(btrim(body)) BETWEEN 1 AND 4000),
    CONSTRAINT generic_record_comment_source_check CHECK (actor_source IN ('agent', 'member')),
    CONSTRAINT generic_record_comment_label_check CHECK (char_length(actor_label) BETWEEN 1 AND 40)
);
