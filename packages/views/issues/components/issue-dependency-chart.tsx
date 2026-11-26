"use client";

import { useEffect, useId, useMemo } from "react";
import { AlertTriangle } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import type { Issue } from "@multica/core/types";
import { useT } from "../../i18n";
import {
  PEEK_TARGET_ATTR,
  useIssuePeekActions,
  useIssuePeekId,
} from "../surface/peek-context";

export type IssueDependencyEdgeKind = "required" | "conditional";

export interface IssueDependencyEdge {
  id?: string;
  /** Prerequisite issue ID (arrow tail). */
  fromId: string;
  /** Dependent issue ID (arrow head). */
  toId: string;
  kind?: IssueDependencyEdgeKind;
  reason?: string;
}

export interface NormalizedDependencyEdge {
  id: string;
  fromId: string;
  toId: string;
  kind: IssueDependencyEdgeKind;
  reason: string;
  isBackEdge: boolean;
}

export interface DependencyNodePosition {
  issue: Issue;
  layer: number;
  rowIndex: number;
  x: number;
  y: number;
  width: number;
  height: number;
  cx: number;
  cy: number;
  inCycle: boolean;
}

export interface DependencyChartLayout {
  nodes: DependencyNodePosition[];
  nodeById: Map<string, DependencyNodePosition>;
  edges: NormalizedDependencyEdge[];
  unrelatedRows: Issue[];
  hasCycle: boolean;
  canvasWidth: number;
  canvasHeight: number;
}

export interface IssueDependencyChartProps {
  rows: readonly Issue[];
  edges: readonly IssueDependencyEdge[];
  emptyMessage?: string;
  relationLabel?: string;
  className?: string;
}

const NODE_WIDTH = 236;
const NODE_HEIGHT = 76;
const COL_GAP = 64;
const ROW_GAP = 16;
const PAD_X = 24;
const PAD_Y = 24;

/**
 * Pure layered DAG layout for visible issues and directed prerequisite -> dependent
 * edges. Keeps multi-prerequisite and multi-dependent links intact, handles cycles
 * deterministically without throwing, and separates unrelated rows without
 * fabricating edges.
 */
