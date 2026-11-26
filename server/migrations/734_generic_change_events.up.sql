-- Extend generic_change with typed event metadata so record creates/deletes and
-- table/field/view schema changes share the same per-table ordered sequence and
-- row lock while keeping existing record.updated rows, merge replay, and prior
-- Rust binary writes (which omit event_type/topic/entity_id on record.updated)
-- intact.
ALTER TABLE generic_change
    ADD COLUMN IF NOT EXISTS event_type TEXT NOT NULL DEFAULT 'record.updated',
    ADD COLUMN IF NOT EXISTS topic TEXT NOT NULL DEFAULT 'record',
    ADD COLUMN IF NOT EXISTS entity_id UUID;

UPDATE generic_change
SET entity_id = record_id
WHERE entity_id IS NULL AND record_id IS NOT NULL;

ALTER TABLE generic_change
    ALTER COLUMN record_id DROP NOT NULL;

ALTER TABLE generic_change
    DROP CONSTRAINT IF EXISTS generic_change_revision_check,
    DROP CONSTRAINT IF EXISTS generic_change_event_type_check,
    DROP CONSTRAINT IF EXISTS generic_change_topic_check;

ALTER TABLE generic_change
    ADD CONSTRAINT generic_change_event_type_check CHECK (
        event_type IN (
            'record.created',
            'record.updated',
            'record.deleted',
            'table.renamed',
            'field.created',
            'field.updated',
            'field.deleted',
            'view.created',
            'view.updated',
            'view.deleted'
        )
    ),
    ADD CONSTRAINT generic_change_topic_check CHECK (
        topic IN ('record', 'table', 'field', 'view')
    ),
    ADD CONSTRAINT generic_change_revision_check CHECK (
        (event_type = 'record.updated'
            AND record_id IS NOT NULL
            AND (entity_id IS NULL OR entity_id = record_id)
            AND base_revision > 0 AND client_revision > 0 AND record_revision = base_revision + 1)
        OR (event_type = 'record.created'
            AND record_id IS NOT NULL
            AND entity_id = record_id
            AND base_revision = 0 AND client_revision = 0 AND record_revision = 1)
        OR (event_type = 'record.deleted'
            AND record_id IS NOT NULL
            AND entity_id = record_id
            AND base_revision > 0 AND client_revision > 0 AND record_revision = base_revision)
        OR (event_type IN ('table.renamed', 'field.created', 'field.updated', 'field.deleted', 'view.created', 'view.updated', 'view.deleted')
            AND record_id IS NULL
            AND entity_id IS NOT NULL
            AND base_revision = 0 AND client_revision = 0 AND record_revision = 0)
    );
