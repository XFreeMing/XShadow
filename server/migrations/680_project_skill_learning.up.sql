ALTER TABLE skill ADD COLUMN project_id uuid;

CREATE TABLE project_skill_proposal (
    workspace_id uuid NOT NULL,
    project_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    method_revision integer NOT NULL,
    trace_revision integer NOT NULL,
    candidate_commit text NOT NULL,
    lesson jsonb NOT NULL,
    proposed_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    decided_by uuid,
    skill_id uuid,
    skill_version integer
);
