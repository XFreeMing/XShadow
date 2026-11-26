CREATE TABLE IF NOT EXISTS goal_problem_solving (
    workspace_id uuid NOT NULL,
    goal_id uuid NOT NULL,
    revision integer NOT NULL CHECK (revision > 0),
    state jsonb NOT NULL
);
