-- Dropping the constraint drops the index it took over.
ALTER TABLE generic_view DROP CONSTRAINT IF EXISTS generic_view_pkey;
