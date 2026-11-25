-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE generic_change
    ADD CONSTRAINT generic_change_pkey PRIMARY KEY USING INDEX generic_change_seq_uidx;
