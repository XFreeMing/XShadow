"use client";

import { useMemo, useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { goalListPagesOptions, goalPlanOptions } from "@multica/core/goals/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions } from "@multica/core/projects/queries";
import { propertyListOptions } from "@multica/core/properties";
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

type ArchitectureSourceMode = "hierarchy" | "goal_dependencies" | "relation";

export interface IssueArchitectureSourcesProps {
  serverQuery: IssueTableQuerySpec;
  projectId?: string;
  children?: ReactNode;
  renderHierarchy?: (rowPages: ArchitectureRowPages) => ReactNode;
}

const DEPENDENCY_ROW_PAGE_LIMIT = 100;
const DEPENDENCY_ROW_AUTO_LOAD_MAX_PAGES = 10;
type ArchitectureRowPages = ReturnType<typeof useIssueTableFlatRowPages>;
type DependencyGraphData = { rows: readonly Issue[]; edges: IssueDependencyEdge[] };

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

export function IssueArchitectureSources({
  serverQuery,
  projectId,
  children,
  renderHierarchy,
}: IssueArchitectureSourcesProps) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const [manualMode, setManualMode] = useState<ArchitectureSourceMode | null>(null);

  const rowPages = useIssueTableFlatRowPages(wsId, serverQuery, DEPENDENCY_ROW_PAGE_LIMIT, {
    autoLoadAll: true,
    maxAutoLoadPages: DEPENDENCY_ROW_AUTO_LOAD_MAX_PAGES,
  });
  const { hasVisibleHierarchy, visibleProjectIds } = useMemo(() => {
    const issueIds = new Set(rowPages.issues.map((issue) => issue.id));
    return {
      hasVisibleHierarchy: rowPages.issues.some(
        (issue) => issue.parent_issue_id !== null && issueIds.has(issue.parent_issue_id),
      ),
      visibleProjectIds: new Set(rowPages.issues.flatMap((issue) => issue.project_id ? [issue.project_id] : [])),
    };
  }, [rowPages.issues]);

  const canChooseGoal = !manualMode && !hasVisibleHierarchy && visibleProjectIds.size > 0;
  const goalsQuery = useInfiniteQuery({
    ...goalListPagesOptions(wsId, { projectId }),
    enabled: !!wsId && canChooseGoal,
  });
  const visibleGoals = goalsQuery.data?.pages
    .flatMap((page) => page.goals)
    .filter((goal) => goal.project_id !== null && visibleProjectIds.has(goal.project_id)) ?? [];
  const autoGoal = !goalsQuery.hasNextPage && visibleGoals.length === 1 ? visibleGoals[0] : undefined;
  const planQuery = useQuery({
    ...goalPlanOptions(wsId, autoGoal?.id ?? ""),
    enabled: !!wsId && canChooseGoal && !!autoGoal,
  });
  const autoGoalId = useMemo(() => {
    const plan = planQuery.data;
    if (hasVisibleHierarchy || !autoGoal || plan?.goal_id !== autoGoal.id
      || plan.project_id !== autoGoal.project_id) return undefined;
    const { edges } = buildGoalDependencyGraphData(plan, rowPages.issues, autoGoal.project_id);
    return edges.length > 0 ? autoGoal.id : undefined;
  }, [hasVisibleHierarchy, autoGoal, planQuery.data, rowPages.issues]);
  const activeMode = manualMode ?? (autoGoalId ? "goal_dependencies" : "hierarchy");

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 gap-1 border-b px-3 py-1.5">
        {(
          [
            ["hierarchy", t(($) => $.table.hierarchy)],
            ["goal_dependencies", t(($) => $.gantt.goal_plan)],
            ["relation", t(($) => $.org_chart.relation_source)],
          ] as const
        ).map(([value, label]) => (
          <Button
            key={value}
            size="sm"
            variant={activeMode === value ? "secondary" : "ghost"}
            aria-pressed={activeMode === value}
            onClick={() => setManualMode(value)}
          >
            {label}
          </Button>
        ))}
      </div>
      {activeMode === "hierarchy" ? (
        renderHierarchy ? renderHierarchy(rowPages) : children
      ) : activeMode === "goal_dependencies" ? (
        <GoalDependencyPicker
          projectId={projectId}
          defaultGoalId={autoGoalId}
          rowPages={rowPages}
        />
      ) : (
        <IssueRelationDependencyPicker rowPages={rowPages} />
      )}
    </div>
  );
}

