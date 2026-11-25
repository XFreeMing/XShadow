-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE generic_field
    ADD CONSTRAINT generic_field_pkey PRIMARY KEY USING INDEX generic_field_pkey_uidx;
