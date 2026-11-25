-- Field names are unique inside one sheet.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_field_name_uidx ON generic_field (table_id, name);
