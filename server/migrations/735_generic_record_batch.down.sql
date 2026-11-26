ALTER TABLE generic_change
    DROP COLUMN IF EXISTS batch_id;

DROP TABLE IF EXISTS generic_record_batch_idempotency;
