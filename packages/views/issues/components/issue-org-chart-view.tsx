"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AlertTriangle,
  ChevronDown,
  ChevronRight,
  GitBranch,
  Loader2,
  Minus,
  Plus,
  Unlink,
} from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { errorCode } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { issueStatusCategory } from "@multica/core/issues";
import { formatDateOnly } from "@multica/core/issues/date";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { childIssueProgressOptions } from "@multica/core/issues/queries";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { useModalStore } from "@multica/core/modals";
import { projectListOptions } from "@multica/core/projects/queries";
import { propertyListOptions } from "@multica/core/properties";
import type { Issue, IssueProperty, Project } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { GraphCardFrame, GraphCanvasEdges, type GraphEdgeItem } from "../../common/graph-visuals";
import { PropertyIcon } from "../../common/property-icon";
import { useLocale, useT } from "../../i18n";
import { LabelChip } from "../../labels/label-chip";
import { ProjectIcon } from "../../projects/components/project-icon";
import { IssueActionsContextMenu } from "../actions";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";
import {
  PEEK_TARGET_ATTR,
  useIsIssuePeeked,
  useIssuePeekActions,
  useIssuePeekId,
} from "../surface/peek-context";
import { resolveCardCustomProperties } from "../utils/card-custom-properties";
import {
  buildOrgChartLayout,
  collectOrgChartFocusChain,
  type OrgChartNode,
  type OrgChartOrientation,
} from "../utils/org-chart";
import { ComputedPropertyViewBoundary } from "./computed-property-view-boundary";
import type { ChildProgress } from "./list-row";
import { CustomPropertyValueDisplay } from "./pickers/custom-property-picker";
import { PriorityIcon } from "./priority-icon";
import { ProgressRing } from "./progress-ring";
import { StatusIcon } from "./status-icon";
import type { useIssueTableFlatRowPages } from "./use-issue-table-flat-row-pages";

const MIN_ZOOM = 0.5;
const MAX_ZOOM = 1.5;
const ZOOM_STEP = 0.15;

export interface IssueOrgChartViewProps {
  rowPages: ReturnType<typeof useIssueTableFlatRowPages>;
}

