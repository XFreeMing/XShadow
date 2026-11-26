import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { goalKeys } from "./queries";
import { chatKeys, sortChatSessions } from "../chat/queries";
import type {
  ChatSession,
  CreateGoalPlanGenerationRequest,
  Goal,
  GoalPlan,
  GoalPlanGeneration,
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

export function useOpenGoalConversation() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async () => {
      const session = await api.openGoalConversation({ workspaceId: wsId });
      if (!session.id || session.workspace_id !== wsId) {
        throw new Error("The server did not confirm the goal conversation.");
      }
      return session;
    },
    onSuccess: (session) => {
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), (old) => {
        if (!old) return old;
        const existing = old.find((chat) => chat.id === session.id);
        const current = existing ? { ...existing, status: session.status, is_goal_conversation: session.is_goal_conversation } : session;
        return sortChatSessions([...old.filter((chat) => chat.id !== session.id), current]);
      });
      void qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
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

export function useChangeGoalProblemSolving() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async (input: Parameters<typeof api.changeGoalProblemSolving>[0]) => {
      const result = await api.changeGoalProblemSolving(input);
      if (!result || result.goal_id !== input.id || result.revision <= input.revision) {
        throw new Error("The server did not confirm the goal change.");
      }
      return result;
    },
    onSuccess: (result) => {
      qc.setQueryData(goalKeys.problemSolving(wsId, result.goal_id), result);
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: goalKeys.all(wsId) });
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

export function useSetGoalExecution() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async ({ goalId, enabled, revision }: {
      goalId: string;
      enabled: boolean;
      revision: number;
    }) => {
      const plan = requireConfirmedPlan(await api.setGoalExecution(goalId, {
        enabled, expected_revision: revision,
      }), goalId);
      if (plan.revision !== revision || (enabled ? plan.execution_state !== "active" : !!plan.execution_state)) {
        throw new Error("The server did not confirm the execution change.");
      }
      return plan;
    },
    onSettled: (_data, _error, { goalId }) => qc.invalidateQueries({
      queryKey: goalKeys.plan(wsId, goalId),
      exact: true,
    }),
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
