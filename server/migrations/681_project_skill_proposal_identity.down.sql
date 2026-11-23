DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM skill WHERE project_id IS NOT NULL) THEN
        RAISE EXCEPTION 'Remove project skills before reverting project skill scope';
    END IF;
    DROP INDEX IF EXISTS idx_project_skill_proposal_identity;
END $$;
