import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { goalKeys } from "./queries";
import type { CreateGoalRequest, Goal, UpdateGoalRequest } from "../types";

/**
 * Goal writes wait for the server before the UI treats them as done. There is
 * no optimistic patch here on purpose: linking is constraint-checked (409),
 * deleting a goal also removes its links, and a rollback of those projections
 * would be guesswork.
 */
/**
 * A write is only a success when the server confirms a real goal. The schema
 * degrades a malformed body to an empty goal, so a missing or invalid id must
 * fail the mutation rather than let the caller toast "created" or navigate.
 */
function requireConfirmedGoal(goal: Goal): Goal {
  if (!goal.id) throw new Error("The server did not confirm the goal.");
  return goal;
}

export function useCreateGoal() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async (data: CreateGoalRequest) => requireConfirmedGoal(await api.createGoal(data)),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: goalKeys.listAll(wsId) });
    },
  });
}

export function useUpdateGoal() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async ({ id, ...data }: { id: string } & UpdateGoalRequest) =>
      requireConfirmedGoal(await api.updateGoal(id, data)),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: goalKeys.all(wsId) });
    },
  });
}

export function useDeleteGoal() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteGoal(id),
    onSuccess: (_data, id) => {
      // A read started before DELETE may still resolve; do not let it restore
      // the removed goal or its now-gone links.
      void qc.cancelQueries({ queryKey: goalKeys.detail(wsId, id) });
      void qc.cancelQueries({ queryKey: goalKeys.issues(wsId, id) });
      qc.removeQueries({ queryKey: goalKeys.detail(wsId, id) });
      qc.removeQueries({ queryKey: goalKeys.issues(wsId, id) });
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: goalKeys.listAll(wsId) });
    },
  });
}

export function useLinkGoalIssue() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ goalId, issueId }: { goalId: string; issueId: string }) =>
      api.linkGoalIssue(goalId, issueId),
    onSettled: (_data, _error, { goalId }) => {
      qc.invalidateQueries({ queryKey: goalKeys.issues(wsId, goalId) });
    },
  });
}

export function useUnlinkGoalIssue() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ goalId, issueId }: { goalId: string; issueId: string }) =>
      api.unlinkGoalIssue(goalId, issueId),
    onSettled: (_data, _error, { goalId }) => {
      qc.invalidateQueries({ queryKey: goalKeys.issues(wsId, goalId) });
    },
  });
}