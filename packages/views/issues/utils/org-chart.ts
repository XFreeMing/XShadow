import type { Issue } from "@multica/core/types";

export type OrgChartOrientation = "vertical" | "horizontal";

export type OrgChartRootReason = "top_level" | "missing_parent" | "cycle";

export interface OrgChartNode {
  issue: Issue;
  parentId: string | null;
  depth: number;
  isRoot: boolean;
  rootReason: OrgChartRootReason | null;
  directChildCount: number;
  descendantCount: number;
  descendantIds: string[];
  collapsed: boolean;
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface OrgChartEdge {
  id: string;
  parentId: string;
  childId: string;
  path: string;
}

export interface OrgChartLayout {
  nodes: OrgChartNode[];
  edges: OrgChartEdge[];
  width: number;
  height: number;
  collapsibleIds: string[];
  cycleIssueIds: string[];
}

export const ORG_CHART_CARD_WIDTH = 256;
export const ORG_CHART_CARD_HEIGHT = 118;
const SIBLING_GAP = 24;
const LEVEL_GAP = 52;
const ROOT_GAP = 40;
const CANVAS_PADDING = 32;

/**
 * Build a deterministic, acyclic forest layout from a flat list of authorized
 * issues using `parent_issue_id`.
 *
 * - Issues with `parent_issue_id === null` are top-level roots.
 * - Issues whose `parent_issue_id` is absent from the current visible set
 *   (filtered out, outside scope, or not yet loaded) become roots tagged with
 *   `rootReason: "missing_parent"`.
 * - Abnormal cycles (including self-parenting) are detected and broken at the
 *   earliest cycle member in input order, tagged with `rootReason: "cycle"`.
 */
export function buildOrgChartLayout(
  issues: readonly Issue[],
  options: {
    collapsedIds?: ReadonlySet<string>;
    orientation?: OrgChartOrientation;
  } = {},
): OrgChartLayout {
  const collapsedIds = options.collapsedIds ?? new Set<string>();
  const orientation = options.orientation ?? "vertical";

  const uniqueIssues: Issue[] = [];
  const issueById = new Map<string, Issue>();
  const orderById = new Map<string, number>();

  for (const issue of issues) {
    if (issueById.has(issue.id)) continue;
    issueById.set(issue.id, issue);
    orderById.set(issue.id, uniqueIssues.length);
    uniqueIssues.push(issue);
  }

  if (uniqueIssues.length === 0) {
    return {
      nodes: [],
      edges: [],
      width: 0,
      height: 0,
      collapsibleIds: [],
      cycleIssueIds: [],
    };
  }

  const effectiveParent = new Map<string, string | null>();
  const rootReasonById = new Map<string, OrgChartRootReason>();
  const cycleSet = new Set<string>();

  for (const issue of uniqueIssues) {
    const parentId = issue.parent_issue_id;
    if (!parentId) {
      effectiveParent.set(issue.id, null);
      rootReasonById.set(issue.id, "top_level");
    } else if (parentId === issue.id) {
      effectiveParent.set(issue.id, null);
      rootReasonById.set(issue.id, "cycle");
      cycleSet.add(issue.id);
    } else if (!issueById.has(parentId)) {
      effectiveParent.set(issue.id, null);
      rootReasonById.set(issue.id, "missing_parent");
    } else {
      effectiveParent.set(issue.id, parentId);
    }
  }

  // Detect multi-node cycles along parent pointers and detach the earliest
  // member in input order so the resulting graph is strictly a forest.
  const visitState = new Map<string, 1 | 2>();
  for (const issue of uniqueIssues) {
    if (visitState.has(issue.id)) continue;
    const path: string[] = [];
    const indexInPath = new Map<string, number>();
    let current: string | null = issue.id;

    while (current !== null && !visitState.has(current)) {
      indexInPath.set(current, path.length);
      path.push(current);
      visitState.set(current, 1);

      const parent: string | null = effectiveParent.get(current) ?? null;
      if (parent !== null && indexInPath.has(parent)) {
        const cycleStart = indexInPath.get(parent)!;
        const cycleMembers = path.slice(cycleStart);
        let breakerId = cycleMembers[0]!;
        let breakerOrder = orderById.get(breakerId) ?? Number.MAX_SAFE_INTEGER;
        for (const memberId of cycleMembers) {
          cycleSet.add(memberId);
          const order = orderById.get(memberId) ?? Number.MAX_SAFE_INTEGER;
          if (order < breakerOrder) {
            breakerOrder = order;
            breakerId = memberId;
          }
        }
        effectiveParent.set(breakerId, null);
        rootReasonById.set(breakerId, "cycle");
        break;
      }
      current = parent;
    }

    for (const id of path) {
      visitState.set(id, 2);
    }
  }

  const childrenByParent = new Map<string, Issue[]>();
  const roots: Issue[] = [];
  for (const issue of uniqueIssues) {
    const parentId = effectiveParent.get(issue.id) ?? null;
    if (parentId === null) {
      roots.push(issue);
    } else {
      const siblings = childrenByParent.get(parentId);
      if (siblings) siblings.push(issue);
      else childrenByParent.set(parentId, [issue]);
    }
  }

  const descendantIdsById = new Map<string, string[]>();
  const collectDescendants = (id: string): string[] => {
    const cached = descendantIdsById.get(id);
    if (cached) return cached;
    const children = childrenByParent.get(id) ?? [];
    const descendants: string[] = [];
    for (const child of children) {
      descendants.push(child.id, ...collectDescendants(child.id));
    }
    descendantIdsById.set(id, descendants);
    return descendants;
  };
  for (const root of roots) {
    collectDescendants(root.id);
  }

  const collapsibleIds: string[] = [];
  for (const issue of uniqueIssues) {
    if ((childrenByParent.get(issue.id)?.length ?? 0) > 0) {
      collapsibleIds.push(issue.id);
    }
  }

  const visibleChildrenOf = (id: string): Issue[] => {
    if (collapsedIds.has(id)) return [];
    return childrenByParent.get(id) ?? [];
  };

  // Compute primary-axis span for each visible subtree.
  const primaryCardSize =
    orientation === "vertical" ? ORG_CHART_CARD_WIDTH : ORG_CHART_CARD_HEIGHT;
  const spanById = new Map<string, number>();

  const measureSpan = (id: string): number => {
    const children = visibleChildrenOf(id);
    if (children.length === 0) {
      spanById.set(id, primaryCardSize);
      return primaryCardSize;
    }
    let total = 0;
    for (let i = 0; i < children.length; i += 1) {
      total += measureSpan(children[i]!.id);
      if (i > 0) total += SIBLING_GAP;
    }
    const span = Math.max(primaryCardSize, total);
    spanById.set(id, span);
    return span;
  };

  for (const root of roots) {
    measureSpan(root.id);
  }

  const nodes: OrgChartNode[] = [];
  const nodeById = new Map<string, OrgChartNode>();
  const visibleEdges: Array<{ parentId: string; childId: string }> = [];

  const placeSubtree = (
    issue: Issue,
    parentId: string | null,
    depth: number,
    offset: number,
  ) => {
    const span = spanById.get(issue.id) ?? primaryCardSize;
    const x =
      orientation === "vertical"
        ? offset + (span - ORG_CHART_CARD_WIDTH) / 2
        : CANVAS_PADDING + depth * (ORG_CHART_CARD_WIDTH + LEVEL_GAP);
    const y =
      orientation === "vertical"
        ? CANVAS_PADDING + depth * (ORG_CHART_CARD_HEIGHT + LEVEL_GAP)
        : offset + (span - ORG_CHART_CARD_HEIGHT) / 2;

    const directChildren = childrenByParent.get(issue.id) ?? [];
    const descendants = descendantIdsById.get(issue.id) ?? [];
    const isRoot = parentId === null;
    const node: OrgChartNode = {
      issue,
      parentId,
      depth,
      isRoot,
      rootReason: isRoot ? (rootReasonById.get(issue.id) ?? "top_level") : null,
      directChildCount: directChildren.length,
      descendantCount: descendants.length,
      descendantIds: descendants,
      collapsed: directChildren.length > 0 && collapsedIds.has(issue.id),
      x,
      y,
      width: ORG_CHART_CARD_WIDTH,
      height: ORG_CHART_CARD_HEIGHT,
    };
    nodes.push(node);
    nodeById.set(issue.id, node);

    const visibleChildren = visibleChildrenOf(issue.id);
    if (visibleChildren.length === 0) return;

    let childrenTotal = 0;
    for (let i = 0; i < visibleChildren.length; i += 1) {
      childrenTotal += spanById.get(visibleChildren[i]!.id) ?? primaryCardSize;
      if (i > 0) childrenTotal += SIBLING_GAP;
    }
    let cursor = offset + (span - childrenTotal) / 2;
    for (const child of visibleChildren) {
      visibleEdges.push({ parentId: issue.id, childId: child.id });
      placeSubtree(child, issue.id, depth + 1, cursor);
      cursor += (spanById.get(child.id) ?? primaryCardSize) + SIBLING_GAP;
    }
  };

  let rootCursor = CANVAS_PADDING;
  for (let i = 0; i < roots.length; i += 1) {
    const root = roots[i]!;
    placeSubtree(root, null, 0, rootCursor);
    rootCursor += (spanById.get(root.id) ?? primaryCardSize) + ROOT_GAP;
  }

  const edges: OrgChartEdge[] = [];
  for (const { parentId, childId } of visibleEdges) {
    const parent = nodeById.get(parentId);
    const child = nodeById.get(childId);
    if (!parent || !child) continue;

    let path: string;
    if (orientation === "vertical") {
      const x1 = parent.x + parent.width / 2;
      const y1 = parent.y + parent.height;
      const x2 = child.x + child.width / 2;
      const y2 = child.y;
      const midY = y1 + (y2 - y1) / 2;
      path = `M ${x1} ${y1} V ${midY} H ${x2} V ${y2}`;
    } else {
      const x1 = parent.x + parent.width;
      const y1 = parent.y + parent.height / 2;
      const x2 = child.x;
      const y2 = child.y + child.height / 2;
      const midX = x1 + (x2 - x1) / 2;
      path = `M ${x1} ${y1} H ${midX} V ${y2} H ${x2}`;
    }

    edges.push({
      id: `${parentId}->${childId}`,
      parentId,
      childId,
      path,
    });
  }

  let maxRight = 0;
  let maxBottom = 0;
  for (const node of nodes) {
    maxRight = Math.max(maxRight, node.x + node.width);
    maxBottom = Math.max(maxBottom, node.y + node.height);
  }

  return {
    nodes,
    edges,
    width: maxRight + CANVAS_PADDING,
    height: maxBottom + CANVAS_PADDING,
    collapsibleIds,
    cycleIssueIds: [...cycleSet],
  };
}
