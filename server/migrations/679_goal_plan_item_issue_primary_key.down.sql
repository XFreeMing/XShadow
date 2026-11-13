-- Dropping the constraint drops the index it took over, leaving 678's down
-- direction a no-op via IF EXISTS.
ALTER TABLE goal_plan_item_issue DROP CONSTRAINT IF EXISTS goal_plan_item_issue_pkey;