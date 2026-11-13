-- Links an EXISTING issue to one goal. The link carries no status of its own:
-- the issue is not converted into a goal and the goal's status never derives
-- from the issue's.
--
-- The primary key (workspace_id, issue_id) is built CONCURRENTLY in 655/656 and
-- enforces both uniqueness and "one goal per issue".
CREATE TABLE IF NOT EXISTS goal_issue (
    workspace_id UUID NOT NULL,
    goal_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
