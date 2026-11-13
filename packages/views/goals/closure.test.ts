// @vitest-environment node
import { describe, expect, it } from "vitest";
import { ApiError } from "@multica/core/api";
import type { ClosureReview, ClosureState } from "@multica/core/types";
import {
  OUTCOME_COPY_KEY,
  OUTCOME_HINT_KEY,
  REASON_COPY_KEY,
  VERDICT_COPY_KEY,
  closureOutcomeDisplay,
  closureOutcomeTone,
  closureReasonGroup,
  isClosureAcceptable,
  isSupersededReview,
  parseClosureRefusal,
  rejectionDelivery,
  reviewVerdictDisplay,
} from "./closure";
import { RESOURCES } from "../locales";

const en = RESOURCES.en.goals as unknown as Record<string, Record<string, string>>;

function review(overrides: Partial<ClosureReview> = {}): ClosureReview {
  return {
    id: "review-1",
    subject_type: "goal",
    subject_id: "goal-1",
    action: "goal_accept",
    delivery_version: "9f2c4a1",
    delivery_tree: "tree-1",
    criteria_revision: "plan-r2",
    author_type: "agent",
    author_id: "agent-1",
    author_model: "deepseek-v4.1-flash",
    reviewer_type: "agent",
    reviewer_id: "agent-2",
    reviewer_model: "gpt-5-codex",
    verdict: "pass",
    criteria: [],
    platforms: [],
    missing: [],
    reason: "",
    superseded_by: null,
    accepted_at: null,
    accepted_by_type: "",
    accepted_by_id: null,
    recorded_at: "",
    ...overrides,
  };
}

function closure(overrides: Partial<ClosureState> = {}): ClosureState {
  return {
    subject_type: "goal",
    subject_id: "goal-1",
    status: "in_progress",
    managed: true,
    delivery_version: "",
    delivery_tree: "",
    criteria_revision: "",
    outcome: "blocked",
    reason: "",
    detail: "",
    next_actor: "",
    already_accepted: false,
    authorization: null,
    reviews: [],
    ...overrides,
  };
}

describe("closure display mapping", () => {
  it("treats only can_close as acceptable", () => {
    expect(isClosureAcceptable("can_close")).toBe(true);
    for (const outcome of [
      "wait_review",
      "wait_human",
      "needs_changes",
      "blocked",
      "",
      "accepted_someday",
    ]) {
      expect(isClosureAcceptable(outcome)).toBe(false);
    }
  });

  it("keeps an unrecognized outcome in its own bucket", () => {
    expect(closureOutcomeDisplay("can_close")).toBe("can_close");
    expect(closureOutcomeDisplay("accepted_by_a_newer_server")).toBe("unknown");
    expect(closureOutcomeTone("accepted_by_a_newer_server")).toBe("muted");
    expect(closureOutcomeTone("can_close")).toBe("success");
  });

  it("groups every server reason code and never maps an unknown one to success", () => {
    const groups = {
      subject_cancelled: "cancelled",
      not_managed: "not_managed",
      missing_delivery_version: "no_delivery",
      human_step_pending: "human_step",
      no_authorization: "authorization",
      authorization_revoked: "authorization_revoked",
      authorization_expired: "authorization_expired",
      authorization_out_of_scope: "authorization_scope",
      authorization_missing_action: "authorization_scope",
      authorization_stale_criteria: "authorization_stale",
      no_independent_review: "no_independent_review",
      review_not_independent: "no_independent_review",
      review_stale_version: "no_independent_review",
      review_superseded: "no_independent_review",
      review_failed: "review_failed",
      review_needs_changes: "review_needs_changes",
      missing_deliverable: "missing_deliverable",
      deliverables_unknown: "unknown_evidence",
      caller_not_closer: "not_closer",
    } as const;
    for (const [reason, group] of Object.entries(groups)) {
      expect(closureReasonGroup(reason)).toBe(group);
    }
    expect(closureReasonGroup("invented_reason")).toBe("unknown");
    expect(closureReasonGroup("")).toBe("unknown");
  });

  it("maps verdicts without turning an unknown verdict into a pass", () => {
    expect(reviewVerdictDisplay("pass")).toBe("pass");
    expect(reviewVerdictDisplay("fail")).toBe("fail");
    expect(reviewVerdictDisplay("needs_changes")).toBe("needs_changes");
    expect(reviewVerdictDisplay("pending")).toBe("pending");
    expect(reviewVerdictDisplay("approved_by_newer_server")).toBe("unknown");
  });

  it("has copy for every outcome, reason group and verdict in every locale", () => {
    const enClosure = en.closure ?? {};
    const keyGroups = [
      Object.values(OUTCOME_COPY_KEY),
      Object.values(OUTCOME_HINT_KEY),
      Object.values(REASON_COPY_KEY),
      Object.values(VERDICT_COPY_KEY),
    ];
    for (const keys of keyGroups) {
      for (const key of keys) {
        expect(enClosure[key], key).toBeTruthy();
      }
    }
    for (const [locale, bundle] of Object.entries(RESOURCES)) {
      const closureCopy = (bundle as { goals: { closure: Record<string, string> } }).goals
        .closure;
      for (const keys of keyGroups) {
        for (const key of keys) {
          expect(closureCopy[key], `${locale}.${key}`).toBeTruthy();
        }
      }
    }
  });
});

