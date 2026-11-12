-- Goals are a first-class deliverable object owned by a workspace and,
-- optionally, one of its projects (project_id NULL = a workspace-level goal).
-- They are deliberately NOT a renamed issue: issue status, parent/stage and
-- dependencies keep their own meaning, and a goal's lifecycle only changes by
-- an explicit goal write.
--
-- One goal per issue: goal_issue's primary key is (workspace_id, issue_id).
-- No foreign keys (repo rule): issue, project and workspace teardown remove
-- these rows in application code.
--
-- The primary key is built CONCURRENTLY in 650/651, following 597-599.
CREATE TABLE IF NOT EXISTS goal (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    project_id UUID,
    title TEXT NOT NULL,
    intent TEXT NOT NULL DEFAULT '',
    scope TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    owner_type TEXT CHECK (owner_type IN ('member', 'agent', 'squad')),
    owner_id UUID,
    status TEXT NOT NULL DEFAULT 'planned'
        CHECK (status IN ('planned', 'in_progress', 'paused', 'completed', 'cancelled')),
    creator_type TEXT NOT NULL,
    creator_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- The owner is either absent or a complete (type, id) pair.
    CONSTRAINT goal_owner_pair_check CHECK ((owner_type IS NULL) = (owner_id IS NULL))
);
