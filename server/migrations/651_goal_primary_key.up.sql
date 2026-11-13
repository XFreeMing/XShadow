-- Attach the CONCURRENTLY-built unique index as the table's primary key.
ALTER TABLE goal
    ADD CONSTRAINT goal_pkey PRIMARY KEY USING INDEX goal_pkey_uidx;
