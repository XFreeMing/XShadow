-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE generic_view
    ADD CONSTRAINT generic_view_pkey PRIMARY KEY USING INDEX generic_view_pkey_uidx;
