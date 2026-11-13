import { ApiError } from "@multica/core/api";
import type { ClosureReview, ClosureState } from "@multica/core/types";

/**
 * Display-side mapping for the closure contract. Everything here only reads
 * the server's decision: no helper derives success from a task status, a run
 * exit or the absence of `fire_at`. Unknown values are their own bucket so a
 * newer server decision can never be rendered as "accepted".
 */

export type ClosureOutcomeDisplay =
  | "can_close"
  | "wait_review"
  | "wait_human"
  | "needs_changes"
  | "blocked"
  | "unknown";

export function closureOutcomeDisplay(outcome: string): ClosureOutcomeDisplay {
  switch (outcome) {
    case "can_close":
    case "wait_review":
    case "wait_human":
    case "needs_changes":
    case "blocked":
      return outcome;
    default:
      return "unknown";
  }
}

/** Only `can_close` is a success. Everything else is a wait or a refusal. */
export function isClosureAcceptable(outcome: string): boolean {
  return closureOutcomeDisplay(outcome) === "can_close";
}

export const OUTCOME_COPY_KEY = {
  can_close: "outcome_can_close",
  wait_review: "outcome_wait_review",
  wait_human: "outcome_wait_human",
  needs_changes: "outcome_needs_changes",
  blocked: "outcome_blocked",
  unknown: "outcome_unknown",
} as const satisfies Record<ClosureOutcomeDisplay, string>;

export const OUTCOME_HINT_KEY = {
  can_close: "hint_can_close",
  wait_review: "hint_wait_review",
  wait_human: "hint_wait_human",
  needs_changes: "hint_needs_changes",
  blocked: "hint_blocked",
  unknown: "hint_unknown",
} as const satisfies Record<ClosureOutcomeDisplay, string>;

export type ClosureTone = "success" | "wait" | "attention" | "danger" | "muted";

export function closureOutcomeTone(outcome: string): ClosureTone {
  switch (closureOutcomeDisplay(outcome)) {
    case "can_close":
      return "success";
    case "wait_review":
    case "wait_human":
      return "wait";
    case "needs_changes":
      return "attention";
    case "blocked":
      return "danger";
    default:
      return "muted";
  }
}

export function closureToneClass(tone: ClosureTone): string {
  switch (tone) {
    case "success":
      return "border-success/40 text-success";
    case "wait":
      return "border-warning/40 text-warning";
    case "attention":
      return "border-warning/40 text-warning";
    case "danger":
      return "border-destructive/40 text-destructive";
    default:
      return "text-muted-foreground";
  }
}

/**
 * Reason codes are stable server identifiers. They are grouped only for copy;
 * an unrecognized code keeps its own "unknown" explanation instead of falling
 * back to a success-flavoured message.
 */
export type ClosureReasonGroup =
  | "cancelled"
  | "not_managed"
  | "no_delivery"
  | "human_step"
  | "authorization"
  | "authorization_revoked"
  | "authorization_expired"
  | "authorization_scope"
  | "authorization_stale"
  | "no_independent_review"
  | "review_failed"
  | "review_needs_changes"
  | "missing_deliverable"
  | "unknown_evidence"
  | "not_closer"
  | "unknown";

export function closureReasonGroup(reason: string): ClosureReasonGroup {
  switch (reason) {
    case "subject_cancelled":
      return "cancelled";
    case "not_managed":
      return "not_managed";
    case "missing_delivery_version":
      return "no_delivery";
    case "human_step_pending":
      return "human_step";
    case "no_authorization":
      return "authorization";
    case "authorization_revoked":
      return "authorization_revoked";
    case "authorization_expired":
      return "authorization_expired";
    case "authorization_out_of_scope":
    case "authorization_missing_action":
      return "authorization_scope";
    case "authorization_stale_criteria":
      return "authorization_stale";
    case "no_independent_review":
    case "review_not_independent":
    case "review_stale_version":
    case "review_superseded":
      return "no_independent_review";
    case "review_failed":
      return "review_failed";
    case "review_needs_changes":
      return "review_needs_changes";
    case "missing_deliverable":
      return "missing_deliverable";
    case "deliverables_unknown":
      return "unknown_evidence";
    case "caller_not_closer":
      return "not_closer";
    default:
      return "unknown";
  }
}

