-- Attach the CONCURRENTLY-built unique index as the table's primary key.
ALTER TABLE goal_plan
    ADD CONSTRAINT goal_plan_pkey PRIMARY KEY USING INDEX goal_plan_pkey_uidx;