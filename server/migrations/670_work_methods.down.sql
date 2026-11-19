DROP TRIGGER IF EXISTS issue_work_method_closure ON issue;
DROP FUNCTION IF EXISTS check_issue_work_method_closure();
ALTER TABLE issue DROP COLUMN IF EXISTS work_method_run;
ALTER TABLE workspace DROP COLUMN IF EXISTS work_method_templates;