export function buildDependencyChartLayout(
  rows: readonly Issue[],
  edges: readonly IssueDependencyEdge[],
): DependencyChartLayout {
  const uniqueRows: Issue[] = [];
  const rowOrder = new Map<string, number>();
  const issueById = new Map<string, Issue>();

  for (const row of rows) {
    if (issueById.has(row.id)) continue;
    issueById.set(row.id, row);
    rowOrder.set(row.id, uniqueRows.length);
    uniqueRows.push(row);
  }

  const dedupedEdges = new Map<
    string,
    { id: string; fromId: string; toId: string; kind: IssueDependencyEdgeKind; reason: string }
  >();

  for (const edge of edges) {
    if (!issueById.has(edge.fromId) || !issueById.has(edge.toId)) continue;
    if (edge.fromId === edge.toId) continue;
    const key = `${edge.fromId}\u0000${edge.toId}`;
    const kind: IssueDependencyEdgeKind =
      edge.kind === "conditional" ? "conditional" : "required";
    const reason = (edge.reason ?? "").trim();
    const existing = dedupedEdges.get(key);
    if (!existing) {
      dedupedEdges.set(key, {
        id: edge.id ?? `${edge.fromId}->${edge.toId}`,
        fromId: edge.fromId,
        toId: edge.toId,
        kind,
        reason,
      });
    } else {
      if (existing.kind === "conditional" && kind === "required") {
        existing.kind = "required";
      }
      if (!existing.reason && reason) {
        existing.reason = reason;
      }
    }
  }

  const validEdges = [...dedupedEdges.values()];
  const outgoing = new Map<string, string[]>();
  const incoming = new Map<string, string[]>();
  const connectedIds = new Set<string>();

  for (const edge of validEdges) {
    connectedIds.add(edge.fromId);
    connectedIds.add(edge.toId);
    const out = outgoing.get(edge.fromId) ?? [];
    out.push(edge.toId);
    outgoing.set(edge.fromId, out);
    const inc = incoming.get(edge.toId) ?? [];
    inc.push(edge.fromId);
    incoming.set(edge.toId, inc);
  }

  const connectedRows = uniqueRows.filter((row) => connectedIds.has(row.id));
  const unrelatedRows = uniqueRows.filter((row) => !connectedIds.has(row.id));

  if (connectedRows.length === 0) {
    return {
      nodes: [],
      nodeById: new Map(),
      edges: [],
      unrelatedRows,
      hasCycle: false,
      canvasWidth: 0,
      canvasHeight: 0,
    };
  }

  // Topological layering via Kahn's algorithm with deterministic cycle breaking.
  const inDegree = new Map<string, number>();
  const layer = new Map<string, number>();
  for (const row of connectedRows) {
    inDegree.set(row.id, incoming.get(row.id)?.length ?? 0);
    layer.set(row.id, 0);
  }

  const visited = new Set<string>();
  const queue: string[] = [];
  for (const row of connectedRows) {
    if ((inDegree.get(row.id) ?? 0) === 0) {
      queue.push(row.id);
    }
  }

  const drainQueue = () => {
    while (queue.length > 0) {
      const current = queue.shift()!;
      if (visited.has(current)) continue;
      visited.add(current);
      const currentLayer = layer.get(current) ?? 0;
      for (const next of outgoing.get(current) ?? []) {
        if (!visited.has(next)) {
          layer.set(next, Math.max(layer.get(next) ?? 0, currentLayer + 1));
        }
        const nextDeg = (inDegree.get(next) ?? 0) - 1;
        inDegree.set(next, nextDeg);
        if (nextDeg === 0 && !visited.has(next)) {
          queue.push(next);
        }
      }
    }
  };

  drainQueue();

  let hasCycle = false;
  const cycleNodeIds = new Set<string>();

  if (visited.size < connectedRows.length) {
    hasCycle = true;
    for (const row of connectedRows) {
      if (!visited.has(row.id)) {
        cycleNodeIds.add(row.id);
      }
    }

    while (visited.size < connectedRows.length) {
      let bestId: string | null = null;
      let bestDegree = Infinity;
      for (const row of connectedRows) {
        if (visited.has(row.id)) continue;
        const deg = inDegree.get(row.id) ?? 0;
        if (deg < bestDegree) {
          bestDegree = deg;
          bestId = row.id;
        }
      }
      if (!bestId) break;
      const visitedPredLayers = (incoming.get(bestId) ?? [])
        .filter((pred) => visited.has(pred))
        .map((pred) => layer.get(pred) ?? 0);
      if (visitedPredLayers.length > 0) {
        layer.set(bestId, Math.max(layer.get(bestId) ?? 0, Math.max(...visitedPredLayers) + 1));
      }
      queue.push(bestId);
      drainQueue();
    }
  }

  // Group into columns by layer, ordering each column by predecessor barycenter.
  const maxLayer = Math.max(0, ...connectedRows.map((row) => layer.get(row.id) ?? 0));
  const columns: Issue[][] = Array.from({ length: maxLayer + 1 }, () => []);
  for (const row of connectedRows) {
    columns[layer.get(row.id) ?? 0]!.push(row);
  }

  const rowIndexInLayer = new Map<string, number>();
  for (let colIdx = 0; colIdx < columns.length; colIdx += 1) {
    const col = columns[colIdx]!;
    if (colIdx > 0) {
      col.sort((a, b) => {
        const aPreds = (incoming.get(a.id) ?? []).filter((id) => rowIndexInLayer.has(id));
        const bPreds = (incoming.get(b.id) ?? []).filter((id) => rowIndexInLayer.has(id));
        const aAvg =
          aPreds.length > 0
            ? aPreds.reduce((sum, id) => sum + (rowIndexInLayer.get(id) ?? 0), 0) / aPreds.length
            : Number.POSITIVE_INFINITY;
        const bAvg =
          bPreds.length > 0
            ? bPreds.reduce((sum, id) => sum + (rowIndexInLayer.get(id) ?? 0), 0) / bPreds.length
            : Number.POSITIVE_INFINITY;
        if (aAvg !== bAvg) return aAvg - bAvg;
        return (rowOrder.get(a.id) ?? 0) - (rowOrder.get(b.id) ?? 0);
      });
    }
    col.forEach((issue, idx) => {
      rowIndexInLayer.set(issue.id, idx);
    });
  }

  const maxColHeight = Math.max(
    1,
    ...columns.map((col) =>
      col.length > 0 ? col.length * NODE_HEIGHT + (col.length - 1) * ROW_GAP : 0,
    ),
  );
  const backEdgeMargin = hasCycle ? 28 : 0;
  const nodes: DependencyNodePosition[] = [];
  const nodeById = new Map<string, DependencyNodePosition>();

  columns.forEach((col, colIdx) => {
    const x = PAD_X + colIdx * (NODE_WIDTH + COL_GAP);
    col.forEach((issue, rowIdx) => {
      const y = PAD_Y + backEdgeMargin + rowIdx * (NODE_HEIGHT + ROW_GAP);
      const pos: DependencyNodePosition = {
        issue,
        layer: colIdx,
        rowIndex: rowIdx,
        x,
        y,
        width: NODE_WIDTH,
        height: NODE_HEIGHT,
        cx: x + NODE_WIDTH / 2,
        cy: y + NODE_HEIGHT / 2,
        inCycle: cycleNodeIds.has(issue.id),
      };
      nodes.push(pos);
      nodeById.set(issue.id, pos);
    });
  });

  const normalizedEdges: NormalizedDependencyEdge[] = validEdges.map((edge) => {
    const fromLayer = layer.get(edge.fromId) ?? 0;
    const toLayer = layer.get(edge.toId) ?? 0;
    return {
      ...edge,
      isBackEdge: fromLayer >= toLayer,
    };
  });

  const canvasWidth =
    PAD_X * 2 + columns.length * NODE_WIDTH + Math.max(0, columns.length - 1) * COL_GAP;
  const canvasHeight = PAD_Y * 2 + backEdgeMargin + maxColHeight;

  return {
    nodes,
    nodeById,
    edges: normalizedEdges,
    unrelatedRows,
    hasCycle,
    canvasWidth,
    canvasHeight,
  };
}