function OrgChartNodeCard({
  node,
  orientation,
  project,
  childProgress,
  workspaceProperties,
  isDimmed,
  isFocused,
  onToggleCollapse,
  onChangeParent,
  onRemoveParent,
  onFocusNode,
  onBlurNode,
  onHoverNode,
  onUnhoverNode,
}: {
  node: OrgChartNode;
  orientation: OrgChartOrientation;
  project?: Project;
  childProgress?: ChildProgress;
  workspaceProperties: IssueProperty[];
  isDimmed: boolean;
  isFocused: boolean;
  onToggleCollapse: (issueId: string) => void;
  onChangeParent: (node: OrgChartNode) => void;
  onRemoveParent: (issue: Issue) => void;
  onFocusNode: (issueId: string) => void;
  onBlurNode: (issueId: string) => void;
  onHoverNode: (issueId: string) => void;
  onUnhoverNode: (issueId: string) => void;
}) {
  const { issue } = node;
  const wsId = useWorkspaceId();
  const locale = useLocale();
  const { t } = useT("issues");
  const { colorOf, iconOf } = useIssueStatuses(wsId);
  const peekActions = useIssuePeekActions();
  const peeked = useIsIssuePeeked(issue.id);
  const cardProperties = useViewStore((s) => s.cardProperties);
  const cardPropertyIds = useViewStore((s) => s.cardPropertyIds);

  const cardCustomProperties = resolveCardCustomProperties(
    cardPropertyIds,
    workspaceProperties,
    issue,
  );

  const rootBadge =
    node.rootReason === "top_level"
      ? {
          label: t(($) => $.org_chart.root_badge),
          className: "bg-brand/10 text-brand",
        }
      : node.rootReason === "missing_parent"
        ? {
            label: t(($) => $.org_chart.hidden_parent_badge),
            className: "bg-warning/15 text-warning-foreground",
          }
        : node.rootReason === "cycle"
          ? {
              label: t(($) => $.org_chart.cycle_badge),
              className: "bg-destructive/15 text-destructive",
            }
          : null;

  const hasVisibleMetadata =
    (cardProperties.priority && issue.priority !== "none") ||
    (cardProperties.assignee && !!issue.assignee_type && !!issue.assignee_id) ||
    (cardProperties.startDate && !!issue.start_date) ||
    (cardProperties.dueDate && !!issue.due_date) ||
    (cardProperties.project && !!project) ||
    (cardProperties.childProgress && !!childProgress) ||
    (cardProperties.labels && (issue.labels?.length ?? 0) > 0) ||
    cardCustomProperties.length > 0;

  const statusCategory = issueStatusCategory(issue);

  return (
    <div
      className="absolute"
      style={{
        left: node.x,
        top: node.y,
        width: node.width,
        height: node.height,
      }}
    >
      <IssueActionsContextMenu issue={issue}>
        <GraphCardFrame
          {...{ [PEEK_TARGET_ATTR]: issue.id }}
          data-issue-id={issue.id}
          data-org-root={node.rootReason ?? undefined}
          isSelected={peeked || isFocused}
          isDimmed={isDimmed}
          tone={statusCategory === "done" ? "done" : undefined}
          onMouseEnter={() => onHoverNode(issue.id)}
          onMouseLeave={() => onUnhoverNode(issue.id)}
          className={cn(
            "group h-full",
            node.rootReason === "top_level" && "border-t-2 border-t-brand/70",
            node.rootReason === "missing_parent" && "border-dashed border-warning/70",
            node.rootReason === "cycle" && "border-destructive/60",
          )}
        >
          {/* Top row: status, identifier, root badge, parent actions */}
          <div className="flex items-center gap-1.5 min-w-0">
            <StatusIcon
              status={issue.status}
              color={colorOf(issue.status)}
              icon={iconOf(issue.status)}
              category={statusCategory ?? undefined}
              className="size-3.5 shrink-0"
            />
            <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
              {issue.identifier}
            </span>
            {rootBadge && (
              <span
                className={cn(
                  "inline-flex shrink-0 items-center rounded px-1.5 py-0.5 text-micro font-medium leading-none",
                  rootBadge.className,
                )}
              >
                {rootBadge.label}
              </span>
            )}
            <div className="relative z-10 ml-auto flex shrink-0 items-center gap-0.5">
              <button
                type="button"
                aria-label={t(($) => $.org_chart.change_parent)}
                title={t(($) => $.org_chart.change_parent)}
                onClick={(event) => {
                  event.stopPropagation();
                  onChangeParent(node);
                }}
                className="inline-flex size-5 items-center justify-center rounded text-muted-foreground opacity-70 transition-colors hover:bg-accent hover:text-foreground group-hover:opacity-100"
              >
                <GitBranch className="size-3" aria-hidden="true" />
              </button>
              {issue.parent_issue_id && (
                <button
                  type="button"
                  aria-label={t(($) => $.org_chart.remove_parent)}
                  title={t(($) => $.org_chart.remove_parent)}
                  onClick={(event) => {
                    event.stopPropagation();
                    onRemoveParent(issue);
                  }}
                  className="inline-flex size-5 items-center justify-center rounded text-muted-foreground opacity-70 transition-colors hover:bg-accent hover:text-foreground group-hover:opacity-100"
                >
                  <Unlink className="size-3" aria-hidden="true" />
                </button>
              )}
            </div>
          </div>

          {/* Title button — opens original task, supports up to two lines like GoalTaskMap */}
          <button
            type="button"
            aria-pressed={peeked}
            onClick={() => peekActions?.open(issue.id)}
            onFocus={() => onFocusNode(issue.id)}
            onBlur={() => onBlurNode(issue.id)}
            className="mt-0.5 block w-full text-left text-caption font-medium text-foreground outline-none after:absolute after:inset-0 after:rounded-lg hover:underline focus-visible:after:ring-2 focus-visible:after:ring-ring"
            title={issue.title}
          >
            <span className="line-clamp-2">{issue.title}</span>
          </button>

          {/* Footer metadata row */}
          <div className="pointer-events-none relative z-10 mt-0.5 flex min-h-4 items-center gap-1.5 overflow-hidden text-micro text-muted-foreground [&>*]:pointer-events-auto">
            {hasVisibleMetadata ? (
              <>
                {cardProperties.priority && issue.priority !== "none" && (
                  <PriorityIcon priority={issue.priority} />
                )}
                {cardProperties.project && project && (
                  <span className="inline-flex min-w-0 items-center gap-1 truncate">
                    <ProjectIcon project={project} size="sm" />
                    <span className="truncate text-micro">{project.title}</span>
                  </span>
                )}
                {cardProperties.startDate && issue.start_date && (
                  <span className="shrink-0 tabular-nums text-micro">
                    {formatDateOnly(issue.start_date, { month: "short", day: "numeric" }, locale)}
                  </span>
                )}
                {cardProperties.dueDate && issue.due_date && (
                  <span className="shrink-0 tabular-nums text-micro">
                    {formatDateOnly(issue.due_date, { month: "short", day: "numeric" }, locale)}
                  </span>
                )}
                {cardProperties.childProgress && childProgress && (
                  <span className="inline-flex shrink-0 items-center gap-1">
                    <ProgressRing done={childProgress.done} total={childProgress.total} size={12} />
                    <span className="tabular-nums text-micro">
                      {childProgress.done}/{childProgress.total}
                    </span>
                  </span>
                )}
                {cardProperties.labels && issue.labels && issue.labels.length > 0 && (
                  <span className="inline-flex min-w-0 items-center gap-1 overflow-hidden">
                    {issue.labels.slice(0, 2).map((label) => (
                      <LabelChip key={label.id} label={label} />
                    ))}
                  </span>
                )}
                {cardCustomProperties.slice(0, 2).map((property) => (
                  <span key={property.id} className="inline-flex min-w-0 items-center gap-1 truncate text-micro">
                    <PropertyIcon property={property} className="size-3 shrink-0" />
                    <CustomPropertyValueDisplay
                      property={property}
                      value={issue.properties?.[property.id]}
                      issueId={issue.id}
                    />
                  </span>
                ))}
                {cardProperties.assignee && issue.assignee_type && issue.assignee_id && (
                  <span className="ml-auto shrink-0">
                    <ActorAvatar
                      actorType={issue.assignee_type}
                      actorId={issue.assignee_id}
                      size="sm"
                      enableHoverCard
                    />
                  </span>
                )}
              </>
            ) : (
              <span className="text-micro text-faint-foreground">
                {node.directChildCount > 0 ? `${node.directChildCount}` : ""}
              </span>
            )}
          </div>
        </GraphCardFrame>
      </IssueActionsContextMenu>

      {/* Expand / collapse subtree trigger */}
      {node.directChildCount > 0 && (
        <button
          type="button"
          aria-expanded={!node.collapsed}
          aria-label={
            node.collapsed
              ? t(($) => $.org_chart.expand_node)
              : t(($) => $.org_chart.collapse_node)
          }
          onClick={(event) => {
            event.stopPropagation();
            onToggleCollapse(issue.id);
          }}
          className={cn(
            // eslint-disable-next-line no-restricted-syntax -- subtree child count indicator pill on card edge
            "absolute z-10 inline-flex h-5 items-center gap-0.5 rounded-full border border-border bg-background px-1.5 text-micro font-medium tabular-nums text-muted-foreground shadow-2xs transition-colors hover:border-foreground/30 hover:text-foreground",
            orientation === "vertical"
              ? "bottom-0 left-1/2 -translate-x-1/2 translate-y-1/2"
              : "right-0 top-1/2 translate-x-1/2 -translate-y-1/2",
          )}
        >
          {node.collapsed ? (
            <>
              <ChevronRight className="size-3" aria-hidden="true" />
              <span>+{node.descendantCount}</span>
            </>
          ) : (
            <>
              <ChevronDown className="size-3" aria-hidden="true" />
              <span>{node.directChildCount}</span>
            </>
          )}
        </button>
      )}
    </div>
  );
}

