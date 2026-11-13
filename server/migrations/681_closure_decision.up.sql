-- Human decision on a closure subject: an explicit accept or reject, recorded
-- even when independently verified delivery evidence is still incomplete. A
-- decision needs no delivery version (a goal may be refused or accepted before
-- any reviewable delivery exists) and is append-only. It is not a review: it
-- carries no verdict, grants no acceptance and never fabricates success, so
-- evaluated closure remains driven solely by closure_review rows.
CREATE TABLE IF NOT EXISTS closure_decision (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    project_id UUID,
    subject_kind TEXT NOT NULL,
    subject_id UUID NOT NULL,
    decision TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    criteria_revision TEXT NOT NULL DEFAULT '',
    delivery_version TEXT NOT NULL DEFAULT '',
    actor_type TEXT NOT NULL,
    actor_id UUID,
    actor_model TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);