-- Dropping the constraint drops the index it took over, leaving 682's down
-- direction a no-op via IF EXISTS.
ALTER TABLE closure_decision DROP CONSTRAINT IF EXISTS closure_decision_pkey;