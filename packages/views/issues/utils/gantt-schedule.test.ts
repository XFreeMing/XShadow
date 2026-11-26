import { describe, expect, it } from "vitest";
import {
  buildGanttDateUpdateRequest,
  computeDueDateEditPatch,
  computeStartDateEditPatch,
} from "./gantt-schedule";

describe("Gantt schedule editing", () => {
  it("moves both dates across a daylight-saving boundary using calendar days", () => {
    const patch = computeStartDateEditPatch("2026-03-06", "2026-03-10", "2026-03-08");
    expect(patch).toEqual({ start_date: "2026-03-08", due_date: "2026-03-12" });
    expect(buildGanttDateUpdateRequest({ revision: 7 }, patch!)).toEqual({
      start_date: "2026-03-08",
      due_date: "2026-03-12",
      expected_revision: 7,
    });
  });

  it("keeps the interval valid and skips legacy values for the same day", () => {
    expect(computeDueDateEditPatch("2026-11-25", "2026-11-28", "2026-11-22"))
      .toEqual({ start_date: "2026-11-22", due_date: "2026-11-22" });
    expect(computeStartDateEditPatch("2026-11-25T00:00:00Z", "2026-11-28", "2026-11-25"))
      .toBeNull();
    expect(computeDueDateEditPatch(null, "2026-11-28T00:00:00Z", "2026-11-28"))
      .toBeNull();
  });
});
