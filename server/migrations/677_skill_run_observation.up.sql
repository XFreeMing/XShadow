CREATE TABLE skill_run_observation (
    workspace_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    task_id uuid NOT NULL,
    dispatched_at timestamptz NOT NULL,
    content_hash text NOT NULL,
    version integer,
    provided_at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz,
    reviewed_by uuid,
    outcome text CHECK (outcome IN ('not_applied', 'helped', 'neutral', 'regressed')),
    evidence text,
    CHECK (
        (reviewed_at IS NULL AND reviewed_by IS NULL AND outcome IS NULL AND evidence IS NULL) OR
        (reviewed_at IS NOT NULL AND reviewed_by IS NOT NULL AND outcome IS NOT NULL AND evidence IS NOT NULL)
    )
);
