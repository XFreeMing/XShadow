-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE issue_type
    ADD CONSTRAINT issue_type_pkey PRIMARY KEY USING INDEX issue_type_pkey_uidx;
