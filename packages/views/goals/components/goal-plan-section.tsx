"use client";

import { useCallback, useEffect, useRef, useState, type FocusEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Goal, GoalPlanEdge, GoalPlanGeneration } from "@multica/core/types";
import { goalKeys, goalPlanOptions, useSaveGoalPlan } from "@multica/core/goals";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentListOptions,
  memberListOptions,
  squadListOptions,
} from "@multica/core/workspace/queries";
import { ApiError, errorCode } from "@multica/core/api";
import { CircleAlert } from "@multica/ui/icons";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { useT } from "../../i18n";
import { isGoalUnavailableError } from "../status";
import {
  draftFromPlan,
  isGenerationProjectConflict,
  mergeCandidate,
  planConflictRevision,
  planEdgeKey,
  validatePlanDraft,
  type PlanDraft,
  type PlanDraftContent,
  type PlanValidationIssue,
} from "../plan";
import { GoalPlanEditor, type PlanContent } from "./goal-plan-editor";
import {
  GoalPlanGenerationPanel,
  type ApplyCandidateOutcome,
} from "./goal-plan-generate-panel";
import { PlanActualBasis } from "./goal-plan-basis";

type SaveOutcome = "saved" | "revision_conflict" | "generation_project_conflict" | "failed";

/**
 * Deletions that a candidate still has to respect. A candidate generated from
 * revision N contains the nodes the user had at N; a deletion recorded at N or
 * later happened while that run was in flight or after it, so it wins. A
 * deletion made before the run started is already absent from its basis.
 */
function deletionsForRun(
  removed: ReadonlyMap<string, number>,
  baseRevision: number,
): ReadonlySet<string> {
  const kept = new Set<string>();
  for (const [id, revision] of removed) {
    if (revision >= baseRevision) kept.add(id);
  }
  return kept;
}

function saveFailureMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.status < 500 && error.message) {
    return error.message;
  }
  return fallback;
}

/**
 * The goal's plan: server draft + local edits + planning runs. A plan is
 * workspace data, so this section is only rendered for a goal the viewer can
 * still see; losing that access hides the whole area, including cached content.
 */
