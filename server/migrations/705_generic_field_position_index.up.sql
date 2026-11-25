-- One field occupies one position in a sheet.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_field_position_uidx ON generic_field (table_id, position);
