-- Backing index for generic_record's primary key, attached in 695.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_record_pkey_uidx ON generic_record (id);
