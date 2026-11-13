-- Dropping the constraint drops the index it took over, leaving 672's down
-- direction a no-op via IF EXISTS.
ALTER TABLE closure_review DROP CONSTRAINT IF EXISTS closure_review_pkey;