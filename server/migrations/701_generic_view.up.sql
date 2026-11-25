-- A saved presentation of one generic sheet: visible column order, one
-- filter and one sort. Views do not copy records.
-- No foreign keys. The primary key is attached in 703.
CREATE TABLE IF NOT EXISTS generic_view (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    name TEXT NOT NULL,
    columns JSONB,
    filter JSONB,
    sort JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT generic_view_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 80)
);
