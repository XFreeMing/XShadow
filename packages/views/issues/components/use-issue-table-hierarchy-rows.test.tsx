/**
 * @vitest-environment jsdom
 */
import { describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { issueKeys } from "@multica/core/issues/queries";
import type { Issue, IssueTableRowsRequest, IssueTableRowsResponse } from "@multica/core/types";
import { useIssueTableHierarchyRows } from "./use-issue-table-hierarchy-rows";
import type { HierarchyGridFooter } from "./issue-table-server-branches";

const query = {
  scope: { kind: "workspace" as const },
  filters: { include_sub_issues: true },
  sort: { field: "position" as const, direction: "asc" as const },
};

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

describe("useIssueTableHierarchyRows", () => {
  it("drops a loaded continuation when the current head cursor changes", async () => {
    const head = makeIssue({ id: "head" });
    const oldTail = makeIssue({ id: "old-tail" });
    const group = { kind: "none" } as const;
    const page = (issue: Issue, nextCursor: string | null): IssueTableRowsResponse => ({
      query_fingerprint: "fp",
      group_key: null,
      parent_id: null,
      total: 2,
      branch_total: 2,
      rows: [{ issue, direct_child_count: 0 }],
      next_cursor: nextCursor,
    });
    const listIssueTableRows = vi.fn(async (request: IssueTableRowsRequest) =>
      request.page?.cursor ? page(oldTail, null) : page(head, "old-cursor"),
    );
    setApiInstance({ listIssueTableRows } as unknown as ApiClient);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useIssueTableHierarchyRows({
      wsId: "ws-1",
      serverQuery: query,
      serverGroupSpec: group,
      tableHierarchy: true,
      usesServerGrouping: false,
      groupKeys: [],
      collapsedGroupKeys: [],
      collapsedParentKeys: [],
      enabled: true,
    }), { wrapper });
    const continuation = () => result.current.gridItems.find(
      (item): item is HierarchyGridFooter => item.kind === "footer" && item.state === "has_more",
    );

    await waitFor(() => expect(continuation()).toBeDefined());
    act(() => continuation()?.onAction?.());
    await waitFor(() => expect(result.current.issues.map((issue) => issue.id)).toEqual(["head", "old-tail"]));

    act(() => client.setQueriesData({
      queryKey: issueKeys.tableRows("ws-1", query, group, null, true, null),
      predicate: (entry) => (entry.state.data as IssueTableRowsResponse)?.rows[0]?.issue.id === "head",
    }, page(head, "new-cursor")));
    await waitFor(() => expect(result.current.issues.map((issue) => issue.id)).toEqual(["head"]));
    await waitFor(() => expect(continuation()?.key).toContain("new-cursor"));
  });

  it("drops previous rows as soon as filter membership changes", async () => {
    const pending = new Promise<IssueTableRowsResponse>(() => {});
    const listIssueTableRows = vi.fn(
      (request: IssueTableRowsRequest): Promise<IssueTableRowsResponse> =>
        request.query.search
          ? pending
          : Promise.resolve({
              query_fingerprint: "original-scope",
              group_key: null,
              parent_id: null,
              total: 1,
              branch_total: 1,
              next_cursor: null,
              rows: [{ issue: makeIssue({ id: "old-row" }), direct_child_count: 0 }],
            }),
    );
    setApiInstance({ listIssueTableRows } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result, rerender } = renderHook(
      (search: string) => useIssueTableHierarchyRows({
        wsId: "ws-1",
        serverQuery: { ...query, search },
        serverGroupSpec: { kind: "none" },
        tableHierarchy: true,
        usesServerGrouping: false,
        groupKeys: [],
        collapsedGroupKeys: [],
        collapsedParentKeys: [],
        enabled: true,
      }),
      { wrapper, initialProps: "" },
    );
    await waitFor(() => expect(result.current.issues.map((issue) => issue.id)).toEqual(["old-row"]));
    rerender("new filter");
    await waitFor(() => expect(listIssueTableRows).toHaveBeenCalledWith(expect.objectContaining({
      query: expect.objectContaining({ search: "new filter" }),
    })));
    expect(result.current.issues).toEqual([]);
  });

  it("loads grouped parent and child in the same native group branch", async () => {
    const groupKey = "todo";
    const parent = makeIssue({
      id: "parent-1",
      title: "Parent task",
      status: "todo",
    });
    const child = makeIssue({
      id: "child-1",
      title: "Child task",
      parent_issue_id: "parent-1",
      status: "todo",
    });
    const listIssueTableRows = vi.fn(
      async (req: IssueTableRowsRequest): Promise<IssueTableRowsResponse> => {
        if (req.group_key !== groupKey) {
          return {
            query_fingerprint: "fp",
            group_key: req.group_key,
            parent_id: null,
            total: 0,
            branch_total: 0,
            next_cursor: null,
            rows: [],
          };
        }
        if (req.parent_id === null) {
          return {
            query_fingerprint: "fp",
            group_key: groupKey,
            parent_id: null,
            total: 1,
            branch_total: 1,
            next_cursor: null,
            rows: [{ issue: parent, direct_child_count: 1 }],
          };
        }
        return {
          query_fingerprint: "fp",
          group_key: groupKey,
          parent_id: "parent-1",
          total: 1,
          branch_total: 1,
          next_cursor: null,
          rows: [{ issue: child, direct_child_count: 0 }],
        };
      },
    );
    setApiInstance({ listIssueTableRows } as unknown as ApiClient);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(
      () =>
        useIssueTableHierarchyRows({
          wsId: "ws-1",
          serverQuery: query,
          serverGroupSpec: { kind: "status" },
          tableHierarchy: true,
          usesServerGrouping: true,
          groupKeys: [groupKey],
          collapsedGroupKeys: [],
          collapsedParentKeys: [],
          enabled: true,
        }),
      { wrapper },
    );

    await waitFor(() =>
      expect(result.current.issues.map((issue) => issue.title)).toEqual([
        "Parent task",
        "Child task",
      ]),
    );
    expect(
      result.current.gridItems.some(
        (item) => item.kind === "issue" && item.groupKey === groupKey,
      ),
    ).toBe(true);
  });

  it("returns no rows while disabled (ungrouped plan)", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(
      () =>
        useIssueTableHierarchyRows({
          wsId: "ws-1",
          serverQuery: query,
          serverGroupSpec: { kind: "none" },
          tableHierarchy: true,
          usesServerGrouping: false,
          groupKeys: [],
          collapsedGroupKeys: [],
          collapsedParentKeys: [],
          enabled: false,
        }),
      { wrapper },
    );
    expect(result.current.issues).toEqual([]);
    expect(result.current.gridItems).toEqual([]);
  });

  it("loads parent and nested child native branches", async () => {
    const parent = makeIssue({ id: "parent-1", title: "Parent task" });
    const child = makeIssue({
      id: "child-1",
      title: "Child task",
      parent_issue_id: "parent-1",
    });
    const listIssueTableRows = vi.fn(
      async (req: IssueTableRowsRequest): Promise<IssueTableRowsResponse> => {
        if (req.parent_id === null) {
          return {
            query_fingerprint: "fp",
            group_key: null,
            parent_id: null,
            total: 1,
            branch_total: 1,
            next_cursor: null,
            rows: [{ issue: parent, direct_child_count: 1 }],
          };
        }
        return {
          query_fingerprint: "fp",
          group_key: null,
          parent_id: req.parent_id,
          total: 1,
          branch_total: 1,
          next_cursor: null,
          rows: [{ issue: child, direct_child_count: 0 }],
        };
      },
    );
    setApiInstance({ listIssueTableRows } as unknown as ApiClient);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
  const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(
      () =>
        useIssueTableHierarchyRows({
          wsId: "ws-1",
          serverQuery: query,
          serverGroupSpec: { kind: "none" },
          tableHierarchy: true,
          usesServerGrouping: false,
          groupKeys: [],
          collapsedGroupKeys: [],
          collapsedParentKeys: [],
          enabled: true,
        }),
      { wrapper },
    );

    await waitFor(() =>
      expect(result.current.issues.map((issue) => issue.title)).toEqual([
        "Parent task",
        "Child task",
      ]),
    );
  });

  it("pages grandchild branches and hides collapsed descendants", async () => {
    const parent = makeIssue({ id: "parent-1", title: "Parent task" });
    const child = makeIssue({
      id: "child-1",
      title: "Child task",
      parent_issue_id: "parent-1",
    });
    const grandchild = makeIssue({
      id: "gc-1",
      title: "Grandchild task",
      parent_issue_id: "child-1",
    });
    const listIssueTableRows = vi.fn(
      async (req: IssueTableRowsRequest): Promise<IssueTableRowsResponse> => {
        if (req.parent_id === null) {
          return {
            query_fingerprint: "fp",
            group_key: null,
            parent_id: null,
            total: 1,
            branch_total: 1,
            next_cursor: null,
            rows: [{ issue: parent, direct_child_count: 1 }],
          };
        }
        if (req.parent_id === "parent-1") {
          return {
            query_fingerprint: "fp",
            group_key: null,
            parent_id: "parent-1",
            total: 1,
            branch_total: 1,
            next_cursor: null,
            rows: [{ issue: child, direct_child_count: 1 }],
          };
        }
        if (req.page?.cursor === "gc-next") {
          return {
            query_fingerprint: "fp",
            group_key: null,
            parent_id: "child-1",
            total: 2,
            branch_total: 1,
            next_cursor: null,
            rows: [{ issue: grandchild, direct_child_count: 0 }],
          };
        }
        return {
          query_fingerprint: "fp",
          group_key: null,
          parent_id: "child-1",
          total: 2,
          branch_total: 0,
          next_cursor: "gc-next",
          rows: [],
        };
      },
    );
    setApiInstance({ listIssueTableRows } as unknown as ApiClient);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result, rerender } = renderHook(
      (collapsedParentKeys: string[]) =>
        useIssueTableHierarchyRows({
          wsId: "ws-1",
          serverQuery: query,
          serverGroupSpec: { kind: "none" },
          tableHierarchy: true,
          usesServerGrouping: false,
          groupKeys: [],
          collapsedGroupKeys: [],
          collapsedParentKeys,
          enabled: true,
        }),
      { wrapper, initialProps: [] as string[] },
    );

    await waitFor(() => expect(result.current.issues).toHaveLength(2));
    const loadMoreFooter = result.current.gridItems.find(
      (item): item is HierarchyGridFooter =>
        item.kind === "footer" && item.state === "has_more",
    );
    expect(loadMoreFooter).toBeDefined();
    loadMoreFooter?.onAction?.();
    await waitFor(() =>
      expect(result.current.issues.map((issue) => issue.title)).toContain(
        "Grandchild task",
      ),
    );

    rerender(["parent-1"]);
    await waitFor(() =>
      expect(
        result.current.issues.some((issue) => issue.id === "child-1"),
      ).toBe(false),
    );
  });
});
