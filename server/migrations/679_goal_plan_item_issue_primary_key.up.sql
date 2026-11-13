-- Attach the CONCURRENTLY-built unique index as the table's primary key.
ALTER TABLE goal_plan_item_issue
    ADD CONSTRAINT goal_plan_item_issue_pkey PRIMARY KEY USING INDEX goal_plan_item_issue_pkey_uidx;