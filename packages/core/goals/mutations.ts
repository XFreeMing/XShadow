import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { goalKeys } from "./queries";
import type {
  CreateClosureAuthorizationRequest,
  CreateClosureReviewRequest,
  CreateGoalPlanGenerationRequest,
  CreateGoalRequest,
  Goal,
  GoalPlan,
  GoalPlanGeneration,
  MaterializeGoalPlanRequest,
  MaterializeGoalPlanResponse,
  SaveGoalPlanRequest,
  UpdateGoalRequest,
} from "../types";

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

/**
 * A save is only success when the server returns the goal's own plan with the
 * new revision. The schema degrades a malformed body to an empty draft, so
 * accepting that would reset the editor's revision to 0 and invite a bogus
 * next write.
 */
function requireConfirmedPlan(plan: GoalPlan, goalId: string): GoalPlan {
  if (!plan.goal_id || plan.goal_id !== goalId || (plan.id === null && plan.revision === 0)) {
    throw new Error("The server did not confirm the saved plan.");
  }
  return plan;
}

/**
 * Saves the full edited draft. A `generation_id` marks that candidate applied
 * in the same transaction, so the generation can never be applied twice.
 */
export function useSaveGoalPlan() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async ({
      goalId,
      data,
    }: {
      goalId: string;
      data: SaveGoalPlanRequest;
    }) => requireConfirmedPlan(await api.saveGoalPlan(goalId, data), goalId),
    onSuccess: (saved, { goalId, data }) => {
      // The PUT reply is authoritative; write it straight into the cache
      // instead of an immediate refetch, which could race another writer.
      qc.setQueryData(goalKeys.plan(wsId, goalId), saved);
      if (data.generation_id) {
        qc.invalidateQueries({ queryKey: goalKeys.planGenerationAll(wsId, goalId) });
      }
    },
  });
}

/**
 * Starts one planning run. A malformed reply must not be treated as a started
 * run, so a generation without its own id fails the mutation.
 */
export function useCreateGoalPlanGeneration() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async ({
      goalId,
      data,
    }: {
      goalId: string;
      data: CreateGoalPlanGenerationRequest;
    }): Promise<GoalPlanGeneration> => {
      const generation = await api.createGoalPlanGeneration(goalId, data);
      if (!generation.id || generation.goal_id !== goalId) {
        throw new Error("The server did not confirm the planning run.");
      }
      return generation;
    },
    onSuccess: (generation, { goalId }) => {
      qc.setQueryData(
        goalKeys.planGeneration(wsId, goalId, generation.id),
        generation,
      );
    },
  });
}

/**
 * Records a human's scoped closure authorization. Only a human may grant it,
 * and the server echoes the stored grant — a reply without an id did not
 * record anything, so the caller must not show it as granted.
 */
export function useGrantGoalClosureAuthorization() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async ({
      goalId,
      data,
    }: {
      goalId: string;
      data: CreateClosureAuthorizationRequest;
    }) => {
      const grant = await api.grantGoalClosureAuthorization(goalId, data);
      if (!grant.id) throw new Error("The server did not confirm the authorization.");
      return grant;
    },
    onSettled: (_grant, _error, { goalId }) => {
      void qc.invalidateQueries({ queryKey: goalKeys.closure(wsId, goalId) });
    },
  });
}

/**
 * Records one review verdict. Used by the goal acceptance panel for the
 * human's explicit "reject and send back" decision (a needs_changes verdict);
 * a review is evidence, so the server's stored copy is authoritative.
 */
export function useCreateGoalClosureReview() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({
      goalId,
      data,
    }: {
      goalId: string;
      data: CreateClosureReviewRequest;
    }) => api.createGoalClosureReview(goalId, data),
    onSettled: (_review, _error, { goalId }) => {
      void qc.invalidateQueries({ queryKey: goalKeys.closure(wsId, goalId) });
      // A new verdict can change whether the goal may be accepted, and the
      // goal detail's own status is unchanged by recording evidence.
      void qc.invalidateQueries({ queryKey: goalKeys.detail(wsId, goalId) });
    },
  });
}

/**
 * Materializes saved-plan items into tasks. The server is idempotent per plan
 * item, so a retried click returns the same bindings with `created=false`.
 * Success invalidates the linked-task list and the closure decision: new
 * enrolled issues and newly enqueued work change both.
 */
export function useMaterializeGoalPlan() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (args: {
      goalId: string;
      data: MaterializeGoalPlanRequest;
    }): Promise<MaterializeGoalPlanResponse> =>
      api.materializeGoalPlan(args.goalId, args.data),
    onSuccess: (result, { goalId }) => {
      if (result.goal_id && result.goal_id !== goalId) return;
      void qc.invalidateQueries({ queryKey: goalKeys.issues(wsId, goalId) });
      void qc.invalidateQueries({ queryKey: goalKeys.closure(wsId, goalId) });
    },
  });
}