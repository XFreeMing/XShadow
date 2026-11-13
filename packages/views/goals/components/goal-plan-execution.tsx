"use client";

import { useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Goal, GoalPlanItem, MaterializeGoalPlanResponse } from "@multica/core/types";
import {
  goalKeys,
  goalPlanOptions,
  useMaterializeGoalPlan,
} from "@multica/core/goals";
import { useWorkspaceId } from "@multica/core/hooks";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { useQuery } from "@tanstack/react-query";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import {
  DEPENDENCY_WAIT_EVENT,
  WAIT_MAIN_COPY_KEY,
  knownIssueStatus,
  executionWaitSummary,
  mergeExecutionViews,
  triggerCopyKey,
  type ExecutionItemView,
} from "../execution";
import { GoalOwnerPicker, EMPTY_GOAL_OWNER, type GoalOwnerValue } from "./goal-owner-picker";

/**
 * Executes a saved plan: creates one task per unbound `binding.type = new`
 * item with an optional assignee, then shows the server's own bindings,
 * dependency verdicts and the real event that releases a parked task.
 *
 * Bindings survive a refresh through the plan's execution read model; the
 * materialize reply is used only for what it alone reports (`created` vs
 * reused, whether a run was enqueued). A repeated run is safe because the
 * server is idempotent per plan item, and the panel never derives readiness
 * or resumption from a timer, a retry or a task's own status.
 */
