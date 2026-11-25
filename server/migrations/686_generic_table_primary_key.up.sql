-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE generic_table
    ADD CONSTRAINT generic_table_pkey PRIMARY KEY USING INDEX generic_table_pkey_uidx;
