import type {
  GoalPlanExecution,
  GoalPlanExecutionWait,
  MaterializedGoalPlanItem,
  MaterializeGoalPlanResponse,
  PlanDependencyState,
} from "@multica/core/types";

/**
 * Display-side mapping for plan execution. Two sources describe one item:
 *
 * - the materialize reply, which reports this write (`created` vs reused and
 *   whether it enqueued a run) and is the only place those facts exist;
 * - the plan's execution read model, which is what a refreshed page can prove
 *   (persisted binding, assignee, dependency verdict and release event).
 *
 * The read model is authoritative for durable state; the reply is used only for
 * facts the read model does not carry. Neither ever implies that a parked task
 * resumes by itself.
 */

export type DependencyDisplay =
  | "ready"
  | "not_done"
  | "not_accepted"
  | "unknown"
  | "unreadable"
  | "unrecognized";

export function dependencyDisplay(reason: string): DependencyDisplay {
  switch (reason) {
    case "":
      return "ready";
    case "dependency_not_done":
      return "not_done";
    case "dependency_not_accepted":
      return "not_accepted";
    case "dependency_unknown":
      return "unknown";
    case "dependency_unreadable":
      return "unreadable";
    default:
      return "unrecognized";
  }
}

export interface ExecutionReadItem {
  itemId: string;
  issueId: string;
  bindingType: string;
  materialized: boolean;
  issueStatus: string;
  assigneeType: string;
  assigneeId: string | null;
  dependency: PlanDependencyState | null;
  wait: GoalPlanExecutionWait | null;
}

/**
 * Bound items from the execution read model, or null when the server supplied
 * none for this revision. An item without an issue is not a binding and is
 * skipped: showing it as bound would claim a task exists.
 */
export function executionReadItems(
  execution: GoalPlanExecution | null | undefined,
  planRevision: number,
): Map<string, ExecutionReadItem> | null {
  if (!execution || execution.plan_revision !== planRevision) return null;
  const items = new Map<string, ExecutionReadItem>();
  for (const row of execution.items) {
    if (!row.item_id || !row.issue_id) continue;
    items.set(row.item_id, {
      itemId: row.item_id,
      issueId: row.issue_id,
      bindingType: row.binding_type,
      materialized: row.materialized,
      issueStatus: row.issue_status,
      assigneeType: row.assignee_type,
      assigneeId: row.assignee_id,
      dependency: row.dependency,
      wait: row.wait,
    });
  }
  return items;
}

export interface ExecutionItemView {
  itemId: string;
  issueId: string;
  /** From this session's reply only; null when the binding was restored. */
  created: boolean | null;
  assigneeType: string;
  assigneeId: string | null;
  dependency: PlanDependencyState | null;
  wait: GoalPlanExecutionWait | null;
  /** From this session's reply only; null when unknown. */
  taskEnqueued: boolean | null;
  /** True when no reply describes this item in the current revision. */
  restored: boolean;
}

/**
 * One row per item, combining the durable read model with this session's
 * reply. A reply about another goal or revision is ignored entirely, so an
 * older write can never be shown as the current plan's execution.
 */
export function mergeExecutionViews({
  execution,
  reply,
  goalId,
  planRevision,
}: {
  execution: GoalPlanExecution | null | undefined;
  reply: MaterializeGoalPlanResponse | null;
  goalId: string;
  planRevision: number;
}): Map<string, ExecutionItemView> | null {
  const read = executionReadItems(execution, planRevision);
  const usableReply =
    reply && reply.goal_id === goalId && reply.plan_revision === planRevision ? reply : null;
  const replyRows = new Map<string, MaterializedGoalPlanItem>();
  for (const row of usableReply?.items ?? []) {
    if (row.item_id && row.issue_id) replyRows.set(row.item_id, row);
  }
  if (!read && replyRows.size === 0) return null;

  const views = new Map<string, ExecutionItemView>();
  const itemIds = new Set<string>([...(read?.keys() ?? []), ...replyRows.keys()]);
  for (const itemId of itemIds) {
    const readRow = read?.get(itemId) ?? null;
    const replyRow = replyRows.get(itemId) ?? null;
    const issueId = replyRow?.issue_id || readRow?.issueId || "";
    if (!issueId) continue;
    // A read row about a different issue cannot describe this binding.
    const sameIssue = readRow?.issueId === issueId ? readRow : null;
    views.set(itemId, {
      itemId,
      issueId,
      created: replyRow ? replyRow.created : null,
      assigneeType: sameIssue?.assigneeType || replyRow?.assignee_type || "",
      assigneeId: sameIssue?.assigneeId ?? replyRow?.assignee_id ?? null,
      // A reply is this request's own verdict; a read row is the durable one.
      dependency: replyRow ? replyRow.dependency : (sameIssue?.dependency ?? null),
      wait: sameIssue?.wait ?? null,
      taskEnqueued: replyRow ? replyRow.task_enqueued : null,
      restored: !replyRow,
    });
  }
  return views;
}

