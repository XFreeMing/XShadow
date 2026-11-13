-- Durable mapping from a stable plan item id to the issue that materialized it.
-- The plan content keeps `binding.type = "new"` (it is only an intent), so this
-- table is the persisted binding: a retried or concurrent materialize trigger
-- resolves the same issue instead of creating another one. goal_plan.revision
-- is recorded for audit but never gates idempotency; the unique index in 680 is
-- what makes the first writer win.
CREATE TABLE IF NOT EXISTS goal_plan_item_issue (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    goal_id UUID NOT NULL,
    item_id TEXT NOT NULL,
    issue_id UUID NOT NULL,
    plan_revision INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);