-- Backing index for generic_view's primary key, attached in 703.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_view_pkey_uidx ON generic_view (id);
