// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { GoalPlanItem, Issue } from "@multica/core/types";
import { buildGoalTaskGraph, collectDependencyChain, GOAL_ROOT_ID } from "./task-tree";

function planItem(id: string, title: string, overrides: Partial<GoalPlanItem> = {}): GoalPlanItem {
  return {
    id,
    title,
    outcome: "",
    acceptance_criteria: "",
    evidence_refs: [],
    binding: { type: "new", issue_id: null },
    proposed_owner: null,
    stage: null,
    coordination: null,
    ...overrides,
  };
}

function issue(id: string, identifier: string, title: string, status = "in_progress"): Issue {
  return {
    id,
    identifier,
    title,
    status,
  } as Issue;
}

describe("buildGoalTaskGraph", () => {
  it("layers dependent plan items from left to right and builds a single-occurrence tree", () => {
    const graph = buildGoalTaskGraph({
      items: [
        planItem("a", "Upload book", { binding: { type: "existing", issue_id: "i-1" } }),
        planItem("b", "Track background progress"),
        planItem("c", "Read partial knowledge"),
        planItem("d", "Verify personal memory"),
      ],
      edges: [
        { prerequisite_item_id: "a", dependent_item_id: "b", reason: "needs upload", kind: "required" },
        { prerequisite_item_id: "a", dependent_item_id: "c", reason: "needs upload", kind: "required" },
        { prerequisite_item_id: "b", dependent_item_id: "d", reason: "needs progress", kind: "conditional" },
        { prerequisite_item_id: "c", dependent_item_id: "d", reason: "needs knowledge", kind: "required" },
      ],
      linkedIssues: [
        issue("i-1", "DEV-2", "Upload book", "done"),
        issue("i-extra", "DEV-9", "Manual linked task", "todo"),
      ],
      progress: {
        phase: "delivering",
        total: 4,
        completed: 1,
        item_id: "b",
        waiting_for: ["c"],
      },
    });

    expect(graph.columns).toHaveLength(3);
    expect(graph.columns[0]!.map((n) => n.id)).toEqual(["a", "issue:i-extra"]);
    expect(graph.columns[1]!.map((n) => n.id)).toEqual(["b", "c"]);
    expect(graph.columns[2]!.map((n) => n.id)).toEqual(["d"]);

    expect(graph.nodeById.get("a")?.isDone).toBe(true);
    expect(graph.nodeById.get("b")?.isCurrent).toBe(true);
    expect(graph.nodeById.get("c")?.isWaiting).toBe(true);
    expect(graph.unplannedLinkedNodes.map((n) => n.issue?.identifier)).toEqual(["DEV-9"]);

    // Tree places `d` once under its primary prerequisite and records the second prerequisite.
    expect(graph.treeRoots).toHaveLength(1);
    const rootA = graph.treeRoots[0]!;
    expect(rootA.node.id).toBe("a");
    expect(rootA.children.map((c) => c.node.id)).toEqual(["b", "c"]);
    const branchB = rootA.children[0]!;
    expect(branchB.children.map((c) => c.node.id)).toEqual(["d"]);
    expect(branchB.children[0]!.extraPrerequisiteIds).toEqual(["c"]);

    // Mind map connects goal root to depth-0 items.
    expect(
      graph.edges.filter((e) => e.fromId === GOAL_ROOT_ID).map((e) => e.toId),
    ).toEqual(["a", "issue:i-extra"]);

    // Dependency chain for `b` includes upstream `a` and downstream `d`, not sibling `c`.
    expect([...collectDependencyChain("b", graph.nodeById)].sort()).toEqual(["a", "b", "d"]);
  });

  it("groups by stage when there are no dependency edges", () => {
    const graph = buildGoalTaskGraph({
      items: [
        planItem("s1", "Stage 1 task", { stage: 1 }),
        planItem("s2", "Stage 2 task", { stage: 2 }),
      ],
      edges: [],
      linkedIssues: [],
    });

    expect(graph.columns).toHaveLength(2);
    expect(graph.columns[0]!.map((n) => n.id)).toEqual(["s1"]);
    expect(graph.columns[1]!.map((n) => n.id)).toEqual(["s2"]);
  });

  it("handles cycles without infinite recursion", () => {
    const graph = buildGoalTaskGraph({
      items: [planItem("a", "A"), planItem("b", "B")],
      edges: [
        { prerequisite_item_id: "a", dependent_item_id: "b", reason: "r1" },
        { prerequisite_item_id: "b", dependent_item_id: "a", reason: "r2" },
      ],
      linkedIssues: [],
    });

    expect(graph.nodes).toHaveLength(2);
    expect(graph.treeRoots.length).toBeGreaterThan(0);
  });
});
