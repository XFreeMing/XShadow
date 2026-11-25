// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWithFallback } from "../api/schema";
import {
  EMPTY_GENERIC_SHEET,
  genericCellValueSchema,
  genericRecordSchema,
  genericSheetSchema,
} from "./schema";

describe("generic table values", () => {
  it("keeps checkbox false distinct from an empty cell", () => {
    expect(genericCellValueSchema.parse(false)).toBe(false);
    expect(genericCellValueSchema.parse(null)).toBeNull();
    expect(genericCellValueSchema.parse(0)).toBe(0);
    expect(genericCellValueSchema.safeParse({}).success).toBe(false);

    const record = genericRecordSchema.parse({
      id: "rec",
      revision: 2,
      created_at: "2026-11-25T00:00:00Z",
      updated_at: "2026-11-25T00:00:00Z",
      values: { confirmed: false, cleared: null, count: 0 },
    });
    expect(record.values.confirmed).toBe(false);
    expect(record.values.cleared).toBeNull();
    expect(record.values.count).toBe(0);
  });

  it("falls back when a sheet payload is malformed", () => {
    expect(parseWithFallback({ table: { id: 4 } }, genericSheetSchema, EMPTY_GENERIC_SHEET, {
      endpoint: "GET /api/tables/:id",
    })).toEqual(EMPTY_GENERIC_SHEET);
  });
});
