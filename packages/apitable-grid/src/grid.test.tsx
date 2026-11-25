import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApitableGrid } from "./grid";
import type { GridFieldView, GridLabels, GridRecordView } from "./types";

const labels: GridLabels = {
  rename: "重命名字段",
  clear: "清空",
  emptyRecords: "还没有记录",
  emptyValue: "空",
  checked: "已勾选",
  unchecked: "未勾选",
};

const fields: GridFieldView[] = [
  { id: "name", name: "资料名称", kind: "text" },
  { id: "count", name: "数量", kind: "number" },
  { id: "level", name: "风险等级", kind: "single_select", config: { options: [{ id: "high", label: "高" }] } },
  { id: "done", name: "已确认", kind: "checkbox" },
];

const records: GridRecordView[] = [
  { id: "rec1", revision: 1, values: { name: "合同", count: 0, level: null, done: null } },
];

describe("ApitableGrid", () => {
  it("selects a cell, commits a changed text value, and keeps zero", async () => {
    const user = userEvent.setup();
    const onCellCommit = vi.fn();
    render(
      <ApitableGrid
        datasheetId="dst"
        fields={fields}
        records={records}
        labels={labels}
        onCellCommit={onCellCommit}
        onRenameField={vi.fn()}
      />,
    );

    const name = screen.getByRole("textbox", { name: "资料名称" });
    await user.click(name);
    expect(name.closest("td")).toHaveAttribute("data-active", "true");
    await user.clear(name);
    await user.type(name, "合同扫描件");
    await user.tab();
    expect(onCellCommit).toHaveBeenCalledWith("rec1", "name", "合同扫描件", 1);

    const count = screen.getByRole("textbox", { name: "数量" });
    await user.click(count);
    await user.tab();
    expect(onCellCommit).not.toHaveBeenCalledWith("rec1", "count", 0);
  });

  it("shows a received record and a conflict mark from the remote patch", () => {
    render(
      <ApitableGrid
        datasheetId="dst"
        fields={fields.slice(0, 1)}
        records={records}
        remote={{
          records: [{ id: "rec2", revision: 4, values: { name: "收到的记录" } }],
          conflicts: [{ recordId: "rec2", fieldId: "name", message: "冲突" }],
        }}
        labels={labels}
        onCellCommit={vi.fn()}
        onRenameField={vi.fn()}
      />,
    );
    expect(screen.getByRole("textbox", { name: "资料名称" })).toHaveValue("收到的记录");
    expect(screen.getByRole("cell").getAttribute("data-conflict")).toBe("true");
    expect(screen.queryByDisplayValue("合同")).toBeNull();
  });

  it("keeps a focused draft and does not write the original value after a remote update", async () => {
    const user = userEvent.setup();
    const onCellCommit = vi.fn();
    const onEditEnd = vi.fn();
    const props = {
      datasheetId: "dst",
      fields: fields.slice(0, 1),
      labels,
      onCellCommit,
      onRenameField: vi.fn(),
      onEditEnd,
    };
    const { rerender } = render(
      <ApitableGrid {...props} records={[{ id: "rec1", revision: 1, values: { name: "旧" } }]} />,
    );
    const name = screen.getByRole("textbox", { name: "资料名称" });
    await user.click(name);
    rerender(<ApitableGrid {...props} records={[{ id: "rec1", revision: 2, values: { name: "别人" } }]} />);
    expect(name).toHaveValue("旧");
    await user.tab();
    expect(onCellCommit).not.toHaveBeenCalled();
    expect(onEditEnd).toHaveBeenCalled();
    expect(name).toHaveValue("别人");

    await user.click(name);
    await user.clear(name);
    await user.type(name, "草稿");
    rerender(<ApitableGrid {...props} records={[{ id: "rec1", revision: 3, values: { name: "又更新" } }]} />);
    expect(name).toHaveValue("草稿");
    await user.tab();
    expect(onCellCommit).toHaveBeenCalledWith("rec1", "name", "草稿", 2);
  });
});
