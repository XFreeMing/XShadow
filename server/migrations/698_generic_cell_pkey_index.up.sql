-- Backing index for generic_cell's primary key, attached in 699.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_cell_pkey_uidx ON generic_cell (record_id, field_id);
