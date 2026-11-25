// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { GenericField, GenericRecord, GenericRecordComment, GenericSheet, GenericView } from "@multica/core/tables";
import { buildFormPayload } from "./form-view";
import { mergeComments } from "./record-detail-dialog";
import { patchCachedSheetView } from "./sheet-page";
import { buildTableChatDraft } from "./table-chat-launcher";
import {
  buildCalendarMonthGrid,
  buildGanttModel,
  buildOrgTree,
  parseCalendarDate,
  resolveEffectiveViewConfig,
  type OrgNode,
} from "./view-helpers";

function field(id: string, kind: string, config: GenericField["config"] = {}): GenericField {
  return {
    id,
    name: id,
    kind,
    config,
    position: 1,
    created_at: "2026-11-25T00:00:00Z",
    updated_at: "2026-11-25T00:00:00Z",
  };
}

function record(id: string, values: GenericRecord["values"]): GenericRecord {
  return {
    id,
    revision: 1,
    created_at: "2026-11-25T00:00:00Z",
    updated_at: "2026-11-25T00:00:00Z",
    values,
  };
}

function collectIds(nodes: OrgNode[]): string[] {
  return nodes.flatMap((node) => [node.record.id, ...collectIds(node.children)]);
}

describe("view-helpers", () => {
  it("parses calendar dates without timezone day slip and builds month grid", () => {
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
  });

  it("separates valid Gantt bars from missing and reversed date ranges", () => {
    const model = buildGanttModel(
      [
        record("r1", { start: "2026-11-01", end: "2026-11-05" }),
        record("r2", { start: "2026-11-10", end: null }),
        record("r3", { start: "2026-11-10", end: "2026-11-05" }),
        record("r4", { start: null, end: "2026-11-12" }),
      ],
      "start",
      "end",
    );
    expect(model.scheduled.map((bar) => bar.record.id)).toEqual(["r1", "r2"]);
    expect(model.scheduled[0]!.endIndex - model.scheduled[0]!.startIndex).toBe(4);
    expect(model.scheduled[1]!.end.iso).toBe("2026-11-10");
    expect(model.unscheduled).toEqual([
      { record: expect.objectContaining({ id: "r3" }), reason: "invalid" },
      { record: expect.objectContaining({ id: "r4" }), reason: "missing" },
    ]);
  });

  it("builds org chart tree safely when cycles or filtered parents exist", () => {
    const tree = buildOrgTree(
      [
        record("ceo", { parent: null }),
        record("vp", { parent: ["ceo"] }),
        record("cyc1", { parent: ["cyc2"] }),
        record("cyc2", { parent: ["cyc1"] }),
        record("self", { parent: ["self"] }),
        record("orphan", { parent: ["filtered-out"] }),
      ],
      "parent",
    );
    const allIds = collectIds(tree).sort();
    expect(allIds).toEqual(["ceo", "cyc1", "cyc2", "orphan", "self", "vp"]);
    const ceo = tree.find((node) => node.record.id === "ceo");
    expect(ceo?.children.map((child) => child.record.id)).toEqual(["vp"]);
    expect(tree.find((node) => node.record.id === "self")?.status).toBe("cycle");
    expect(tree.find((node) => node.record.id === "orphan")?.status).toBe("missing_parent");
  });

  it("resolves default fields for sparse views and keeps explicit saved config", () => {
    const tableId = "table-1";
    const fields = [
      field("f-text", "text"),
      field("f-select", "single_select", { options: [{ id: "a", label: "A" }] }),
      field("f-date1", "date"),
      field("f-date2", "date"),
      field("f-self", "relation", { target_table_id: tableId, bidirectional: false }),
      field("f-cross", "relation", { target_table_id: "table-2", bidirectional: false }),
    ];
    expect(resolveEffectiveViewConfig("kanban", {}, fields, tableId).groupFieldId).toBe("f-select");
    expect(resolveEffectiveViewConfig("gantt", {}, fields, tableId)).toMatchObject({
      dateFieldId: "f-date1",
      endDateFieldId: "f-date2",
    });
    expect(resolveEffectiveViewConfig("org_chart", {}, fields, tableId).parentFieldId).toBe("f-self");
  });

  it("builds form payload keeping 0 and false while excluding computed fields", () => {
    const fields = [
      field("f-text", "text"),
      field("f-num", "number"),
      field("f-check", "checkbox"),
      field("f-formula", "formula"),
      field("f-lookup", "lookup"),
    ];
    const payload = buildFormPayload(fields, {
      "f-text": "hello",
      "f-num": "0",
      "f-check": false,
      "f-formula": "123",
      "f-lookup": "abc",
    });
    expect(payload).toEqual({
      "f-text": "hello",
      "f-num": 0,
      "f-check": false,
    });
  });

  it("deduplicates and orders record comments by seq across polling and pagination", () => {
    const mk = (id: string, seq: number, body: string): GenericRecordComment => ({
      id,
      record_id: "r1",
      parent_id: null,
      seq,
      body,
      actor: { source: "member", id: "u1", label: "Alice", color: "#7B67EE" },
      created_at: "2026-11-25T10:00:00Z",
    });
    const merged = mergeComments(
      [mk("c2", 2, "second"), mk("c1", 1, "first")],
      [mk("c2", 2, "second-fresh"), mk("c3", 3, "third")],
    );
    expect(merged.map((c) => `${c.seq}:${c.id}:${c.body}`)).toEqual([
      "1:c1:first",
      "2:c2:second-fresh",
      "3:c3:third",
    ]);
  });

  it("builds bounded table and record chat drafts with truncation indicator", () => {
    const fields = [field("f-name", "text"), field("f-count", "number")];
    const records = Array.from({ length: 20 }, (_, i) =>
      record(`rec-${String(i + 1).padStart(4, "0")}`, { "f-name": `Row ${i + 1}`, "f-count": i }),
    );
    const labels = {
      checked: "Yes",
      unchecked: "No",
      emptyValue: "Empty",
      truncatedNotice: "Truncated additional fields or records",
    };

    const tableDraft = buildTableChatDraft(
      {
        table: { id: "tbl-1", name: "Tasks" },
        view: { id: "v-1", name: "Board", kind: "kanban" },
        fields,
        visibleFields: fields,
        records,
        recordOptionsByTable: {},
      },
      labels,
    );
    expect(tableDraft).toContain("Table: Tasks (tbl-1)");
    expect(tableDraft).toContain("View: Board [kanban]");
    expect(tableDraft).toContain("Records (15/20):");
    expect(tableDraft).toContain("[Truncated additional fields or records]");

    const recordDraft = buildTableChatDraft(
      {
        table: { id: "tbl-1", name: "Tasks" },
        view: { id: "v-1", name: "Board", kind: "kanban" },
        fields,
        visibleFields: fields,
        records,
        selectedRecord: records[0],
        titleFieldId: "f-name",
        recordOptionsByTable: {},
      },
      labels,
    );
    expect(recordDraft).toContain("Selected record: Row 1 (rec-0001)");
    expect(recordDraft).toContain("- f-name: Row 1");
    expect(recordDraft).toContain("- f-count: 0");
    expect(recordDraft).not.toContain("[Truncated additional fields or records]");

    // Hidden fields are never expanded in either table or record chat drafts.
    const secretField = field("f-secret", "text");
    const secretRecord = record("rec-9999", { "f-name": "Visible", "f-secret": "TOP_SECRET" });
    const hiddenTableDraft = buildTableChatDraft(
      {
        table: { id: "tbl-1", name: "Tasks" },
        view: { id: "v-1", name: "Empty", kind: "grid" },
        fields: [fields[0]!, secretField],
        visibleFields: [],
        records: [secretRecord],
        recordOptionsByTable: {},
      },
      labels,
    );
    expect(hiddenTableDraft).toContain("Fields (0/0):");
    expect(hiddenTableDraft).not.toContain("TOP_SECRET");
    expect(hiddenTableDraft).not.toContain("f-secret");

    const hiddenRecordDraft = buildTableChatDraft(
      {
        table: { id: "tbl-1", name: "Tasks" },
        view: { id: "v-1", name: "Grid", kind: "grid" },
        fields: [fields[0]!, secretField],
        visibleFields: [fields[0]!],
        records: [secretRecord],
        selectedRecord: secretRecord,
        recordOptionsByTable: {},
      },
      labels,
    );
    expect(hiddenRecordDraft).toContain("- f-name: Visible");
    expect(hiddenRecordDraft).not.toContain("TOP_SECRET");
    expect(hiddenRecordDraft).not.toContain("f-secret");
  });

  it("patches only the matching view in cached sheet snapshots", () => {
    const mkView = (id: string, kind: GenericView["kind"], config: GenericView["config"]): GenericView => ({
      id,
      name: id,
      kind,
      config,
      columns: null,
      filter: null,
      sort: null,
      group: null,
      created_at: "2026-11-25T10:00:00Z",
      updated_at: "2026-11-25T10:00:00Z",
    });
    const kanban = mkView("v-kanban", "kanban", {});
    const gallery = mkView("v-gallery", "gallery", { title_field_id: "f1" });
    const sheet: GenericSheet = {
      table: { id: "t1", name: "T1", created_at: "", updated_at: "" },
      fields: [],
      views: [kanban, gallery],
      records: [],
      active_view_id: "v-gallery",
    };
    const updatedKanban = mkView("v-kanban", "kanban", { group_field_id: "f-sel" });
    const patched = patchCachedSheetView(sheet, "v-kanban", updatedKanban);
    expect(patched?.views[0]?.config).toEqual({ group_field_id: "f-sel" });
    expect(patched?.views[1]).toBe(gallery);
    expect(patched?.active_view_id).toBe("v-gallery");
  });
});
