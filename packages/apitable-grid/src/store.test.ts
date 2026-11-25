// @vitest-environment node

import { describe, expect, it } from "vitest";
import { clearSelection, setActiveCell, setEditStatus } from "../third-party/actions";
import { getActiveCell, getEditingCell, getSelection } from "../third-party/selectors";
import { createGridStore, hostSnapshot, setPageDatasheet } from "./store";
import type { GridSnapshot } from "../third-party/state";

const snapshot: GridSnapshot = {
  visibleFieldIds: ["name", "count"],
  visibleRecordIds: ["rec1"],
  records: {
    rec1: { id: "rec1", revision: 1, values: { name: "合同", count: 0 } },
  },
  conflicts: {},
};

describe("APITable selection store", () => {
  it("keeps the active cell and editing flag in the local pack", () => {
    const store = createGridStore();
    store.dispatch(setPageDatasheet("dst"));
    store.dispatch(hostSnapshot("dst", snapshot));
    store.dispatch(setActiveCell("dst", { recordId: "rec1", fieldId: "name" }));
    store.dispatch(setEditStatus("dst", { recordId: "rec1", fieldId: "name" }));

    expect(getActiveCell(store.getState())?.fieldId).toBe("name");
    expect(getSelection(store.getState())?.ranges).toEqual([
      { start: { recordId: "rec1", fieldId: "name" }, end: { recordId: "rec1", fieldId: "name" } },
    ]);
    expect(getEditingCell(store.getState())?.fieldId).toBe("name");
  });

  it("drops a selection that leaves the host snapshot and keeps it when only values change", () => {
    const store = createGridStore();
    store.dispatch(setPageDatasheet("dst"));
    store.dispatch(hostSnapshot("dst", snapshot));
    store.dispatch(setActiveCell("dst", { recordId: "rec1", fieldId: "name" }));

    store.dispatch(hostSnapshot("dst", {
      ...snapshot,
      records: { rec1: { id: "rec1", revision: 2, values: { name: "新", count: 0 } } },
      conflicts: { "rec1:name": "revision conflict" },
    }));
    expect(getActiveCell(store.getState())?.recordId).toBe("rec1");

    store.dispatch(clearSelection("dst"));
    expect(getSelection(store.getState())).toBeNull();

    store.dispatch(setActiveCell("dst", { recordId: "rec1", fieldId: "gone" }));
    expect(getSelection(store.getState())).toBeNull();
  });
});
