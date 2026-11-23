-- Goal imagery is fetched independently of the goal text supplied to agents.
CREATE TABLE goal_outcome_gallery (
    goal_id UUID NOT NULL,
    images JSONB NOT NULL
);
