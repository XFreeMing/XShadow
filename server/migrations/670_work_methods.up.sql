ALTER TABLE workspace ADD COLUMN IF NOT EXISTS work_method_templates jsonb NOT NULL DEFAULT '[]';
ALTER TABLE issue ADD COLUMN IF NOT EXISTS work_method_run jsonb;

-- Protect alternate status writers and concurrent reviews as well as HTTP transitions.
CREATE OR REPLACE FUNCTION check_issue_work_method_closure() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.work_method_run IS NOT NULL AND
       NEW.work_method_run->>'state' IS DISTINCT FROM 'closed' AND
       issue_effective_status(NEW.workspace_id, NEW.status) = 'done' THEN
        RAISE EXCEPTION 'work method requires review before completion' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS issue_work_method_closure ON issue;
CREATE TRIGGER issue_work_method_closure BEFORE UPDATE OF status, work_method_run ON issue
FOR EACH ROW EXECUTE FUNCTION check_issue_work_method_closure();
