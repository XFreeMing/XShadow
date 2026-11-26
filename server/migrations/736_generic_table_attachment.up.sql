-- Generic-table scoped attachments and attachment field kind.
-- No foreign keys; uniqueness constraints are inline on the new table.
CREATE TABLE IF NOT EXISTS generic_table_attachment (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    uploader_type TEXT NOT NULL,
    uploader_id UUID NOT NULL,
    idempotency_key UUID NOT NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    content_sha256 TEXT NOT NULL,
    storage_key TEXT NOT NULL,
    object_url TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at TIMESTAMPTZ,
    UNIQUE (workspace_id, table_id, uploader_type, uploader_id, idempotency_key),
    CONSTRAINT generic_table_attachment_uploader_type_check CHECK (uploader_type IN ('member', 'agent')),
    CONSTRAINT generic_table_attachment_size_check CHECK (size_bytes >= 0),
    CONSTRAINT generic_table_attachment_sha256_check CHECK (char_length(content_sha256) = 64),
    CONSTRAINT generic_table_attachment_status_check CHECK (status IN ('pending', 'ready'))
);

ALTER TABLE generic_field DROP CONSTRAINT IF EXISTS generic_field_kind_check;
ALTER TABLE generic_field
    ADD CONSTRAINT generic_field_kind_check
    CHECK (kind IN ('text', 'number', 'date', 'single_select', 'checkbox', 'relation', 'lookup', 'formula', 'attachment'));