describe("rejectionDelivery", () => {
  it("uses the server's own delivery for the goal", () => {
    expect(
      rejectionDelivery(
        closure({
          delivery_version: "head-sha",
          delivery_tree: "tree",
          criteria_revision: "plan-r4",
        }),
      ),
    ).toEqual({ version: "head-sha", tree: "tree", criteriaRevision: "plan-r4" });
  });

  it("falls back to the newest non-superseded review", () => {
    const delivery = rejectionDelivery(
      closure({
        reviews: [
          review({ id: "newest", delivery_version: "new", delivery_tree: "new-tree" }),
          review({ id: "old", superseded_by: "newest", delivery_version: "old" }),
        ],
        criteria_revision: "plan-r7",
      }),
    );
    expect(delivery).toEqual({ version: "new", tree: "new-tree", criteriaRevision: "plan-r7" });
  });

  it("returns null when nothing names a delivery version", () => {
    expect(rejectionDelivery(closure())).toBeNull();
  });

  it("ignores a superseded review when no other delivery exists", () => {
    expect(
      rejectionDelivery(
        closure({ reviews: [review({ superseded_by: "review-2", delivery_version: "old" })] }),
      ),
    ).toBeNull();
  });
});

describe("parseClosureRefusal", () => {
  it("reads the server's decision fields from a 409 refusal", () => {
    const refusal = parseClosureRefusal(
      new ApiError("refused", 409, "Conflict", {
        code: "closure_not_allowed",
        outcome: "wait_human",
        reason: "human_step_pending",
        detail: "a person must approve release",
        next_actor: "user-7",
      }),
    );
    expect(refusal).toEqual({
      outcome: "wait_human",
      reason: "human_step_pending",
      detail: "a person must approve release",
      nextActor: "user-7",
    });
  });

  it("does not treat another 409 or a non-conflict failure as a closure decision", () => {
    expect(
      parseClosureRefusal(new ApiError("stale", 409, "Conflict", { code: "plan_revision_conflict" })),
    ).toBeNull();
    expect(
      parseClosureRefusal(
        new ApiError("forbidden", 403, "Forbidden", { code: "closure_not_allowed" }),
      ),
    ).toBeNull();
    expect(parseClosureRefusal(new Error("network down"))).toBeNull();
  });

  it("tolerates a refusal body without the optional fields", () => {
    expect(
      parseClosureRefusal(
        new ApiError("refused", 409, "Conflict", { code: "closure_not_allowed" }),
      ),
    ).toEqual({ outcome: "", reason: "", detail: "", nextActor: "" });
  });
});

describe("review helpers", () => {
  it("flags only a review with superseded_by as superseded", () => {
    expect(isSupersededReview(review())).toBe(false);
    expect(isSupersededReview(review({ superseded_by: "review-2" }))).toBe(true);
  });
});