DROP TRIGGER IF EXISTS generic_cell_history_trg ON generic_cell;
DROP TRIGGER IF EXISTS generic_record_history_trg ON generic_record;
DROP TRIGGER IF EXISTS generic_view_history_trg ON generic_view;
DROP TRIGGER IF EXISTS generic_field_history_trg ON generic_field;
DROP TRIGGER IF EXISTS generic_table_history_trg ON generic_table;

DROP FUNCTION IF EXISTS generic_history_capture();

DROP TABLE IF EXISTS generic_history_version;
DROP TABLE IF EXISTS generic_history_revision;
