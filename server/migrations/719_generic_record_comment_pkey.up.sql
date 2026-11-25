-- Attach the CONCURRENTLY-built unique index as the primary key.
ALTER TABLE generic_record_comment
    ADD CONSTRAINT generic_record_comment_pkey PRIMARY KEY USING INDEX generic_record_comment_pkey_uidx;
