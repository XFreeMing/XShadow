-- DEV-7 closure contract: one independent verdict about one fixed delivery.
-- A row counts as evidence only when reviewer and author identity/run were
-- derived by the server; a client-supplied reviewer name is never trusted.
-- accepted_at is the persisted completion action, so downstream work is
-- released only after this row exists for the current delivery.
--
-- The primary key is built CONCURRENTLY in 672/673, following 659/660.
CREATE TABLE IF NOT EXISTS closure_review (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    project_id UUID,
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('issue', 'goal')),
    subject_id UUID NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('issue_close', 'goal_accept')),
    delivery_version TEXT NOT NULL,
    delivery_tree TEXT NOT NULL DEFAULT '',
    criteria_revision TEXT NOT NULL DEFAULT '',
    author_type TEXT NOT NULL DEFAULT '',
    author_id UUID,
    author_run_id UUID,
    author_model TEXT NOT NULL DEFAULT '',
    reviewer_type TEXT NOT NULL,
    reviewer_id UUID NOT NULL,
    reviewer_run_id UUID,
    reviewer_model TEXT NOT NULL DEFAULT '',
    verdict TEXT NOT NULL CHECK (verdict IN ('pending', 'pass', 'fail', 'needs_changes')),
    criteria JSONB NOT NULL DEFAULT '[]'::jsonb,
    platforms JSONB NOT NULL DEFAULT '[]'::jsonb,
    missing JSONB NOT NULL DEFAULT '[]'::jsonb,
    reason TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    superseded_by UUID,
    accepted_at TIMESTAMPTZ,
    accepted_by_type TEXT NOT NULL DEFAULT '',
    accepted_by_id UUID,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);