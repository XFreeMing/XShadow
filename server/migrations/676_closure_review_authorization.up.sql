-- The grant a persisted acceptance was executed under. Downstream readiness
-- reads it to refuse release after that grant is revoked or expires, instead
-- of trusting accepted_at alone. No index: readiness looks the accepted review
-- up by (workspace, subject), which 674 already covers.
ALTER TABLE closure_review ADD COLUMN IF NOT EXISTS authorization_id UUID;