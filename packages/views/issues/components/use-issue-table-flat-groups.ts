"use client";

import { useMemo } from "react";
import type { Issue, IssueTableQuerySpec } from "@multica/core/types";
import {
  useIssueGroupBranches,
  type IssueGroupBranches,
} from "../surface/use-issue-group-branches";
import { tableGroupSpec } from "./issue-table-grouping";

export function useIssueTableFlatGroups({
  wsId,
  serverQuery,
  tableGrouping,
  collapsedGroupKeys,
  rowsEnabled = true,
}: {
  wsId: string;
  serverQuery: IssueTableQuerySpec;
  tableGrouping: string;
  collapsedGroupKeys: readonly string[];
  rowsEnabled?: boolean;
}): IssueGroupBranches & { serverGroupSpec: ReturnType<typeof tableGroupSpec> } {
  const serverGroupSpec = useMemo(
    () => tableGroupSpec(tableGrouping),
    [tableGrouping],
  );
  const usesServerGrouping = serverGroupSpec.kind !== "none";
  const requestGroup =
    serverGroupSpec.kind === "none"
      ? ({ kind: "status" } as const)
      : serverGroupSpec;

  const collapsedSet = useMemo(
    () => new Set(collapsedGroupKeys),
    [collapsedGroupKeys],
  );

  const branchState = useIssueGroupBranches({
    wsId,
    query: serverQuery,
    group: requestGroup,
    enabled: usesServerGrouping,
    rowsEnabled,
    activateHeadOnMount: true,
    collapsedBranchKeys: collapsedSet,
  });

  return {
    ...branchState,
    enabled: usesServerGrouping,
    serverGroupSpec,
  };
}

export function visibleTableGroupIssues(
  branchState: Pick<IssueGroupBranches, "descriptors" | "issuesByKey">,
  collapsedGroupKeys: ReadonlySet<string>,
): Issue[] {
  return branchState.descriptors.flatMap((descriptor) =>
    collapsedGroupKeys.has(descriptor.key)
      ? []
      : (branchState.issuesByKey[descriptor.key] ?? []),
  );
}
