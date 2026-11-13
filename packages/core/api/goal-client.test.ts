// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

const goal = {
  id: "goal-1",
  workspace_id: "workspace-1",
  project_id: null,
  title: "Goal",
  intent: "",
  scope: "",
  acceptance_criteria: "",
  owner_type: null,
  owner_id: null,
  status: "planned",
  creator_type: "member",
  creator_id: "user-1",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
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

describe("goal API response boundaries", () => {
  const endpoints = ["list", "get", "create", "update"] as const;

  it.each(endpoints)("validates %s responses from older backends", async (endpoint) => {
    stubFetch(endpoint === "list" ? { goals: [goal], total: 1 } : goal);
    const client = new ApiClient("https://api.example.test");
    const result =
      endpoint === "list"
        ? (await client.listGoals()).goals[0]
        : endpoint === "get"
          ? await client.getGoal("goal-1")
          : endpoint === "create"
            ? await client.createGoal({ title: "Goal" })
            : await client.updateGoal("goal-1", { title: "Goal" });
    expect(result).toMatchObject({ id: "goal-1", status: "planned" });
  });

  it.each(endpoints)("falls back on a malformed %s response", async (endpoint) => {
    stubFetch({ id: 42, goals: "invalid" });
    const client = new ApiClient("https://api.example.test");
    if (endpoint === "list") {
      expect(await client.listGoals()).toEqual({ goals: [], total: 0 });
      return;
    }
    const result =
      endpoint === "get"
        ? await client.getGoal("goal-1")
        : endpoint === "create"
          ? await client.createGoal({ title: "Goal" })
          : await client.updateGoal("goal-1", { title: "Goal" });
    // An empty id is the signal a mutation must not treat as saved.
    expect(result.id).toBe("");
  });

  it("lists linked tasks with an empty-id-safe fallback", async () => {
    stubFetch({ issues: "invalid" });
    const client = new ApiClient("https://api.example.test");
    expect(await client.listGoalIssues("goal-1")).toEqual({ issues: [], total: 0 });
  });
});

describe("goal API request contract", () => {
  it("sends list filters as query parameters", async () => {
    const fetch = stubFetch({ goals: [], total: 0 });
    const client = new ApiClient("https://api.example.test");
    await client.listGoals({ project_id: "none", status: "in_progress", limit: 50, offset: 100 });
    const [url, init] = lastCall(fetch);
    expect(url).toContain("/api/goals?");
    expect(url).toContain("project_id=none");
    expect(url).toContain("status=in_progress");
    expect(url).toContain("limit=50");
    expect(url).toContain("offset=100");
    expect(init?.method).toBeUndefined();
  });

  it("creates with POST and updates with PUT", async () => {
    const fetch = stubFetch(goal);
    const client = new ApiClient("https://api.example.test");
    await client.createGoal({ title: "Goal" });
    expect(lastCall(fetch)[0]).toContain("/api/goals");
    expect(lastCall(fetch)[1]?.method).toBe("POST");

    await client.updateGoal("goal-1", { title: "Renamed" });
    expect(lastCall(fetch)[0]).toContain("/api/goals/goal-1");
    expect(lastCall(fetch)[1]?.method).toBe("PUT");
    expect(lastCall(fetch)[1]?.body).toBe(JSON.stringify({ title: "Renamed" }));
  });

  it("deletes, links and unlinks with the contract's methods", async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetch);
    const client = new ApiClient("https://api.example.test");

    await client.deleteGoal("goal-1");
    expect(lastCall(fetch)[0]).toContain("/api/goals/goal-1");
    expect(lastCall(fetch)[1]?.method).toBe("DELETE");

    await client.linkGoalIssue("goal-1", "issue-1");
    expect(lastCall(fetch)[0]).toContain("/api/goals/goal-1/issues/issue-1");
    expect(lastCall(fetch)[1]?.method).toBe("PUT");

    await client.unlinkGoalIssue("goal-1", "issue-1");
    expect(lastCall(fetch)[0]).toContain("/api/goals/goal-1/issues/issue-1");
    expect(lastCall(fetch)[1]?.method).toBe("DELETE");
  });

  it("pages linked tasks by offset", async () => {
    const fetch = stubFetch({ issues: [], total: 0 });
    const client = new ApiClient("https://api.example.test");
    await client.listGoalIssues("goal-1", { limit: 50, offset: 50 });
    const [url] = lastCall(fetch);
    expect(url).toContain("/api/goals/goal-1/issues?");
    expect(url).toContain("limit=50");
    expect(url).toContain("offset=50");
  });
});