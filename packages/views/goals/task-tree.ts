import type { GoalPlan, GoalPlanEdge, GoalPlanItem, Issue } from "@multica/core/types";

export interface GoalTaskNode {
  id: string;
  kind: "plan" | "linked";
  title: string;
  item: GoalPlanItem | null;
  issue: Issue | null;
  stage: number | null;
  isCurrent: boolean;
  isWaiting: boolean;
  isDone: boolean;
  prerequisiteIds: string[];
  dependentIds: string[];
  /** 0-based depth column to the right of the goal root. */
  depth: number;
}

export interface MindMapNodePosition {
  id: string;
  x: number;
  y: number;
  width: number;
  height: number;
  cx: number;
  cy: number;
}

export interface MindMapEdge {
  id: string;
  fromId: string;
  toId: string;
  kind: "goal" | "required" | "conditional";
  reason?: string;
}

export interface GoalTreeBranch {
  node: GoalTaskNode;
  children: GoalTreeBranch[];
  extraPrerequisiteIds: string[];
}

export interface GoalTaskGraph {
  nodes: GoalTaskNode[];
  nodeById: Map<string, GoalTaskNode>;
  columns: GoalTaskNode[][];
  edges: MindMapEdge[];
  treeRoots: GoalTreeBranch[];
  unplannedLinkedNodes: GoalTaskNode[];
  positions: Map<string, MindMapNodePosition>;
  rootPosition: MindMapNodePosition;
  canvasWidth: number;
  canvasHeight: number;
}

export const GOAL_ROOT_ID = "__goal_root__";

const ROOT_WIDTH = 224;
const ROOT_HEIGHT = 112;
const NODE_WIDTH = 232;
const NODE_HEIGHT = 84;
const COL_GAP = 56;
const ROW_GAP = 14;
const PAD_X = 20;
const PAD_Y = 20;

function isIssueDone(issue: Issue | null | undefined): boolean {
  if (!issue) return false;
  return issue.status_category === "done" || issue.status === "done";
}

/**
 * Builds both the left-to-right mind-map layout and the hierarchical tree
 * structure from a goal's plan items, dependency edges, and linked issues.
 */
