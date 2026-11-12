/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { Goal, ListGoalIssuesResponse, ListGoalsResponse } from "../types";
import { setApiInstance, ApiError, errorCode } from "../api";
import type { ApiClient } from "../api/client";
import { goalKeys } from "./queries";
import {
  useCreateGoal,
  useDeleteGoal,
  useLinkGoalIssue,
  useUnlinkGoalIssue,
  useUpdateGoal,
} from "./mutations";

vi.mock("../hooks", () => ({ useWorkspaceId: () => "ws-1" }));

const WS = "ws-1";

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

const goal: Goal = {
  id: "goal-1",
  workspace_id: WS,
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
  created_at: "",
  updated_at: "",
};

describe("goal mutations", () => {
  let qc: QueryClient;
  const api = {
    createGoal: vi.fn(),
    updateGoal: vi.fn(),
    deleteGoal: vi.fn(),
    listGoals: vi.fn(),
    listGoalIssues: vi.fn(),
    linkGoalIssue: vi.fn(),
    unlinkGoalIssue: vi.fn(),
  };

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
    api.createGoal.mockResolvedValue(goal);
    api.updateGoal.mockResolvedValue(goal);
    api.deleteGoal.mockResolvedValue(undefined);
    api.listGoals.mockResolvedValue({ goals: [], total: 0 } satisfies ListGoalsResponse);
    api.listGoalIssues.mockResolvedValue({ issues: [], total: 0 } satisfies ListGoalIssuesResponse);
    api.linkGoalIssue.mockResolvedValue(undefined);
    api.unlinkGoalIssue.mockResolvedValue(undefined);
    setApiInstance(api as unknown as ApiClient);
  });

  afterEach(() => {
    qc.clear();
    vi.clearAllMocks();
  });

  it("does not treat a create without a confirmed id as saved", async () => {
    api.createGoal.mockResolvedValue({ ...goal, id: "" });
    const { result } = renderHook(() => useCreateGoal(), { wrapper: createWrapper(qc) });
    await expect(
      act(async () => {
        await result.current.mutateAsync({ title: "Goal" });
      }),
    ).rejects.toThrow(/did not confirm/i);
  });

  it("does not treat an update without a confirmed id as saved", async () => {
    api.updateGoal.mockResolvedValue({ ...goal, id: "" });
    const { result } = renderHook(() => useUpdateGoal(), { wrapper: createWrapper(qc) });
    await expect(
      act(async () => {
        await result.current.mutateAsync({ id: "goal-1", title: "Renamed" });
      }),
    ).rejects.toThrow(/did not confirm/i);
  });

  it("creates through the API and refreshes the goal lists", async () => {
    qc.setQueryData(goalKeys.list(WS, {}), { pages: [], pageParams: [] });
    const { result } = renderHook(() => useCreateGoal(), { wrapper: createWrapper(qc) });
    await act(async () => {
      await result.current.mutateAsync({ title: "Goal" });
    });
    expect(api.createGoal).toHaveBeenCalledWith({ title: "Goal" });
    await waitFor(() =>
      expect(qc.getQueryState(goalKeys.list(WS, {}))?.isInvalidated).toBe(true),
    );
  });

  it("updates through the API and refreshes the detail", async () => {
    qc.setQueryData(goalKeys.detail(WS, "goal-1"), goal);
    const { result } = renderHook(() => useUpdateGoal(), { wrapper: createWrapper(qc) });
    await act(async () => {
      await result.current.mutateAsync({ id: "goal-1", status: "completed" });
    });
    expect(api.updateGoal).toHaveBeenCalledWith("goal-1", { status: "completed" });
    await waitFor(() =>
      expect(qc.getQueryState(goalKeys.detail(WS, "goal-1"))?.isInvalidated).toBe(true),
    );
  });

  it("surfaces a server conflict code to the caller", async () => {
    api.updateGoal.mockRejectedValue(
      new ApiError("locked", 409, "Conflict", { code: "goal_has_linked_issues" }),
    );
    const { result } = renderHook(() => useUpdateGoal(), { wrapper: createWrapper(qc) });
    const error = await act(async () => {
      try {
        await result.current.mutateAsync({ id: "goal-1", project_id: "p2" });
      } catch (err) {
        return err;
      }
      return undefined;
    });
    expect(error).toBeInstanceOf(ApiError);
    expect(errorCode(error)).toBe("goal_has_linked_issues");
  });

  it("drops a deleted goal's cached detail and linked tasks", async () => {
    qc.setQueryData(goalKeys.detail(WS, "goal-1"), goal);
    qc.setQueryData(goalKeys.issues(WS, "goal-1"), { pages: [], pageParams: [] });
    const { result } = renderHook(() => useDeleteGoal(), { wrapper: createWrapper(qc) });
    await act(async () => {
      await result.current.mutateAsync("goal-1");
    });
    expect(api.deleteGoal).toHaveBeenCalledWith("goal-1");
    expect(qc.getQueryData(goalKeys.detail(WS, "goal-1"))).toBeUndefined();
    expect(qc.getQueryData(goalKeys.issues(WS, "goal-1"))).toBeUndefined();
  });

  it("links and unlinks a task, refreshing that goal's task page", async () => {
    qc.setQueryData(goalKeys.issues(WS, "goal-1"), { pages: [], pageParams: [] });
    const link = renderHook(() => useLinkGoalIssue(), { wrapper: createWrapper(qc) });
    await act(async () => {
      await link.result.current.mutateAsync({ goalId: "goal-1", issueId: "issue-1" });
    });
    expect(api.linkGoalIssue).toHaveBeenCalledWith("goal-1", "issue-1");
    await waitFor(() =>
      expect(qc.getQueryState(goalKeys.issues(WS, "goal-1"))?.isInvalidated).toBe(true),
    );

    const unlink = renderHook(() => useUnlinkGoalIssue(), { wrapper: createWrapper(qc) });
    await act(async () => {
      await unlink.result.current.mutateAsync({ goalId: "goal-1", issueId: "issue-1" });
    });
    expect(api.unlinkGoalIssue).toHaveBeenCalledWith("goal-1", "issue-1");
  });

  it("keeps goal data in place when a link is rejected", async () => {
    qc.setQueryData(goalKeys.issues(WS, "goal-1"), { pages: [], pageParams: [] });
    api.linkGoalIssue.mockRejectedValue(
      new ApiError("mismatch", 409, "Conflict", { code: "goal_project_mismatch" }),
    );
    const { result } = renderHook(() => useLinkGoalIssue(), { wrapper: createWrapper(qc) });
    const error = await act(async () => {
      try {
        await result.current.mutateAsync({ goalId: "goal-1", issueId: "issue-1" });
      } catch (err) {
        return err;
      }
      return undefined;
    });
    expect(errorCode(error)).toBe("goal_project_mismatch");
    expect(qc.getQueryData(goalKeys.issues(WS, "goal-1"))).toEqual({ pages: [], pageParams: [] });
  });
});