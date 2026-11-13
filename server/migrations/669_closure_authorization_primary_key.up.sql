-- Attach the CONCURRENTLY-built unique index as the table's primary key.
ALTER TABLE closure_authorization
    ADD CONSTRAINT closure_authorization_pkey PRIMARY KEY USING INDEX closure_authorization_pkey_uidx;