"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { Goal, GoalPlanGeneration } from "@multica/core/types";
import { ApiError, errorCode } from "@multica/core/api";
import {
  goalKeys,
  goalPlanGenerationOptions,
  isGoalPlanGenerationPending,
  isGoalPlanGenerationTerminal,
  useCreateGoalPlanGeneration,
} from "@multica/core/goals";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions } from "@multica/core/workspace/queries";
import { CircleAlert, Clock, RefreshCw, Sparkles } from "@multica/ui/icons";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";
import { isGoalUnavailableError } from "../status";
import {
  isCandidateUsable,
  isGenerationProjectConflict,
  isProjectConflict,
  planConflictRevision,
  readActualBasis,
  readInputContext,
} from "../plan";
import { PlanBasis } from "./goal-plan-basis";

/** What applying a candidate did to the saved plan. */
export type ApplyCandidateOutcome = "applied" | "conflict" | "failed";

function newRequestKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;
}

/**
 * Starts and reviews one planning run. A run is the server's: the panel only
 * displays its state and never assumes content before the server reports a
 * candidate. A failed start retries with the same `request_key`; only an
 * explicit new run uses a new one.
 */
export function GoalPlanGenerationPanel({
  goal,
  planRevision,
  dirty,
  generationNonce,
  applyBlocked,
  onApplyCandidate,
  onProjectConflict,
  onRevisionConflict,
  onUnavailable,
  onReloadPlan,
  onStartNewGeneration,
}: {
  goal: Goal;
  planRevision: number;
  dirty: boolean;
  generationNonce: number;
  applyBlocked: boolean;
  onApplyCandidate: (generation: GoalPlanGeneration) => Promise<ApplyCandidateOutcome>;
  onProjectConflict: () => void;
  onRevisionConflict: (revision: number | null) => void;
  onUnavailable: () => void;
  onReloadPlan: () => Promise<boolean>;
  onStartNewGeneration: () => Promise<void>;
}) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const createGeneration = useCreateGoalPlanGeneration();

  const [intent, setIntent] = useState(() => goal.intent.trim() || goal.title);
  const [agentId, setAgentId] = useState("");
  const [generationId, setGenerationId] = useState<string | null>(null);
  const [projectConflict, setProjectConflict] = useState(false);
  const [runUnavailable, setRunUnavailable] = useState(false);
  const [startError, setStartError] = useState<string | null>(null);
  // A definite refusal created no run. The request key/body belong to that
  // refused attempt, so recovery is an explicit new run once the reader has
  // resolved whatever blocked it (a plan revision choice, an agent change).
  const [startRefused, setStartRefused] = useState(false);
  // A start whose commit outcome is unknown still owns the recorded key/body.
  // The ordinary start action must replay it before any explicit new run,
  // even when an older terminal run is still on screen.
  const [startUnknown, setStartUnknown] = useState(false);
  const [reloadError, setReloadError] = useState<string | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const requestKeyRef = useRef<string | null>(null);
  // The body that belongs to the stored request_key. A retry must replay the
  // original request, not whatever the form happens to say later: the server
  // resolves a key to the run it may already have created.
  const requestBodyRef = useRef<{
    intent: string;
    agentId: string;
    expectedRevision: number;
  } | null>(null);
  const handledNonceRef = useRef(0);

  const activeAgents = useMemo(
    () => agents.filter((agent) => !agent.archived_at),
    [agents],
  );
  const activeAgentIds = activeAgents.map((agent) => agent.id).join(",");

  useEffect(() => {
    if (agentId && activeAgents.some((agent) => agent.id === agentId)) return;
    setAgentId(activeAgents[0]?.id ?? "");
  }, [agentId, activeAgentIds, activeAgents]);

  const generationQuery = useQuery(
    goalPlanGenerationOptions(wsId, goal.id, generationId),
  );
  // A terminal read refusal means this viewer can no longer read the run. A
  // cached candidate and its basis must not stay on screen just because the
  // Query cache still holds them.
  const generationUnavailable =
    generationQuery.isError && isGoalUnavailableError(generationQuery.error);
  const generation =
    generationId && !generationUnavailable && generationQuery.data?.id
      ? generationQuery.data
      : null;
  const pending = generation ? isGoalPlanGenerationPending(generation.state) : false;
  const terminal = generation ? isGoalPlanGenerationTerminal(generation.state) : false;
  const starting = createGeneration.isPending;

  useEffect(() => {
    if (!generationQuery.isError) return;
    if (!isGoalUnavailableError(generationQuery.error)) return;
    if (!generationId) return;
    queryClient.removeQueries({
      queryKey: goalKeys.planGeneration(wsId, goal.id, generationId),
    });
    setGenerationId(null);
    setConfirmOpen(false);
    setRunUnavailable(true);
    // Re-read the goal and plan; if the goal is truly gone the section stops
    // rendering instead of leaving a cached editor behind.
    onUnavailable();
  }, [
    generationQuery.isError,
    generationQuery.error,
    generationId,
    queryClient,
    wsId,
    goal.id,
    onUnavailable,
  ]);

  // A run started in another project is not a late candidate. Drop its cached
  // payload, stop polling, and let the user reload before starting anything.
  useEffect(() => {
    if (!generationQuery.isError) return;
    if (!isGenerationProjectConflict(generationQuery.error)) return;
    if (!generationId && projectConflict) return;
    if (generationId) {
      queryClient.removeQueries({
        queryKey: goalKeys.planGeneration(wsId, goal.id, generationId),
      });
      setGenerationId(null);
      setConfirmOpen(false);
    }
    requestKeyRef.current = null;
    setProjectConflict(true);
    onProjectConflict();
  }, [
    generationQuery.isError,
    generationQuery.error,
    generationId,
    projectConflict,
    queryClient,
    wsId,
    goal.id,
    onProjectConflict,
  ]);

  // Any cached run belongs to the project it was started in. When the goal
  // moves, never keep showing or polling it. A project-conflict notice is kept
  // until the user reloads or starts a new run: dropping the run must not also
  // erase the explanation of why nothing was applied.
  const projectId = goal.project_id;
  useEffect(() => {
    if (generationId) {
      queryClient.removeQueries({
        queryKey: goalKeys.planGeneration(wsId, goal.id, generationId),
      });
      setGenerationId(null);
      setProjectConflict(false);
      setConfirmOpen(false);
    }
    setStartError(null);
    setRunUnavailable(false);
    setStartRefused(false);
    setStartUnknown(false);
    setReloadError(null);
    requestKeyRef.current = null;
    requestBodyRef.current = null;
    // `generationId` is read once per project change; re-running on every
    // state tick would erase a run the user just started.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);

  const startErrorMessage = (err: unknown): string => {
    const code = errorCode(err);
    if (code === "generation_request_conflict") return t(($) => $.plan.request_conflict);
    if (code === "goal_project_required") return t(($) => $.plan.project_required);
    // The run's commit outcome is unknown; retrying the same request_key is
    // how the server resolves it, so the retry must not rotate the key.
    if (code === "goal_plan_generation_retryable") {
      return t(($) => $.plan.generation_retryable);
    }
    if (err instanceof ApiError && err.status === 403) {
      // The auth refusal this endpoint returns is "no access to this agent";
      // it says nothing about the goal or its project.
      return t(($) => $.plan.no_permission);
    }
    if (err instanceof ApiError && err.status === 404) {
      // Either the goal/project is no longer visible or the agent is gone.
      // Revalidate; do not blame the agent for a goal this viewer lost.
      return t(($) => $.plan.start_unavailable);
    }
    return t(($) => $.plan.start_failed);
  };

  const startGeneration = async (freshKey = false) => {
    const trimmed = intent.trim();
    if (!trimmed || !agentId || starting) return;
    // A refused request may only recover after the reader has resolved the
    // plan conflict it hit; starting earlier would be refused again.
    if (startRefused && applyBlocked) return;
    if (freshKey) {
      requestKeyRef.current = null;
      requestBodyRef.current = null;
      setProjectConflict(false);
      setRunUnavailable(false);
      setStartRefused(false);
      setStartUnknown(false);
      setReloadError(null);
    }
    if (!requestKeyRef.current) {
      requestKeyRef.current = newRequestKey();
      requestBodyRef.current = {
        intent: trimmed,
        agentId,
        expectedRevision: planRevision,
      };
    }
    const requestKey = requestKeyRef.current;
    // Replay the body recorded with this key. A retry after a lost response
    // must resolve to the same server run even if the form changed since.
    const body = requestBodyRef.current ?? {
      intent: trimmed,
      agentId,
      expectedRevision: planRevision,
    };
    setStartError(null);
    try {
      const started = await createGeneration.mutateAsync({
        goalId: goal.id,
        data: {
          intent: body.intent,
          request_key: requestKey,
          expected_revision: body.expectedRevision,
          agent_id: body.agentId,
        },
      });
      setStartRefused(false);
      setStartUnknown(false);
      setGenerationId(started.id);
      // Read the authoritative state right away instead of waiting for the
      // first poll tick: the creation response is only the start signal.
      void queryClient.invalidateQueries({
        queryKey: goalKeys.planGeneration(wsId, goal.id, started.id),
      });
    } catch (err) {
      // The goal crossed a project boundary between the request's read and its
      // protected transaction, or this request_key resolves to a run of the
      // project the goal has left. Nothing was created or applied: drop the old
      // run (candidate and basis included), forget its key, and re-read the
      // goal and plan. The local intent stays, and only an explicit new run
      // gets a fresh request_key.
      if (isProjectConflict(err)) {
        if (generationId) {
          queryClient.removeQueries({
            queryKey: goalKeys.planGeneration(wsId, goal.id, generationId),
          });
          setGenerationId(null);
        }
        setConfirmOpen(false);
        setStartError(null);
        setStartRefused(false);
        setStartUnknown(false);
        requestKeyRef.current = null;
        requestBodyRef.current = null;
        setProjectConflict(true);
        onProjectConflict();
        return;
      }
      // A revision conflict is ordinary contention on the saved plan, not a
      // project move: keep the request key and let the section offer reload or
      // overwrite of the local edits.
      const revision = planConflictRevision(err);
      if (revision !== null) {
        setStartRefused(true);
        setStartUnknown(false);
        setStartError(t(($) => $.plan.start_revision_conflict));
        onRevisionConflict(revision);
        return;
      }
      if (err instanceof ApiError && err.status === 404) onUnavailable();
      // A 4xx is a definite refusal: no run exists, so the recorded key/body
      // cannot resolve to one. Recovery is an explicit new request after the
      // reader resolves the cause. A network failure or a 5xx leaves the
      // outcome unknown and keeps the recorded key/body for a same-request
      // retry.
      const refusedByServer = err instanceof ApiError && err.status >= 400 && err.status < 500;
      setStartRefused(refusedByServer);
      setStartUnknown(!refusedByServer);
      setStartError(startErrorMessage(err));
    }
  };

  // The section discards the old-project draft first, then bumps the nonce so
  // this run starts only after the new revision is in hand.
  useEffect(() => {
    if (generationNonce === 0) return;
    if (handledNonceRef.current === generationNonce) return;
    handledNonceRef.current = generationNonce;
    setGenerationId(null);
    setProjectConflict(false);
    setStartError(null);
    setStartRefused(false);
    setStartUnknown(false);
    requestKeyRef.current = null;
    void startGeneration(true);
    // Only the nonce decides this; the call reads the current render's props.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [generationNonce]);

  const handleApply = async () => {
    if (!generation || applyBlocked) return;
    const outcome = await onApplyCandidate(generation);
    if (outcome === "conflict") {
      queryClient.removeQueries({
        queryKey: goalKeys.planGeneration(wsId, goal.id, generation.id),
      });
      setGenerationId(null);
      setProjectConflict(true);
      requestKeyRef.current = null;
      requestBodyRef.current = null;
    }
  };

  const handleReloadPlan = async () => {
    // Only a confirmed read of the current project's plan may release the
    // project-conflict gate. A failed reload keeps the notice, the hidden
    // candidate and the recorded request untouched.
    const reloaded = await onReloadPlan();
    if (!reloaded) {
      setReloadError(t(($) => $.plan.load_failed));
      return;
    }
    setReloadError(null);
    setProjectConflict(false);
    setGenerationId(null);
    setStartError(null);
    setStartRefused(false);
    setStartUnknown(false);
    setRunUnavailable(false);
    requestKeyRef.current = null;
    requestBodyRef.current = null;
  };

  const handleNewGeneration = async () => {
    await onStartNewGeneration();
  };

  if (projectConflict) {
    return (
      <div
        className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-3"
        role="alert"
        data-testid="plan-project-conflict"
      >
        <p className="flex items-center gap-1.5 text-body font-medium">
          <CircleAlert className="size-4 text-destructive" />
          {t(($) => $.plan.project_conflict_title)}
        </p>
        <p className="text-caption text-muted-foreground">
          {t(($) => $.plan.project_conflict_description)}
        </p>
        {reloadError ? (
          <p className="text-caption text-destructive" role="alert">
            {reloadError}
          </p>
        ) : null}
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onClick={() => void handleReloadPlan()}>
            {t(($) => $.plan.project_conflict_reload)}
          </Button>
          <Button size="sm" onClick={() => void handleNewGeneration()}>
            <Sparkles className="size-3.5" />
            {t(($) => $.plan.project_conflict_generate)}
          </Button>
        </div>
      </div>
    );
  }

  const usable = generation ? isCandidateUsable(generation.candidate) : false;
  const inputBasis = generation ? readInputContext(generation.input_context) : null;
  // An unconfirmed start owns the recorded request until the server resolves
  // it. Only an explicit new run may replace it, regardless of whether an
  // older terminal run is still displayed above.
  const explicitNewRun = (generation !== null || startRefused) && !startUnknown;
  const stateLabel = generation
    ? generation.state === "preparing"
      ? t(($) => $.plan.state_preparing)
      : generation.state === "queued"
        ? t(($) => $.plan.state_queued)
        : generation.state === "running"
          ? t(($) => $.plan.state_running)
          : generation.state === "succeeded"
            ? t(($) => $.plan.state_succeeded)
            : generation.state === "failed"
              ? t(($) => $.plan.state_failed)
              : t(($) => $.plan.state_unknown, { state: generation.state })
    : null;

  return (
    <div className="space-y-3" data-testid="plan-generation-panel">
      <div className="space-y-2">
        <div className="space-y-1">
          <Label htmlFor="plan-generation-intent">{t(($) => $.plan.intent_label)}</Label>
          <Textarea
            id="plan-generation-intent"
            value={intent}
            placeholder={t(($) => $.plan.intent_placeholder)}
            className="min-h-16"
            disabled={pending || starting}
            onChange={(event) => setIntent(event.target.value)}
          />
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <div className="space-y-1">
            <Label>{t(($) => $.plan.agent_label)}</Label>
            <Select
              items={activeAgents.map((agent) => ({ value: agent.id, label: agent.name }))}
              value={agentId}
              disabled={pending || starting || activeAgents.length === 0}
              onValueChange={(next) => setAgentId(next ?? "")}
            >
              <SelectTrigger size="sm" className="w-52">
                <SelectValue placeholder={t(($) => $.plan.agent_placeholder)}>
                  {activeAgents.find((agent) => agent.id === agentId)?.name}
                </SelectValue>
              </SelectTrigger>
              <SelectContent align="start">
                {activeAgents.map((agent) => (
                  <SelectItem key={agent.id} value={agent.id}>
                    {agent.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button
            size="sm"
            onClick={() => void startGeneration(explicitNewRun)}
            disabled={
              !intent.trim() ||
              !agentId ||
              pending ||
              starting ||
              (startRefused && applyBlocked)
            }
          >
            {starting ? <Spinner className="size-3.5" /> : <Sparkles className="size-3.5" />}
            {starting
              ? t(($) => $.plan.starting)
              : startUnknown
                ? t(($) => $.plan.retry)
                : generation
                  ? t(($) => $.plan.generate_again)
                  : t(($) => $.plan.generate)}
          </Button>
        </div>
        {activeAgents.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.plan.no_agents)}</p>
        ) : null}
        {dirty ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.plan.unsaved_hint)}</p>
        ) : null}
        {startError ? (
          <p className="text-caption text-destructive" role="alert">
            {startError}
          </p>
        ) : null}
        {startRefused ? (
          <div className="space-y-1" data-testid="plan-start-refused">
            <p className="text-caption text-muted-foreground">
              {t(($) => $.plan.start_refused)}
            </p>
            <Button
              size="sm"
              variant="outline"
              onClick={() => void startGeneration(true)}
              disabled={
                !intent.trim() ||
                !agentId ||
                pending ||
                starting ||
                applyBlocked
              }
            >
              {t(($) => $.plan.project_conflict_generate)}
            </Button>
          </div>
        ) : null}
      </div>

      {runUnavailable || generationUnavailable ? (
        <p className="text-caption text-destructive" role="alert" data-testid="plan-run-unavailable">
          {t(($) => $.plan.generation_unavailable)}
        </p>
      ) : generationQuery.isError && !isGenerationProjectConflict(generationQuery.error) ? (
        <div className="flex items-center gap-2 text-caption text-destructive" role="alert">
          <span>{t(($) => $.plan.load_failed)}</span>
          <Button size="sm" variant="outline" onClick={() => void generationQuery.refetch()}>
            <RefreshCw className="size-3.5" />
            {t(($) => $.plan.retry)}
          </Button>
        </div>
      ) : null}

      {generation ? (
        <div className="space-y-2 rounded-md border px-3 py-3" data-testid="plan-generation-status">
          <div className="flex flex-wrap items-center gap-2">
            {pending ? (
              <Spinner className="size-3.5" />
            ) : generation.state === "failed" ? (
              <CircleAlert className="size-3.5 text-destructive" />
            ) : generation.state === "succeeded" ? (
              <Sparkles className="size-3.5" />
            ) : (
              <Clock className="size-3.5 text-muted-foreground" />
            )}
            <span className="text-body font-medium">{stateLabel}</span>
            {generation.stale ? (
              <Badge variant="outline">{t(($) => $.plan.stale_badge)}</Badge>
            ) : null}
            {generation.applied ? (
              <Badge variant="secondary">
                {generation.applied_revision !== null
                  ? t(($) => $.plan.applied, { revision: generation.applied_revision })
                  : t(($) => $.plan.applied_unknown)}
              </Badge>
            ) : null}
          </div>

          {pending ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.plan.run_pending)}</p>
          ) : null}

          {generation.state === "failed" ? (
            <div className="space-y-1">
              <p className="text-caption text-destructive">
                {generation.error || t(($) => $.plan.run_failed)}
              </p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => void startGeneration(!startUnknown)}
                disabled={starting}
              >
                {t(($) => $.plan.generate_again)}
              </Button>
            </div>
          ) : null}

          {generation.state === "succeeded" && !generation.candidate ? (
            <div className="space-y-1">
              <p className="text-caption text-destructive">
                {t(($) => $.plan.run_without_plan)}
              </p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => void startGeneration(!startUnknown)}
                disabled={starting}
              >
                {t(($) => $.plan.generate_again)}
              </Button>
            </div>
          ) : null}

          {generation && !pending && !terminal ? (
            <div className="space-y-1">
              <p className="text-caption text-muted-foreground">
                {t(($) => $.plan.unknown_state_hint)}
              </p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => void startGeneration(!startUnknown)}
                disabled={starting}
              >
                {t(($) => $.plan.generate_again)}
              </Button>
            </div>
          ) : null}

          {inputBasis ? (
            <PlanBasis input={inputBasis} />
          ) : null}

          {generation.state === "succeeded" && generation.candidate ? (
            <div className="space-y-2" data-testid="plan-candidate">
              <p className="text-body font-medium">{t(($) => $.plan.candidate_title)}</p>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.plan.candidate_summary, {
                  items: generation.candidate.items.length,
                  edges: generation.candidate.edges.length,
                })}
              </p>
              {generation.stale ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.plan.stale_description, {
                    base: generation.base_revision,
                    current: generation.current_revision,
                  })}
                </p>
              ) : null}
              <PlanBasis actual={readActualBasis(generation.candidate.context_snapshot)} />
              {!usable ? (
                <p className="text-caption text-destructive">
                  {t(($) => $.plan.candidate_incomplete)}
                </p>
              ) : null}
              {generation.applied ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.plan.candidate_applied_hint)}
                </p>
              ) : null}
              {!generation.applied ? (
                <Button
                  size="sm"
                  onClick={() => setConfirmOpen(true)}
                  disabled={!usable || starting || applyBlocked}
                >
                  {t(($) => $.plan.apply_action)}
                </Button>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}

      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.plan.apply_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.plan.apply_description)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.plan.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setConfirmOpen(false);
                void handleApply();
              }}
            >
              {t(($) => $.plan.apply_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}