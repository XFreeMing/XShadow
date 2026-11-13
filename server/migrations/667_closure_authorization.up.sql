-- DEV-7 closure contract: the persisted, scoped grant that lets an agent close
-- a delivered subject without waiting for a human. Nothing here is inferred
-- from a project match, a parent relation, a role name or workflow_id=null; a
-- grant names its subject, actions and acceptance scope explicitly.
--
-- The primary key is built CONCURRENTLY in 668/669, following 659/660.
CREATE TABLE IF NOT EXISTS closure_authorization (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    project_id UUID,
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('issue', 'goal')),
    subject_id UUID NOT NULL,
    actions TEXT[] NOT NULL CHECK (cardinality(actions) > 0),
    covers_children BOOLEAN NOT NULL DEFAULT false,
    criteria_revision TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'revoked')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    source_ref TEXT NOT NULL DEFAULT '',
    granted_by_type TEXT NOT NULL DEFAULT '',
    granted_by_id UUID,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);