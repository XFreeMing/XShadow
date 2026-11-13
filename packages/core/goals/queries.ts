import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { ListGoalsParams } from "../types";

/** How often a live planning run is polled, in milliseconds. */
export const GOAL_PLAN_GENERATION_POLL_MS = 2500;

/** Page size for both goal lists and a goal's linked tasks. */
export const GOAL_PAGE_SIZE = 50;

/**
 * Goal query keys. Every key starts with `["goals", wsId]`, so one
 * invalidation reaches lists, details and linked tasks alike.
 */
export const goalKeys = {
  all: (wsId: string) => ["goals", wsId] as const,
  listAll: (wsId: string) => [...goalKeys.all(wsId), "list"] as const,
  list: (wsId: string, filters: GoalListFilters) =>
    [...goalKeys.listAll(wsId), filters] as const,
  detailAll: (wsId: string) => [...goalKeys.all(wsId), "detail"] as const,
  detail: (wsId: string, goalId: string) =>
    [...goalKeys.detailAll(wsId), goalId] as const,
  issuesAll: (wsId: string) => [...goalKeys.all(wsId), "issues"] as const,
  issues: (wsId: string, goalId: string) =>
    [...goalKeys.issuesAll(wsId), goalId] as const,
  /** The goal's editable plan draft, under `goalKeys.all` so every existing
   *  `goal:changed` / reconnect invalidation reaches it too. */
  plan: (wsId: string, goalId: string) =>
    [...goalKeys.all(wsId), "plan", goalId] as const,
  planGenerationAll: (wsId: string, goalId: string) =>
    [...goalKeys.plan(wsId, goalId), "generation"] as const,
  planGeneration: (wsId: string, goalId: string, generationId: string) =>
    [...goalKeys.planGenerationAll(wsId, goalId), generationId] as const,
  /** The goal's server-owned closure decision, under `goalKeys.all`. */
  closure: (wsId: string, goalId: string) =>
    [...goalKeys.all(wsId), "closure", goalId] as const,
};

/** Generation states that still represent live work on the server. */
export function isGoalPlanGenerationPending(state: string): boolean {
  return state === "preparing" || state === "queued" || state === "running";
}

/**
 * A generation poll stops as soon as the server reports a terminal state.
 * An unknown state is treated as terminal: this client must not assume a run
 * is progressing, and must never assume it succeeded.
 */
export function isGoalPlanGenerationTerminal(state: string): boolean {
  return state === "succeeded" || state === "failed";
}

export interface GoalListFilters {
  /** A project UUID, `"none"` for workspace-level goals, or null for all. */
  projectId?: string | null;
  status?: string | null;
}

function toApiParams(filters: GoalListFilters): ListGoalsParams {
  const params: ListGoalsParams = { limit: GOAL_PAGE_SIZE };
  if (filters.projectId === "none") params.project_id = "none";
  else if (filters.projectId) params.project_id = filters.projectId;
  if (filters.status) params.status = filters.status as ListGoalsParams["status"];
  return params;
}

/**
 * Visible goals for the workspace (or one project / workspace-level goals).
 * Offset pages so a large workspace can see everything without a second
 * "total" contract.
 */
export function goalListPagesOptions(wsId: string, filters: GoalListFilters = {}) {
  return infiniteQueryOptions({
    queryKey: goalKeys.list(wsId, filters),
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.listGoals({ ...toApiParams(filters), offset: pageParam }),
    getNextPageParam: (lastPage, allPages) => {
      const loaded = allPages.reduce((sum, page) => sum + page.goals.length, 0);
      return loaded < lastPage.total ? loaded : undefined;
    },
    enabled: !!wsId,
  });
}

export function goalDetailOptions(wsId: string, goalId: string) {
  return queryOptions({
    queryKey: goalKeys.detail(wsId, goalId),
    queryFn: () => api.getGoal(goalId),
    enabled: !!wsId && !!goalId,
  });
}

/** A goal's linked tasks, newest relation first, paged by offset. */
export function goalIssuesPagesOptions(wsId: string, goalId: string) {
  return infiniteQueryOptions({
    queryKey: goalKeys.issues(wsId, goalId),
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api.listGoalIssues(goalId, { limit: GOAL_PAGE_SIZE, offset: pageParam }),
    getNextPageParam: (lastPage, allPages) => {
      const loaded = allPages.reduce((sum, page) => sum + page.issues.length, 0);
      return loaded < lastPage.total ? loaded : undefined;
    },
    enabled: !!wsId && !!goalId,
  });
}

/**
 * The goal's current plan draft. Server data (TanStack Query) only; local
 * edits live in the editor component and are never written into this cache.
 */
export function goalPlanOptions(wsId: string, goalId: string) {
  return queryOptions({
    queryKey: goalKeys.plan(wsId, goalId),
    queryFn: () => api.getGoalPlan(goalId),
    enabled: !!wsId && !!goalId,
  });
}

/**
 * One planning run. Polls only while the server reports live work, so a
 * terminal or unknown state stops the timer instead of hammering the API.
 */
export function goalPlanGenerationOptions(
  wsId: string,
  goalId: string,
  generationId: string | null,
) {
  return queryOptions({
    queryKey: goalKeys.planGeneration(wsId, goalId, generationId ?? ""),
    queryFn: () => api.getGoalPlanGeneration(goalId, generationId ?? ""),
    enabled: !!wsId && !!goalId && !!generationId,
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      return state && isGoalPlanGenerationPending(state) ? GOAL_PLAN_GENERATION_POLL_MS : false;
    },
  });
}

/**
 * The goal's closure decision: whether the server considers the goal closable,
 * why not, what independent evidence exists and what authorization applies.
 * The client never recomputes this from issue statuses or run outcomes, so a
 * refetch is the only way the panel's verdict changes.
 */
export function goalClosureOptions(wsId: string, goalId: string) {
  return queryOptions({
    queryKey: goalKeys.closure(wsId, goalId),
    queryFn: () => api.getGoalClosure(goalId),
    enabled: !!wsId && !!goalId,
  });
}