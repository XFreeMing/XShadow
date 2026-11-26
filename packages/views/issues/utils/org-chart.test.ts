import type { Issue } from "@multica/core/types";
import { describe, expect, it } from "vitest";
import { buildOrgChartLayout } from "./org-chart";

const issue = (id: string, parentId: string | null = null) =>
  ({ id, parent_issue_id: parentId }) as Issue;

describe("issue architecture", () => {
  it("keeps filtered children visible and reconnects them when their parent loads", () => {
    const parent = issue("parent");
    const child = issue("child", parent.id);
    const grandchild = issue("grandchild", child.id);

    const partial = buildOrgChartLayout([child, grandchild]);
    expect(partial.nodes[0]?.rootReason).toBe("missing_parent");
    expect(partial.edges.map((edge) => edge.id)).toEqual(["child->grandchild"]);

    const complete = buildOrgChartLayout([child, grandchild, parent]);
    expect(complete.edges.map((edge) => edge.id)).toEqual([
      "parent->child",
      "child->grandchild",
    ]);
    const folded = buildOrgChartLayout([child, grandchild, parent], {
      collapsedIds: new Set([parent.id]),
    });
    expect(folded.nodes.map((node) => node.issue.id)).toEqual(["parent"]);
    expect(folded.nodes[0]?.descendantCount).toBe(2);
    expect(folded.edges).toEqual([]);
  });

  it("renders every task once when legacy data contains a parent cycle", () => {
    const layout = buildOrgChartLayout([
      issue("a", "b"),
      issue("b", "a"),
      issue("self", "self"),
    ]);
    expect(layout.nodes.map((node) => node.issue.id)).toEqual(["a", "b", "self"]);
    expect(layout.nodes.filter((node) => node.rootReason === "cycle").map((node) => node.issue.id))
      .toEqual(["a", "self"]);
    expect(layout.edges.map((edge) => edge.id)).toEqual(["a->b"]);
  });
});
