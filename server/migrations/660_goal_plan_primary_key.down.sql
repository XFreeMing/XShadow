-- Dropping the constraint drops the index it took over, leaving 659's down
-- direction a no-op via IF EXISTS.
ALTER TABLE goal_plan DROP CONSTRAINT IF EXISTS goal_plan_pkey;