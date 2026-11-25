-- Extend generic_field.kind to allow relation, lookup, and formula fields.
-- Existing rows are unchanged.
ALTER TABLE generic_field DROP CONSTRAINT IF EXISTS generic_field_kind_check;
ALTER TABLE generic_field
    ADD CONSTRAINT generic_field_kind_check
    CHECK (kind IN ('text', 'number', 'date', 'single_select', 'checkbox', 'relation', 'lookup', 'formula'));
