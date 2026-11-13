// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

const plan = {
  id: "plan-1",
  goal_id: "goal-1",
  project_id: "project-1",
  revision: 2,
  state: "draft",
  items: [],
  edges: [],
  context_snapshot: {},
  execution: null,
  created_at: null,
  updated_at: null,
};

const generation = {
  id: "gen-1",
  goal_id: "goal-1",
  project_id: "project-1",
  request_key: "req-1",
  intent: "Plan it",
  agent_id: "agent-1",
  base_revision: 2,
  state: "queued",
  chat_session_id: null,
  task_id: null,
  input_context: {},
  candidate: null,
  applied: false,
  applied_revision: null,
  observed_revision: null,
  current_revision: 2,
  stale: false,
  error: "",
  created_at: "",
  updated_at: "",
};

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

afterEach(() => vi.unstubAllGlobals());

describe("goal plan API contract", () => {
  it("reads the draft plan", async () => {
    const fetch = stubFetch(plan);
    const client = new ApiClient("https://api.example.test");
    expect((await client.getGoalPlan("goal-1")).revision).toBe(2);
    const [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/plan");
    expect(init?.method).toBeUndefined();
  });

  it("falls back to the empty draft on a malformed read", async () => {
    stubFetch({ revision: "nope" });
    const client = new ApiClient("https://api.example.test");
    expect(await client.getGoalPlan("goal-1")).toEqual({
      id: null,
      goal_id: "",
      project_id: null,
      revision: 0,
      state: "draft",
      items: [],
      edges: [],
      context_snapshot: {},
      execution: null,
      created_at: null,
      updated_at: null,
    });
  });

  it("keeps the durable execution read model after a refresh", async () => {
    stubFetch({
      ...plan,
      execution: {
        plan_revision: 2,
        items: [
          {
            item_id: "item-a",
            binding_type: "new",
            issue_id: "issue-a",
            materialized: true,
            issue_status: "deferred",
            assignee_type: "agent",
            assignee_id: "agent-1",
            dependency: {
              ready: false,
              reason: "dependency_not_done",
              detail: "prerequisite issue is not done",
            },
            wait: {
              event: "dependency_changed",
              triggers: ["prerequisite.issue_status_changed"],
              description: "released only by a real dependency change",
              prerequisites: [
                {
                  item_id: "item-b",
                  issue_id: "issue-b",
                  status: "in_progress",
                  reason: "dependency_not_done",
                  detail: "no",
                },
              ],
            },
          },
          { item_id: "item-c", binding_type: "new", materialized: false },
        ],
      },
    });
    const client = new ApiClient("https://api.example.test");
    const execution = (await client.getGoalPlan("goal-1")).execution;

    expect(execution?.plan_revision).toBe(2);
    expect(execution?.items[0]).toMatchObject({
      item_id: "item-a",
      issue_id: "issue-a",
      materialized: true,
      assignee_id: "agent-1",
      dependency: { ready: false, reason: "dependency_not_done" },
      wait: { event: "dependency_changed", prerequisites: [{ status: "in_progress" }] },
    });
    // An unbound item stays visible as unbound instead of inventing a task.
    expect(execution?.items[1]).toMatchObject({ item_id: "item-c", issue_id: "" });
    expect(execution?.items[1]?.dependency).toBeNull();
  });

  it("degrades a malformed execution row without failing the plan read", async () => {
    stubFetch({
      ...plan,
      execution: { plan_revision: "two", items: [{ item_id: 7, dependency: "yes", wait: 4 }] },
    });
    const client = new ApiClient("https://api.example.test");
    const planRead = await client.getGoalPlan("goal-1");

    expect(planRead.revision).toBe(2);
    expect(planRead.execution?.plan_revision).toBe(0);
    expect(planRead.execution?.items[0]).toMatchObject({
      item_id: "",
      issue_id: "",
      materialized: false,
      dependency: null,
      wait: null,
    });
  });

  it("saves with PUT, expected_revision and an optional generation_id", async () => {
    const fetch = stubFetch({ ...plan, revision: 3 });
    const client = new ApiClient("https://api.example.test");
    await client.saveGoalPlan("goal-1", {
      expected_revision: 2,
      items: [],
      edges: [],
      context_snapshot: { repo_sha: "abc" },
      generation_id: "gen-1",
    });
    const [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/plan");
    expect(init?.method).toBe("PUT");
    expect(JSON.parse(String(init?.body))).toEqual({
      expected_revision: 2,
      items: [],
      edges: [],
      context_snapshot: { repo_sha: "abc" },
      generation_id: "gen-1",
    });
  });

  it("creates a generation with POST and reads it back", async () => {
    const fetch = stubFetch(generation, 202);
    const client = new ApiClient("https://api.example.test");
    const created = await client.createGoalPlanGeneration("goal-1", {
      intent: "Plan it",
      request_key: "req-1",
      expected_revision: 2,
      agent_id: "agent-1",
    });
    expect(created.id).toBe("gen-1");
    let [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/plan/generations");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      intent: "Plan it",
      request_key: "req-1",
      expected_revision: 2,
      agent_id: "agent-1",
    });

    fetch.mockResolvedValue(jsonResponse({ ...generation, state: "running" }));
    const polled = await client.getGoalPlanGeneration("goal-1", "gen-1");
    expect(polled.state).toBe("running");
    [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/plan/generations/gen-1");
    expect(init?.method).toBeUndefined();
  });

  it("returns an empty generation id when the reply is malformed", async () => {
    stubFetch({ state: "queued" });
    const client = new ApiClient("https://api.example.test");
    expect((await client.createGoalPlanGeneration("goal-1", {
      intent: "Plan it",
      request_key: "req-1",
      expected_revision: 0,
      agent_id: "agent-1",
    })).id).toBe("");
  });
});