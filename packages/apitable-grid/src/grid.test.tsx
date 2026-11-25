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

  it("discards a text edit on Escape without committing when the host opts in", async () => {
    const user = userEvent.setup();
    const onCellCommit = vi.fn();
    render(
      <ApitableGrid
        datasheetId="dst"
        fields={fields.map((field) => field.kind === "text" ? { ...field, cancelOnEscape: true } : field)}
        records={records}
        labels={labels}
        onCellCommit={onCellCommit}
      />,
    );

    const name = screen.getByRole("textbox", { name: "资料名称" });
    await user.click(name);
    await user.clear(name);
    await user.type(name, "丢弃的草稿");
    await user.keyboard("{Escape}");
    expect(name).toHaveValue("合同");
    expect(onCellCommit).not.toHaveBeenCalled();

    await user.click(name);
    await user.clear(name);
    await user.type(name, "保留的修改");
    await user.tab();
    expect(onCellCommit).toHaveBeenCalledExactlyOnceWith("rec1", "name", "保留的修改", 1);
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

  it("renders relation chips, opens chooser, displays lookup/formula values & errors, and blocks readonly Delete", async () => {
    const user = userEvent.setup();
    const onCellCommit = vi.fn();
    const onOpenRelationCell = vi.fn();
    const onEditField = vi.fn();
    const derivedFields: GridFieldView[] = [
      { id: "rel", name: "关联项目", kind: "relation", config: { target_table_id: "tbl-b", bidirectional: true } },
      { id: "lkp", name: "查找数量", kind: "lookup", config: { relation_field_id: "rel", target_field_id: "f-num" } },
      { id: "frm", name: "合计", kind: "formula", config: { expression: "SUM({查找数量})" } },
      { id: "bad", name: "异常", kind: "formula", config: { expression: "1 / 0" } },
    ];
    const derivedRecords: GridRecordView[] = [
      {
        id: "rec1",
        revision: 2,
        values: {
          rel: ["r-b1"],
          lkp: [0, false, null, 5],
          frm: 5,
          bad: null,
        },
        errors: {
          bad: "公式不能除以零",
        },
      },
    ];
    render(
      <ApitableGrid
        datasheetId="dst"
        fields={derivedFields}
        records={derivedRecords}
        recordOptionsByTable={{ "tbl-b": [{ id: "r-b1", label: "项目 Alpha" }] }}
        labels={{ ...labels, configureField: "设置", addLink: "关联记录" }}
        onCellCommit={onCellCommit}
        onRenameField={vi.fn()}
        onEditField={onEditField}
        onOpenRelationCell={onOpenRelationCell}
      />,
    );

    const relButton = screen.getByRole("button", { name: "关联项目: 项目 Alpha" });
    expect(relButton).toHaveTextContent("项目 Alpha");
    await user.click(relButton);
    expect(onOpenRelationCell).toHaveBeenCalledWith("rec1", derivedFields[0], ["r-b1"], 2);

    const lookupCell = screen.getByRole("textbox", { name: "查找数量" });
    expect(lookupCell).toHaveAttribute("aria-readonly", "true");
    expect(lookupCell).toHaveTextContent("0");
    expect(lookupCell).toHaveTextContent("未勾选");
    expect(lookupCell).toHaveTextContent("5");
    await user.click(lookupCell);
    await user.keyboard("{Delete}{Backspace}abc");
    expect(onCellCommit).not.toHaveBeenCalled();

    const formulaCell = screen.getByRole("textbox", { name: "合计" });
    expect(formulaCell).toHaveTextContent("5");
    await user.click(formulaCell);
    await user.keyboard("{Delete}");
    expect(onCellCommit).not.toHaveBeenCalled();

    const errCell = screen.getByRole("textbox", { name: "异常" });
    expect(errCell).toHaveTextContent("公式不能除以零");

    await user.click(screen.getByRole("button", { name: "设置 合计" }));
    expect(onEditField).toHaveBeenCalledWith(derivedFields[2]);
  });

  it("scopes lookup option and relation labels per field without rewriting literal formula or text strings", () => {
    const scopedFields: GridFieldView[] = [
      {
        id: "lkp_a",
        name: "表A状态",
        kind: "lookup",
        config: {
          relation_field_id: "rel_a",
          target_field_id: "status_a",
          target_options: [{ id: "opt1", label: "进行中" }],
        },
      },
      {
        id: "lkp_b",
        name: "表B等级",
        kind: "lookup",
        config: {
          relation_field_id: "rel_b",
          target_field_id: "level_b",
          target_options: [{ id: "opt1", label: "高风险" }],
        },
      },
      {
        id: "lkp_text",
        name: "原样文本",
        kind: "lookup",
        config: { relation_field_id: "rel_a", target_field_id: "note_a" },
      },
      {
        id: "lkp_rel",
        name: "关联负责人",
        kind: "lookup",
        config: {
          relation_field_id: "rel_a",
          target_field_id: "owner_rel",
          target_relation_table_id: "tbl-owners",
        },
      },
      {
        id: "frm_lit",
        name: "字面公式",
        kind: "formula",
        config: { expression: "'opt1'" },
      },
    ];
    const scopedRecords: GridRecordView[] = [
      {
        id: "rec1",
        revision: 1,
        values: {
          lkp_a: ["opt1"],
          lkp_b: ["opt1"],
          lkp_text: ["opt1"],
          lkp_rel: [["owner-1"]],
          frm_lit: "opt1",
        },
      },
    ];
    render(
      <ApitableGrid
        datasheetId="dst"
        fields={scopedFields}
        records={scopedRecords}
        recordOptionsByTable={{
          "tbl-owners": [{ id: "owner-1", label: "张三" }],
          "tbl-other": [{ id: "opt1", label: "误替换" }],
        }}
        labels={labels}
        onCellCommit={vi.fn()}
        onRenameField={vi.fn()}
      />,
    );

    expect(screen.getByRole("textbox", { name: "表A状态" })).toHaveTextContent("进行中");
    expect(screen.getByRole("textbox", { name: "表B等级" })).toHaveTextContent("高风险");
    expect(screen.getByRole("textbox", { name: "原样文本" })).toHaveTextContent("opt1");
    expect(screen.getByRole("textbox", { name: "关联负责人" })).toHaveTextContent("张三");
    expect(screen.getByRole("textbox", { name: "字面公式" })).toHaveTextContent("opt1");
  });

  it("renders fixed read-only column headers when onRenameField is omitted and supports custom renderCell slots", () => {
    const customFields: GridFieldView[] = [
      { id: "title", name: "Title", kind: "text" },
      { id: "assignee", name: "Assignee", kind: "text" },
    ];
    const customRecords: GridRecordView[] = [
      { id: "issue-uuid-1", revision: 3, values: { title: "Native issue", assignee: "member:u1" } },
    ];
    render(
      <ApitableGrid
        datasheetId="issues-grid"
        fields={customFields}
        records={customRecords}
        labels={labels}
        onCellCommit={vi.fn()}
        renderCell={({ field, record }) =>
          field.id === "assignee" ? (
            <button type="button" data-testid={`actor-slot-${record.id}`}>
              Alice
            </button>
          ) : undefined
        }
      />,
    );

    expect(screen.queryByRole("textbox", { name: labels.rename })).not.toBeInTheDocument();
    expect(screen.getByText("Title")).toBeInTheDocument();
    expect(screen.getByText("Assignee")).toBeInTheDocument();
    expect(screen.getByTestId("actor-slot-issue-uuid-1")).toHaveTextContent("Alice");
    expect(screen.getByDisplayValue("Native issue")).toBeInTheDocument();
  });

  it("pins the header row only when stickyHeader is set", () => {
    const props = {
      datasheetId: "dst",
      fields: fields.slice(0, 1),
      records,
      labels,
      onCellCommit: vi.fn(),
    };
    const { container, rerender } = render(<ApitableGrid {...props} />);
    const table = container.querySelector("table");
    expect(table).toHaveClass("apitable-grid");
    expect(table).not.toHaveClass("apitable-grid--sticky-head");

    rerender(<ApitableGrid {...props} stickyHeader />);
    expect(container.querySelector("table")).toHaveClass("apitable-grid", "apitable-grid--sticky-head");
  });
});
