// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import {
  EMPTY_GOAL,
  GoalSchema,
  ListGoalIssuesResponseSchema,
  ListGoalsResponseSchema,
} from "./schemas";

const goal = {
  id: "goal-1",
  workspace_id: "workspace-1",
  project_id: null,
  title: "Ship goal management",
  intent: "Let a team track a deliverable",
  scope: "Web only",
  acceptance_criteria: "An owner can confirm completion",
  owner_type: "member",
  owner_id: "user-1",
  status: "in_progress",
  creator_type: "member",
  creator_id: "user-1",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("GoalSchema response compatibility", () => {
  it("keeps a complete goal intact", () => {
    expect(GoalSchema.parse(goal)).toEqual(goal);
  });

  it("degrades missing editable fields instead of failing the page", () => {
    const parsed = GoalSchema.parse({ id: "goal-1", title: "Only a title" });
    expect(parsed).toMatchObject({
      id: "goal-1",
      title: "Only a title",
      project_id: null,
      intent: "",
      scope: "",
      acceptance_criteria: "",
      owner_type: null,
      owner_id: null,
      status: "planned",
      created_at: "",
    });
  });

  it("defaults a malformed project or owner reference to none", () => {
    const parsed = GoalSchema.parse({ ...goal, project_id: 42, owner_type: {}, owner_id: [] });
    expect(parsed.project_id).toBeNull();
    expect(parsed.owner_type).toBeNull();
    expect(parsed.owner_id).toBeNull();
  });

  it("preserves an owner kind this client does not know", () => {
    const parsed = GoalSchema.parse({ ...goal, owner_type: "team", owner_id: "team-1" });
    expect(parsed.owner_type).toBe("team");
    expect(parsed.owner_id).toBe("team-1");
  });

  it("preserves an unknown status so it can never be read as completed", () => {
    const parsed = GoalSchema.parse({ ...goal, status: "archived" });
    expect(parsed.status).toBe("archived");
  });

  it("rejects a response without a real id", () => {
    expect(GoalSchema.safeParse({ ...goal, id: "" }).success).toBe(false);
    expect(GoalSchema.safeParse({ ...goal, id: 42 }).success).toBe(false);
    expect(GoalSchema.safeParse({ title: "no id" }).success).toBe(false);
    expect(EMPTY_GOAL.id).toBe("");
  });
});

describe("goal list response compatibility", () => {
  it("keeps a valid page", () => {
    expect(ListGoalsResponseSchema.parse({ goals: [goal], total: 1 })).toEqual({
      goals: [goal],
      total: 1,
    });
  });

  it("degrades an invalid page to empty", () => {
    expect(
      parseWithFallback({ goals: "invalid" }, ListGoalsResponseSchema, { goals: [], total: 0 }, {
        endpoint: "GET /api/goals",
      }),
    ).toEqual({ goals: [], total: 0 });
    expect(ListGoalsResponseSchema.parse({})).toEqual({ goals: [], total: 0 });
  });

  it("degrades a malformed linked-task page to empty", () => {
    expect(
      parseWithFallback(
        { issues: [42], total: 1 },
        ListGoalIssuesResponseSchema,
        { issues: [], total: 0 },
        { endpoint: "GET /api/goals/:id/issues" },
      ),
    ).toEqual({ issues: [], total: 0 });
    expect(ListGoalIssuesResponseSchema.parse({})).toEqual({ issues: [], total: 0 });
  });
});