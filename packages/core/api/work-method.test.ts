// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

const method = {
  id: "seven-step",
  kind: "research",
  name: "Research",
  keywords: [],
  review: "Check the sources",
  rework_step: 0,
  steps: [{ name: "Study", output: "Evidence" }],
};

it("rejects a method whose return step does not exist", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json({ methods: [{ ...method, rework_step: 1 }] }),
    ),
  );
  await expect(
    new ApiClient("https://example.test").listWorkMethods(),
  ).rejects.toThrow("Malformed");
});

it("rejects a working run that has lost its pinned method", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json({ method: null, step: 0, state: "working", revision: 1 }),
    ),
  );
  await expect(
    new ApiClient("https://example.test").getIssueWorkMethod("DEV-1"),
  ).rejects.toThrow("Malformed");
});

it("reads a goal recommendation without creating a run", async () => {
  const fetch = vi.fn(async (_input: RequestInfo | URL) =>
    Response.json({ method, step: 0, state: "ready", revision: 0 }),
  );
  vi.stubGlobal("fetch", fetch);
  const goal = await new ApiClient("https://example.test").getGoalWorkMethod(
    "goal",
  );
  expect(goal.method?.id).toBe("seven-step");
  expect(goal.history).toEqual([]);
  expect(fetch.mock.calls[0]?.[0]).toContain("/api/goals/goal/work-method");
});

it("previews a new issue using the draft deliverable and project", async () => {
  const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
    Response.json({
      triggers: [],
      total_count: 0,
      project_handoff_suppressed: true,
    }),
  );
  vi.stubGlobal("fetch", fetch);
  const client = new ApiClient("https://example.test");
  const preview = await client.previewIssueTrigger({
    isCreate: true,
    title: "Research the choices",
    description: "Interview users",
    projectId: "project",
  });
  expect(JSON.parse(String(fetch.mock.calls[0]?.[1]?.body))).toMatchObject({
    is_create: true,
    title: "Research the choices",
    description: "Interview users",
    project_id: "project",
  });
  expect(preview.project_handoff_suppressed).toBe(true);
});

it("rejects malformed results from template creation and evidence submission", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => Response.json({ id: "wrong" })),
  );
  const client = new ApiClient("https://example.test");
  await expect(
    client.createWorkMethod({
      kind: "research",
      name: "Interviews",
      keywords: ["interview"],
      steps: [{ name: "Interview", output: "Notes" }],
      review: "Review sources",
      rework_step: 0,
    }),
  ).rejects.toThrow("Malformed");
  await expect(
    client.changeIssueWorkMethod("DEV-1", {
      action: "advance",
      base_revision: 0,
      method_id: "seven-step",
      evidence: "Notes",
    }),
  ).rejects.toThrow("Malformed");
});

it("updates the template version opened in the editor and reads back its roles", async () => {
  const step = {
    name: "Interview",
    output: "Notes",
    inputs: "Question list",
    instructions: "Interview two users",
    checks: ["Consent recorded"],
    owner: { kind: "creator" as const },
    reviewer: { kind: "project_lead" as const },
  };
  const update = {
    name: "Two interviews",
    kind: "research" as const,
    keywords: ["interview"],
    steps: [step],
    review: "Audit evidence",
    rework_step: 0,
  };
  const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
    Response.json({ ...update, id: "seven-step", revision: 3 }),
  );
  vi.stubGlobal("fetch", fetch);
  const client = new ApiClient("https://example.test");
  const result = await client.updateWorkMethod("seven-step", 2, update);
  expect(fetch.mock.calls[0]?.[0]).toContain("/api/work-methods/seven-step");
  expect(fetch.mock.calls[0]?.[1]?.method).toBe("PUT");
  expect(JSON.parse(String(fetch.mock.calls[0]?.[1]?.body))).toEqual({
    ...update,
    base_revision: 2,
  });
  expect(result.steps[0]?.reviewer).toEqual(step.reviewer);
  expect(result.revision).toBe(3);
});

it("rejects malformed archived review contracts in a revision adoption", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({
    method, step: 0, state: "working", revision: 2,
    history: [{ action: "adopt_revision", step: 0, actor: "member/A", at: "today",
      method_revision: 2, previous_method: { ...method, steps: [] } }],
  })));
  await expect(new ApiClient("https://example.test").getIssueWorkMethod("DEV-1"))
    .rejects.toThrow("Malformed");
});