export const REASON_COPY_KEY = {
  cancelled: "reason_cancelled",
  not_managed: "reason_not_managed",
  no_delivery: "reason_no_delivery",
  human_step: "reason_human_step",
  authorization: "reason_authorization",
  authorization_revoked: "reason_authorization_revoked",
  authorization_expired: "reason_authorization_expired",
  authorization_scope: "reason_authorization_scope",
  authorization_stale: "reason_authorization_stale",
  no_independent_review: "reason_no_independent_review",
  review_failed: "reason_review_failed",
  review_needs_changes: "reason_review_needs_changes",
  missing_deliverable: "reason_missing_deliverable",
  unknown_evidence: "reason_unknown_evidence",
  not_closer: "reason_not_closer",
  unknown: "reason_unknown",
} as const satisfies Record<ClosureReasonGroup, string>;

export type ReviewVerdictDisplay = "pass" | "fail" | "needs_changes" | "pending" | "unknown";

export function reviewVerdictDisplay(verdict: string): ReviewVerdictDisplay {
  switch (verdict) {
    case "pass":
    case "fail":
    case "needs_changes":
    case "pending":
      return verdict;
    default:
      return "unknown";
  }
}

export const VERDICT_COPY_KEY = {
  pass: "review_verdict_pass",
  fail: "review_verdict_fail",
  needs_changes: "review_verdict_needs_changes",
  pending: "review_verdict_pending",
  unknown: "review_verdict_unknown",
} as const satisfies Record<ReviewVerdictDisplay, string>;

export function verdictTone(verdict: string): ClosureTone {
  switch (reviewVerdictDisplay(verdict)) {
    case "pass":
      return "success";
    case "fail":
      return "danger";
    case "needs_changes":
      return "attention";
    case "pending":
      return "wait";
    default:
      return "muted";
  }
}

export function isSupersededReview(review: ClosureReview): boolean {
  return !!review.superseded_by;
}

export function isAcceptedReview(review: ClosureReview): boolean {
  return !!review.accepted_at;
}

/**
 * A rejection is recorded against a fixed delivery version. The goal's own
 * closure read already falls back to the newest review's delivery, so use it
 * before giving up; without any delivery there is nothing the contract lets us
 * reject, and the caller must not invent one.
 */
export function rejectionDelivery(
  closure: ClosureState,
): { version: string; tree: string; criteriaRevision: string } | null {
  const newest = closure.reviews.find((review) => !isSupersededReview(review));
  const version = closure.delivery_version || newest?.delivery_version || "";
  if (!version) return null;
  return {
    version,
    tree: closure.delivery_version
      ? closure.delivery_tree
      : (newest?.delivery_tree ?? ""),
    criteriaRevision: closure.criteria_revision || newest?.criteria_revision || "",
  };
}

export interface ClosureRefusal {
  outcome: string;
  reason: string;
  detail: string;
  nextActor: string;
}

/**
 * A success-terminal write refused by the closure gate. The server sends the
 * decision fields beside the error code; anything else is an ordinary failure
 * and must not be presented as a closure decision.
 */
export function parseClosureRefusal(error: unknown): ClosureRefusal | null {
  if (!(error instanceof ApiError) || error.status !== 409) return null;
  const body = error.body;
  if (!body || typeof body !== "object") return null;
  const record = body as Record<string, unknown>;
  if (record.code !== "closure_not_allowed") return null;
  const text = (value: unknown) => (typeof value === "string" ? value : "");
  return {
    outcome: text(record.outcome),
    reason: text(record.reason),
    detail: text(record.detail),
    nextActor: text(record.next_actor),
  };
}

/**
 * The decision that should be shown after a refusal. A refusal names the same
 * fields as the read model, so it can be rendered in place until the next read
 * lands.
 */
export function decisionFromRefusal(refusal: ClosureRefusal): ClosureState {
  return {
    subject_type: "goal",
    subject_id: "",
    status: "",
    managed: true,
    delivery_version: "",
    delivery_tree: "",
    criteria_revision: "",
    outcome: refusal.outcome,
    reason: refusal.reason,
    detail: refusal.detail,
    next_actor: refusal.nextActor,
    already_accepted: false,
    authorization: null,
    reviews: [],
  };
}