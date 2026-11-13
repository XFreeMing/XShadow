-- Attach the CONCURRENTLY-built unique index as the table's primary key.
ALTER TABLE closure_decision
    ADD CONSTRAINT closure_decision_pkey PRIMARY KEY USING INDEX closure_decision_pkey_uidx;