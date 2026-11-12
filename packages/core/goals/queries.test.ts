// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { ListGoalIssuesResponse, ListGoalsResponse } from "../types";
import {
  GOAL_PAGE_SIZE,
  GOAL_PLAN_GENERATION_POLL_MS,
  goalIssuesPagesOptions,
  goalKeys,
  goalListPagesOptions,
  goalPlanGenerationOptions,
  goalPlanOptions,
  isGoalPlanGenerationPending,
  isGoalPlanGenerationTerminal,
} from "./queries";

const api = {
  listGoals: vi.fn(),
  listGoalIssues: vi.fn(),
  getGoalPlan: vi.fn(),
};

beforeEach(() => {
  api.listGoals.mockResolvedValue({ goals: [], total: 0 } satisfies ListGoalsResponse);
  api.listGoalIssues.mockResolvedValue({ issues: [], total: 0 } satisfies ListGoalIssuesResponse);
  setApiInstance(api as unknown as ApiClient);
});

afterEach(() => vi.clearAllMocks());

describe("goal query pagination", () => {
  it("turns list filters into server parameters", async () => {
    const options = goalListPagesOptions("ws-1", { projectId: "none", status: "paused" });
    await options.queryFn!({ pageParam: 50 } as never);
    expect(api.listGoals).toHaveBeenCalledWith({
      project_id: "none",
      status: "paused",
      limit: GOAL_PAGE_SIZE,
      offset: 50,
    });
  });

  it("omits an unscoped project filter", async () => {
    const options = goalListPagesOptions("ws-1", { projectId: null, status: null });
    await options.queryFn!({ pageParam: 0 } as never);
    expect(api.listGoals).toHaveBeenCalledWith({ limit: GOAL_PAGE_SIZE, offset: 0 });
  });

  it("keeps paging until the loaded rows reach the server total", () => {
    const options = goalListPagesOptions("ws-1", {});
    const getNext = options.getNextPageParam!;
    expect(
      getNext({ goals: [], total: 120 } as never, [{ goals: Array(50) }] as never, 0 as never, [0] as never),
    ).toBe(50);
    expect(
      getNext({ goals: [], total: 120 } as never, [
        { goals: Array(50) },
        { goals: Array(50) },
      ] as never, 50 as never, [0, 50] as never),
    ).toBe(100);
    // 100 loaded of 120: the next page starts at 100.
    expect(
      getNext({ goals: [], total: 120 } as never, [
        { goals: Array(50) },
        { goals: Array(50) },
        { goals: Array(20) },
      ] as never, 100 as never, [0, 50, 100] as never),
    ).toBeUndefined();
  });

  it("pages a goal's linked tasks by offset", async () => {
    const options = goalIssuesPagesOptions("ws-1", "goal-1");
    await options.queryFn!({ pageParam: 50 } as never);
    expect(api.listGoalIssues).toHaveBeenCalledWith("goal-1", {
      limit: GOAL_PAGE_SIZE,
      offset: 50,
    });
    expect(
      options.getNextPageParam!(
        { issues: [], total: 60 } as never,
        [{ issues: Array(50) }] as never,
        0 as never,
        [0] as never,
      ),
    ).toBe(50);
  });
});

describe("goal plan queries", () => {
  it("keeps plan and generation keys under goalKeys.all", () => {
    expect(goalKeys.plan("ws-1", "goal-1")).toEqual(
      [...goalKeys.all("ws-1"), "plan", "goal-1"],
    );
    expect(goalKeys.planGeneration("ws-1", "goal-1", "gen-1")).toEqual([
      ...goalKeys.plan("ws-1", "goal-1"),
      "generation",
      "gen-1",
    ]);
  });

  it("reads the draft plan for the goal", async () => {
    api.getGoalPlan = vi.fn().mockResolvedValue({
      id: null,
      goal_id: "goal-1",
      project_id: null,
      revision: 0,
      state: "draft",
      items: [],
      edges: [],
      context_snapshot: {},
      created_at: null,
      updated_at: null,
    });
    setApiInstance(api as unknown as ApiClient);
    const options = goalPlanOptions("ws-1", "goal-1");
    expect(options.queryKey).toEqual(goalKeys.plan("ws-1", "goal-1"));
    await options.queryFn!({} as never);
    expect(api.getGoalPlan).toHaveBeenCalledWith("goal-1");
  });

  it("polls only while the run is live", () => {
    const options = goalPlanGenerationOptions("ws-1", "goal-1", "gen-1");
    const interval = options.refetchInterval as (query: unknown) => number | false;
    const state = (value: string) => ({ state: { data: { state: value } } });
    expect(interval(state("preparing"))).toBe(GOAL_PLAN_GENERATION_POLL_MS);
    expect(interval(state("queued"))).toBe(GOAL_PLAN_GENERATION_POLL_MS);
    expect(interval(state("running"))).toBe(GOAL_PLAN_GENERATION_POLL_MS);
    expect(interval(state("succeeded"))).toBe(false);
    expect(interval(state("failed"))).toBe(false);
    // An unknown state stops the timer: this client never assumes progress.
    expect(interval(state("archived"))).toBe(false);
    expect(interval({ state: { data: undefined } })).toBe(false);
  });

  it("classifies generation states conservatively", () => {
    expect(isGoalPlanGenerationPending("queued")).toBe(true);
    expect(isGoalPlanGenerationPending("succeeded")).toBe(false);
    expect(isGoalPlanGenerationTerminal("succeeded")).toBe(true);
    expect(isGoalPlanGenerationTerminal("failed")).toBe(true);
    expect(isGoalPlanGenerationTerminal("archived")).toBe(false);
  });
});