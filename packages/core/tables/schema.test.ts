// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWithFallback } from "../api/schema";
import {
  EMPTY_GENERIC_RECORD_COMMENTS,
  EMPTY_GENERIC_SHEET,
  genericCellValueSchema,
  genericRecordCommentsSchema,
  genericRecordSchema,
  genericSheetSchema,
  genericViewSchema,
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

  it("keeps an old view ungrouped and defaults kind/config when omitted", () => {
    const legacy = genericViewSchema.parse({
      id: "view",
      name: "全部",
      columns: null,
      filter: null,
      sort: null,
      created_at: "2026-11-25T00:00:00Z",
      updated_at: "2026-11-25T00:00:00Z",
    });
    expect(legacy.group).toBeNull();
    expect(legacy.kind).toBe("grid");
    expect(legacy.config).toEqual({});

    const typed = genericViewSchema.parse({
      id: "view-2",
      name: "看板",
      kind: "kanban",
      config: { group_field_id: "f1", title_field_id: "f2" },
      columns: null,
      filter: null,
      sort: null,
      group: null,
      created_at: "2026-11-25T00:00:00Z",
      updated_at: "2026-11-25T00:00:00Z",
    });
    expect(typed.kind).toBe("kanban");
    expect(typed.config).toEqual({ group_field_id: "f1", title_field_id: "f2" });

    const unknownKind = genericViewSchema.parse({
      id: "view-3",
      name: "未来视图",
      kind: "future_kind",
      config: {},
      columns: null,
      filter: null,
      sort: null,
      created_at: "2026-11-25T00:00:00Z",
      updated_at: "2026-11-25T00:00:00Z",
    });
    expect(unknownKind.kind).toBe("grid");
  });

  it("falls back when a sheet payload is malformed", () => {
    expect(parseWithFallback({ table: { id: 4 } }, genericSheetSchema, EMPTY_GENERIC_SHEET, {
      endpoint: "GET /api/tables/:id",
    })).toEqual(EMPTY_GENERIC_SHEET);
  });

  it("parses relation arrays, nested lookup arrays, and per-cell formula errors", () => {
    const record = genericRecordSchema.parse({
      id: "rec-1",
      revision: 3,
      created_at: "2026-11-25T00:00:00Z",
      updated_at: "2026-11-25T00:00:00Z",
      values: {
        rel: ["11111111-1111-4111-8111-111111111111"],
        lkp: [0, false, null, ["nested"]],
        frm: null,
      },
      errors: {
        frm: "公式不能除以零",
      },
    });
    expect(record.values.rel).toEqual(["11111111-1111-4111-8111-111111111111"]);
    expect(record.values.lkp).toEqual([0, false, null, ["nested"]]);
    expect(record.errors?.frm).toBe("公式不能除以零");
  });

  it("parses record comments list with cursor and falls back when malformed", () => {
    const parsed = genericRecordCommentsSchema.parse({
      comments: [
        {
          id: "c1",
          record_id: "r1",
          seq: 1,
          body: "第一条讨论",
          actor: { source: "member", id: "u1", label: "张三", color: "#7B67EE" },
          created_at: "2026-11-25T10:00:00Z",
        },
      ],
      has_more: true,
      next_cursor: 1,
    });
    expect(parsed.comments[0]?.parent_id).toBeNull();
    expect(parsed.has_more).toBe(true);
    expect(parsed.next_cursor).toBe(1);

    expect(
      parseWithFallback({ comments: "bad" }, genericRecordCommentsSchema, EMPTY_GENERIC_RECORD_COMMENTS, {
        endpoint: "GET /api/tables/:id/records/:recordId/comments",
      }),
    ).toEqual(EMPTY_GENERIC_RECORD_COMMENTS);
  });
});