function buildEdgePath(
  from: DependencyNodePosition,
  to: DependencyNodePosition,
  isBackEdge: boolean,
): string {
  const startX = from.x + from.width;
  const startY = from.cy;
  const endX = to.x;
  const endY = to.cy;

  if (!isBackEdge && endX > startX) {
    const dx = endX - startX;
    const span = to.layer - from.layer;
    // Slight arc when jumping across intermediate columns at the same vertical level.
    const midLift = span > 1 && Math.abs(startY - endY) < 4 ? -24 : 0;
    const c1x = startX + dx * 0.45;
    const c2x = endX - dx * 0.45;
    return `M ${startX} ${startY} C ${c1x} ${startY + midLift}, ${c2x} ${endY + midLift}, ${endX} ${endY}`;
  }

  // Back-edge or same-column cycle edge: route above the cards to preserve left->right arrowhead.
  const loopY = Math.min(from.y, to.y) - 20;
  return `M ${startX} ${startY} C ${startX + 28} ${startY}, ${startX + 28} ${loopY}, ${(startX + endX) / 2} ${loopY} S ${endX - 28} ${endY}, ${endX} ${endY}`;
}

export function IssueDependencyChart({
  rows,
  edges,
  emptyMessage,
  relationLabel,
  className,
}: IssueDependencyChartProps) {
  const { t: issueText } = useT("issues");
  const { t: goalText } = useT("goals");
  const kindLabel = (kind: IssueDependencyEdgeKind) => relationLabel ??
    (kind === "conditional"
      ? goalText(($) => $.plan.dependency_conditional)
      : goalText(($) => $.plan.dependency_hard));
  const noLinks = relationLabel
    ? issueText(($) => $.org_chart.relation_no_edges)
    : goalText(($) => $.plan.no_dependencies);
  const arrowId = useId().replace(/:/g, "");
  const peekActions = useIssuePeekActions();
  const peekId = useIssuePeekId();

  const layout = useMemo(() => buildDependencyChartLayout(rows, edges), [rows, edges]);

  const activeIssueId = peekId && layout.nodeById.has(peekId) ? peekId : null;
  const activeNode = activeIssueId ? layout.nodeById.get(activeIssueId) ?? null : null;

  // Publish visible issue ids to peek navigation in layout order (connected nodes first, then unrelated)
  const visibleOrderIds = useMemo(() => {
    const ids: string[] = [];
    for (const node of layout.nodes) ids.push(node.issue.id);
    for (const row of layout.unrelatedRows) ids.push(row.id);
    return ids;
  }, [layout.nodes, layout.unrelatedRows]);

  useEffect(() => {
    peekActions?.publishColumns([visibleOrderIds]);
  }, [peekActions, visibleOrderIds]);

  useEffect(() => () => peekActions?.publishColumns(null), [peekActions]);

  const { upstreamEdges, downstreamEdges, highlightedIssueIds } = useMemo(() => {
    if (!activeIssueId) {
      return {
        upstreamEdges: [] as NormalizedDependencyEdge[],
        downstreamEdges: [] as NormalizedDependencyEdge[],
        highlightedIssueIds: new Set<string>(),
      };
    }
    const upstream = layout.edges.filter((edge) => edge.toId === activeIssueId);
    const downstream = layout.edges.filter((edge) => edge.fromId === activeIssueId);
    const ids = new Set<string>([activeIssueId]);
    for (const edge of upstream) ids.add(edge.fromId);
    for (const edge of downstream) ids.add(edge.toId);
    return {
      upstreamEdges: upstream,
      downstreamEdges: downstream,
      highlightedIssueIds: ids,
    };
  }, [activeIssueId, layout.edges]);

  if (layout.nodes.length === 0 && layout.unrelatedRows.length === 0) {
    return (
      <div className={cn("flex flex-1 items-center justify-center p-6 text-body text-muted-foreground", className)}>
        {emptyMessage ?? issueText(($) => $.org_chart.empty)}
      </div>
    );
  }

  return (
    <div className={cn("flex min-h-0 flex-1 flex-col overflow-hidden", className)}>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2 text-caption text-muted-foreground">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <span className="inline-flex items-center gap-1.5">
            <span className="w-5 border-t border-foreground/70" aria-hidden="true" />
            {kindLabel("required")}
          </span>
          {!relationLabel && (
            <span className="inline-flex items-center gap-1.5">
              <span className="w-5 border-t border-dashed border-foreground/70" aria-hidden="true" />
              {kindLabel("conditional")}
            </span>
          )}
          {layout.hasCycle && (
            <span
              role="status"
              className="inline-flex items-center gap-1 rounded bg-destructive/10 px-1.5 py-0.5 text-destructive"
            >
              <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
              {relationLabel ? issueText(($) => $.org_chart.relation_cycle) : goalText(($) => $.plan.validation_cycle)}
            </span>
          )}
        </div>
        {layout.nodes.length > 0 && (
          <span>{goalText(($) => $.map.click_hint)}</span>
        )}
      </div>

      <div
        data-board-scroller=""
        className="min-h-0 flex-1 overflow-auto group-data-[peek-open]/peek:pr-[var(--issue-peek-width)]"
      >
        {layout.nodes.length > 0 && (
          <div
            className="relative"
            style={{ width: layout.canvasWidth, height: layout.canvasHeight }}
            data-testid="dependency-chart-canvas"
          >
            <svg
              aria-hidden="true"
              width={layout.canvasWidth}
              height={layout.canvasHeight}
              className="pointer-events-none absolute inset-0"
            >
              <defs>
                <marker
                  id={`dep-arrow-${arrowId}`}
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
              {layout.edges.map((edge) => {
                const from = layout.nodeById.get(edge.fromId);
                const to = layout.nodeById.get(edge.toId);
                if (!from || !to) return null;
                const path = buildEdgePath(from, to, edge.isBackEdge);
                const isHighlighted =
                  activeIssueId !== null &&
                  (edge.fromId === activeIssueId || edge.toId === activeIssueId);
                const isDimmed = activeIssueId !== null && !isHighlighted;
                const tooltip = `${from.issue.identifier} → ${to.issue.identifier} (${kindLabel(edge.kind)})${edge.reason ? `: ${edge.reason}` : ""}`;

                return (
                  <g
                    key={edge.id}
                    data-dependency-edge={edge.id}
                    data-from-id={edge.fromId}
                    data-to-id={edge.toId}
                    data-edge-kind={edge.kind}
                    data-edge-reason={edge.reason}
                  >
                    <path
                      d={path}
                      fill="none"
                      stroke="currentColor"
                      strokeWidth={isHighlighted ? 2.25 : 1.5}
                      strokeDasharray={edge.kind === "conditional" ? "6 4" : undefined}
                      markerEnd={`url(#dep-arrow-${arrowId})`}
                      className={cn(
                        "transition-opacity",
                        isHighlighted
                          ? "text-brand"
                          : edge.isBackEdge
                            ? "text-destructive/80"
                            : "text-muted-foreground/80",
                      )}
                      opacity={isDimmed ? 0.18 : 0.92}
                    >
                      <title>{tooltip}</title>
                    </path>
                  </g>
                );
              })}
            </svg>

            {layout.nodes.map((node) => {
              const isSelected = activeIssueId === node.issue.id;
              const isHighlighted = highlightedIssueIds.has(node.issue.id);
              const isDimmed = activeIssueId !== null && !isHighlighted;
              return (
                <button
                  key={node.issue.id}
                  type="button"
                  aria-pressed={isSelected}
                  {...{ [PEEK_TARGET_ATTR]: node.issue.id }}
                  data-testid="dependency-chart-card"
                  data-issue-id={node.issue.id}
                  style={{
                    left: node.x,
                    top: node.y,
                    width: node.width,
                    height: node.height,
                  }}
                  onClick={() => peekActions?.open(node.issue.id)}
                  className={cn(
                    "absolute flex cursor-pointer flex-col justify-between rounded-lg border bg-card px-3 py-2 text-left shadow-2xs transition-all",
                    isSelected
                      ? "border-brand ring-2 ring-brand/40"
                      : isHighlighted
                        ? "border-brand/60 bg-brand/[0.03]"
                        : node.inCycle
                          ? "border-destructive/60"
                          : "border-border hover:border-foreground/30",
                    isDimmed && "opacity-35",
                  )}
                >
                  <span className="truncate font-mono text-caption text-muted-foreground">
                    {node.issue.identifier}
                  </span>
                  <span className="line-clamp-2 text-body font-medium text-foreground">
                    {node.issue.title}
                  </span>
                </button>
              );
            })}
          </div>
        )}

        {layout.unrelatedRows.length > 0 && (
          <section
            className={cn("px-6 py-4", layout.nodes.length > 0 && "border-t")}
            aria-label={noLinks}
          >
            <h3 className="mb-2 text-caption font-medium text-muted-foreground">
              {noLinks}
            </h3>
            <div className="flex flex-wrap gap-3">
              {layout.unrelatedRows.map((issue) => (
                <button
                  key={issue.id}
                  type="button"
                  aria-pressed={peekId === issue.id}
                  {...{ [PEEK_TARGET_ATTR]: issue.id }}
                  data-testid="dependency-chart-unrelated-card"
                  data-issue-id={issue.id}
                  style={{ width: NODE_WIDTH }}
                  onClick={() => peekActions?.open(issue.id)}
                  className={cn(
                    "flex cursor-pointer flex-col gap-1 rounded-lg border bg-card px-3 py-2 shadow-2xs transition-all text-left",
                    peekId === issue.id
                      ? "border-brand ring-2 ring-brand/40"
                      : "border-border hover:border-foreground/30",
                  )}
                >
                  <span className="truncate font-mono text-caption text-muted-foreground">
                    {issue.identifier}
                  </span>
                  <span className="line-clamp-2 text-body font-medium text-foreground">
                    {issue.title}
                  </span>
                </button>
              ))}
            </div>
          </section>
        )}
      </div>

      {activeNode && (
        <div
          className="shrink-0 border-t bg-muted/30 px-4 py-3 text-body"
          data-testid="dependency-edge-inspector"
        >
          <div className="mb-2 flex items-center justify-between gap-2">
            <div className="flex min-w-0 items-center gap-2">
              <span className="shrink-0 font-mono text-caption text-muted-foreground">
                {activeNode.issue.identifier}
              </span>
              <span className="truncate font-medium text-foreground">
                {activeNode.issue.title}
              </span>
            </div>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => peekActions?.close()}
            >
              {goalText(($) => $.map.close_inspector)}
            </Button>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <p className="mb-1 text-caption font-medium text-muted-foreground">
                {relationLabel ? issueText(($) => $.org_chart.relation_to) : goalText(($) => $.map.upstream)}
              </p>
              {upstreamEdges.length === 0 ? (
                <p className="text-caption text-muted-foreground">
                  {noLinks}
                </p>
              ) : (
                <ul className="space-y-1.5">
                  {upstreamEdges.map((edge) => {
                    const prereq = layout.nodeById.get(edge.fromId)?.issue;
                    if (!prereq) return null;
                    return (
                      <li key={edge.id} className="rounded border bg-background px-2.5 py-1.5 text-caption">
                        <div className="flex flex-wrap items-center gap-1.5">
                          <button
                            type="button"
                            onClick={() => peekActions?.open(prereq.id)}
                            className="font-mono text-muted-foreground hover:underline"
                          >
                            {prereq.identifier}
                          </button>
                          <span className="font-medium text-foreground">{prereq.title}</span>
                          <span
                            className={cn(
                              "rounded px-1.5 py-0.5 text-[11px]",
                              edge.kind === "conditional"
                                ? "border border-dashed border-border text-muted-foreground"
                                : "bg-secondary text-secondary-foreground",
                            )}
                          >
                            {kindLabel(edge.kind)}
                          </span>
                        </div>
                        {edge.reason && (
                          <p className="mt-1 text-muted-foreground">
                            {goalText(($) => $.plan.dependency_reason)}: {edge.reason}
                          </p>
                        )}
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
            <div>
              <p className="mb-1 text-caption font-medium text-muted-foreground">
                {relationLabel ? issueText(($) => $.org_chart.relation_from) : goalText(($) => $.map.downstream)}
              </p>
              {downstreamEdges.length === 0 ? (
                <p className="text-caption text-muted-foreground">
                  {noLinks}
                </p>
              ) : (
                <ul className="space-y-1.5">
                  {downstreamEdges.map((edge) => {
                    const dep = layout.nodeById.get(edge.toId)?.issue;
                    if (!dep) return null;
                    return (
                      <li key={edge.id} className="rounded border bg-background px-2.5 py-1.5 text-caption">
                        <div className="flex flex-wrap items-center gap-1.5">
                          <button
                            type="button"
                            onClick={() => peekActions?.open(dep.id)}
                            className="font-mono text-muted-foreground hover:underline"
                          >
                            {dep.identifier}
                          </button>
                          <span className="font-medium text-foreground">{dep.title}</span>
                          <span
                            className={cn(
                              "rounded px-1.5 py-0.5 text-[11px]",
                              edge.kind === "conditional"
                                ? "border border-dashed border-border text-muted-foreground"
                                : "bg-secondary text-secondary-foreground",
                            )}
                          >
                            {kindLabel(edge.kind)}
                          </span>
                        </div>
                        {edge.reason && (
                          <p className="mt-1 text-muted-foreground">
                            {goalText(($) => $.plan.dependency_reason)}: {edge.reason}
                          </p>
                        )}
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
