// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  catalogGridColumnOrder,
  gridPropertyColumnId,
  isGridColumnKey,
  mergeGridColumnState,
  normalizeGridColumnOrder,
  projectTableColumnsToGrid,
  type GridColumnKey,
} from "./grid-columns";

const LEGACY_TABLE_SYSTEM_COLUMNS: GridColumnKey[] = [
  "priority",
  "start_date",
  "due_date",
  "labels",
  "identifier",
  "project",
  "child_progress",
  "creator",
  "created_at",
  "updated_at",
];

describe("grid column catalog merge", () => {
  const catalog = catalogGridColumnOrder(true, ["estimate", "url"]);

  it("places title once when the saved order is empty", () => {
    expect(normalizeGridColumnOrder(catalog, [])).toEqual([
      "title",
      "type",
      "status",
      "assignee",
      ...LEGACY_TABLE_SYSTEM_COLUMNS,
      gridPropertyColumnId("estimate"),
      gridPropertyColumnId("url"),
    ]);
  });

  it("rejects invalid persisted keys", () => {
    expect(isGridColumnKey("status")).toBe(true);
    expect(isGridColumnKey("start_date")).toBe(true);
    expect(isGridColumnKey("due_date")).toBe(true);
    expect(isGridColumnKey("priority")).toBe(true);
    expect(isGridColumnKey("property:owner")).toBe(true);
    expect(isGridColumnKey("property:")).toBe(false);
    expect(isGridColumnKey("bogus")).toBe(false);
    const merged = normalizeGridColumnOrder(
      catalog,
      ["title", "bogus", "status", "property:"] as unknown as GridColumnKey[],
    );
    expect(merged).toEqual([
      "title",
      "status",
      "type",
      "assignee",
      ...LEGACY_TABLE_SYSTEM_COLUMNS,
      gridPropertyColumnId("estimate"),
      gridPropertyColumnId("url"),
    ]);
  });

  it("keeps title first and appends new property columns as visible", () => {
    const merged = mergeGridColumnState(
      catalog,
      ["title", "status", "assignee"],
      ["type"],
    );
    expect(merged.order[0]).toBe("title");
    expect(merged.order).toEqual([
      "title",
      "status",
      "assignee",
      "type",
      ...LEGACY_TABLE_SYSTEM_COLUMNS,
      gridPropertyColumnId("estimate"),
      gridPropertyColumnId("url"),
    ]);
    expect(merged.hidden).toEqual(["type"]);
  });

  it("drops removed or unknown column ids without leaving holes", () => {
    const merged = mergeGridColumnState(
      catalog,
      [
        "title",
        "status",
        "property:gone",
        "assignee",
        "property:estimate",
      ],
      ["property:ghost"],
    );
    expect(merged.order).toEqual([
      "title",
      "status",
      "assignee",
      gridPropertyColumnId("estimate"),
      "type",
      ...LEGACY_TABLE_SYSTEM_COLUMNS,
      gridPropertyColumnId("url"),
    ]);
    expect(merged.hidden).toEqual([]);
  });

  it("omits type when the workspace has no task types", () => {
    const withoutTypes = catalogGridColumnOrder(false, ["estimate"]);
    const merged = normalizeGridColumnOrder(withoutTypes, [
      "title",
      "type",
      "status",
      "assignee",
    ]);
    expect(merged).toEqual([
      "title",
      "status",
      "assignee",
      ...LEGACY_TABLE_SYSTEM_COLUMNS,
      gridPropertyColumnId("estimate"),
    ]);
  });

  it("projects legacy tableColumns order and visibility onto grid schema", () => {
    const projected = projectTableColumnsToGrid(
      [
        { key: "title" },
        { key: "priority" },
        { key: "start_date" },
        { key: "due_date" },
        { key: "property:estimate" },
      ],
      catalog,
    );
    expect(projected.order.slice(0, 5)).toEqual([
      "title",
      "priority",
      "start_date",
      "due_date",
      gridPropertyColumnId("estimate"),
    ]);
    expect(projected.hidden).toContain("status");
    expect(projected.hidden).toContain("assignee");
    expect(projected.hidden).toContain(gridPropertyColumnId("url"));
    expect(projected.hidden).not.toContain("title");
    expect(projected.hidden).not.toContain("priority");
    expect(projected.hidden).not.toContain("start_date");
    expect(projected.hidden).not.toContain("due_date");
    expect(projected.hidden).not.toContain(gridPropertyColumnId("estimate"));
  });
});