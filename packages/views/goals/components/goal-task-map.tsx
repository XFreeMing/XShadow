"use client";

import { useId, useMemo, useState, type ReactNode } from "react";
import type { Goal, GoalPlan, Issue } from "@multica/core/types";
import {
  ArrowUpRight,
  ChevronDown,
  ChevronRight,
  CircleDashed,
  GitBranch,
  Link2,
  ListTree,
  Network,
  Target,
  Unlink,
  X,
} from "@multica/ui/icons";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import { StatusIcon } from "../../issues/components/status-icon";
import { GoalStatusBadge } from "./goal-status-badge";
import {
  buildGoalTaskGraph,
  collectDependencyChain,
  GOAL_ROOT_ID,
  type GoalTreeBranch,
} from "../task-tree";

export type GoalTaskMapView = "mindmap" | "tree";

export interface GoalTaskMapIssues {
  linkedIssues: Issue[];
  issuesLoading: boolean;
  issuesError: boolean;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  isFetchNextPageError: boolean;
  unlinkingIssueId: string | null;
  issueHref: (issue: Issue) => string;
  onRetryIssues: () => void;
  onFetchNextPage: () => void;
  onOpenLinkDialog: () => void;
  onUnlinkIssue: (issueId: string) => void;
}

interface GoalTaskMapProps extends GoalTaskMapIssues {
  goal: Goal;
  plan: GoalPlan | null;
  selectedId: string | null;
  onSelectTask: (id: string | null) => void;
  toolbar: ReactNode;
  children: ReactNode;
  onToggleExecution?: (enabled: boolean) => void;
  executionPending?: boolean;
  executionEnableBlocked: boolean;
}

