"use client";

import { useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { goalListPagesOptions, goalPlanOptions } from "@multica/core/goals/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import type { Goal, GoalPlan, Issue, IssueTableQuerySpec } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { useT } from "../../i18n";
import {
  IssueDependencyChart,
  type IssueDependencyEdge,
} from "./issue-dependency-chart";
import { useIssueTableFlatRowPages } from "./use-issue-table-flat-row-pages";

export interface IssueGoalDependencyViewProps {
  serverQuery: IssueTableQuerySpec;
  projectId?: string;
}

const DEPENDENCY_ROW_PAGE_LIMIT = 100;
const DEPENDENCY_ROW_AUTO_LOAD_MAX_PAGES = 10;
type DependencyRowPages = ReturnType<typeof useIssueTableFlatRowPages>;

/**
 * Maps a confirmed goal plan onto the currently authorized & filtered table rows.
 * Only plan items bound to a visible issue in the goal's project are included, and
 * only edges where both prerequisite and dependent are visible are emitted.
 */
export function buildGoalDependencyGraphData(
  plan: GoalPlan,
  visibleIssues: readonly Issue[],
  projectId: string | null,
): { rows: Issue[]; edges: IssueDependencyEdge[] } {
  const visibleById = new Map<string, Issue>();
  for (const issue of visibleIssues) {
    if (projectId && issue.project_id !== projectId) continue;
    if (!visibleById.has(issue.id)) {
      visibleById.set(issue.id, issue);
    }
  }

  const itemIdToIssueId = new Map<string, string>();
  const rows: Issue[] = [];
  const addedIssueIds = new Set<string>();

  for (const item of plan.items) {
    const issueId = item.binding.issue_id;
    if (!issueId) continue;
    const issue = visibleById.get(issueId);
    if (!issue) continue;
    itemIdToIssueId.set(item.id, issue.id);
    if (!addedIssueIds.has(issue.id)) {
      addedIssueIds.add(issue.id);
      rows.push(issue);
    }
  }

  const edges: IssueDependencyEdge[] = [];
  for (const edge of plan.edges) {
    const fromId = itemIdToIssueId.get(edge.prerequisite_item_id);
    const toId = itemIdToIssueId.get(edge.dependent_item_id);
    if (!fromId || !toId || fromId === toId) continue;
    edges.push({
      id: `${edge.prerequisite_item_id}->${edge.dependent_item_id}`,
      fromId,
      toId,
      kind: edge.kind === "conditional" ? "conditional" : "required",
      reason: edge.reason,
    });
  }

  return { rows, edges };
}

export function IssueGoalDependencyView({
  serverQuery,
  projectId,
}: IssueGoalDependencyViewProps) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const rowPages = useIssueTableFlatRowPages(wsId, serverQuery, DEPENDENCY_ROW_PAGE_LIMIT, {
    autoLoadAll: true,
    maxAutoLoadPages: DEPENDENCY_ROW_AUTO_LOAD_MAX_PAGES,
  });
  const query = useInfiniteQuery(goalListPagesOptions(wsId, { projectId }));
  const goals =
    query.data?.pages.flatMap((page) => page.goals).filter((goal) => goal.project_id) ?? [];
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const goal = goals.find((item) => item.id === selectedId) ?? goals[0];
  const choices = goals.map((item) => ({ value: item.id, label: item.title }));

  if (query.isLoading) return <Spinner className="m-4" />;
  if (query.isError && !query.isFetchNextPageError) {
    return <GoalLoadRetry onRetry={() => void query.refetch()} />;
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2.5">
        {goal ? (
          <Select value={goal.id} items={choices} onValueChange={setSelectedId}>
            <SelectTrigger size="sm" aria-label={t(($) => $.list.title)} className="max-w-full">
              <SelectValue className="truncate" />
            </SelectTrigger>
            <SelectContent>
              {choices.map((choice) => (
                <SelectItem key={choice.value} value={choice.value}>
                  {choice.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <span className="text-body text-muted-foreground">
            {t(($) => $.list.empty_title)}
          </span>
        )}
        {query.hasNextPage && (
          <Button
            size="sm"
            variant="outline"
            disabled={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            {query.isFetchNextPageError ? t(($) => $.list.retry) : t(($) => $.list.load_more)}
          </Button>
        )}
      </div>
      {goal && (
        <GoalDependencyCanvas
          key={goal.id}
          wsId={wsId}
          goal={goal}
          rowPages={rowPages}
        />
      )}
    </div>
  );
}

function GoalLoadRetry({ onRetry }: { onRetry: () => void }) {
  const { t } = useT("goals");
  return (
    <div className="flex items-center gap-2 p-4 text-body" role="alert">
      {t(($) => $.plan.load_failed)}
      <Button size="sm" variant="outline" onClick={onRetry}>
        {t(($) => $.plan.retry)}
      </Button>
    </div>
  );
}

function GoalDependencyCanvas({
  wsId,
  goal,
  rowPages,
}: {
  wsId: string;
  goal: Goal;
  rowPages: DependencyRowPages;
}) {
  const { t: issueText } = useT("issues");
  const { t: goalText } = useT("goals");
  const planQuery = useQuery(goalPlanOptions(wsId, goal.id));
  const plan = planQuery.data;
  const confirmed = plan?.goal_id === goal.id && plan?.project_id === goal.project_id;

  if (planQuery.isLoading) return <Spinner className="m-4" />;
  if (!confirmed || planQuery.isError) {
    return <GoalLoadRetry onRetry={() => void planQuery.refetch()} />;
  }
  if (!plan.items.length) {
    return <p className="px-4 py-3 text-body text-muted-foreground">{issueText(($) => $.gantt.empty_plan)}</p>;
  }
  if (rowPages.isHeadLoading) return <Spinner className="m-4" />;
  if (rowPages.isHeadError) {
    return (
      <div className="flex items-center gap-2 p-4 text-body" role="alert">
        {issueText(($) => $.table.load_failed)}
        <Button size="sm" variant="outline" onClick={rowPages.retryHead}>
          {issueText(($) => $.table.load_failed_retry)}
        </Button>
      </div>
    );
  }
  const graphData = buildGoalDependencyGraphData(plan, rowPages.issues, goal.project_id);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {(rowPages.isTailLoading || rowPages.isTailError || rowPages.canLoadMore) && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-muted/20 px-4 py-1.5 text-caption text-muted-foreground">
          {rowPages.total > 0 && (
            <span>
              {issueText(($) => $.org_chart.loaded_progress, {
                loaded: rowPages.issues.length,
                total: rowPages.total,
              })}
            </span>
          )}
          {rowPages.isTailLoading && <span>{issueText(($) => $.org_chart.loading_more)}</span>}
          {rowPages.isTailError && (
            <Button size="sm" variant="outline" onClick={rowPages.retryTail}>
              {issueText(($) => $.table.load_failed_retry)}
            </Button>
          )}
          {rowPages.canLoadMore && !rowPages.isTailLoading && (
            <Button size="sm" variant="outline" onClick={rowPages.handleLoadMore}>
              {goalText(($) => $.list.load_more)}
            </Button>
          )}
        </div>
      )}
      <IssueDependencyChart
        rows={graphData.rows}
        edges={graphData.edges}
      />
    </div>
  );
}