export function buildGoalTaskGraph({
  items,
  edges,
  linkedIssues,
  progress,
}: {
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  linkedIssues: Issue[];
  progress?: GoalPlan["progress"] | null;
}): GoalTaskGraph {
  const issueById = new Map(linkedIssues.map((issue) => [issue.id, issue]));
  const boundIssueIds = new Set<string>();
  const itemIds = new Set(items.map((item) => item.id));

  const prereqMap = new Map<string, string[]>();
  const depMap = new Map<string, string[]>();
  const validEdges: GoalPlanEdge[] = [];
  const seenEdge = new Set<string>();

  for (const edge of edges) {
    if (!itemIds.has(edge.prerequisite_item_id) || !itemIds.has(edge.dependent_item_id)) continue;
    if (edge.prerequisite_item_id === edge.dependent_item_id) continue;
    const key = `${edge.prerequisite_item_id}\u0000${edge.dependent_item_id}`;
    if (seenEdge.has(key)) continue;
    seenEdge.add(key);
    validEdges.push(edge);
    const prereqs = prereqMap.get(edge.dependent_item_id) ?? [];
    prereqs.push(edge.prerequisite_item_id);
    prereqMap.set(edge.dependent_item_id, prereqs);
    const deps = depMap.get(edge.prerequisite_item_id) ?? [];
    deps.push(edge.dependent_item_id);
    depMap.set(edge.prerequisite_item_id, deps);
  }

  const waitingSet = new Set(progress?.waiting_for ?? []);
  const planNodes: GoalTaskNode[] = items.map((item) => {
    const issue = item.binding.issue_id ? (issueById.get(item.binding.issue_id) ?? null) : null;
    if (issue) boundIssueIds.add(issue.id);
    return {
      id: item.id,
      kind: "plan",
      title: item.title,
      item,
      issue,
      stage: item.stage,
      isCurrent: progress?.item_id === item.id,
      isWaiting: waitingSet.has(item.id),
      isDone: isIssueDone(issue),
      prerequisiteIds: prereqMap.get(item.id) ?? [],
      dependentIds: depMap.get(item.id) ?? [],
      depth: 0,
    };
  });

  const unplannedLinkedNodes: GoalTaskNode[] = linkedIssues
    .filter((issue) => !boundIssueIds.has(issue.id))
    .map((issue) => ({
      id: `issue:${issue.id}`,
      kind: "linked",
      title: issue.title,
      item: null,
      issue,
      stage: null,
      isCurrent: false,
      isWaiting: false,
      isDone: isIssueDone(issue),
      prerequisiteIds: [],
      dependentIds: [],
      depth: 0,
    }));

  // Compute depth for plan nodes using dependency DAG (with cycle protection),
  // falling back to stage ordering when no dependency edges are recorded.
  if (validEdges.length > 0) {
    const depthMemo = new Map<string, number>();
    const visiting = new Set<string>();
    const computeDepth = (id: string): number => {
      const cached = depthMemo.get(id);
      if (cached !== undefined) return cached;
      if (visiting.has(id)) return 0;
      visiting.add(id);
      const prereqs = prereqMap.get(id) ?? [];
      let depth = 0;
      for (const pre of prereqs) {
        depth = Math.max(depth, computeDepth(pre) + 1);
      }
      visiting.delete(id);
      depthMemo.set(id, depth);
      return depth;
    };
    for (const node of planNodes) {
      node.depth = computeDepth(node.id);
    }
  } else {
    const distinctStages = [...new Set(planNodes.map((n) => n.stage).filter((s): s is number => s !== null))].sort(
      (a, b) => a - b,
    );
    if (distinctStages.length > 1) {
      const stageToDepth = new Map(distinctStages.map((s, idx) => [s, idx]));
      for (const node of planNodes) {
        node.depth = node.stage !== null ? (stageToDepth.get(node.stage) ?? 0) : 0;
      }
    }
  }

  const allNodes = [...planNodes, ...unplannedLinkedNodes];
  const nodeById = new Map(allNodes.map((n) => [n.id, n]));
  const orderIndex = new Map(allNodes.map((n, i) => [n.id, i]));

  // Group into columns by depth.
  const maxDepth = allNodes.reduce((max, n) => Math.max(max, n.depth), 0);
  const columns: GoalTaskNode[][] = allNodes.length === 0 ? [] : Array.from({ length: maxDepth + 1 }, () => []);
  for (const node of allNodes) {
    columns[node.depth]!.push(node);
  }
  for (const col of columns) {
    col.sort((a, b) => {
      if (a.kind !== b.kind) return a.kind === "plan" ? -1 : 1;
      const sa = a.stage ?? Number.MAX_SAFE_INTEGER;
      const sb = b.stage ?? Number.MAX_SAFE_INTEGER;
      if (sa !== sb) return sa - sb;
      return (orderIndex.get(a.id) ?? 0) - (orderIndex.get(b.id) ?? 0);
    });
  }

  // Compute mind-map coordinates.
  const maxRows = Math.max(1, ...columns.map((c) => c.length));
  const contentHeight = Math.max(ROOT_HEIGHT, maxRows * NODE_HEIGHT + Math.max(0, maxRows - 1) * ROW_GAP);
  const canvasHeight = contentHeight + PAD_Y * 2;
  const rootY = PAD_Y + (contentHeight - ROOT_HEIGHT) / 2;
  const rootPosition: MindMapNodePosition = {
    id: GOAL_ROOT_ID,
    x: PAD_X,
    y: rootY,
    width: ROOT_WIDTH,
    height: ROOT_HEIGHT,
    cx: PAD_X + ROOT_WIDTH / 2,
    cy: rootY + ROOT_HEIGHT / 2,
  };

  const positions = new Map<string, MindMapNodePosition>();
  columns.forEach((col, colIndex) => {
    const colHeight = col.length * NODE_HEIGHT + Math.max(0, col.length - 1) * ROW_GAP;
    const startY = PAD_Y + (contentHeight - colHeight) / 2;
    const x = PAD_X + ROOT_WIDTH + COL_GAP + colIndex * (NODE_WIDTH + COL_GAP);
    col.forEach((node, rowIndex) => {
      const y = startY + rowIndex * (NODE_HEIGHT + ROW_GAP);
      positions.set(node.id, {
        id: node.id,
        x,
        y,
        width: NODE_WIDTH,
        height: NODE_HEIGHT,
        cx: x + NODE_WIDTH / 2,
        cy: y + NODE_HEIGHT / 2,
      });
    });
  });

  const canvasWidth =
    columns.length === 0
      ? PAD_X * 2 + ROOT_WIDTH
      : PAD_X * 2 + ROOT_WIDTH + columns.length * (COL_GAP + NODE_WIDTH);

  // Build mind-map edges.
  const mindMapEdges: MindMapEdge[] = [];
  for (const node of allNodes) {
    if (node.prerequisiteIds.length === 0) {
      if (node.depth === 0) {
        mindMapEdges.push({
          id: `${GOAL_ROOT_ID}->${node.id}`,
          fromId: GOAL_ROOT_ID,
          toId: node.id,
          kind: "goal",
        });
      } else {
        // Stage-grouped node without explicit edges connects from Goal root.
        mindMapEdges.push({
          id: `${GOAL_ROOT_ID}->${node.id}`,
          fromId: GOAL_ROOT_ID,
          toId: node.id,
          kind: "goal",
        });
      }
    }
  }
  for (const edge of validEdges) {
    mindMapEdges.push({
      id: `${edge.prerequisite_item_id}->${edge.dependent_item_id}`,
      fromId: edge.prerequisite_item_id,
      toId: edge.dependent_item_id,
      kind: edge.kind === "conditional" ? "conditional" : "required",
      reason: edge.reason,
    });
  }

  // Build single-occurrence tree hierarchy for Tree view.
  const childrenByParent = new Map<string, GoalTaskNode[]>();
  const extraPrereqsByNode = new Map<string, string[]>();
  const rootPlanNodes: GoalTaskNode[] = [];

  for (const node of planNodes) {
    if (node.prerequisiteIds.length === 0) {
      rootPlanNodes.push(node);
      continue;
    }
    // Pick the deepest prerequisite as primary parent for clean tree nesting.
    const sortedPrereqs = [...node.prerequisiteIds].sort((a, b) => {
      const da = nodeById.get(a)?.depth ?? 0;
      const db = nodeById.get(b)?.depth ?? 0;
      if (db !== da) return db - da;
      return (orderIndex.get(a) ?? 0) - (orderIndex.get(b) ?? 0);
    });
    const [primary, ...extras] = sortedPrereqs;
    if (primary) {
      const list = childrenByParent.get(primary) ?? [];
      list.push(node);
      childrenByParent.set(primary, list);
      if (extras.length > 0) extraPrereqsByNode.set(node.id, extras);
    } else {
      rootPlanNodes.push(node);
    }
  }

  const visitedTree = new Set<string>();
  const buildBranch = (node: GoalTaskNode): GoalTreeBranch => {
    visitedTree.add(node.id);
    const kids = (childrenByParent.get(node.id) ?? []).filter((child) => !visitedTree.has(child.id));
    return {
      node,
      children: kids.map(buildBranch),
      extraPrerequisiteIds: extraPrereqsByNode.get(node.id) ?? [],
    };
  };

  const treeRoots: GoalTreeBranch[] = rootPlanNodes.map(buildBranch);
  // Ensure any cyclic or unreachable plan nodes are still shown.
  for (const node of planNodes) {
    if (!visitedTree.has(node.id)) {
      treeRoots.push(buildBranch(node));
    }
  }

  return {
    nodes: allNodes,
    nodeById,
    columns,
    edges: mindMapEdges,
    treeRoots,
    unplannedLinkedNodes,
    positions,
    rootPosition,
    canvasWidth,
    canvasHeight,
  };
}

/**
 * Returns the set of node IDs on the upstream + downstream dependency chain of
 * `focusId` (including `focusId` itself).
 */
export function collectDependencyChain(
  focusId: string,
  nodeById: ReadonlyMap<string, GoalTaskNode>,
): Set<string> {
  const result = new Set<string>();
  if (!nodeById.has(focusId)) return result;
  result.add(focusId);

  const walkUp = (id: string) => {
    const node = nodeById.get(id);
    if (!node) return;
    for (const pre of node.prerequisiteIds) {
      if (!result.has(pre)) {
        result.add(pre);
        walkUp(pre);
      }
    }
  };
  const walkDown = (id: string) => {
    const node = nodeById.get(id);
    if (!node) return;
    for (const dep of node.dependentIds) {
      if (!result.has(dep)) {
        result.add(dep);
        walkDown(dep);
      }
    }
  };
  walkUp(focusId);
  walkDown(focusId);
  return result;
}
