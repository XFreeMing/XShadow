-- A goal plan is the editable draft produced from one sentence about a goal.
-- It is NOT an execution queue: no issue is created, assigned or enqueued
-- here. One current plan per goal, versioned by `revision`; the content is
-- versioned JSON so a save replaces the whole draft atomically.
--
-- `context_snapshot` records what the generation read (project, resources,
-- linked tasks, capture time) so the UI can show the evidence behind a plan.
--
-- The primary key is built CONCURRENTLY in 659/660, following 650/651.
CREATE TABLE IF NOT EXISTS goal_plan (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    goal_id UUID NOT NULL,
    project_id UUID NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft')),
    content JSONB NOT NULL DEFAULT '{"items": [], "edges": []}'::jsonb,
    context_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);