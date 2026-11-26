-- Custom issue property lookup and formula: read-only projections on tasks.
--
-- Lookup follows a workspace record_relation property into a generic_table field.
-- Formula stores a compiled AST with property-id refs for same-task evaluation.
-- Values are never stored on issue.properties for these types; Rust projects them
-- at read time. NOT VALID + VALIDATE keeps the ACCESS EXCLUSIVE lock instantaneous.
ALTER TABLE issue_property DROP CONSTRAINT IF EXISTS issue_property_type_check;
ALTER TABLE issue_property ADD CONSTRAINT issue_property_type_check
    CHECK (type IN (
        'text', 'number', 'select', 'multi_select', 'date', 'checkbox', 'url',
        'actor', 'multi_actor', 'multi_text', 'multi_url', 'record_relation',
        'lookup', 'formula'
    )) NOT VALID;
ALTER TABLE issue_property VALIDATE CONSTRAINT issue_property_type_check;