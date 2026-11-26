import type { Issue, IssueTableRowsResponse } from "@multica/core/types";

export type ServerBranch = {
  key: string;
  groupKey: string | null;
  parentId: string | null;
  ancestorIds: string[];
  cursors: Array<string | null>;
};

export type ServerBranchState = {
  identity: string;
  structureIdentity: string;
  branches: Map<string, ServerBranch>;
};

export type ServerBranchData = {
  rows: IssueTableRowsResponse["rows"];
  total: number;
  nextCursor: string | null;
  validCursorCount: number;
  loading: boolean;
  error: boolean;
};

export function serverBranchKey(
  groupKey: string | null,
  parentId: string | null,
) {
  return `${groupKey ?? "ungrouped"}::${parentId ?? "root"}`;
}

/** Server-owned task table membership (filters/search/scope), scoped to workspace. */
export function issueTableMembershipIdentity(
  wsId: string,
  query: {
    scope: unknown;
    filters: unknown;
    search?: string;
  },
) {
  return JSON.stringify([
    wsId,
    query.scope,
    query.filters,
    query.search ?? "",
  ]);
}

function sameStringArray(left: string[], right: string[]) {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}

export function rebaseServerBranchState(
  previous: ServerBranchState,
  identity: string,
  structureIdentity: string,
  usesServerGrouping: boolean,
): ServerBranchState {
  if (previous.identity === identity) return previous;

  const branches =
    previous.structureIdentity === structureIdentity
      ? new Map(
          [...previous.branches].map(([key, branch]) => [
            key,
            { ...branch, cursors: [null] },
          ]),
        )
      : new Map<string, ServerBranch>();

  if (!usesServerGrouping) {
    const key = serverBranchKey(null, null);
    if (!branches.has(key)) {
      branches.set(key, {
        key,
        groupKey: null,
        parentId: null,
        ancestorIds: [],
        cursors: [null],
      });
    }
  }

  return { identity, structureIdentity, branches };
}

export type HierarchyGridRow = {
  kind: "issue";
  key: string;
  groupKey: string | null;
  issue: Issue;
  depth: number;
  hasChildren: boolean;
  collapsed: boolean;
  directChildCount: number;
};

export type HierarchyGridFooter = {
  kind: "footer";
  key: string;
  groupKey: string | null;
  insertAfterIssueId: string | null;
  state: "loading" | "has_more" | "error" | "activate";
  onAction?: () => void;
};

export type HierarchyGridItem = HierarchyGridRow | HierarchyGridFooter;

export function buildHierarchyGridItems({
  usesServerGrouping,
  groupKeys,
  tableHierarchy,
  collapsedGroupSet,
  collapsedParentSet,
  activeServerBranches,
  serverBranchData,
  activateBranch,
  loadMoreBranch,
  retryBranch,
}: {
  usesServerGrouping: boolean;
  groupKeys: readonly string[];
  tableHierarchy: boolean;
  collapsedGroupSet: ReadonlySet<string>;
  collapsedParentSet: ReadonlySet<string>;
  activeServerBranches: Map<string, ServerBranch>;
  serverBranchData: Record<string, ServerBranchData>;
  activateBranch: (
    groupKey: string | null,
    parentId: string | null,
    ancestorIds: string[],
  ) => void;
  loadMoreBranch: (branchKey: string, cursor: string) => void;
  retryBranch: (branchKey: string) => void;
}): HierarchyGridItem[] {
  const result: HierarchyGridItem[] = [];
  const seenIssueIds = new Set<string>();

  const appendBranch = (
    groupKey: string | null,
    parentId: string | null,
    depth: number,
    ancestorIds: string[],
    insertAfterIssueId: string | null,
  ): string | null => {
    const key = serverBranchKey(groupKey, parentId);
    const data = serverBranchData[key];
    let lastIssueId = insertAfterIssueId;

    const pushFooter = (
      footerKey: string,
      state: HierarchyGridFooter["state"],
      onAction?: () => void,
    ) => {
      result.push({
        kind: "footer",
        key: footerKey,
        groupKey,
        insertAfterIssueId: lastIssueId,
        state,
        onAction,
      });
    };

    if (!data) {
      const registered = activeServerBranches.has(key);
      pushFooter(
        `${registered ? "loading" : "activate"}:${key}`,
        registered ? "loading" : "activate",
        registered
          ? undefined
          : () => activateBranch(groupKey, parentId, ancestorIds),
      );
      return lastIssueId;
    }

    if (data.rows.length === 0 && data.loading) {
      pushFooter(`loading:${key}`, "loading");
    }

    for (const row of data.rows) {
      if (seenIssueIds.has(row.issue.id)) continue;
      seenIssueIds.add(row.issue.id);
      const collapsed = collapsedParentSet.has(row.issue.id);
      const hasChildren = tableHierarchy && row.direct_child_count > 0;
      result.push({
        kind: "issue",
        key: row.issue.id,
        groupKey,
        issue: row.issue,
        depth,
        hasChildren,
        collapsed,
        directChildCount: row.direct_child_count,
      });
      lastIssueId = row.issue.id;
      if (tableHierarchy && row.direct_child_count > 0 && !collapsed) {
        lastIssueId =
          appendBranch(
            groupKey,
            row.issue.id,
            depth + 1,
            [...ancestorIds, row.issue.id],
            lastIssueId,
          ) ?? lastIssueId;
      }
    }

    if (data.error) {
      pushFooter(`retry:${key}`, "error", () => retryBranch(key));
    } else if (data.nextCursor) {
      const nextCursor = data.nextCursor;
      pushFooter(`more:${key}:${nextCursor}`, data.loading ? "loading" : "has_more", () =>
        loadMoreBranch(key, nextCursor),
      );
    }

    return lastIssueId;
  };

  if (usesServerGrouping) {
    for (const groupKey of groupKeys) {
      if (collapsedGroupSet.has(groupKey)) continue;
      appendBranch(groupKey, null, 0, [], null);
    }
  } else {
    appendBranch(null, null, 0, [], null);
  }

  return result;
}

export { sameStringArray };
