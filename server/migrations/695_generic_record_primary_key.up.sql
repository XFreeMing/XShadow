-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE generic_record
    ADD CONSTRAINT generic_record_pkey PRIMARY KEY USING INDEX generic_record_pkey_uidx;
