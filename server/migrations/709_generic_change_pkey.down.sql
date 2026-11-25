-- Dropping the constraint drops the index it took over.
ALTER TABLE generic_change DROP CONSTRAINT IF EXISTS generic_change_pkey;
