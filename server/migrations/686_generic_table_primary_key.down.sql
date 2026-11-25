-- Dropping the constraint drops the index it took over.
ALTER TABLE generic_table DROP CONSTRAINT IF EXISTS generic_table_pkey;
