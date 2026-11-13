// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import {
  EMPTY_GOAL_PLAN,
  EMPTY_GOAL_PLAN_GENERATION,
  GoalPlanGenerationSchema,
  GoalPlanSchema,
} from "./schemas";

const item = {
  id: "5b0f7a48-2f6e-4f6c-9f1a-1a2b3c4d5e6f",
  title: "Ship the plan editor",
  outcome: "A user can adjust a draft plan",
  acceptance_criteria: "Draft edits save with a revision guard",
  evidence_refs: [
    {
      kind: "file",
      ref: "packages/views/goals/components/goal-detail-page.tsx:72",
      note: "Existing detail page embeds the plan",
      version: "9aa692be9",
    },
  ],
  binding: { type: "new", issue_id: null },
  proposed_owner: { type: "agent", id: "agent-1" },
  stage: 0,
  coordination: {
    parallel_reason: "Independent module",
    write_scope: ["packages/views/goals"],
    exclusive_resources: [],
    integration_owner: "agent-2",
    next_evaluation: "When the API contract changes",
  },
};

const plan = {
  id: "plan-1",
  goal_id: "goal-1",
  project_id: "project-1",
  revision: 3,
  state: "draft",
  items: [item],
  edges: [],
  context_snapshot: { repo_sha: "abc123", missing: [] },
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const generation = {
  id: "gen-1",
  goal_id: "goal-1",
  project_id: "project-1",
  request_key: "req-1",
  intent: "Plan the goal",
  agent_id: "agent-1",
  base_revision: 2,
  state: "succeeded",
  chat_session_id: "chat-1",
  task_id: "task-1",
  input_context: {
    project_id: "project-1",
    project_title: "XShadow",
    resources: [{ id: "res-1", type: "github_repo", read: false }],
    plan_revision: 2,
    missing: ["project_resources"],
    read_at: "2026-01-01T00:00:00Z",
  },
  candidate: {
    items: [{ ...item, id: "candidate-node-1" }],
    edges: [],
    context_snapshot: { repo_sha: "def456", read_at: "2026-01-01T00:05:00Z" },
  },
  applied: false,
  applied_revision: null,
  observed_revision: 2,
  current_revision: 3,
  stale: true,
  error: "",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:05:00Z",
};

describe("GoalPlanSchema", () => {
  it("keeps a complete saved plan intact", () => {
    expect(GoalPlanSchema.parse(plan)).toEqual(plan);
  });

  it("accepts the synthetic empty draft at revision zero", () => {
    const empty = {
      id: null,
      goal_id: "goal-1",
      project_id: null,
      revision: 0,
      state: "draft",
      items: [],
      edges: [],
      context_snapshot: {},
      created_at: null,
      updated_at: null,
    };
    expect(GoalPlanSchema.parse(empty)).toEqual(empty);
  });

  it("degrades malformed editable fields instead of failing the editor", () => {
    const parsed = GoalPlanSchema.parse({
      id: "plan-1",
      goal_id: "goal-1",
      revision: 2,
      items: [{ id: 7, title: 42, evidence_refs: "nope" }],
      edges: [{ dependent_item_id: 1 }],
      context_snapshot: "not-an-object",
    });
    expect(parsed.revision).toBe(2);
    expect(parsed.items[0]).toMatchObject({
      id: "",
      title: "",
      evidence_refs: [],
      binding: { type: "new", issue_id: null },
      proposed_owner: null,
      stage: null,
      coordination: null,
    });
    expect(parsed.edges[0]).toMatchObject({
      dependent_item_id: "",
      prerequisite_item_id: "",
      reason: "",
    });
    expect(parsed.context_snapshot).toEqual({});
  });

  it("preserves an unknown plan state so it can be shown conservatively", () => {
    expect(GoalPlanSchema.parse({ ...plan, state: "archived" }).state).toBe("archived");
  });

  it("falls back to the empty draft on an unusable body", () => {
    expect(
      parseWithFallback({ revision: "three" }, GoalPlanSchema, EMPTY_GOAL_PLAN, {
        endpoint: "GET /api/goals/:id/plan",
      }),
    ).toEqual(EMPTY_GOAL_PLAN);
    // A missing body still parses: every field has a default.
    expect(GoalPlanSchema.parse({}).revision).toBe(0);
  });
});

describe("GoalPlanGenerationSchema", () => {
  it("keeps a candidate, its basis and the stale signal", () => {
    const parsed = GoalPlanGenerationSchema.parse(generation);
    expect(parsed.candidate?.items[0]?.id).toBe("candidate-node-1");
    expect(parsed.candidate?.context_snapshot).toEqual({
      repo_sha: "def456",
      read_at: "2026-01-01T00:05:00Z",
    });
    expect(parsed.stale).toBe(true);
    expect(parsed.applied).toBe(false);
    expect(parsed.input_context.missing).toEqual(["project_resources"]);
  });

  it("accepts a null candidate while the run is live", () => {
    const parsed = GoalPlanGenerationSchema.parse({
      ...generation,
      state: "running",
      candidate: null,
      stale: false,
    });
    expect(parsed.candidate).toBeNull();
  });

  it("preserves an unknown state instead of reading it as success", () => {
    const parsed = GoalPlanGenerationSchema.parse({ ...generation, state: "paused" });
    expect(parsed.state).toBe("paused");
  });

  it("rejects a generation without a real id", () => {
    expect(GoalPlanGenerationSchema.safeParse({ ...generation, id: "" }).success).toBe(false);
    expect(
      parseWithFallback(
        { ...generation, id: "" },
        GoalPlanGenerationSchema,
        EMPTY_GOAL_PLAN_GENERATION,
        { endpoint: "POST /api/goals/:id/plan/generations" },
      ).id,
    ).toBe("");
  });
});