// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubFetch(body: unknown, status = 200) {
  const fetch = vi.fn().mockImplementation(async () => jsonResponse(body, status));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function lastCall(fetch: ReturnType<typeof vi.fn>): [string, RequestInit | undefined] {
  const call = fetch.mock.calls.at(-1);
  return [String(call?.[0]), call?.[1] as RequestInit | undefined];
}

const closureState = {
  subject_type: "goal",
  subject_id: "goal-1",
  status: "in_progress",
  managed: true,
  delivery_version: "9f2c4a1",
  delivery_tree: "tree-1",
  criteria_revision: "plan-r2",
  outcome: "wait_review",
  reason: "no_independent_review",
  detail: "the newest review names an older delivery",
  next_actor: "user-1",
  already_accepted: false,
  authorization: null,
  decisions: [
    {
      id: "decision-1",
      subject_type: "goal",
      subject_id: "goal-1",
      decision: "reject",
      reason: "The acceptance evidence is incomplete",
      criteria_revision: "plan-r2",
      delivery_version: "",
      actor_type: "member",
      actor_id: "user-1",
      recorded_at: "2026-11-11T08:00:00Z",
    },
  ],
  reviews: [
    {
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
      criteria: [{ criterion: "It runs", result: "pass", evidence: "tests" }],
      platforms: ["linux"],
      missing: [],
      reason: "",
      superseded_by: null,
      accepted_at: null,
      accepted_by_type: "",
      accepted_by_id: null,
      recorded_at: "2026-11-10T08:00:00Z",
    },
  ],
};

const reviewResponse = closureState.reviews[0];

const authorizationResponse = {
  id: "grant-1",
  subject_type: "goal",
  subject_id: "goal-1",
  actions: ["goal_accept"],
  covers_children: false,
  criteria_revision: "plan-r2",
  state: "active",
  revision: 1,
  source_ref: "thread 123",
  granted_by_type: "member",
  granted_by_id: "user-1",
  expires_at: null,
  revoked_at: null,
};

afterEach(() => vi.unstubAllGlobals());

