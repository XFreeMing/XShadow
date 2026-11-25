-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE generic_cell
    ADD CONSTRAINT generic_cell_pkey PRIMARY KEY USING INDEX generic_cell_pkey_uidx;
