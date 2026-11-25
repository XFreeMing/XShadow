-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE generic_presence
    ADD CONSTRAINT generic_presence_pkey PRIMARY KEY USING INDEX generic_presence_actor_uidx;
