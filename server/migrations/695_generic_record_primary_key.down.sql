-- Dropping the constraint drops the index it took over.
ALTER TABLE generic_record DROP CONSTRAINT IF EXISTS generic_record_pkey;