export function GoalPlanExecution({
  goal,
  items,
  planRevision,
  saved,
  dirty,
  blocked,
}: {
  goal: Goal;
  items: GoalPlanItem[];
  planRevision: number;
  /** A real server plan exists; a synthetic empty draft cannot be executed. */
  saved: boolean;
  dirty: boolean;
  /** A revision or project conflict currently prevents a safe write. */
  blocked: boolean;
}) {
  const { t } = useT("goals");
  const { t: tIssues } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const { getActorName } = useActorName();
  const planQuery = useQuery(goalPlanOptions(wsId, goal.id));
  const materialize = useMaterializeGoalPlan();

  const [assignees, setAssignees] = useState<Record<string, GoalOwnerValue>>({});
  const [result, setResult] = useState<MaterializeGoalPlanResponse | null>(null);
  const [runError, setRunError] = useState<string | null>(null);
  const [unconfirmed, setUnconfirmed] = useState(false);

  // A result describes the plan revision it was produced from. Once the saved
  // plan moves, its bindings and dependency verdicts are no longer current.
  useEffect(() => {
    setResult((current) => (current && current.plan_revision !== planRevision ? null : current));
  }, [planRevision]);

  const execution =
    planQuery.data && planQuery.data.goal_id === goal.id ? planQuery.data.execution : null;

  const views = useMemo(
    () => mergeExecutionViews({ execution, reply: result, goalId: goal.id, planRevision }),
    [execution, result, goal.id, planRevision],
  );

  // Every unbound plan item keeps its row: once the read model reports a
  // binding the row shows that task instead of offering to create it again.
  const newItems = useMemo(
    () => items.filter((item) => item.binding.type === "new" && !item.binding.issue_id),
    [items],
  );
  const runnableItems = useMemo(
    () => newItems.filter((item) => !views?.get(item.id)),
    [newItems, views],
  );
  const existingItems = useMemo(
    () => items.filter((item) => !!item.binding.issue_id),
    [items],
  );

  const actionBlocked = dirty || blocked || !saved || materialize.isPending;

  const handleRun = async () => {
    setRunError(null);
    setUnconfirmed(false);
    try {
      const payload = await materialize.mutateAsync({
        goalId: goal.id,
        data: {
          items: runnableItems.map((item) => {
            const assignee = assignees[item.id];
            return assignee?.type && assignee.id
              ? { item_id: item.id, assignee_type: assignee.type, assignee_id: assignee.id }
              : { item_id: item.id };
          }),
        },
      });
      if (payload.goal_id !== goal.id || payload.plan_revision !== planRevision) {
        // A reply about another goal or an older plan cannot be shown as this
        // plan's execution result.
        setResult(null);
        setUnconfirmed(true);
        setRunError(t(($) => $.execution.unconfirmed));
        return;
      }
      const missing = runnableItems.filter(
        (item) => !payload.items.some((row) => row.item_id === item.id && row.issue_id),
      );
      if (missing.length > 0) {
        setResult(null);
        setUnconfirmed(true);
        setRunError(t(($) => $.execution.unconfirmed));
        return;
      }
      setResult(payload);
      // The durable read model is the page's source after a refresh; reread it
      // so the reply is backed by persisted bindings.
      void queryClient.invalidateQueries({ queryKey: goalKeys.plan(wsId, goal.id) });
      const created = payload.items.filter((row) => row.created).length;
      const reused = payload.items.length - created;
      toast.success(t(($) => $.execution.created_summary, { created, reused }));
    } catch {
      setRunError(t(($) => $.execution.create_failed));
    }
  };

  const refresh = () => {
    setRunError(null);
    setUnconfirmed(false);
    void planQuery.refetch();
    void queryClient.invalidateQueries({ queryKey: goalKeys.closure(wsId, goal.id) });
    void queryClient.invalidateQueries({ queryKey: goalKeys.issues(wsId, goal.id) });
  };

  const statusLabel = (status: string) => {
    const known = knownIssueStatus(status);
    return known ? tIssues(($) => $.status[known]) : status;
  };

  const triggerLabel = (code: string) => {
    const key = triggerCopyKey(code);
    return key ? t(($) => $.execution[key]) : code;
  };

  const assigneeName = (view: ExecutionItemView) =>
    view.assigneeType && view.assigneeId
      ? getActorName(view.assigneeType, view.assigneeId)
      : null;

  /**
   * The primary wait message comes from the structured reason and prerequisite
   * statuses, so it reads in the reader's language. The server's own English
   * sentences are diagnostics, shown after the localized lines.
   */
  const renderDependency = (view: ExecutionItemView) => {
    const summary = executionWaitSummary(view.dependency, view.wait);
    if (!summary) {
      return view.dependency ? (
        <p className="text-caption text-success">{t(($) => $.execution.dep_ready)}</p>
      ) : null;
    }
    const statuses = summary.statuses.map(statusLabel).join(" · ");
    const triggers = summary.triggers.map(triggerLabel).join(", ");
    const unknownEvent = !!summary.event && summary.event !== DEPENDENCY_WAIT_EVENT;
    return (
      <div className="space-y-1" data-testid={`execution-wait-${view.itemId}`}>
        <p className="text-caption text-warning">
          {t(($) => $.execution[WAIT_MAIN_COPY_KEY[summary.reason]], {
            reason: summary.dependencyReason,
          })}
        </p>
        {statuses ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.execution.wait_statuses, { statuses })}
          </p>
        ) : null}
        {triggers ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.execution.wait_triggers, { triggers })}
          </p>
        ) : (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.execution.dep_recovery)}
          </p>
        )}
        {unknownEvent ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.execution.wait_event_unknown, { event: summary.event })}
          </p>
        ) : null}
        {summary.detail ? (
          <p className="break-words font-mono text-caption text-muted-foreground">
            {summary.detail}
          </p>
        ) : null}
      </div>
    );
  };

  const renderRunState = (view: ExecutionItemView) => {
    if (view.taskEnqueued === null) return null;
    const waiting = !!view.dependency && !view.dependency.ready;
    return (
      <p className="text-caption text-muted-foreground">
        {view.taskEnqueued
          ? t(($) => $.execution.run_started)
          : !view.assigneeId
            ? t(($) => $.execution.run_no_assignee)
            : waiting
              ? t(($) => $.execution.run_waiting_dependency)
              : t(($) => $.execution.run_not_enqueued)}
      </p>
    );
  };

  const renderView = (view: ExecutionItemView) => (
    <div className="space-y-1" data-testid={`execution-result-${view.itemId}`}>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge
          variant="outline"
          className={
            view.created === true ? "border-success/40 text-success" : "text-muted-foreground"
          }
        >
          {view.created === true
            ? t(($) => $.execution.created)
            : t(($) => $.execution.reused)}
        </Badge>
        <AppLink
          href={paths.issueDetail(view.issueId)}
          className="text-caption underline-offset-2 hover:underline"
        >
          {t(($) => $.execution.open_task)}
        </AppLink>
        <span className="text-caption text-muted-foreground">
          {assigneeName(view) ?? t(($) => $.execution.unassigned)}
        </span>
      </div>
      {renderDependency(view)}
      {renderRunState(view)}
    </div>
  );

  return (
    <section className="space-y-2" data-testid="goal-plan-execution">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-body font-medium">{t(($) => $.execution.title)}</h3>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="ghost" onClick={refresh}>
            {t(($) => $.execution.refresh)}
          </Button>
          <Button
            size="sm"
            disabled={actionBlocked || runnableItems.length === 0}
            aria-busy={materialize.isPending}
            onClick={() => void handleRun()}
          >
            {materialize.isPending ? <Spinner className="size-3.5" aria-hidden="true" /> : null}
            {materialize.isPending
              ? t(($) => $.execution.creating)
              : t(($) => $.execution.create)}
          </Button>
        </div>
      </div>
      <p className="text-caption text-muted-foreground">{t(($) => $.execution.description)}</p>

      {!saved || dirty || blocked ? (
        <p className="text-caption text-warning" data-testid="execution-save-first">
          {t(($) => $.execution.save_first)}
        </p>
      ) : null}

      {items.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.execution.none)}</p>
      ) : (
        <ul className="divide-y divide-border/60 rounded-md border">
          {existingItems.map((item) => {
            const view = views?.get(item.id);
            return (
              <li
                key={item.id}
                className="space-y-1 px-3 py-2"
                data-testid={`execution-existing-${item.id}`}
              >
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="outline" className="text-muted-foreground">
                    {t(($) => $.plan.binding_existing)}
                  </Badge>
                  <span className="min-w-0 flex-1 truncate text-body">{item.title}</span>
                  {item.binding.issue_id ? (
                    <AppLink
                      href={paths.issueDetail(item.binding.issue_id)}
                      className="text-caption underline-offset-2 hover:underline"
                    >
                      {t(($) => $.execution.open_task)}
                    </AppLink>
                  ) : null}
                </div>
                {view ? (
                  <>
                    {renderDependency(view)}
                    <p className="text-caption text-muted-foreground">
                      {assigneeName(view) ?? t(($) => $.execution.unassigned)}
                    </p>
                  </>
                ) : null}
              </li>
            );
          })}
          {newItems.map((item) => {
            const view = views?.get(item.id);
            return (
              <li
                key={item.id}
                className="space-y-2 px-3 py-2"
                data-testid={`execution-item-${item.id}`}
              >
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="outline">{t(($) => $.plan.binding_new)}</Badge>
                  <span className="min-w-0 flex-1 truncate text-body">{item.title}</span>
                  {view ? null : (
                    <GoalOwnerPicker
                      value={assignees[item.id] ?? EMPTY_GOAL_OWNER}
                      onChange={(next) =>
                        setAssignees((current) => ({ ...current, [item.id]: next }))
                      }
                    />
                  )}
                </div>
                {view ? renderView(view) : null}
              </li>
            );
          })}
        </ul>
      )}

      <p className="text-caption text-muted-foreground">{t(($) => $.execution.assignee_note)}</p>
      <p className="text-caption text-muted-foreground">{t(($) => $.execution.deferred_note)}</p>

      {runError ? (
        <p className="text-caption text-destructive" role="alert" data-testid="execution-error">
          {runError}
        </p>
      ) : null}
      {unconfirmed ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.execution.unconfirmed_hint)}</p>
      ) : null}
    </section>
  );
}