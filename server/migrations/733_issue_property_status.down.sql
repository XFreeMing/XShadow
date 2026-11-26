DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM issue_property WHERE type = 'status') THEN
        RAISE EXCEPTION 'cannot roll back 733 while status property definitions exist';
    END IF;
END
$$;

ALTER TABLE issue_property DROP CONSTRAINT IF EXISTS issue_property_type_check;
ALTER TABLE issue_property ADD CONSTRAINT issue_property_type_check
    CHECK (type IN (
        'text', 'number', 'select', 'multi_select', 'date', 'checkbox', 'url',
        'actor', 'multi_actor', 'multi_text', 'multi_url', 'record_relation',
        'lookup', 'formula'
    )) NOT VALID;
ALTER TABLE issue_property VALIDATE CONSTRAINT issue_property_type_check;
