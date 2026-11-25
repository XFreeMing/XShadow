-- Workspace-level user-configurable task/issue type registry.
-- Each type references existing issue_property definitions via ordered entries
-- in `fields` (JSONB array of {property_id, required, default_value?}).
-- No foreign keys. The primary key is attached in 723.
CREATE TABLE IF NOT EXISTS issue_type (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    fields JSONB NOT NULL DEFAULT '[]'::jsonb,
    position DOUBLE PRECISION NOT NULL DEFAULT 0,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT issue_type_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 64),
    CONSTRAINT issue_type_fields_array_check CHECK (jsonb_typeof(fields) = 'array')
);
