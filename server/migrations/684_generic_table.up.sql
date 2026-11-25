-- Generic sheets owned by a workspace.
-- Task attributes, task types, knowledge sources and cross-sheet links are
-- other aggregates. They are not stored in this table.
-- No foreign keys: application code checks workspace ownership.
-- The primary key is attached in 686 from the index built in 685.
CREATE TABLE IF NOT EXISTS generic_table (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT generic_table_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 80)
);
