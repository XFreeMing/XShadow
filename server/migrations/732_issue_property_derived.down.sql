-- Restore the pre-derived type allowlist.
--
-- Fails closed when any lookup or formula definition still exists.
DO $$
DECLARE
    derived_defs BIGINT;
BEGIN
    SELECT count(*) INTO derived_defs
    FROM issue_property
    WHERE type IN ('lookup', 'formula');

    IF derived_defs > 0 THEN
        RAISE EXCEPTION 'cannot roll back 732: % lookup/formula property definition(s) still exist', derived_defs
            USING HINT = 'Archive or remove those definitions first; this migration will not delete user data.';
    END IF;
END
$$;

ALTER TABLE issue_property DROP CONSTRAINT IF EXISTS issue_property_type_check;
ALTER TABLE issue_property ADD CONSTRAINT issue_property_type_check
    CHECK (type IN (
        'text', 'number', 'select', 'multi_select', 'date', 'checkbox', 'url',
        'actor', 'multi_actor', 'multi_text', 'multi_url', 'record_relation'
    )) NOT VALID;
ALTER TABLE issue_property VALIDATE CONSTRAINT issue_property_type_check;