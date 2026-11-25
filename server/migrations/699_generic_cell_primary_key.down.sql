-- Dropping the constraint drops the index it took over.
ALTER TABLE generic_cell DROP CONSTRAINT IF EXISTS generic_cell_pkey;
