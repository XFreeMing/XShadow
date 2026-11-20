// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { GoalPlanEdge } from "../types";
import { plannedDays, scheduleGoalPlan } from "./plan-schedule";

const edge = (from: string, to: string, kind: GoalPlanEdge["kind"] = "required"): GoalPlanEdge => ({
  prerequisite_item_id: from, dependent_item_id: to, reason: from + to, kind,
});
const nodes = (durations: Record<string, number | null>) =>
  Object.entries(durations).map(([id, duration]) => ({ id, duration }));

describe("goal dependency schedule", () => {
  const diamond = [edge("a", "b"), edge("a", "c"), edge("b", "d"), edge("c", "d")];

  it("waits for the longer parallel branch and measures the peak", () => {
    const result = scheduleGoalPlan(nodes({ d: 2, b: 4, a: 1, c: 1 }), diamond);
    expect(result).toMatchObject({ kind: "scheduled", unit: "day", length: 7, peak: 2 });
    if (result.kind !== "scheduled") throw Error("schedule missing");
    expect(result.rows.map(({ id, start, end, critical }) => [id, start, end, critical])).toEqual([
      ["a", 0, 1, true], ["b", 1, 5, true], ["c", 1, 2, false], ["d", 5, 7, true],
    ]);
  });

  it("uses equal stages when a duration is missing and retains every tied critical path", () => {
    const result = scheduleGoalPlan(nodes({ a: null, b: 4, c: 2, d: 1 }), [...diamond, edge("b", "c", "conditional")]);
    expect(result).toMatchObject({ kind: "scheduled", unit: "stage", length: 3, peak: 2 });
    if (result.kind !== "scheduled") throw Error("schedule missing");
    expect(result.rows.every(row => row.critical)).toBe(true);
  });

  it("includes independent tasks and refuses a required cycle", () => {
    expect(scheduleGoalPlan(nodes({ a: 1, b: 2 }), [])).toMatchObject({ length: 2, peak: 2 });
    expect(scheduleGoalPlan(nodes({ a: 1, b: 2 }), [edge("a", "b"), edge("b", "a")])).toMatchObject({ kind: "invalid" });
  });
});


it("counts both scheduled dates and leaves partial or inverted dates unknown", () => {
  expect(plannedDays("2026-10-31", "2026-11-02")).toBe(3);
  expect(plannedDays("2026-11-01", "2026-11-01")).toBe(1);
  expect(plannedDays("2026-11-01", null)).toBeNull();
  expect(plannedDays("2026-11-02", "2026-11-01")).toBeNull();
});