export function buildIssueRelationGraphData(
  rows: readonly Issue[],
  propertyId: string,
  publicProjectIds: ReadonlySet<string>,
): DependencyGraphData {
  const visibleTargetIds = new Set(
    rows.filter((issue) => !issue.project_id || publicProjectIds.has(issue.project_id)).map((issue) => issue.id),
  );
  const edges: IssueDependencyEdge[] = [];
  for (const issue of rows) {
    const value = issue.properties[propertyId];
    if (!Array.isArray(value)) continue;
    for (const prerequisiteId of value) {
      if (prerequisiteId !== issue.id && visibleTargetIds.has(prerequisiteId)) {
        edges.push({ fromId: prerequisiteId, toId: issue.id });
      }
    }
  }
  return { rows, edges };
}

function IssueRelationDependencyPicker({ rowPages }: { rowPages: ArchitectureRowPages }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const properties = useQuery(propertyListOptions(wsId));
  const projects = useQuery(projectListOptions(wsId));
  const [selectedId, setSelectedId] = useState("");
  const fields = (properties.data ?? []).filter((field) =>
    !field.archived && field.type === "record_relation" && field.config.target_kind === "issues",
  );
  const field = fields.find((choice) => choice.id === selectedId) ?? fields[0];
  const publicProjectIds = useMemo(() => new Set(
    (projects.data ?? []).filter((project) => project.visibility !== "private").map((project) => project.id),
  ), [projects.data]);
  if (properties.isPending || projects.isPending) return <Spinner className="m-4" />;
  if (properties.isError || projects.isError) {
    return (
      <div className="flex items-center gap-2 p-4 text-body" role="alert">
        {t(($) => $.table.load_failed)}
        <Button size="sm" variant="outline" onClick={() => void Promise.all([properties.refetch(), projects.refetch()])}>
          {t(($) => $.table.load_failed_retry)}
        </Button>
      </div>
    );
  }
  if (!field) return <p className="p-4 text-body text-muted-foreground">{t(($) => $.org_chart.relation_no_fields)}</p>;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b px-4 py-2.5">
        <Select
          value={field.id}
          items={fields.map((choice) => ({ value: choice.id, label: choice.name }))}
          onValueChange={(id) => setSelectedId(id ?? "")}
        >
          <SelectTrigger size="sm" aria-label={t(($) => $.org_chart.relation_source)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {fields.map((choice) => <SelectItem key={choice.id} value={choice.id}>{choice.name}</SelectItem>)}
          </SelectContent>
        </Select>
        <span className="text-caption text-muted-foreground">{t(($) => $.org_chart.relation_direction)}</span>
      </div>
      <VisibleDependencyRows
        key={field.id}
        rowPages={rowPages}
        relationLabel={field.name}
        graph={(rows) => buildIssueRelationGraphData(rows, field.id, publicProjectIds)}
      />
    </div>
  );
}

function GoalDependencyPicker({
  projectId,
  defaultGoalId,
  rowPages,
}: {
  projectId?: string;
  defaultGoalId?: string;
  rowPages: ArchitectureRowPages;
}) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const query = useInfiniteQuery(goalListPagesOptions(wsId, { projectId }));
  const goals =
    query.data?.pages.flatMap((page) => page.goals).filter((goal) => goal.project_id) ?? [];
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const effectiveGoalId = selectedId ?? defaultGoalId;
  const goal = (effectiveGoalId ? goals.find((item) => item.id === effectiveGoalId) : null) ?? goals[0];
  const choices = goals.map((item) => ({ value: item.id, label: item.title }));

  if (query.isLoading) return <Spinner className="m-4" />;
  if (query.isError && !query.isFetchNextPageError) {
    return <GoalLoadRetry onRetry={() => void query.refetch()} />;
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2.5">
        {goal ? (
          <Select value={goal.id} items={choices} onValueChange={(id) => setSelectedId(id ?? "")}>
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
            {t(($) => $.plan.project_required)}
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
  rowPages: ArchitectureRowPages;
}) {
  const { t: issueText } = useT("issues");
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
  return (
    <VisibleDependencyRows
      rowPages={rowPages}
      graph={(rows) => buildGoalDependencyGraphData(plan, rows, goal.project_id)}
    />
  );
}

function VisibleDependencyRows({
  rowPages,
  graph,
  relationLabel,
}: {
  rowPages: ArchitectureRowPages;
  graph: (rows: readonly Issue[]) => DependencyGraphData;
  relationLabel?: string;
}) {
  const { t: issueText } = useT("issues");
  const { t: goalText } = useT("goals");
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
  const graphData = graph(rowPages.issues);
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
        relationLabel={relationLabel}
      />
    </div>
  );
}
