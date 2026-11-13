-- Dropping the constraint drops the index it took over, leaving 668's down
-- direction a no-op via IF EXISTS.
ALTER TABLE closure_authorization DROP CONSTRAINT IF EXISTS closure_authorization_pkey;