import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { ListGoalsParams } from "../types";

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
};

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