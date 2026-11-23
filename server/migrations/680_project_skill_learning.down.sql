DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM skill WHERE project_id IS NOT NULL) THEN
        RAISE EXCEPTION 'Remove project skills before reverting project skill scope';
    END IF;
END $$;

DROP TABLE project_skill_proposal;
ALTER TABLE skill DROP COLUMN project_id;
