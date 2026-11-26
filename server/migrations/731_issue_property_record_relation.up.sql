-- Custom issue property record_relation: ordered links to generic-table rows.
--
-- Adds record_relation to the type allowlist. Values are a JSON array of record
-- UUIDs in insertion order; validation, dedup, workspace/table scoping, and the
-- entry cap live in the handler and Rust write path. Config carries
-- target_table_id only (no bidirectional projection on tasks in this increment).
--
-- NOT VALID + VALIDATE keeps the ACCESS EXCLUSIVE lock instantaneous.
ALTER TABLE issue_property DROP CONSTRAINT IF EXISTS issue_property_type_check;
ALTER TABLE issue_property ADD CONSTRAINT issue_property_type_check
    CHECK (type IN (
        'text', 'number', 'select', 'multi_select', 'date', 'checkbox', 'url',
        'actor', 'multi_actor', 'multi_text', 'multi_url', 'record_relation'
    )) NOT VALID;
ALTER TABLE issue_property VALIDATE CONSTRAINT issue_property_type_check;