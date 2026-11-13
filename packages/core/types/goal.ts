import type { Issue } from "./issue";
import type { PlanDependencyState } from "./closure";

/**
 * A goal is a first-class deliverable object. It owns its intent, scope,
 * acceptance criteria, owner and lifecycle; it is not an issue, never inherits
 * issue status, and no issue transition changes it.
 *
 * The server may grow statuses ahead of this client. Callers must treat an
 * unrecognized status as unknown rather than mapping it to `completed`.
 */
export type GoalStatus =
  | "planned"
  | "in_progress"
  | "paused"
  | "completed"
  | "cancelled";

/** Known goal owner kinds. An owner is responsibility only; it starts no run. */
export type GoalOwnerType = "member" | "agent" | "squad";

export interface Goal {
  id: string;
  workspace_id: string;
  /** null means a workspace-level goal rather than a project goal. */
  project_id: string | null;
  title: string;
  intent: string;
  scope: string;
  acceptance_criteria: string;
  owner_type: GoalOwnerType | null;
  owner_id: string | null;
  status: GoalStatus;
  creator_type: string;
  creator_id: string;
  created_at: string;
  updated_at: string;
}

export interface CreateGoalRequest {
  title: string;
  intent?: string;
  scope?: string;
  acceptance_criteria?: string;
  /** Omit or null for a workspace-level goal. */
  project_id?: string | null;
  /** owner_type and owner_id are set or cleared together. */
  owner_type?: GoalOwnerType | null;
  owner_id?: string | null;
  /** Defaults to the server's `planned`. */
  status?: GoalStatus;
}

/**
 * Partial update. Only keys present in the body change: omitting a key keeps
 * the current value, while `null` clears `project_id`/owner and `""` clears a
 * body field.
 */
export interface UpdateGoalRequest {
  title?: string;
  intent?: string | null;
  scope?: string | null;
  acceptance_criteria?: string | null;
  project_id?: string | null;
  owner_type?: GoalOwnerType | null;
  owner_id?: string | null;
  status?: GoalStatus;
}

export interface ListGoalsParams {
  /** A project UUID, `"none"` for workspace-level goals, or omitted for all. */
  project_id?: string;
  status?: GoalStatus;
  limit?: number;
  offset?: number;
}

export interface ListGoalsResponse {
  goals: Goal[];
  total: number;
}

export interface ListGoalIssuesParams {
  limit?: number;
  offset?: number;
}

export interface ListGoalIssuesResponse {
  issues: Issue[];
  total: number;
}

/**
 * A goal plan (DEV-6) is an editable draft, not a set of executable tasks.
 * Nodes carry stable UUIDs; a node only becomes an issue when a later
 * increment explicitly starts the plan. Nothing in this shape dispatches,
 * assigns or wakes anything.
 *
 * Server enums may grow ahead of this client. `state`, `kind`, binding and
 * owner `type` stay plain strings so an unknown value survives parsing and is
 * rendered conservatively instead of being coerced into a known one.
 */
export interface GoalPlanEvidenceRef {
  kind: string;
  ref: string;
  note: string;
  version: string;
}

export interface GoalPlanBinding {
  type: string;
  /** Present only for an existing-task binding. */
  issue_id: string | null;
}

/** A suggestion only: it never becomes `issue.assignee` and starts no run. */
export interface GoalPlanProposedOwner {
  type: string;
  id: string;
}

export interface GoalPlanCoordination {
  parallel_reason: string;
  write_scope: string[];
  exclusive_resources: string[];
  integration_owner: string;
  next_evaluation: string;
}

export interface GoalPlanItem {
  /** Stable node UUID, preserved across edits and merges. */
  id: string;
  title: string;
  outcome: string;
  acceptance_criteria: string;
  evidence_refs: GoalPlanEvidenceRef[];
  binding: GoalPlanBinding;
  proposed_owner: GoalPlanProposedOwner | null;
  stage: number | null;
  coordination: GoalPlanCoordination | null;
}

/** `dependent` must wait for `prerequisite`; no edge means they may run in parallel. */
export interface GoalPlanEdge {
  dependent_item_id: string;
  prerequisite_item_id: string;
  reason: string;
}

/** Arbitrary JSON recorded about what a plan was based on. */
export type GoalPlanContextSnapshot = Record<string, unknown>;

/** One blocker keeping a plan item parked. `reason` is a stable code. */
export interface GoalPlanExecutionPrerequisite {
  item_id: string;
  issue_id: string;
  /** Issue status at read time; empty when the issue could not be read. */
  status: string;
  /** dependency_not_done | dependency_not_accepted | ... or a newer code. */
  reason: string;
  /** Free text, may be English: diagnostics only, never the primary message. */
  detail: string;
}

/**
 * The real event that releases a parked item. Triggers are the writes that
 * re-check readiness today; the UI must not imply a timer or a retry.
 */
export interface GoalPlanExecutionWait {
  /** dependency_changed, or a newer value. */
  event: string;
  /** prerequisite.issue_status_changed | goal_plan.saved | ... */
  triggers: string[];
  /** Free text, may be English: diagnostics only. */
  description: string;
  prerequisites: GoalPlanExecutionPrerequisite[];
}

/**
 * One item's durable execution binding, restored after a refresh. The
 * materialize reply describes a single write; this is what the read path can
 * confirm. `dependency` is absent while the item has no bound task.
 */
export interface GoalPlanExecutionItem {
  item_id: string;
  /** The plan's own vocabulary: new | existing. */
  binding_type: string;
  issue_id: string;
  /** True only when the persisted materialization row supplied the issue. */
  materialized: boolean;
  issue_status: string;
  assignee_type: string;
  assignee_id: string | null;
  dependency: PlanDependencyState | null;
  wait: GoalPlanExecutionWait | null;
}

/** The saved plan's execution read model, keyed by stable plan item id. */
export interface GoalPlanExecution {
  plan_revision: number;
  items: GoalPlanExecutionItem[];
}

export interface GoalPlan {
  /** null for a goal that has never saved a plan (synthetic empty draft). */
  id: string | null;
  goal_id: string;
  project_id: string | null;
  revision: number;
  state: string;
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  context_snapshot: GoalPlanContextSnapshot;
  /** null when the server predates the execution read model. */
  execution: GoalPlanExecution | null;
  created_at: string | null;
  updated_at: string | null;
}

export interface SaveGoalPlanRequest {
  expected_revision: number;
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  context_snapshot: GoalPlanContextSnapshot;
  /** Marks this generation applied in the same transaction as the save. */
  generation_id?: string | null;
}

export interface GoalPlanCandidate {
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  context_snapshot: GoalPlanContextSnapshot;
}

export interface GoalPlanGeneration {
  id: string;
  goal_id: string;
  project_id: string;
  request_key: string;
  intent: string;
  agent_id: string;
  base_revision: number;
  /** preparing | queued | running | succeeded | failed, or a newer value. */
  state: string;
  chat_session_id: string | null;
  task_id: string | null;
  input_context: GoalPlanContextSnapshot;
  candidate: GoalPlanCandidate | null;
  applied: boolean;
  applied_revision: number | null;
  observed_revision: number | null;
  current_revision: number;
  /** A late candidate: the draft moved on since the run started. */
  stale: boolean;
  error: string;
  created_at: string;
  updated_at: string;
}

export interface CreateGoalPlanGenerationRequest {
  intent: string;
  /** Stable for one user action; a replay returns the original generation. */
  request_key: string;
  expected_revision: number;
  agent_id: string;
}