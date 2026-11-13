-- Dropping the constraint drops the index it took over, leaving 650's down
-- direction a no-op via IF EXISTS.
ALTER TABLE goal DROP CONSTRAINT IF EXISTS goal_pkey;