export function IssueOrgChartView({ rowPages }: IssueOrgChartViewProps) {
  const wsId = useWorkspaceId();
  const { t } = useT("issues");
  const cardProperties = useViewStore((s) => s.cardProperties);
  const cardPropertyIds = useViewStore((s) => s.cardPropertyIds);
  const surfaceActions = useIssueSurfaceActionsOptional();
  const updateIssueMutation = useUpdateIssue();

  const [orientation, setOrientation] = useState<OrgChartOrientation>("horizontal");
  const [collapsedIds, setCollapsedIds] = useState<ReadonlySet<string>>(() => new Set());
  const [zoom, setZoom] = useState(1);
  const [hoveredIssueId, setHoveredIssueId] = useState<string | null>(null);
  const [focusedIssueId, setFocusedIssueId] = useState<string | null>(null);
  const peekedIssueId = useIssuePeekId();

  const {
    issues,
    total,
    isHeadLoading,
    isHeadError,
    isTailLoading,
    isTailError,
    canLoadMore,
    handleLoadMore,
    retryHead,
    retryTail,
  } = rowPages;

  const layout = useMemo(
    () => buildOrgChartLayout(issues, { collapsedIds, orientation }),
    [collapsedIds, issues, orientation],
  );

  const visibleIssueIds = useMemo(
    () => layout.nodes.map((node) => node.issue.id),
    [layout.nodes],
  );

  const visibleIssueIdSet = useMemo(
    () => new Set(visibleIssueIds),
    [visibleIssueIds],
  );

  // Active focus is hover, then keyboard focus, then peeked issue (if visible in layout).
  const activeFocusId = useMemo(() => {
    if (hoveredIssueId && visibleIssueIdSet.has(hoveredIssueId)) return hoveredIssueId;
    if (focusedIssueId && visibleIssueIdSet.has(focusedIssueId)) return focusedIssueId;
    if (peekedIssueId && visibleIssueIdSet.has(peekedIssueId)) return peekedIssueId;
    return null;
  }, [hoveredIssueId, focusedIssueId, peekedIssueId, visibleIssueIdSet]);

  const highlightedChainIds = useMemo(() => {
    if (!activeFocusId) return null;
    return collectOrgChartFocusChain(activeFocusId, layout.nodes);
  }, [activeFocusId, layout.nodes]);

  const handleFocusNode = useCallback((id: string) => {
    setFocusedIssueId(id);
  }, []);

  const handleBlurNode = useCallback((id: string) => {
    setFocusedIssueId((cur) => (cur === id ? null : cur));
  }, []);

  // Side peek steps through visible nodes in layout order.
  const peek = useIssuePeekActions();
  useEffect(() => {
    peek?.publishColumns([visibleIssueIds]);
  }, [peek, visibleIssueIds]);
  useEffect(() => () => peek?.publishColumns(null), [peek]);

  const propertiesQuery = useQuery(propertyListOptions(wsId));
  const workspaceProperties = propertiesQuery.data ?? [];
  const projectsQuery = useQuery({
    ...projectListOptions(wsId),
    enabled: cardProperties.project,
  });
  const childProgressQuery = useQuery({
    ...childIssueProgressOptions(wsId),
    enabled: cardProperties.childProgress,
  });
  const projectMap = useMemo(
    () => new Map((projectsQuery.data ?? []).map((project) => [project.id, project])),
    [projectsQuery.data],
  );
  const childProgressMap = childProgressQuery.data ?? new Map<string, ChildProgress>();

  const toggleCollapse = useCallback((issueId: string) => {
    setCollapsedIds((prev) => {
      const next = new Set(prev);
      if (next.has(issueId)) next.delete(issueId);
      else next.add(issueId);
      return next;
    });
  }, []);

  const expandAll = useCallback(() => {
    setCollapsedIds(new Set());
  }, []);

  const collapseAll = useCallback(() => {
    setCollapsedIds(new Set(layout.collapsibleIds));
  }, [layout.collapsibleIds]);

  const handleChangeParent = useCallback((node: OrgChartNode) => {
    useModalStore.getState().open("issue-set-parent", {
      issueId: node.issue.id,
      ...(node.issue.revision !== undefined
        ? { expectedRevision: node.issue.revision }
        : {}),
      excludeIds: node.descendantIds,
    });
  }, []);

  const handleRemoveParent = useCallback(
    (issue: Issue) => {
      const updates = {
        parent_issue_id: null,
        stage: null,
        ...(issue.revision !== undefined
          ? { expected_revision: issue.revision }
          : {}),
      };
      if (surfaceActions) {
        surfaceActions.updateIssue(issue.id, updates, {
          onSuccess: () =>
            toast.success(t(($) => $.actions.remove_parent_issue_success)),
          errorMessage: t(($) => $.detail.update_failed),
        });
      } else {
        updateIssueMutation.mutate(
          { id: issue.id, ...updates },
          {
            onSuccess: () =>
              toast.success(t(($) => $.actions.remove_parent_issue_success)),
            onError: (err) =>
              toast.error(
                errorCode(err) === "revision_conflict"
                  ? t(($) => $.revision.conflict)
                  : err instanceof Error && err.message
                    ? err.message
                    : t(($) => $.detail.update_failed),
              ),
          },
        );
      }
    },
    [surfaceActions, t, updateIssueMutation],
  );

  const isPartial = !isHeadLoading && !isHeadError && (isTailLoading || isTailError || canLoadMore);
  const isSettledEmpty = !isHeadLoading && !isHeadError && issues.length === 0 && !isPartial;

  const statusSlot = (() => {
    if (isTailError) {
      return (
        <Button
          size="xs"
          variant="ghost"
          className="text-destructive"
          onClick={retryTail}
        >
          {t(($) => $.table.load_more_failed_retry)}
        </Button>
      );
    }
    if (isTailLoading) {
      return (
        <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          <span>{t(($) => $.org_chart.loading_more)}</span>
          {total > 0 && (
            <span className="tabular-nums">
              {t(($) => $.org_chart.loaded_progress, {
                loaded: issues.length,
                total,
              })}
            </span>
          )}
        </span>
      );
    }
    if (canLoadMore) {
      return (
        <span className="inline-flex items-center gap-2 text-caption text-muted-foreground">
          {total > 0 && (
            <span className="tabular-nums">
              {t(($) => $.org_chart.loaded_progress, {
                loaded: issues.length,
                total,
              })}
            </span>
          )}
          <Button size="xs" variant="outline" onClick={handleLoadMore}>
            {t(($) => $.table.load_more)}
          </Button>
        </span>
      );
    }
    if (issues.length > 0 && total > 0) {
      return (
        <span className="text-caption tabular-nums text-muted-foreground">
          {t(($) => $.org_chart.loaded_progress, {
            loaded: issues.length,
            total: Math.max(total, issues.length),
          })}
        </span>
      );
    }
    return null;
  })();

  if (isHeadLoading) {
    return (
      <div
        className="flex flex-1 min-h-0 flex-col overflow-hidden"
        data-view-kind="org_chart"
        aria-busy="true"
      >
        <div className="flex flex-wrap gap-6 p-8">
          {Array.from({ length: 4 }).map((_, index) => (
            <Skeleton key={index} className="h-28 w-64 rounded-lg" />
          ))}
        </div>
      </div>
    );
  }

  if (isHeadError) {
    return (
      <div
        role="alert"
        data-view-kind="org_chart"
        className="flex items-center gap-2 p-4 text-body text-destructive"
      >
        <span>{t(($) => $.table.load_failed)}</span>
        <Button size="sm" variant="outline" onClick={retryHead}>
          {t(($) => $.table.load_failed_retry)}
        </Button>
      </div>
    );
  }

  const content = (
    <div
      className="flex flex-1 min-h-0 flex-col overflow-hidden"
      data-view-kind="org_chart"
      aria-busy={isTailLoading ? true : undefined}
    >
      {/* Toolbar */}
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-3 py-1.5">
        <div className="inline-flex items-center rounded-md border border-foreground/10 p-0.5">
          {(
            [
              { value: "vertical", label: t(($) => $.org_chart.orientation_vertical) },
              { value: "horizontal", label: t(($) => $.org_chart.orientation_horizontal) },
            ] as const
          ).map((opt) => (
            <Button
              key={opt.value}
              size="xs"
              variant={orientation === opt.value ? "secondary" : "ghost"}
              aria-pressed={orientation === opt.value}
              className={cn(
                "text-caption",
                orientation !== opt.value && "text-muted-foreground",
              )}
              onClick={() => setOrientation(opt.value)}
            >
              {opt.label}
            </Button>
          ))}
        </div>

        {layout.collapsibleIds.length > 0 && (
          <div className="inline-flex items-center gap-1">
            <Button size="xs" variant="outline" onClick={expandAll}>
              {t(($) => $.org_chart.expand_all)}
            </Button>
            <Button size="xs" variant="outline" onClick={collapseAll}>
              {t(($) => $.org_chart.collapse_all)}
            </Button>
          </div>
        )}

        <div className="inline-flex items-center gap-0.5 rounded-md border border-foreground/10 p-0.5">
          <Button
            size="icon-xs"
            variant="ghost"
            aria-label={t(($) => $.org_chart.zoom_out)}
            disabled={zoom <= MIN_ZOOM}
            onClick={() => setZoom((z) => Math.max(MIN_ZOOM, Number((z - ZOOM_STEP).toFixed(2))))}
          >
            <Minus className="size-3" aria-hidden="true" />
          </Button>
          <Button
            size="xs"
            variant="ghost"
            aria-label={t(($) => $.org_chart.zoom_reset)}
            className="min-w-11 px-1.5 text-caption tabular-nums"
            onClick={() => setZoom(1)}
          >
            {Math.round(zoom * 100)}%
          </Button>
          <Button
            size="icon-xs"
            variant="ghost"
            aria-label={t(($) => $.org_chart.zoom_in)}
            disabled={zoom >= MAX_ZOOM}
            onClick={() => setZoom((z) => Math.min(MAX_ZOOM, Number((z + ZOOM_STEP).toFixed(2))))}
          >
            <Plus className="size-3" aria-hidden="true" />
          </Button>
        </div>

        <div className="flex-1" />
        {statusSlot}
      </div>

      {/* Cycle notice */}
      {layout.cycleIssueIds.length > 0 && (
        <div
          role="status"
          className="flex shrink-0 items-center gap-2 border-b border-destructive/20 bg-destructive/5 px-3 py-1.5 text-caption text-destructive"
        >
          <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
          <span>{t(($) => $.org_chart.cycle_warning)}</span>
        </div>
      )}

      {/* Canvas or empty state */}
      {isSettledEmpty ? (
        <div
          data-view-empty="org_chart"
          className="flex flex-1 min-h-0 items-center justify-center p-6 text-body text-muted-foreground"
        >
          {t(($) => $.org_chart.empty)}
        </div>
      ) : (
        <div
          data-board-scroller=""
          className="flex-1 min-h-0 overflow-auto bg-muted/15 group-data-[peek-open]/peek:pr-[var(--issue-peek-width)]"
        >
          <div
            className="relative"
            style={{
              width: Math.max(layout.width * zoom, 1),
              height: Math.max(layout.height * zoom, 1),
            }}
          >
            <div
              className="relative origin-top-left"
              style={{
                width: layout.width,
                height: layout.height,
                transform: zoom === 1 ? undefined : `scale(${zoom})`,
              }}
            >
              {/* Parent-child connector lines */}
              <svg
                aria-hidden="true"
                width={layout.width}
                height={layout.height}
                className="pointer-events-none absolute inset-0"
              >
                <GraphCanvasEdges
                  edges={layout.edges.map((edge) => {
                    const isHighlighted =
                      highlightedChainIds !== null &&
                      highlightedChainIds.has(edge.parentId) &&
                      highlightedChainIds.has(edge.childId);
                    const isDimmed = highlightedChainIds !== null && !isHighlighted;
                    const item: GraphEdgeItem = {
                      id: edge.id,
                      "data-org-edge": edge.id,
                      path: edge.path,
                      isHighlighted,
                      isDimmed,
                    };
                    return item;
                  })}
                />
              </svg>

              {/* Issue node cards */}
              {layout.nodes.map((node) => {
                const isDimmed =
                  highlightedChainIds !== null && !highlightedChainIds.has(node.issue.id);

                return (
                  <OrgChartNodeCard
                    key={node.issue.id}
                    node={node}
                    orientation={orientation}
                    project={
                      node.issue.project_id
                        ? projectMap.get(node.issue.project_id)
                        : undefined
                    }
                    childProgress={childProgressMap.get(node.issue.id)}
                    workspaceProperties={workspaceProperties}
                    isDimmed={isDimmed}
                    isFocused={activeFocusId === node.issue.id}
                    onToggleCollapse={toggleCollapse}
                    onChangeParent={handleChangeParent}
                    onRemoveParent={handleRemoveParent}
                    onFocusNode={handleFocusNode}
                    onBlurNode={handleBlurNode}
                    onHoverNode={setHoveredIssueId}
                    onUnhoverNode={(id) => setHoveredIssueId((cur) => (cur === id ? null : cur))}
                  />
                );
              })}
            </div>
          </div>
        </div>
      )}
    </div>
  );

  return (
    <ComputedPropertyViewBoundary
      wsId={wsId}
      issueIds={visibleIssueIds}
      cardPropertyIds={cardPropertyIds}
      workspaceProperties={workspaceProperties}
    >
      {content}
    </ComputedPropertyViewBoundary>
  );
}
