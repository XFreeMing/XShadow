/**
 * DEV-7 closure contract (server: internal/issueclosure).
 *
 * A goal or a goal-linked issue is "enrolled" once a scoped authorization
 * exists for it. Its success terminal is then decided by the server from three
 * separate facts: a persisted authorization, an independent review of the
 * current delivery, and every required deliverable being satisfied.
 *
 * Every enum below is carried as a plain string. The server may add outcomes,
 * reasons, verdicts and actions ahead of this client, and an unknown value must
 * survive parsing so the UI can say "unknown" — never silently read as
 * success, acceptance or a future automatic action.
 */

/** Decisions the server returns; `can_close` is a preview until persisted. */
export type ClosureOutcomeKind =
  | "can_close"
  | "wait_review"
  | "wait_human"
  | "needs_changes"
  | "blocked";

export type ClosureSubjectKind = "issue" | "goal";

export interface ClosureCriterionResult {
  criterion: string;
  /** pass | fail; an unknown value is not a pass. */
  result: string;
  evidence: string;
}

export interface ClosureReview {
  id: string;
  subject_type: string;
  subject_id: string;
  action: string;
  delivery_version: string;
  delivery_tree: string;
  criteria_revision: string;
  author_type: string;
  author_id: string | null;
  author_model: string;
  /** Server-derived; a client never submits its own reviewer identity. */
  reviewer_type: string;
  reviewer_id: string | null;
  reviewer_model: string;
  /** pending | pass | fail | needs_changes, or a newer value. */
  verdict: string;
  criteria: ClosureCriterionResult[];
  platforms: string[];
  /** Required checks the reviewer could not perform. */
  missing: string[];
  reason: string;
  superseded_by: string | null;
  accepted_at: string | null;
  accepted_by_type: string;
  accepted_by_id: string | null;
  recorded_at: string;
}

export interface ClosureAuthorization {
  id: string;
  subject_type: string;
  subject_id: string;
  /** issue_close | goal_accept, or a newer value. */
  actions: string[];
  covers_children: boolean;
  criteria_revision: string;
  /** active | revoked, or a newer value. */
  state: string;
  revision: number;
  source_ref: string;
  granted_by_type: string;
  granted_by_id: string | null;
  expires_at: string | null;
  revoked_at: string | null;
}

/**
 * The single read model shared by Web, Desktop and the server's status-write
 * gate. `outcome` is the server's current judgement; the UI may explain it but
 * must not recompute success from issue statuses, runs or accepted reviews.
 */
export interface ClosureState {
  subject_type: string;
  subject_id: string;
  status: string;
  managed: boolean;
  delivery_version: string;
  delivery_tree: string;
  criteria_revision: string;
  outcome: string;
  /** Stable reason code; free text stays in `detail`. */
  reason: string;
  detail: string;
  next_actor: string;
  /** True when a persisted acceptance already covers this delivery. */
  already_accepted: boolean;
  authorization: ClosureAuthorization | null;
  reviews: ClosureReview[];
}

export interface CreateClosureAuthorizationRequest {
  /** issue_close and/or goal_accept; never implied by another action. */
  actions: string[];
  covers_children?: boolean;
  criteria_revision?: string;
  source_ref?: string;
  expires_at?: string | null;
}

export interface CreateClosureReviewRequest {
  delivery_version: string;
  delivery_tree?: string;
  criteria_revision?: string;
  /** pass | fail | needs_changes (pending is not accepted by the server). */
  verdict: string;
  criteria?: ClosureCriterionResult[];
  platforms?: string[];
  missing?: string[];
  reason?: string;
  /** Stable for one reviewer action; a replay returns the original review. */
  idempotency_key?: string;
}

/**
 * One materialized plan item. Repeating the same request returns the same
 * `issue_id` with `created=false`, so a retried click never creates a second
 * task.
 */
export interface MaterializedGoalPlanItem {
  item_id: string;
  issue_id: string;
  created: boolean;
  /** new | existing */
  binding_type: string;
  assignee_type: string;
  assignee_id: string | null;
  dependency: PlanDependencyState;
  /** True only when this request actually enqueued a run. */
  task_enqueued: boolean;
  task_id: string | null;
}

/**
 * The server's release verdict for one item at materialization time.
 * `reason` is a stable code; `detail` is free text and may be English.
 */
export interface PlanDependencyState {
  ready: boolean;
  reason: string;
  detail: string;
}

export interface MaterializeGoalPlanRequest {
  /** Empty or omitted = every unbound `binding.type = "new"` item. */
  items?: {
    item_id: string;
    assignee_type?: string;
    assignee_id?: string;
  }[];
}

export interface MaterializeGoalPlanResponse {
  goal_id: string;
  plan_revision: number;
  items: MaterializedGoalPlanItem[];
}