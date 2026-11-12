// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  EDITABLE_GOAL_STATUSES,
  goalStatusLabelKey,
  isGoalCompleted,
} from "./status";

describe("goal lifecycle helpers", () => {
  it("recognizes only the exact completed status", () => {
    expect(isGoalCompleted("completed")).toBe(true);
    for (const status of ["planned", "in_progress", "paused", "cancelled", "archived", ""]) {
      expect(isGoalCompleted(status)).toBe(false);
    }
  });

  it("labels every known status and degrades anything else to unknown", () => {
    expect(goalStatusLabelKey("in_progress")).toBe("in_progress");
    expect(goalStatusLabelKey("archived")).toBe("unknown");
    expect(goalStatusLabelKey("")).toBe("unknown");
  });

  it("never offers completed as a form-selectable status", () => {
    expect(EDITABLE_GOAL_STATUSES).not.toContain("completed");
    expect([...EDITABLE_GOAL_STATUSES]).toEqual([
      "planned",
      "in_progress",
      "paused",
      "cancelled",
    ]);
  });
});