-- Dropping the constraint drops the index it took over.
ALTER TABLE issue_type DROP CONSTRAINT IF EXISTS issue_type_pkey;
