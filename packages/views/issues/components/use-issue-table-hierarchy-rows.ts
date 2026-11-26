"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  useQueries,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import { issueKeys, issueTableRowPageOptions } from "@multica/core/issues/queries";
import type { IssueTableGroupSpec, IssueTableQuerySpec, IssueTableRowsResponse } from "@multica/core/types";
import {
  buildHierarchyGridItems,
  issueTableMembershipIdentity,
  rebaseServerBranchState,
  sameStringArray,
  serverBranchKey,
  type HierarchyGridItem,
  type ServerBranch,
  type ServerBranchData,
  type ServerBranchState,
} from "./issue-table-server-branches";

const GRID_PAGE_LIMIT = 50;

type BranchPageTarget = {
  branch: ServerBranch;
  cursor: string | null;
};

export function useIssueTableHierarchyRows({
  wsId,
  serverQuery,
  serverGroupSpec,
  tableHierarchy,
  usesServerGrouping,
  groupKeys,
  collapsedGroupKeys,
  collapsedParentKeys,
  enabled,
}: {
  wsId: string;
  serverQuery: IssueTableQuerySpec;
  serverGroupSpec: IssueTableGroupSpec;
  tableHierarchy: boolean;
  usesServerGrouping: boolean;
  groupKeys: readonly string[];
  collapsedGroupKeys: readonly string[];
  collapsedParentKeys: readonly string[];
  enabled: boolean;
}) {
  const queryClient = useQueryClient();
  const collapsedGroupSet = useMemo(
    () => new Set(collapsedGroupKeys),
    [collapsedGroupKeys],
  );
  const collapsedParentSet = useMemo(
    () => new Set(collapsedParentKeys),
    [collapsedParentKeys],
  );

  const serverIdentity = useMemo(
    () => JSON.stringify([wsId, serverQuery, serverGroupSpec, tableHierarchy]),
    [serverGroupSpec, serverQuery, tableHierarchy, wsId],
  );
  const serverStructureIdentity = useMemo(
    () => JSON.stringify([issueTableMembershipIdentity(wsId, serverQuery), serverGroupSpec, tableHierarchy]),
    [serverGroupSpec, serverQuery, tableHierarchy, wsId],
  );

  const [serverBranchState, setServerBranchState] = useState<ServerBranchState>({
    identity: "",
    structureIdentity: "",
    branches: new Map(),
  });

  const rebasedServerBranchState = useMemo(
    () =>
      rebaseServerBranchState(
        serverBranchState,
        serverIdentity,
        serverStructureIdentity,
        usesServerGrouping,
      ),
    [
      serverBranchState,
      serverIdentity,
      serverStructureIdentity,
      usesServerGrouping,
    ],
  );

  useEffect(() => {
    if (rebasedServerBranchState !== serverBranchState) {
      setServerBranchState(rebasedServerBranchState);
    }
  }, [rebasedServerBranchState, serverBranchState]);

  const activeServerBranches = rebasedServerBranchState.branches;
  const serverBranchPageTargets = useMemo<BranchPageTarget[]>(
    () =>
      enabled
        ? [...activeServerBranches.values()].flatMap((branch) =>
            branch.cursors.map((cursor) => ({ branch, cursor })),
          )
        : [],
    [activeServerBranches, enabled],
  );

  const serverBranchQueries = useMemo(
    () =>
      serverBranchPageTargets.map(({ branch, cursor }) => {
        const base = issueTableRowPageOptions(wsId, {
          query: serverQuery,
          group: serverGroupSpec,
          group_key: branch.groupKey,
          hierarchy: { enabled: tableHierarchy },
          parent_id: branch.parentId,
          page: { limit: GRID_PAGE_LIMIT, cursor },
        });
        return {
          ...base,
          placeholderData: undefined,
          enabled:
            enabled &&
            (branch.groupKey === null ||
              !collapsedGroupSet.has(branch.groupKey)) &&
            !branch.ancestorIds.some((id) => collapsedParentSet.has(id)),
        };
      }),
    [
      collapsedGroupSet,
      collapsedParentSet,
      enabled,
      serverBranchPageTargets,
      serverGroupSpec,
      serverQuery,
      tableHierarchy,
      wsId,
    ],
  );

  const combineServerBranchQueries = useCallback(
    (results: Array<UseQueryResult<IssueTableRowsResponse, Error>>) => {
      const byBranch: Record<string, ServerBranchData> = {};
      const seenByBranch = new Map<string, Set<string>>();
      for (let index = 0; index < serverBranchPageTargets.length; index += 1) {
        const target = serverBranchPageTargets[index];
        const result = results[index];
        if (!target || !result) continue;
        const key = target.branch.key;
        const current = byBranch[key] ?? {
          rows: [],
          total: 0,
          nextCursor: null,
          validCursorCount: 0,
          loading: false,
          error: false,
        };
        if (
          target.cursor !== target.branch.cursors[current.validCursorCount] ||
          target.cursor !== current.nextCursor
        ) continue;
        current.validCursorCount += 1;
        const page = result.data;
        if (page) {
          const seen = seenByBranch.get(key) ?? new Set<string>();
          for (const row of page.rows) {
            if (seen.has(row.issue.id)) continue;
            seen.add(row.issue.id);
            current.rows.push(row);
          }
          seenByBranch.set(key, seen);
          if (target.cursor === null) current.total = page.total;
        }
        current.nextCursor = page?.next_cursor ?? null;
        current.loading ||= result.isPending || result.isFetching;
        current.error ||= result.isError;
        byBranch[key] = current;
      }
      return byBranch;
    },
    [serverBranchPageTargets],
  );

  const serverBranchData = useQueries({
    queries: serverBranchQueries,
    combine: combineServerBranchQueries,
  });

  useEffect(() => {
    if (!enabled) return;
    setServerBranchState((previous) => {
      if (previous.identity !== serverIdentity) return previous;
      let branches: Map<string, ServerBranch> | undefined;
      for (const [key, branch] of previous.branches) {
        const count = serverBranchData[key]?.validCursorCount;
        if (
          count === undefined ||
          count >= branch.cursors.length ||
          activeServerBranches.get(key)?.cursors !== branch.cursors
        ) continue;
        branches ??= new Map(previous.branches);
        branches.set(key, { ...branch, cursors: branch.cursors.slice(0, count) });
      }
      return branches ? { ...previous, branches } : previous;
    });
  }, [activeServerBranches, enabled, serverBranchData, serverIdentity]);

  useEffect(() => {
    if (!enabled) return;
    const desiredAncestors = new Map<string, string[]>();
    const visited = new Set<string>();
    const visit = (
      groupKey: string | null,
      parentId: string | null,
      ancestors: string[],
    ) => {
      const key = serverBranchKey(groupKey, parentId);
      if (visited.has(key)) return;
      visited.add(key);
      desiredAncestors.set(key, ancestors);
      for (const row of serverBranchData[key]?.rows ?? []) {
        if (row.direct_child_count > 0) {
          visit(groupKey, row.issue.id, [...ancestors, row.issue.id]);
        }
      }
    };

    if (usesServerGrouping) {
      for (const groupKey of groupKeys) visit(groupKey, null, []);
    } else {
      visit(null, null, []);
    }

    setServerBranchState((previous) => {
      if (previous.identity !== serverIdentity) return previous;
      let branches: Map<string, ServerBranch> | null = null;
      for (const [key, ancestors] of desiredAncestors) {
        const branch = previous.branches.get(key);
        if (!branch || sameStringArray(branch.ancestorIds, ancestors)) continue;
        branches ??= new Map(previous.branches);
        branches.set(key, { ...branch, ancestorIds: ancestors });
      }
      return branches ? { ...previous, branches } : previous;
    });
  }, [
    enabled,
    groupKeys,
    serverBranchData,
    serverIdentity,
    usesServerGrouping,
  ]);

  const activateBranch = useCallback(
    (
      groupKey: string | null,
      parentId: string | null,
      ancestorIds: string[],
    ) => {
      setServerBranchState((previous) => {
        if (previous.identity !== serverIdentity) return previous;
        const key = serverBranchKey(groupKey, parentId);
        const existing = previous.branches.get(key);
        if (existing && sameStringArray(existing.ancestorIds, ancestorIds)) {
          return previous;
        }
        const branches = new Map(previous.branches);
        branches.set(
          key,
          existing
            ? { ...existing, ancestorIds }
            : {
                key,
                groupKey,
                parentId,
                ancestorIds,
                cursors: [null],
              },
        );
        return { ...previous, branches };
      });
    },
    [serverIdentity],
  );

  const loadMoreBranch = useCallback(
    (branchKey: string, cursor: string) => {
      setServerBranchState((previous) => {
        if (previous.identity !== serverIdentity) return previous;
        const branch = previous.branches.get(branchKey);
        if (!branch || branch.cursors.includes(cursor)) return previous;
        const branches = new Map(previous.branches);
        branches.set(branchKey, {
          ...branch,
          cursors: [...branch.cursors, cursor],
        });
        return { ...previous, branches };
      });
    },
    [serverIdentity],
  );

  const retryBranch = useCallback(
    (branchKey: string) => {
      const branch = activeServerBranches.get(branchKey);
      if (!branch) return;
      void queryClient.refetchQueries({
        queryKey: issueKeys.tableRows(
          wsId,
          serverQuery,
          serverGroupSpec,
          branch.groupKey,
          tableHierarchy,
          branch.parentId,
        ),
        exact: false,
        type: "active",
        predicate: (entry) => entry.state.status === "error",
      });
    },
    [
      activeServerBranches,
      queryClient,
      serverGroupSpec,
      serverQuery,
      tableHierarchy,
      wsId,
    ],
  );

  useEffect(() => {
    if (!enabled || !usesServerGrouping) return;
    for (const groupKey of groupKeys) {
      if (collapsedGroupSet.has(groupKey)) continue;
      const key = serverBranchKey(groupKey, null);
      if (activeServerBranches.has(key)) continue;
      activateBranch(groupKey, null, []);
    }
  }, [
    activateBranch,
    activeServerBranches,
    collapsedGroupSet,
    enabled,
    groupKeys,
    usesServerGrouping,
  ]);

  useEffect(() => {
    if (!enabled || !tableHierarchy) return;
    for (const branch of activeServerBranches.values()) {
      const data = serverBranchData[branch.key];
      if (!data) continue;
      for (const row of data.rows) {
        if (row.direct_child_count === 0) continue;
        if (collapsedParentSet.has(row.issue.id)) continue;
        const childKey = serverBranchKey(branch.groupKey, row.issue.id);
        if (activeServerBranches.has(childKey)) continue;
        activateBranch(branch.groupKey, row.issue.id, [
          ...branch.ancestorIds,
          row.issue.id,
        ]);
      }
    }
  }, [
    activateBranch,
    activeServerBranches,
    collapsedParentSet,
    enabled,
    serverBranchData,
    tableHierarchy,
  ]);

  const gridItems = useMemo<HierarchyGridItem[]>(
    () =>
      enabled
        ? buildHierarchyGridItems({
            usesServerGrouping,
            groupKeys,
            tableHierarchy,
            collapsedGroupSet,
            collapsedParentSet,
            activeServerBranches,
            serverBranchData,
            activateBranch,
            loadMoreBranch: loadMoreBranch,
            retryBranch,
          })
        : [],
    [
      activateBranch,
      activeServerBranches,
      collapsedGroupSet,
      collapsedParentSet,
      enabled,
      groupKeys,
      loadMoreBranch,
      retryBranch,
      serverBranchData,
      tableHierarchy,
      usesServerGrouping,
    ],
  );

  const issues = useMemo(
    () =>
      gridItems.flatMap((item) =>
        item.kind === "issue" ? [item.issue] : [],
      ),
    [gridItems],
  );

  const rootBranchKey = serverBranchKey(null, null);
  const rootBranchData = serverBranchData[rootBranchKey];
  const isHeadLoading = Boolean(
    enabled &&
      !usesServerGrouping &&
      (!rootBranchData ||
        (rootBranchData.loading && rootBranchData.rows.length === 0)),
  );
  const isHeadError = Boolean(
    enabled &&
      !usesServerGrouping &&
      rootBranchData?.error &&
      rootBranchData.rows.length === 0,
  );
  const total = usesServerGrouping ? issues.length : rootBranchData?.total ?? issues.length;

  return {
    gridItems,
    issues,
    total,
    isHeadLoading,
    isHeadError,
    retryRoot: () => retryBranch(rootBranchKey),
  };
}
