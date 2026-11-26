// @vitest-environment node
import { describe, expect, it } from "vitest";
import { nextRecordRelationAfterToggle } from "./record-relation-selection";

describe("nextRecordRelationAfterToggle", () => {
  const A = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
  const B = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
  const C = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";

  it("drops deleted targets and adds a replacement when options have loaded", () => {
    const available = new Set([B.toLowerCase(), C.toLowerCase()]);
    expect(nextRecordRelationAfterToggle([A, B], C, available)).toEqual([B, C]);
  });

  it("removes a linked row when toggled off", () => {
    const available = new Set([B.toLowerCase(), C.toLowerCase()]);
    expect(nextRecordRelationAfterToggle([B, C], B, available)).toEqual([C]);
  });

  it("does not add when at capacity", () => {
    const available = new Set([B.toLowerCase(), C.toLowerCase()]);
    expect(nextRecordRelationAfterToggle([B], C, available, 1)).toEqual([B]);
  });
});