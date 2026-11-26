-- Restore the pre-record_relation type allowlist.
--
-- Fails closed when any record_relation definition still exists — values are
-- keyed by definition id, so deleting those rows would orphan task data.
DO $$
DECLARE
    rel_defs BIGINT;
BEGIN
    SELECT count(*) INTO rel_defs
    FROM issue_property
    WHERE type = 'record_relation';

    IF rel_defs > 0 THEN
        RAISE EXCEPTION 'cannot roll back 731: % record_relation property definition(s) still exist', rel_defs
            USING HINT = 'Convert those definitions and the issue values keyed to them to another type first; this migration will not delete user data.';
    END IF;
END
$$;

ALTER TABLE issue_property DROP CONSTRAINT IF EXISTS issue_property_type_check;
ALTER TABLE issue_property ADD CONSTRAINT issue_property_type_check
    CHECK (type IN (
        'text', 'number', 'select', 'multi_select', 'date', 'checkbox', 'url',
        'actor', 'multi_actor', 'multi_text', 'multi_url'
    )) NOT VALID;
ALTER TABLE issue_property VALIDATE CONSTRAINT issue_property_type_check;