export function GoalPlanSection({ goal }: { goal: Goal }) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();

  const planQuery = useQuery(goalPlanOptions(wsId, goal.id));
  const plan = planQuery.data && planQuery.data.goal_id === goal.id ? planQuery.data : null;
  const savePlan = useSaveGoalPlan();

  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: squads = [] } = useQuery(squadListOptions(wsId));

  const [draft, setDraft] = useState<PlanDraft | null>(null);
  // Which project the local draft was read from and edited against. A goal can
  // move projects; its old draft must never be submitted as the new project's
  // plan, even when both plans happen to be at revision 0.
  const [draftProjectId, setDraftProjectId] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [editedItemIds, setEditedItemIds] = useState<ReadonlySet<string>>(new Set());
  // Each deletion records the revision it was made against. A candidate that
  // started before the deletion still has to respect it, even after the
  // deletion was saved into a newer revision.
  const [removedItemIds, setRemovedItemIds] = useState<ReadonlyMap<string, number>>(
    new Map(),
  );
  const [removedEdgeKeys, setRemovedEdgeKeys] = useState<ReadonlyMap<string, number>>(
    new Map(),
  );
  const [conflictRevision, setConflictRevision] = useState<number | null>(null);
  const [pendingGenerationId, setPendingGenerationId] = useState<string | null>(null);
  const [projectConflictNotice, setProjectConflictNotice] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [validationIssue, setValidationIssue] = useState<PlanValidationIssue | null>(null);
  const [generationNonce, setGenerationNonce] = useState(0);
  const sectionRef = useRef<HTMLElement | null>(null);
  const focusedInsideRef = useRef<HTMLElement | null>(null);
  const conflictReloadRef = useRef<HTMLButtonElement | null>(null);
  const planRetryRef = useRef<HTMLButtonElement | null>(null);

  // Remember where focus is inside the plan area, but forget it as soon as the
  // reader moves focus to a control outside. The memory must describe the
  // element in use when content is removed, not merely the last one focused
  // here at some earlier point.
  const handleFocusIn = (event: FocusEvent<HTMLElement>) => {
    focusedInsideRef.current = event.target as HTMLElement;
  };

  const handleFocusOut = (event: FocusEvent<HTMLElement>) => {
    const next = event.relatedTarget as Node | null;
    if (next && sectionRef.current?.contains(next)) return;
    // A removal can dispatch focusout for an element that is already gone;
    // that element was still in use, so keep the record for the recovery
    // effect below. Moving to an outside control clears it.
    const target = event.target as HTMLElement;
    if (target.isConnected) focusedInsideRef.current = null;
  };

  // Server revisions can move underneath a local draft (another tab, realtime
  // refresh). Never silently replace unsaved work: keep the draft and show the
  // comparison banner instead.
  useEffect(() => {
    if (!plan) return;
    if (!draft) {
      setDraft(draftFromPlan(plan));
      setDraftProjectId(plan.project_id);
      return;
    }
    // A different project's plan is a different plan, even at the same
    // revision: hold the old draft and require an explicit reload.
    if (draftProjectId !== plan.project_id) {
      setProjectConflictNotice(true);
      setConflictRevision(null);
      setPendingGenerationId(null);
      return;
    }
    if (plan.revision === draft.revision) return;
    if (projectConflictNotice) return;
    if (!dirty) {
      setDraft(draftFromPlan(plan));
      setConflictRevision(null);
      return;
    }
    setConflictRevision(plan.revision);
  }, [plan, draft, draftProjectId, dirty, projectConflictNotice]);

  // The goal prop can move to another project before the plan refetch lands.
  // Isolate the old draft immediately rather than waiting for the new plan,
  // which may still be cached from the previous project.
  useEffect(() => {
    if (!draft) return;
    if (draftProjectId === goal.project_id) return;
    setProjectConflictNotice(true);
    setConflictRevision(null);
    setPendingGenerationId(null);
  }, [goal.project_id, draft, draftProjectId]);

  const planRevision = plan?.revision ?? 0;
  const draftIsCurrentProject = draft === null || draftProjectId === goal.project_id;

  const handleContentChange = (next: PlanContent) => {
    setDraft((current) =>
      current ? { ...current, items: next.items, edges: next.edges } : current,
    );
    setDirty(true);
    setValidationIssue(null);
    setSaveError(null);
  };

  const handleItemEdited = (itemId: string) => {
    setEditedItemIds((current) => {
      if (current.has(itemId)) return current;
      const next = new Set(current);
      next.add(itemId);
      return next;
    });
  };

  // Deletions are user decisions a late candidate must not undo. The revision
  // they were made against scopes them to runs that started at or before it.
  const handleItemRemoved = (itemId: string) => {
    setRemovedItemIds((current) => {
      if (current.has(itemId)) return current;
      const next = new Map(current);
      next.set(itemId, planRevision);
      return next;
    });
  };

  const handleEdgeRemoved = (edge: GoalPlanEdge) => {
    const key = planEdgeKey(edge);
    setRemovedEdgeKeys((current) => {
      if (current.has(key)) return current;
      const next = new Map(current);
      next.set(key, planRevision);
      return next;
    });
  };

  const persist = async (
    content: PlanDraftContent,
    expectedRevision: number,
    generationId: string | null,
  ): Promise<SaveOutcome> => {
    try {
      const saved = await savePlan.mutateAsync({
        goalId: goal.id,
        data: {
          expected_revision: expectedRevision,
          items: content.items,
          edges: content.edges,
          context_snapshot: content.context_snapshot,
          generation_id: generationId,
        },
      });
      setDraft(draftFromPlan(saved));
      setDraftProjectId(saved.project_id);
      setDirty(false);
      setEditedItemIds(new Set());
      setConflictRevision(null);
      setPendingGenerationId(null);
      setSaveError(null);
      setValidationIssue(null);
      setProjectConflictNotice(false);
      toast.success(t(($) => $.plan.saved));
      return "saved";
    } catch (err) {
      if (isGenerationProjectConflict(err)) {
        setPendingGenerationId(null);
        setProjectConflictNotice(true);
        setConflictRevision(null);
        void queryClient.invalidateQueries({ queryKey: goalKeys.detail(wsId, goal.id) });
        void planQuery.refetch();
        return "generation_project_conflict";
      }
      const revision = planConflictRevision(err);
      if (revision !== null) {
        setConflictRevision(revision);
        setSaveError(null);
        void planQuery.refetch();
        return "revision_conflict";
      }
      // Server-side refusals of an apply are distinct outcomes the user can
      // act on; the raw English code message would not read as UI copy.
      const code = errorCode(err);
      if (code === "generation_not_ready" || code === "generation_already_applied") {
        setPendingGenerationId(null);
        setSaveError(
          t(($) =>
            code === "generation_not_ready"
              ? $.plan.generation_not_ready
              : $.plan.generation_already_applied,
          ),
        );
        return "failed";
      }
      // A refused write can mean this viewer lost the goal or its project.
      // Re-read both before showing the raw failure; the section hides itself
      // if the plan read is terminated, instead of keeping a cached editor.
      if (err instanceof ApiError && (err.status === 403 || err.status === 404)) {
        void queryClient.invalidateQueries({ queryKey: goalKeys.detail(wsId, goal.id) });
        void planQuery.refetch();
      }
      setSaveError(saveFailureMessage(err, t(($) => $.plan.save_failed)));
      return "failed";
    }
  };

  const handleSave = () => {
    if (!draft || !draftIsCurrentProject || conflictRevision !== null || projectConflictNotice) {
      return;
    }
    const issue = validatePlanDraft(draft);
    if (issue) {
      setValidationIssue(issue);
      return;
    }
    void persist(draft, planRevision, pendingGenerationId);
  };

  const handleSaveMine = () => {
    if (!draft || conflictRevision === null) return;
    const issue = validatePlanDraft(draft);
    if (issue) {
      setValidationIssue(issue);
      return;
    }
    void persist(draft, conflictRevision, pendingGenerationId);
  };

  const handleLoadLatest = useCallback(async (): Promise<boolean> => {
    const result = await planQuery.refetch();
    void queryClient.invalidateQueries({ queryKey: goalKeys.detail(wsId, goal.id) });
    // A cached payload from the old project is not a successful reload, and
    // nothing is cleared until the current project's plan is in hand.
    if (result.isError || !result.data || result.data.project_id !== goal.project_id) {
      setSaveError(t(($) => $.plan.load_failed));
      return false;
    }
    setDraft(draftFromPlan(result.data));
    setDraftProjectId(result.data.project_id);
    setDirty(false);
    setEditedItemIds(new Set());
    setRemovedItemIds(new Map());
    setRemovedEdgeKeys(new Map());
    setConflictRevision(null);
    setPendingGenerationId(null);
    setProjectConflictNotice(false);
    setValidationIssue(null);
    setSaveError(null);
    return true;
  }, [planQuery, queryClient, wsId, goal.id, goal.project_id, t]);

  const handleApplyCandidate = async (
    generation: GoalPlanGeneration,
  ): Promise<ApplyCandidateOutcome> => {
    if (!draft || !generation.candidate) return "failed";
    // A revision conflict is an unresolved choice between two saved versions.
    // Applying a candidate must not count as the user choosing to overwrite
    // the other one; the banner's explicit action stays the only way.
    if (conflictRevision !== null || projectConflictNotice || !draftIsCurrentProject) {
      return "failed";
    }
    const merged = mergeCandidate(draft, generation.candidate, editedItemIds, {
      itemIds: deletionsForRun(removedItemIds, generation.base_revision),
      edgeKeys: deletionsForRun(removedEdgeKeys, generation.base_revision),
    });
    setDraft((current) => (current ? { ...current, ...merged } : current));
    setDirty(true);
    setPendingGenerationId(generation.id);
    setValidationIssue(null);
    const outcome = await persist(merged, planRevision, generation.id);
    if (outcome === "saved") {
      // The run this decision belonged to is now applied; later candidates
      // start from the saved plan and need no local deletion record.
      setRemovedItemIds(new Map());
      setRemovedEdgeKeys(new Map());
      return "applied";
    }
    if (outcome === "generation_project_conflict") return "conflict";
    return "failed";
  };

  const handleProjectConflict = () => {
    setProjectConflictNotice(true);
    setPendingGenerationId(null);
    setConflictRevision(null);
    void queryClient.invalidateQueries({ queryKey: goalKeys.detail(wsId, goal.id) });
    void planQuery.refetch();
  };

  // A refused generation read/write is revalidated against the goal and plan;
  // a real permission loss then hides the section instead of exposing cached
  // payloads. A missing agent alone is not a reason to declare the goal gone.
  const handleRunUnavailable = () => {
    void queryClient.invalidateQueries({ queryKey: goalKeys.detail(wsId, goal.id) });
    void planQuery.refetch();
  };

  // Ordinary contention on the saved plan. The draft is untouched; the banner
  // offers reload or overwrite. This is deliberately not the project-conflict
  // notice: the run was refused before it existed, not hidden after the fact.
  const handleStartRevisionConflict = (revision: number | null) => {
    setPendingGenerationId(null);
    if (revision !== null) setConflictRevision(revision);
    void planQuery.refetch();
  };

  const handleStartNewGeneration = async () => {
    const loaded = await handleLoadLatest();
    if (!loaded) return;
    setGenerationNonce((current) => current + 1);
  };

  const validationMessage = (issue: PlanValidationIssue): string => {
    switch (issue) {
      case "title_required":
        return t(($) => $.plan.validation_title_required);
      case "dependency_reason_required":
        return t(($) => $.plan.validation_dependency_reason_required);
      case "dependency_invalid":
        return t(($) => $.plan.validation_dependency_invalid);
      case "cycle":
        return t(($) => $.plan.validation_cycle);
    }
  };

  const ownerLabel = (item: { proposed_owner: { type: string; id: string } | null }) => {
    const owner = item.proposed_owner;
    if (!owner) return null;
    if (owner.type === "agent") {
      return agents.find((agent) => agent.id === owner.id)?.name ?? null;
    }
    if (owner.type === "member") {
      return members.find((member) => member.user_id === owner.id)?.name ?? null;
    }
    if (owner.type === "squad") {
      return squads.find((squad) => squad.id === owner.id)?.name ?? null;
    }
    return null;
  };

  // A terminal plan read refusal means this viewer can no longer read the
  // plan. Cached plan content, a draft, a candidate and its basis must not be
  // rendered, but the reader still gets an explanation and a recovery action.
  const planUnavailable = planQuery.isError && isGoalUnavailableError(planQuery.error);

  // Only move focus when the element the reader was using was removed with the
  // content. A background refresh that leaves the form mounted keeps focus.
  useEffect(() => {
    const lost = focusedInsideRef.current;
    if (!lost || lost.isConnected) return;
    // A control outside the plan area still has focus; leaving it there is
    // the whole point of only recovering focus that the removal took away.
    const active = document.activeElement as HTMLElement | null;
    if (
      active &&
      active !== document.body &&
      active.isConnected &&
      !sectionRef.current?.contains(active)
    ) {
      return;
    }
    const target = projectConflictNotice
      ? conflictReloadRef.current
      : planUnavailable
        ? planRetryRef.current
        : null;
    if (!target) return;
    focusedInsideRef.current = null;
    target.focus();
  }, [projectConflictNotice, planUnavailable]);

  if (planUnavailable) {
    return (
      <section
        ref={sectionRef}
        onFocusCapture={handleFocusIn}
        onBlurCapture={handleFocusOut}
        className="space-y-2"
        data-testid="goal-plan-section"
      >
        <h2 className="text-title-sm font-semibold">{t(($) => $.plan.title)}</h2>
        <div
          className="flex items-center gap-2 text-caption text-destructive"
          role="alert"
          data-testid="plan-section-unavailable"
        >
          <span>{t(($) => $.plan.plan_unavailable)}</span>
          <Button
            ref={planRetryRef}
            size="sm"
            variant="outline"
            onClick={() => void planQuery.refetch()}
          >
            {t(($) => $.plan.retry)}
          </Button>
        </div>
      </section>
    );
  }

  const header = (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="flex items-baseline gap-2">
        <h2 className="text-title-sm font-semibold">{t(($) => $.plan.title)}</h2>
        {plan ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.plan.revision, { revision: plan.revision })}
          </span>
        ) : null}
      </div>
      <div className="flex items-center gap-2">
        {dirty ? <Badge variant="outline">{t(($) => $.plan.unsaved)}</Badge> : null}
        <Button
          size="sm"
          onClick={handleSave}
          disabled={
            !draft ||
            !draftIsCurrentProject ||
            savePlan.isPending ||
            conflictRevision !== null ||
            projectConflictNotice
          }
        >
          {savePlan.isPending ? <Spinner className="size-3.5" /> : null}
          {savePlan.isPending ? t(($) => $.plan.saving) : t(($) => $.plan.save)}
        </Button>
      </div>
    </div>
  );

  if (!goal.project_id) {
    return (
      <section className="space-y-2">
        <h2 className="text-title-sm font-semibold">{t(($) => $.plan.title)}</h2>
        <p className="rounded-md bg-muted px-3 py-2 text-caption text-muted-foreground">
          {t(($) => $.plan.project_required)}
        </p>
      </section>
    );
  }

  if (planQuery.isLoading && !draft) {
    return (
      <section className="space-y-2">
        {header}
        <Skeleton className="h-24 w-full" />
      </section>
    );
  }

  if (planQuery.isError && !plan && !draft) {
    return (
      <section className="space-y-2">
        <h2 className="text-title-sm font-semibold">{t(($) => $.plan.title)}</h2>
        <div className="flex items-center gap-2 text-caption text-destructive" role="alert">
          <span>{t(($) => $.plan.load_failed)}</span>
          <Button size="sm" variant="outline" onClick={() => void planQuery.refetch()}>
            {t(($) => $.plan.retry)}
          </Button>
        </div>
      </section>
    );
  }

  return (
    <section
      ref={sectionRef}
      onFocusCapture={handleFocusIn}
      onBlurCapture={handleFocusOut}
      className="space-y-3"
      data-testid="goal-plan-section"
    >
      {header}

      {planQuery.isError && plan ? (
        <div className="flex items-center gap-2 text-caption text-destructive" role="alert">
          <span>{t(($) => $.plan.refresh_failed)}</span>
          <Button size="sm" variant="outline" onClick={() => void planQuery.refetch()}>
            {t(($) => $.plan.retry)}
          </Button>
        </div>
      ) : null}

      {projectConflictNotice ? (
        <div
          className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-3"
          role="alert"
          data-testid="plan-section-project-conflict"
        >
          <p className="flex items-center gap-1.5 text-body font-medium">
            <CircleAlert className="size-4 text-destructive" />
            {t(($) => $.plan.project_conflict_title)}
          </p>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.plan.section_project_conflict)}
          </p>
          <Button
            ref={conflictReloadRef}
            size="sm"
            variant="outline"
            onClick={() => void handleLoadLatest()}
          >
            {t(($) => $.plan.project_conflict_reload)}
          </Button>
        </div>
      ) : null}

      {conflictRevision !== null ? (
        <div
          className="space-y-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-3"
          role="alert"
          data-testid="plan-revision-conflict"
        >
          <p className="text-body font-medium">{t(($) => $.plan.conflict_title)}</p>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.plan.conflict_description, { revision: conflictRevision })}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={() => void handleLoadLatest()}>
              {t(($) => $.plan.conflict_reload)}
            </Button>
            <Button size="sm" onClick={handleSaveMine} disabled={savePlan.isPending}>
              {t(($) => $.plan.conflict_save_mine)}
            </Button>
          </div>
        </div>
      ) : null}

      {validationIssue ? (
        <p className="text-caption text-destructive" role="alert">
          {validationMessage(validationIssue)}
        </p>
      ) : null}

      {saveError ? (
        <div className="flex items-center gap-2 text-caption text-destructive" role="alert">
          <span>{saveError}</span>
          <Button size="sm" variant="outline" onClick={handleSave} disabled={savePlan.isPending}>
            {t(($) => $.plan.retry)}
          </Button>
        </div>
      ) : null}

      <GoalPlanGenerationPanel
        key={goal.id}
        goal={goal}
        planRevision={planRevision}
        dirty={dirty}
        generationNonce={generationNonce}
        applyBlocked={conflictRevision !== null || projectConflictNotice || !draftIsCurrentProject}
        onApplyCandidate={handleApplyCandidate}
        onProjectConflict={handleProjectConflict}
        onRevisionConflict={handleStartRevisionConflict}
        onUnavailable={handleRunUnavailable}
        onReloadPlan={handleLoadLatest}
        onStartNewGeneration={handleStartNewGeneration}
      />

      {draft && draftIsCurrentProject && !projectConflictNotice ? (
        <>
          <GoalPlanEditor
            goal={goal}
            items={draft.items}
            edges={draft.edges}
            onChange={handleContentChange}
            onItemEdited={handleItemEdited}
            onItemRemoved={handleItemRemoved}
            onEdgeRemoved={handleEdgeRemoved}
            ownerLabel={ownerLabel}
          />
          {Object.keys(draft.context_snapshot).length > 0 ? (
            <PlanActualBasis snapshot={draft.context_snapshot} />
          ) : null}
        </>
      ) : null}
    </section>
  );
}