export function GoalTaskMap({
  goal,
  plan,
  linkedIssues,
  issuesLoading,
  issuesError,
  hasNextPage,
  isFetchingNextPage,
  isFetchNextPageError,
  unlinkingIssueId,
  issueHref,
  onRetryIssues,
  onFetchNextPage,
  onOpenLinkDialog,
  onUnlinkIssue,
  selectedId,
  onSelectTask,
  toolbar,
  children,
  onToggleExecution,
  executionPending = false,
  executionEnableBlocked,
}: GoalTaskMapProps) {
  const { t } = useT("goals");
  const arrowId = useId().replace(/:/g, "");
  const [view, setView] = useState<GoalTaskMapView>("mindmap");
  const [hoveredId, setHoveredId] = useState<string | null>(null);
  const [collapsedIds, setCollapsedIds] = useState<ReadonlySet<string>>(new Set());

  const progress = plan?.progress ?? null;
  const executionState = plan?.execution_state ?? null;
  const executionAvailable =
    goal.status === "in_progress" && !!progress && progress.completed < progress.total;

  const showExecution = plan && (executionState || (plan.can_manage_execution && executionAvailable));
  const current = plan?.items.find((item) => item.id === progress?.item_id);
  const blockers = plan?.items.filter((item) => progress?.waiting_for?.includes(item.id)) ?? [];

  const graph = useMemo(
    () =>
      buildGoalTaskGraph({
        items: plan?.items ?? [],
        edges: plan?.edges ?? [],
        linkedIssues,
        progress,
      }),
    [plan?.items, plan?.edges, linkedIssues, progress],
  );

  const effectiveSelectedId = selectedId && graph.nodeById.has(selectedId) ? selectedId : null;
  const activeFocusId = hoveredId && graph.nodeById.has(hoveredId) ? hoveredId : effectiveSelectedId;
  const highlightedIds = useMemo(
    () => (activeFocusId ? collectDependencyChain(activeFocusId, graph.nodeById) : null),
    [activeFocusId, graph.nodeById],
  );

  const selectedNode = effectiveSelectedId ? (graph.nodeById.get(effectiveSelectedId) ?? null) : null;

  const completedCount = progress
    ? progress.completed
    : graph.nodes.filter((n) => n.isDone).length;
  const totalCount = progress ? progress.total : graph.nodes.length;
  const progressPct = totalCount > 0 ? Math.round((completedCount / totalCount) * 100) : 0;

  const toggleCollapse = (id: string) => {
    setCollapsedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const renderUnlinkButton = (issue: Issue) => {
    const busy = unlinkingIssueId === issue.id;
    return (
      <Button
        type="button"
        size="icon-xs"
        variant="ghost"
        className="size-6 shrink-0 text-muted-foreground hover:text-destructive"
        aria-label={t(($) => $.detail.unlink)}
        disabled={busy}
        onClick={(event) => {
          event.stopPropagation();
          onUnlinkIssue(issue.id);
        }}
      >
        {busy ? <Spinner className="size-3" aria-hidden="true" /> : <Unlink className="size-3" />}
      </Button>
    );
  };

  const renderTreeBranch = (branch: GoalTreeBranch, depth: number) => {
    const { node, children, extraPrerequisiteIds } = branch;
    const collapsed = collapsedIds.has(node.id);
    const hasChildren = children.length > 0;
    const isSelected = effectiveSelectedId === node.id;
    const isDimmed = highlightedIds !== null && !highlightedIds.has(node.id);

    return (
      <li key={node.id} className="relative">
        <div
          className={cn(
            "group flex items-center gap-2 rounded-md border border-transparent px-2 py-1.5 transition-colors",
            isSelected
              ? "border-brand bg-brand/[0.08]"
              : node.isCurrent
                ? "border-brand/30 bg-brand/[0.03] hover:bg-accent/60"
                : "hover:bg-accent/50",
            isDimmed && "opacity-45",
          )}
          onMouseEnter={() => setHoveredId(node.id)}
          onMouseLeave={() => setHoveredId((cur) => (cur === node.id ? null : cur))}
        >
          {hasChildren ? (
            <button
              type="button"
              aria-label={collapsed ? t(($) => $.map.expand_branch) : t(($) => $.map.collapse_branch)}
              aria-expanded={!collapsed}
              onClick={() => toggleCollapse(node.id)}
              className="flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
            >
              {collapsed ? <ChevronRight className="size-3.5" /> : <ChevronDown className="size-3.5" />}
            </button>
          ) : (
            <span className="flex size-5 shrink-0 items-center justify-center text-border" aria-hidden="true">
              <span className="h-px w-2.5 bg-border" />
            </span>
          )}

          {node.issue ? (
            <StatusIcon status={node.issue.status} className="size-3.5 shrink-0" />
          ) : (
            <CircleDashed className="size-3.5 shrink-0 text-muted-foreground" />
          )}
          {node.issue ? (
            <AppLink
              href={issueHref(node.issue)}
              className="shrink-0 font-mono text-caption text-muted-foreground hover:text-foreground hover:underline"
            >
              {node.issue.identifier}
            </AppLink>
          ) : null}

          <button
            type="button"
            onClick={() => onSelectTask(node.id)}
            aria-pressed={isSelected}
            className="flex min-w-0 flex-1 items-center gap-2 text-left"
          >
            <span className="min-w-0 flex-1 truncate text-body font-medium text-foreground">
              {node.title || t(($) => $.plan.untitled_item)}
            </span>

            {node.stage !== null ? (
              <Badge variant="outline" className="shrink-0 text-micro">
                {t(($) => $.map.stage_short, { stage: node.stage })}
              </Badge>
            ) : null}

            {node.isCurrent ? (
              <Badge variant="secondary" className="shrink-0 bg-brand/15 text-micro text-brand">
                {progress?.method_step || t(($) => $.map.current_badge)}
              </Badge>
            ) : null}

            {node.isWaiting ? (
              <Badge variant="outline" className="shrink-0 border-warning/60 text-micro text-warning">
                {t(($) => $.map.waiting_badge)}
              </Badge>
            ) : null}

            {extraPrerequisiteIds.length > 0 ? (
              <Badge variant="outline" className="shrink-0 gap-1 text-micro text-muted-foreground">
                <GitBranch className="size-2.5" />
                {t(($) => $.map.extra_prereqs, { count: extraPrerequisiteIds.length })}
              </Badge>
            ) : null}

            {hasChildren && collapsed ? (
              <span className="shrink-0 text-micro text-muted-foreground">
                +{children.length}
              </span>
            ) : null}
          </button>

          {node.issue ? renderUnlinkButton(node.issue) : null}
        </div>

        {hasChildren && !collapsed ? (
          <ul
            className={cn(
              "mt-0.5 space-y-0.5 border-l border-border/70 pl-3",
              depth === 0 ? "ml-4" : "ml-3.5",
            )}
          >
            {children.map((child) => renderTreeBranch(child, depth + 1))}
          </ul>
        ) : null}
      </li>
    );
  };

  return (
    <section
      className="@container overflow-clip rounded-xl border bg-card [overflow-anchor:none]"
      data-testid="goal-task-map"
    >
      {/* Toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-2 border-b bg-surface/50 px-4 py-2.5">
        <div className="flex flex-wrap items-center gap-2.5">
          <h2 className="text-body font-semibold text-foreground">{t(($) => $.map.title)}</h2>
          {totalCount > 0 ? (
            <div role="status" className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
              <span className="tabular-nums font-medium text-foreground">
                {t(($) => $.plan_progress.count, { completed: completedCount, total: totalCount })}
              </span>
              <div
                className="h-1.5 w-16 overflow-hidden rounded-full bg-muted"
                aria-hidden="true"
              >
                <div
                  className="h-full rounded-full bg-brand transition-all"
                  style={{ width: `${progressPct}%` }}
                />
              </div>
              {progress ? (
                <Badge variant="secondary" className="text-micro">
                  {t(($) => $.plan_progress.phase[progress.phase])}
                </Badge>
              ) : null}
              {current ? (
                <span className="sr-only">
                  {current.title}
                  {progress?.method_step ? ` · ${progress.method_step}` : ""}
                </span>
              ) : null}
              {blockers.length > 0 ? (
                <span>{t(($) => $.plan_progress.waiting_for, { tasks: blockers.map((item) => item.title).join(" · ") })}</span>
              ) : null}
            </div>
          ) : null}
          {showExecution && onToggleExecution ? (
            <div className="flex flex-wrap items-center gap-1.5 @3xl:border-l @3xl:pl-2.5">
              <span className="text-caption text-muted-foreground">
                {executionState === "active"
                  ? t(($) => $.plan_execution.active, { revision: plan.execution_revision })
                  : executionState === "stale"
                    ? t(($) => $.plan_execution.stale)
                    : t(($) => $.plan_execution.off)}
              </span>
              {plan.can_manage_execution && executionAvailable && executionState !== "active" ? (
                <Button
                  type="button"
                  size="xs"
                  variant="outline"
                  disabled={executionPending || executionEnableBlocked}
                  onClick={() => onToggleExecution(true)}
                >
                  {t(($) => $.plan_execution.enable, { revision: plan.revision })}
                </Button>
              ) : null}
              {plan.can_manage_execution && executionState ? (
                <Button
                  type="button"
                  size="xs"
                  variant="ghost"
                  className="text-muted-foreground"
                  disabled={executionPending}
                  onClick={() => onToggleExecution(false)}
                >
                  {t(($) => $.plan_execution.revoke)}
                </Button>
              ) : null}
            </div>
          ) : null}
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          {/* Mind map / Tree switcher */}
          <div
            role="group"
            aria-label={t(($) => $.map.view_switch)}
            className="inline-flex items-center rounded-md border bg-background p-0.5"
          >
            <Button
              type="button"
              size="xs"
              variant={view === "mindmap" ? "secondary" : "ghost"}
              aria-pressed={view === "mindmap"}
              className="h-6 gap-1 px-2 text-caption"
              onClick={() => setView("mindmap")}
            >
              <Network className="size-3.5" />
              {t(($) => $.map.mindmap_view)}
            </Button>
            <Button
              type="button"
              size="xs"
              variant={view === "tree" ? "secondary" : "ghost"}
              aria-pressed={view === "tree"}
              className="h-6 gap-1 px-2 text-caption"
              onClick={() => setView("tree")}
            >
              <ListTree className="size-3.5" />
              {t(($) => $.map.tree_view)}
            </Button>
          </div>

          <Button type="button" size="xs" variant="outline" onClick={onOpenLinkDialog}>
            <Link2 className="size-3.5" />
            {t(($) => $.detail.link_task)}
          </Button>
          {toolbar}
        </div>
      </div>

      {showExecution ? (
        <p className="border-b px-4 py-1.5 text-caption text-muted-foreground">
          {t(($) => $.plan_execution.scope)}
        </p>
      ) : null}

      {issuesError && !isFetchNextPageError && graph.nodes.length > 0 ? (
        <div role="alert" className="flex items-center gap-2 border-t px-4 py-2 text-caption text-muted-foreground">
          <span>{t(($) => $.detail.tasks_load_failed)}</span>
          <Button variant="ghost" size="sm" onClick={onRetryIssues}>
            {t(($) => $.list.retry)}
          </Button>
        </div>
      ) : null}

      {/* Body */}
      {issuesLoading && graph.nodes.length === 0 ? (
        <div className="space-y-2 p-4">
          <Skeleton className="h-24 w-full" />
        </div>
      ) : issuesError && graph.nodes.length === 0 ? (
        <div className="flex items-center gap-2 p-4 text-body text-muted-foreground">
          <span>{t(($) => $.detail.tasks_load_failed)}</span>
          <Button variant="ghost" size="sm" onClick={onRetryIssues}>
            {t(($) => $.list.retry)}
          </Button>
        </div>
      ) : graph.nodes.length === 0 ? (
        <p className="px-4 py-5 text-center text-body text-muted-foreground">
          {plan ? t(($) => $.plan.empty) : t(($) => $.detail.no_linked_tasks)}
        </p>
      ) : view === "mindmap" ? (
        /* Horizontal Mind Map Canvas */
        <div className="max-h-48 overflow-auto bg-surface/25" data-testid="goal-mindmap-canvas">
          <div
            className="relative"
            style={{ width: graph.canvasWidth, height: graph.canvasHeight }}
          >
            <svg
              aria-hidden="true"
              width={graph.canvasWidth}
              height={graph.canvasHeight}
              className="pointer-events-none absolute inset-0"
            >
              <defs>
                <marker
                  id={arrowId}
                  viewBox="0 0 8 8"
                  refX="7"
                  refY="4"
                  markerWidth="5"
                  markerHeight="5"
                  orient="auto"
                >
                  <path d="M0 0 L8 4 L0 8 Z" fill="context-stroke" />
                </marker>
              </defs>
              {graph.edges.map((edge) => {
                const fromPos =
                  edge.fromId === GOAL_ROOT_ID
                    ? graph.rootPosition
                    : graph.positions.get(edge.fromId);
                const toPos = graph.positions.get(edge.toId);
                if (!fromPos || !toPos) return null;

                const x1 = fromPos.x + fromPos.width;
                const y1 = fromPos.cy;
                const x2 = toPos.x - (edge.kind === "goal" ? 0 : 2);
                const y2 = toPos.cy;
                const dx = Math.max(24, (x2 - x1) * 0.5);
                const d = `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`;

                const isHighlighted =
                  highlightedIds !== null &&
                  (edge.fromId === GOAL_ROOT_ID
                    ? highlightedIds.has(edge.toId)
                    : highlightedIds.has(edge.fromId) && highlightedIds.has(edge.toId));
                const isDimmed = highlightedIds !== null && !isHighlighted;

                return (
                  <path
                    key={edge.id}
                    d={d}
                    fill="none"
                    stroke="currentColor"
                    strokeWidth={isHighlighted ? 2.25 : 1.5}
                    strokeDasharray={edge.kind === "conditional" ? "5 4" : undefined}
                    markerEnd={edge.kind === "goal" ? undefined : `url(#${arrowId})`}
                    className={
                      isHighlighted
                        ? "text-brand transition-opacity"
                        : edge.kind === "goal"
                          ? "text-border transition-opacity"
                          : "text-muted-foreground/70 transition-opacity"
                    }
                    opacity={isDimmed ? 0.16 : 0.9}
                  />
                );
              })}
            </svg>

            {/* Goal Root Node */}
            <div
              className="absolute flex flex-col justify-between rounded-xl border-2 border-brand/40 bg-card p-3 shadow-xs"
              style={{
                left: graph.rootPosition.x,
                top: graph.rootPosition.y,
                width: graph.rootPosition.width,
                height: graph.rootPosition.height,
              }}
            >
              <div className="flex items-center justify-between gap-1.5">
                <span className="inline-flex items-center gap-1 text-caption font-medium text-brand">
                  <Target className="size-3.5 shrink-0" />
                  {t(($) => $.map.goal_root)}
                </span>
                <GoalStatusBadge status={goal.status} />
              </div>
              <p
                title={goal.title}
                className="line-clamp-2 text-label font-semibold text-foreground"
              >
                {goal.title}
              </p>
              <div className="flex items-center justify-between text-micro text-muted-foreground">
                <span className="tabular-nums">
                  {t(($) => $.map.node_progress, { completed: completedCount, total: totalCount })}
                </span>
              </div>
            </div>

            {/* Task Nodes */}
            {graph.nodes.map((node) => {
              const pos = graph.positions.get(node.id);
              if (!pos) return null;
              const isSelected = effectiveSelectedId === node.id;
              const isDimmed = highlightedIds !== null && !highlightedIds.has(node.id);
              const title = node.title || t(($) => $.plan.untitled_item);

              return (
                <div
                  key={node.id}
                  data-testid={`mindmap-node-${node.id}`}
                  style={{
                    left: pos.x,
                    top: pos.y,
                    width: pos.width,
                    height: pos.height,
                  }}
                  onMouseEnter={() => setHoveredId(node.id)}
                  onMouseLeave={() => setHoveredId((cur) => (cur === node.id ? null : cur))}
                  className={cn(
                    "absolute flex flex-col justify-between rounded-lg border bg-card px-2.5 py-2 shadow-2xs transition-all",
                    isSelected
                      ? "border-brand ring-2 ring-brand/45"
                      : node.isCurrent
                        ? "border-brand/70 bg-brand/[0.04]"
                        : node.isWaiting
                          ? "border-warning/60"
                          : node.isDone
                            ? "border-success/40"
                            : "border-border hover:border-foreground/30",
                    isDimmed && "opacity-35",
                  )}
                >
                  <div className="flex items-center justify-between gap-1">
                    <div className="flex min-w-0 items-center gap-1.5">
                      {node.issue ? (
                        <StatusIcon status={node.issue.status} className="size-3.5 shrink-0" />
                      ) : (
                        <CircleDashed className="size-3.5 shrink-0 text-muted-foreground" />
                      )}
                      {node.issue ? (
                        <AppLink
                          href={issueHref(node.issue)}
                          className="truncate font-mono text-micro font-medium text-muted-foreground hover:text-foreground hover:underline"
                        >
                          {node.issue.identifier}
                        </AppLink>
                      ) : node.item ? (
                        <span className="truncate text-micro text-muted-foreground">
                          {node.item.binding.type === "new"
                            ? t(($) => $.plan.binding_new)
                            : node.item.binding.type === "existing"
                              ? t(($) => $.plan.binding_existing)
                              : t(($) => $.plan.binding_unknown, { type: node.item.binding.type || "?" })}
                        </span>
                      ) : null}
                      {node.stage !== null ? (
                        <span className="shrink-0 rounded bg-muted px-1 py-0.2 text-micro text-muted-foreground">
                          S{node.stage}
                        </span>
                      ) : null}
                    </div>
                    <div className="flex shrink-0 items-center gap-0.5">
                      {node.isCurrent ? (
                        <span className="rounded bg-brand/15 px-1.5 py-0.5 text-micro font-medium text-brand">
                          {t(($) => $.map.current_badge)}
                        </span>
                      ) : node.isWaiting ? (
                        <span className="rounded bg-warning/15 px-1.5 py-0.5 text-micro font-medium text-warning">
                          {t(($) => $.map.waiting_badge)}
                        </span>
                      ) : null}
                      {node.issue ? renderUnlinkButton(node.issue) : null}
                    </div>
                  </div>

                  <button
                    type="button"
                    title={title}
                    aria-pressed={isSelected}
                    onClick={() => onSelectTask(node.id)}
                    className="line-clamp-2 text-left text-caption font-medium text-foreground focus-visible:outline-none"
                  >
                    {title}
                  </button>

                  <div className="flex items-center justify-between gap-1 text-micro text-muted-foreground">
                    <span className="truncate">
                      {node.isCurrent && progress?.method_step
                        ? progress.method_step
                        : node.kind === "linked"
                          ? t(($) => $.map.unplanned_badge)
                          : node.prerequisiteIds.length > 0
                            ? t(($) => $.plan.dependencies_count, {
                                value: node.prerequisiteIds.length,
                              })
                            : t(($) => $.map.root_branch)}
                    </span>
                    {node.dependentIds.length > 0 ? (
                      <span className="inline-flex shrink-0 items-center gap-0.5 tabular-nums">
                        <GitBranch className="size-2.5" />
                        {node.dependentIds.length}
                      </span>
                    ) : null}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      ) : (
        /* Hierarchical Tree View */
        <div className="max-h-48 overflow-auto p-3" data-testid="goal-tree-view">
          <div className="mb-2 flex items-center gap-2 rounded-md bg-surface/60 px-2.5 py-2">
            <Target className="size-4 shrink-0 text-brand" />
            <span className="min-w-0 flex-1 truncate text-body font-semibold text-foreground">
              {goal.title}
            </span>
            <GoalStatusBadge status={goal.status} />
          </div>

          {graph.treeRoots.length > 0 ? (
            <ul className="ml-3 space-y-0.5 border-l border-border/70 pl-3">
              {graph.treeRoots.map((branch) => renderTreeBranch(branch, 0))}
            </ul>
          ) : null}

          {graph.unplannedLinkedNodes.length > 0 ? (
            <div className={cn(graph.treeRoots.length > 0 && "mt-3 border-t pt-3")}>
              {graph.treeRoots.length > 0 ? (
                <p className="mb-1 px-2 text-caption font-medium text-muted-foreground">
                  {t(($) => $.map.unplanned_section)}
                </p>
              ) : null}
              <ul className="ml-3 space-y-0.5 border-l border-border/70 pl-3">
                {graph.unplannedLinkedNodes.map((node) =>
                  renderTreeBranch(
                    { node, children: [], extraPrerequisiteIds: [] },
                    0,
                  ),
                )}
              </ul>
            </div>
          ) : null}
        </div>
      )}

      {graph.nodes.length > 0 ? (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t px-4 py-2 text-micro text-muted-foreground">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            {view === "mindmap" ? (
              <>
                <span className="inline-flex items-center gap-1.5">
                  <span className="w-4 border-t border-muted-foreground" />
                  {t(($) => $.plan.dependency_hard)}
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <span className="w-4 border-t border-dashed border-muted-foreground" />
                  {t(($) => $.plan.dependency_conditional)}
                </span>
              </>
            ) : null}
            <span>{t(($) => $.map.click_hint)}</span>
          </div>

          <div className="flex items-center gap-2">
            {issuesError ? (
              <Button variant="ghost" size="xs" onClick={onRetryIssues}>
                {t(($) => $.detail.tasks_load_failed)} · {t(($) => $.list.retry)}
              </Button>
            ) : null}
            {hasNextPage ? (
              <Button
                variant="outline"
                size="xs"
                disabled={isFetchingNextPage}
                aria-busy={isFetchingNextPage}
                onClick={onFetchNextPage}
              >
                {isFetchNextPageError ? t(($) => $.list.retry) : t(($) => $.detail.load_more_tasks)}
              </Button>
            ) : null}
          </div>
        </div>
      ) : null}
      {selectedNode ? (
        <div
          className="space-y-3 border-t px-4 py-3"
          data-testid="goal-task-inspector"
        >
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div className="min-w-0 flex-1 space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                {selectedNode.issue ? (
                  <StatusIcon status={selectedNode.issue.status} className="size-4 shrink-0" />
                ) : (
                  <CircleDashed className="size-4 shrink-0 text-muted-foreground" />
                )}
                {selectedNode.issue ? (
                  <AppLink
                    href={issueHref(selectedNode.issue)}
                    className="inline-flex items-center gap-1 font-mono text-caption font-medium text-brand hover:underline"
                  >
                    {selectedNode.issue.identifier}
                    <ArrowUpRight className="size-3" />
                  </AppLink>
                ) : null}
                <h3 className="text-body font-semibold text-foreground">
                  {selectedNode.title || t(($) => $.plan.untitled_item)}
                </h3>
                {selectedNode.stage !== null ? (
                  <Badge variant="outline" className="text-micro">
                    {t(($) => $.map.stage_short, { stage: selectedNode.stage })}
                  </Badge>
                ) : null}
              </div>

              {selectedNode.prerequisiteIds.length > 0 || selectedNode.dependentIds.length > 0 ? (
                <div className="flex flex-wrap items-center gap-x-4 gap-y-1 pt-1 text-caption">
                  {selectedNode.prerequisiteIds.length > 0 ? (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-muted-foreground">{t(($) => $.map.upstream)}</span>
                      {selectedNode.prerequisiteIds.map((preId) => {
                        const pre = graph.nodeById.get(preId);
                        if (!pre) return null;
                        return (
                          <button
                            key={preId}
                            type="button"
                            onClick={() => onSelectTask(preId)}
                            className="rounded border bg-background px-1.5 py-0.5 text-caption hover:border-brand hover:text-brand"
                          >
                            {pre.issue?.identifier ? `${pre.issue.identifier} · ` : ""}
                            {pre.title || t(($) => $.plan.untitled_item)}
                          </button>
                        );
                      })}
                    </div>
                  ) : null}
                  {selectedNode.dependentIds.length > 0 ? (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-muted-foreground">{t(($) => $.map.downstream)}</span>
                      {selectedNode.dependentIds.map((depId) => {
                        const dep = graph.nodeById.get(depId);
                        if (!dep) return null;
                        return (
                          <button
                            key={depId}
                            type="button"
                            onClick={() => onSelectTask(depId)}
                            className="rounded border bg-background px-1.5 py-0.5 text-caption hover:border-brand hover:text-brand"
                          >
                            {dep.issue?.identifier ? `${dep.issue.identifier} · ` : ""}
                            {dep.title || t(($) => $.plan.untitled_item)}
                          </button>
                        );
                      })}
                    </div>
                  ) : null}
                </div>
              ) : null}
            </div>

            <div className="flex shrink-0 items-center gap-1">
              <Button
                type="button"
                size="icon-xs"
                variant="ghost"
                aria-label={t(($) => $.map.close_inspector)}
                onClick={() => onSelectTask(null)}
              >
                <X className="size-3.5" />
              </Button>
            </div>
          </div>
        </div>
      ) : null}

      {children}
    </section>
  );
}
