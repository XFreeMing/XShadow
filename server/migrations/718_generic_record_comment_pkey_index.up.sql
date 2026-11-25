-- Backing index for generic_record_comment's primary key, attached in 719.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS generic_record_comment_pkey_uidx
    ON generic_record_comment (id);
