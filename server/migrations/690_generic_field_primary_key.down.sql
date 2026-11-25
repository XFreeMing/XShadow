-- Dropping the constraint drops the index it took over.
ALTER TABLE generic_field DROP CONSTRAINT IF EXISTS generic_field_pkey;