describe("goal closure API contract", () => {
  it("reads the goal's closure decision", async () => {
    const fetch = stubFetch(closureState);
    const client = new ApiClient("https://api.example.test");
    const state = await client.getGoalClosure("goal-1");

    expect(state.outcome).toBe("wait_review");
    expect(state.reason).toBe("no_independent_review");
    expect(state.reviews).toHaveLength(1);
    const [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/closure");
    expect(init?.method).toBeUndefined();
  });

  it("reads an issue's closure decision through the issue route", async () => {
    const fetch = stubFetch({ ...closureState, subject_type: "issue", subject_id: "issue-1" });
    const client = new ApiClient("https://api.example.test");
    expect((await client.getIssueClosure("issue-1")).subject_id).toBe("issue-1");
    expect(lastCall(fetch)[0]).toContain("/api/issues/issue-1/closure");
  });

  it("keeps an unknown outcome and reason instead of coercing them", async () => {
    stubFetch({ ...closureState, outcome: "accepted_by_newer_server", reason: "new_code" });
    const client = new ApiClient("https://api.example.test");
    const state = await client.getGoalClosure("goal-1");
    expect(state.outcome).toBe("accepted_by_newer_server");
    expect(state.reason).toBe("new_code");
  });

  it("degrades a malformed read to an empty, unmanaged state", async () => {
    stubFetch({ managed: "yes", reviews: "nope", outcome: 42 });
    const client = new ApiClient("https://api.example.test");
    expect(await client.getGoalClosure("goal-1")).toEqual({
      subject_type: "",
      subject_id: "",
      status: "",
      managed: false,
      delivery_version: "",
      delivery_tree: "",
      criteria_revision: "",
      outcome: "",
      reason: "",
      detail: "",
      next_actor: "",
      already_accepted: false,
      authorization: null,
      reviews: [],
      decisions: [],
    });
  });

  it("grants an acceptance authorization with the explicit action", async () => {
    const fetch = stubFetch(authorizationResponse);
    const client = new ApiClient("https://api.example.test");
    const grant = await client.grantGoalClosureAuthorization("goal-1", {
      actions: ["goal_accept"],
      criteria_revision: "plan-r2",
      source_ref: "thread 123",
    });

    expect(grant.id).toBe("grant-1");
    expect(grant.actions).toEqual(["goal_accept"]);
    const [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/closure/authorizations");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      actions: ["goal_accept"],
      criteria_revision: "plan-r2",
      source_ref: "thread 123",
    });
  });

  it("does not invent an authorization id from a malformed grant reply", async () => {
    stubFetch({ id: 12, actions: "goal_accept" });
    const client = new ApiClient("https://api.example.test");
    const grant = await client.grantGoalClosureAuthorization("goal-1", {
      actions: ["goal_accept"],
    });
    expect(grant.id).toBe("");
    expect(grant.actions).toEqual([]);
  });

  it("records a review verdict against the current delivery", async () => {
    const fetch = stubFetch(reviewResponse, 201);
    const client = new ApiClient("https://api.example.test");
    const review = await client.createGoalClosureReview("goal-1", {
      delivery_version: "9f2c4a1",
      delivery_tree: "tree-1",
      criteria_revision: "plan-r2",
      verdict: "needs_changes",
      reason: "Cover the failure path",
      missing: ["goal-plan-execution"],
    });

    expect(review.id).toBe("review-1");
    const [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/closure/reviews");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toMatchObject({
      delivery_version: "9f2c4a1",
      verdict: "needs_changes",
      reason: "Cover the failure path",
      missing: ["goal-plan-execution"],
    });
  });

  it("does not report a review when the reply is malformed", async () => {
    stubFetch({ id: null, verdict: 7 });
    const client = new ApiClient("https://api.example.test");
    expect(
      await client.createGoalClosureReview("goal-1", {
        delivery_version: "sha",
        verdict: "needs_changes",
      }),
    ).toMatchObject({ id: "", verdict: "" });
  });

  it("reads human decisions separately from reviews", async () => {
    stubFetch(closureState);
    const client = new ApiClient("https://api.example.test");
    const state = await client.getGoalClosure("goal-1");

    expect(state.reviews).toHaveLength(1);
    expect(state.decisions).toHaveLength(1);
    expect(state.decisions[0]).toMatchObject({
      decision: "reject",
      reason: "The acceptance evidence is incomplete",
      delivery_version: "",
    });
    // Neither field stands in for the other.
    expect(state.already_accepted).toBe(false);
    expect(state.outcome).toBe("wait_review");
  });

  it("records a rejection with only a reason", async () => {
    const rejection = {
      id: "decision-2",
      subject_type: "goal",
      subject_id: "goal-1",
      decision: "reject",
      reason: "Cover the failure path",
      criteria_revision: "plan-r2",
      delivery_version: "",
      actor_type: "member",
      actor_id: "user-1",
      recorded_at: "2026-11-12T08:00:00Z",
    };
    const fetch = stubFetch(rejection, 201);
    const client = new ApiClient("https://api.example.test");
    const decision = await client.rejectGoalClosure("goal-1", {
      reason: "Cover the failure path",
    });

    expect(decision).toMatchObject({ id: "decision-2", decision: "reject" });
    const [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/closure/rejections");
    expect(init?.method).toBe("POST");
    // No delivery version is invented for a human decision.
    expect(JSON.parse(String(init?.body))).toEqual({ reason: "Cover the failure path" });
  });

  it("does not report a rejection when the reply is malformed", async () => {
    stubFetch({ id: 3, decision: null });
    const client = new ApiClient("https://api.example.test");
    expect(await client.rejectGoalClosure("goal-1", { reason: "No" })).toMatchObject({
      id: "",
      decision: "",
    });
  });

  it("materializes the saved plan and keeps per-item dependency verdicts", async () => {
    const payload = {
      goal_id: "goal-1",
      plan_revision: 2,
      items: [
        {
          item_id: "a",
          issue_id: "issue-a",
          created: true,
          binding_type: "new",
          assignee_type: "agent",
          assignee_id: "agent-1",
          dependency: { ready: false, reason: "dependency_not_done", detail: "A must finish" },
          task_enqueued: false,
          task_id: null,
        },
      ],
    };
    const fetch = stubFetch(payload);
    const client = new ApiClient("https://api.example.test");
    const result = await client.materializeGoalPlan("goal-1", { items: [{ item_id: "a" }] });

    expect(result.plan_revision).toBe(2);
    expect(result.items[0]).toMatchObject({
      issue_id: "issue-a",
      created: true,
      dependency: { ready: false, reason: "dependency_not_done" },
    });
    const [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/plan/materialize");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ items: [{ item_id: "a" }] });
  });

  it("degrades a malformed materialize reply to no bindings, not a fake success", async () => {
    stubFetch({ goal_id: 5, plan_revision: "2", items: "nope" });
    const client = new ApiClient("https://api.example.test");
    expect(await client.materializeGoalPlan("goal-1", {})).toEqual({
      goal_id: "",
      plan_revision: 0,
      items: [],
    });
  });

  it("defaults a materialized item's missing dependency verdict to not-ready", async () => {
    stubFetch({
      goal_id: "goal-1",
      plan_revision: 2,
      items: [{ item_id: "a", issue_id: "issue-a", created: true }],
    });
    const client = new ApiClient("https://api.example.test");
    const result = await client.materializeGoalPlan("goal-1", {});
    expect(result.items[0]!.dependency).toEqual({ ready: false, reason: "", detail: "" });
  });
});