-- A column on a generic sheet. `kind` is the value checker.
-- `config` holds kind-specific rules (single-select options). It is not a
-- task-type or knowledge-source definition.
-- No foreign keys. The primary key is attached in 690.
CREATE TABLE IF NOT EXISTS generic_field (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    position INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT generic_field_kind_check CHECK (kind IN ('text', 'number', 'date', 'single_select', 'checkbox')),
    CONSTRAINT generic_field_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
    CONSTRAINT generic_field_position_check CHECK (position > 0)
);