export type WaitReasonDisplay =
  | "not_done"
  | "not_accepted"
  | "unknown"
  | "unreadable"
  | "unrecognized";

/** Primary wait copy key per recognized reason; unknown codes stay their own. */
export const WAIT_MAIN_COPY_KEY = {
  not_done: "wait_main_not_done",
  not_accepted: "wait_main_not_accepted",
  unknown: "wait_main_unknown",
  unreadable: "wait_main_unreadable",
  unrecognized: "wait_main_unrecognized",
} as const satisfies Record<WaitReasonDisplay, string>;

/** Release events this build can explain; anything else shows the raw code. */
export const TRIGGER_COPY_KEY = {
  "prerequisite.issue_status_changed": "trigger_issue_status_changed",
  "goal_plan.saved": "trigger_plan_saved",
  "goal_plan.materialized": "trigger_plan_materialized",
} as const;

export type TriggerCopyKey = (typeof TRIGGER_COPY_KEY)[keyof typeof TRIGGER_COPY_KEY];

export function triggerCopyKey(code: string): TriggerCopyKey | null {
  return Object.hasOwn(TRIGGER_COPY_KEY, code)
    ? TRIGGER_COPY_KEY[code as keyof typeof TRIGGER_COPY_KEY]
    : null;
}

/** Issue statuses this build translates; an unknown code stays raw. */
export const KNOWN_ISSUE_STATUSES = [
  "backlog",
  "todo",
  "in_progress",
  "in_review",
  "done",
  "blocked",
  "cancelled",
] as const;

export type KnownIssueStatus = (typeof KNOWN_ISSUE_STATUSES)[number];

export function knownIssueStatus(status: string): KnownIssueStatus | null {
  return (KNOWN_ISSUE_STATUSES as readonly string[]).includes(status)
    ? (status as KnownIssueStatus)
    : null;
}

export const DEPENDENCY_WAIT_EVENT = "dependency_changed";

export interface ExecutionWaitSummary {
  reason: WaitReasonDisplay;
  /** The server's stable reason code this summary was built from. */
  dependencyReason: string;
  /** Distinct structured prerequisite statuses; raw codes for unknown ones. */
  statuses: string[];
  event: string;
  triggers: string[];
  detail: string;
}

/**
 * The localized primary message is built from these structured fields. The
 * server's English `detail`/`description` are kept for diagnostics only: they
 * must not be the main explanation a reader sees.
 */
export function executionWaitSummary(
  dependency: PlanDependencyState | null,
  wait: GoalPlanExecutionWait | null,
): ExecutionWaitSummary | null {
  if (!dependency || dependency.ready) return null;
  const prerequisites = wait?.prerequisites ?? [];
  const code = dependency.reason || prerequisites[0]?.reason || "";
  const display = dependencyDisplay(code);
  const statuses: string[] = [];
  for (const prerequisite of prerequisites) {
    const status = prerequisite.status.trim();
    if (status && !statuses.includes(status)) statuses.push(status);
  }
  return {
    reason: display === "ready" ? "unknown" : display,
    dependencyReason: code,
    statuses,
    event: wait?.event ?? "",
    triggers: wait?.triggers ?? [],
    detail: prerequisites[0]?.detail || dependency.detail,
  };
}