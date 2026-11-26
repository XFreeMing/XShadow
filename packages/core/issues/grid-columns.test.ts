// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  catalogGridColumnOrder,
  gridPropertyColumnId,
  isGridColumnKey,
  mergeGridColumnState,
  normalizeGridColumnOrder,
  type GridColumnKey,
} from "./grid-columns";

describe("grid column catalog merge", () => {
  const catalog = catalogGridColumnOrder(true, ["estimate", "url"]);

  it("places title once when the saved order is empty", () => {
    expect(normalizeGridColumnOrder(catalog, [])).toEqual([
      "title",
      "type",
      "status",
      "assignee",
      gridPropertyColumnId("estimate"),
      gridPropertyColumnId("url"),
    ]);
  });

  it("rejects invalid persisted keys", () => {
    expect(isGridColumnKey("status")).toBe(true);
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
    expect(merged).toEqual(["title", "status", "assignee", gridPropertyColumnId("estimate")]);
  });
});