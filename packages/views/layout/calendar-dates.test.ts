// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  buildCalendarMonthGrid,
  calendarGridDateRange,
  dayOfWeek,
  formatCalendarMonthLabel,
  parseCalendarDate,
} from "./calendar-dates";

describe("calendar-dates", () => {
  it("parses calendar dates without timezone day slip and builds a Sunday-first month grid", () => {
    expect(parseCalendarDate("2026-11-25")).toEqual({
      year: 2026,
      month: 11,
      day: 25,
      iso: "2026-11-25",
    });
    expect(parseCalendarDate("2024-02-29")?.iso).toBe("2024-02-29");
    expect(parseCalendarDate("2026-02-29")).toBeNull();
    expect(parseCalendarDate("invalid")).toBeNull();

    const cells = buildCalendarMonthGrid(2026, 11);
    expect(cells.length % 7).toBe(0);
    const inMonth = cells.filter((cell) => cell.inMonth);
    expect(inMonth).toHaveLength(30);
    expect(inMonth[0]!.date.iso).toBe("2026-11-01");
    expect(inMonth[29]!.date.iso).toBe("2026-11-30");
    expect(dayOfWeek(cells[0]!.date)).toBe(0);
    expect(cells[0]!.date.iso).toBe("2026-11-01");
    expect(cells.some((cell) => cell.date.iso === "2026-12-05" && !cell.inMonth)).toBe(true);
  });

  it("returns an inclusive start and exclusive end for the visible window", () => {
    expect(calendarGridDateRange(2026, 11)).toEqual({
      start: "2026-11-01",
      end: "2026-12-06",
    });
    expect(calendarGridDateRange(2026, 2)).toEqual({
      start: "2026-02-01",
      end: "2026-03-08",
    });
  });

  it("formats month labels in UTC so negative-offset zones do not show the previous month", () => {
    const previous = process.env.TZ;
    process.env.TZ = "America/Los_Angeles";
    try {
      const label = formatCalendarMonthLabel(2026, 11, "en-US");
      expect(label).toContain("November");
      expect(label).toContain("2026");
      expect(label).not.toMatch(/October/i);
    } finally {
      if (previous === undefined) delete process.env.TZ;
      else process.env.TZ = previous;
    }
  });
  it("pads February with leading days when the month does not start on Sunday", () => {
    const cells = buildCalendarMonthGrid(2026, 2);
    expect(dayOfWeek({ year: 2026, month: 2, day: 1, iso: "2026-02-01" })).toBe(0);
    expect(cells[0]!.date.iso).toBe("2026-02-01");
    expect(calendarGridDateRange(2026, 2).start).toBe("2026-02-01");
  });
});