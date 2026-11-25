-- Dropping the constraint drops the index it took over.
ALTER TABLE generic_presence DROP CONSTRAINT IF EXISTS generic_presence_pkey;
