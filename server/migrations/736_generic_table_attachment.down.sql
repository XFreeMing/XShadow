DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM generic_field WHERE kind = 'attachment') THEN
        RAISE EXCEPTION 'cannot roll back 736_generic_table_attachment: generic_field still contains attachment fields; remove them first to preserve data';
    END IF;
    IF to_regclass('public.generic_table_attachment') IS NOT NULL
       AND EXISTS (SELECT 1 FROM generic_table_attachment) THEN
        RAISE EXCEPTION 'cannot roll back 736_generic_table_attachment: generic_table_attachment still contains metadata rows; delete their tables or drain attachments first to preserve uploaded data';
    END IF;
END $$;

ALTER TABLE generic_field DROP CONSTRAINT IF EXISTS generic_field_kind_check;
ALTER TABLE generic_field
    ADD CONSTRAINT generic_field_kind_check
    CHECK (kind IN ('text', 'number', 'date', 'single_select', 'checkbox', 'relation', 'lookup', 'formula'));

DROP TABLE IF EXISTS generic_table_attachment;
