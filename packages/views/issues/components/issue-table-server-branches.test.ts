// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  buildHierarchyGridItems,
  serverBranchKey,
} from "./issue-table-server-branches";
import type { Issue } from "@multica/core/types";

function makeIssue(overrides: Partial<Issue> & { id: string }): Issue {
  return {
    workspace_id: "ws-1",
    number: 1,
    identifier: "XSH-1",
    title: "Task",
    description: null,
    status: "todo",
    status_category: "unstarted",
    priority: "medium",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position: 1,
    stage: null,
    metadata: {},
    properties: {},
    start_date: null,
    due_date: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    revision: 1,
    ...overrides,
  };
}

describe("issue-table-server-branches", () => {
  it("orders parents before nested children and honors collapsed parents", () => {
    const parent = makeIssue({ id: "parent-1", title: "Parent" });
    const child = makeIssue({ id: "child-1", title: "Child", parent_issue_id: "parent-1" });
    const rootKey = serverBranchKey(null, null);
    const childKey = serverBranchKey(null, "parent-1");
    const branches = new Map([
      [
        rootKey,
        {
          key: rootKey,
          groupKey: null,
          parentId: null,
          ancestorIds: [],
          cursors: [null],
        },
      ],
      [
        childKey,
        {
          key: childKey,
          groupKey: null,
          parentId: "parent-1",
          ancestorIds: ["parent-1"],
          cursors: [null],
        },
      ],
    ]);
    const data = {
      [rootKey]: {
        rows: [{ issue: parent, direct_child_count: 1 }],
        total: 1,
        nextCursor: null,
        validCursorCount: 1,
        loading: false,
        error: false,
      },
      [childKey]: {
        rows: [{ issue: child, direct_child_count: 0 }],
        total: 1,
        nextCursor: null,
        validCursorCount: 1,
        loading: false,
        error: false,
      },
    };
    const open = buildHierarchyGridItems({
      usesServerGrouping: false,
      groupKeys: [],
      tableHierarchy: true,
      collapsedGroupSet: new Set(),
      collapsedParentSet: new Set(),
      activeServerBranches: branches,
      serverBranchData: data,
      activateBranch: () => {},
      loadMoreBranch: () => {},
      retryBranch: () => {},
    });
    expect(open.map((item) => (item.kind === "issue" ? item.issue.title : item.kind))).toEqual([
      "Parent",
      "Child",
    ]);

    const collapsed = buildHierarchyGridItems({
      usesServerGrouping: false,
      groupKeys: [],
      tableHierarchy: true,
      collapsedGroupSet: new Set(),
      collapsedParentSet: new Set(["parent-1"]),
      activeServerBranches: branches,
      serverBranchData: data,
      activateBranch: () => {},
      loadMoreBranch: () => {},
      retryBranch: () => {},
    });
    expect(collapsed.some((item) => item.kind === "issue" && item.issue.id === "child-1")).toBe(
      false,
    );
  });
});
