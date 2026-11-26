import { describe, expect, it } from "vitest";
import { GoalProblemSolvingSchema } from "./goal-problem-solving";

const baseline = {
  goal_id: "goal-1",
  revision: 0,
  actor_id: "member/1",
  can_decide: false,
  state: {
    goal_versions: [{ number: 1, charter: { title: "Find my note", intent: "", scope: "", acceptance_criteria: null } }],
    goal_status: "active",
  },
};

describe("goal problem-solving response", () => {
  it("keeps the starting charter and defaults absent collections for an existing goal", () => {
    const value = GoalProblemSolvingSchema.parse(baseline);
    expect(value.state.goal_versions[0]?.charter).toMatchObject({ title: "Find my note", acceptance_criteria: [] });
    expect(value.state.validation_results).toEqual([]);
    expect(value.can_decide).toBe(false);
  });

  it("rejects an invalid revision or ambiguous permission", () => {
    expect(GoalProblemSolvingSchema.safeParse({ ...baseline, revision: -1 }).success).toBe(false);
    expect(GoalProblemSolvingSchema.safeParse({ ...baseline, can_decide: "true" }).success).toBe(false);
    expect(GoalProblemSolvingSchema.safeParse({ ...baseline, state: { goal_versions: [] } }).success).toBe(false);
  });
});
