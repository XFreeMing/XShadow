ALTER TABLE goal_plan
    ADD COLUMN execution_id uuid,
    ADD COLUMN execution_revision integer,
    ADD COLUMN execution_scope text,
    ADD COLUMN execution_by uuid;
