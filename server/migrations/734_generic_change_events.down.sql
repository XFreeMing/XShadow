-- Restore generic_change to record-update-only shape.
-- Supported downgrade limits:
-- - Rolling back before any non-update typed events are written is lossless.
-- - Once record.created / record.deleted or table/field/view metadata events
--   have been written, those non-update rows are dropped because the pre-734
--   schema requires NOT NULL record_id and record_revision = base_revision + 1;
--   all record.updated rows (including rows written by prior binaries with NULL
--   entity_id) are preserved intact.
DELETE FROM generic_change
WHERE event_type <> 'record.updated' OR record_id IS NULL;

ALTER TABLE generic_change
    DROP CONSTRAINT IF EXISTS generic_change_revision_check,
    DROP CONSTRAINT IF EXISTS generic_change_event_type_check,
    DROP CONSTRAINT IF EXISTS generic_change_topic_check;

ALTER TABLE generic_change
    ALTER COLUMN record_id SET NOT NULL,
    DROP COLUMN IF EXISTS entity_id,
    DROP COLUMN IF EXISTS topic,
    DROP COLUMN IF EXISTS event_type;

ALTER TABLE generic_change
    ADD CONSTRAINT generic_change_revision_check CHECK (
        base_revision > 0 AND client_revision > 0 AND record_revision = base_revision + 1
    );
