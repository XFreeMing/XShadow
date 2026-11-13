-- Attach the CONCURRENTLY-built unique index as the table's primary key.
ALTER TABLE closure_review
    ADD CONSTRAINT closure_review_pkey PRIMARY KEY USING INDEX closure_review_pkey_uidx;