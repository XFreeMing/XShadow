-- Backing index for generic_table's primary key, attached in 686.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_table_pkey_uidx ON generic_table (id);
