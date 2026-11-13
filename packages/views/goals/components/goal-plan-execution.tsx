"use client";

import { useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type {
  Goal,
  GoalPlanItem,
  MaterializedGoalPlanItem,
  MaterializeGoalPlanResponse,
} from "@multica/core/types";
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
import { GoalOwnerPicker, EMPTY_GOAL_OWNER, type GoalOwnerValue } from "./goal-owner-picker";

type DependencyDisplay =
  | "ready"
  | "not_done"
  | "not_accepted"
  | "unknown"
  | "unreadable"
  | "unrecognized";

function dependencyDisplay(reason: string): DependencyDisplay {
  switch (reason) {
    case "":
      return "ready";
    case "dependency_not_done":
      return "not_done";
    case "dependency_not_accepted":
      return "not_accepted";
    case "dependency_unknown":
      return "unknown";
    case "dependency_unreadable":
      return "unreadable";
    default:
      return "unrecognized";
  }
}

/**
 * Executes a saved plan: creates one task per `binding.type = new` item with an
 * optional assignee, then shows the server's own binding and dependency
 * verdicts. A repeated run is safe because the server is idempotent per plan
 * item; the UI never dedupes locally and never derives readiness itself.
 *
 * Everything is computed from the materialize reply. The page has no read API
 * for persisted bindings or task scheduling, so it does not claim to know that
 * a parked task will start by itself: it reports the server's dependency
 * verdict and that a real prerequisite event is what releases the work.
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

  const newItems = useMemo(
    () => items.filter((item) => item.binding.type === "new" && !item.binding.issue_id),
    [items],
  );
  const existingItems = useMemo(
    () => items.filter((item) => !!item.binding.issue_id),
    [items],
  );

  const results = useMemo(() => {
    if (!result || result.goal_id !== goal.id || result.plan_revision !== planRevision) return null;
    const map = new Map<string, MaterializedGoalPlanItem>();
    for (const item of result.items) {
      if (item.item_id && item.issue_id) map.set(item.item_id, item);
    }
    return map;
  }, [result, goal.id, planRevision]);

  const actionBlocked = dirty || blocked || !saved || materialize.isPending;

  const handleRun = async () => {
    setRunError(null);
    setUnconfirmed(false);
    try {
      const payload = await materialize.mutateAsync({
        goalId: goal.id,
        data: {
          items: newItems.map((item) => {
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
      const missing = newItems.filter(
        (item) => !payload.items.some((row) => row.item_id === item.id && row.issue_id),
      );
      if (missing.length > 0) {
        setResult(null);
        setUnconfirmed(true);
        setRunError(t(($) => $.execution.unconfirmed));
        return;
      }
      setResult(payload);
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

  const renderResult = (row: MaterializedGoalPlanItem) => {
    const dependency = dependencyDisplay(row.dependency.ready ? "" : row.dependency.reason);
    const assigneeName =
      row.assignee_type && row.assignee_id
        ? getActorName(row.assignee_type, row.assignee_id)
        : null;
    return (
      <div className="space-y-1" data-testid={`execution-result-${row.item_id}`}>
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge
            variant="outline"
            className={row.created ? "border-success/40 text-success" : "text-muted-foreground"}
          >
            {row.created ? t(($) => $.execution.created) : t(($) => $.execution.reused)}
          </Badge>
          <AppLink
            href={paths.issueDetail(row.issue_id)}
            className="text-caption underline-offset-2 hover:underline"
          >
            {t(($) => $.execution.open_task)}
          </AppLink>
          <span className="text-caption text-muted-foreground">
            {assigneeName ?? t(($) => $.execution.unassigned)}
          </span>
        </div>
        <p
          className={
            dependency === "ready"
              ? "text-caption text-success"
              : "text-caption text-warning"
          }
        >
          {t(($) => $.execution[`dep_${dependency}`])}
          {dependency === "unrecognized" ? ` (${row.dependency.reason})` : ""}
        </p>
        {row.dependency.detail ? (
          <p className="break-words font-mono text-caption text-muted-foreground">
            {row.dependency.detail}
          </p>
        ) : null}
        {dependency !== "ready" ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.execution.dep_recovery)}</p>
        ) : null}
        <p className="text-caption text-muted-foreground">
          {row.task_enqueued
            ? t(($) => $.execution.run_started)
            : !row.assignee_id
              ? t(($) => $.execution.run_no_assignee)
              : dependency !== "ready"
                ? t(($) => $.execution.run_waiting_dependency)
                : t(($) => $.execution.run_not_enqueued)}
        </p>
      </div>
    );
  };

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
            disabled={actionBlocked || newItems.length === 0}
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
          {existingItems.map((item) => (
            <li
              key={item.id}
              className="flex flex-wrap items-center gap-2 px-3 py-2"
              data-testid={`execution-existing-${item.id}`}
            >
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
            </li>
          ))}
          {newItems.map((item) => (
            <li
              key={item.id}
              className="space-y-2 px-3 py-2"
              data-testid={`execution-item-${item.id}`}
            >
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="outline">{t(($) => $.plan.binding_new)}</Badge>
                <span className="min-w-0 flex-1 truncate text-body">{item.title}</span>
                <GoalOwnerPicker
                  value={assignees[item.id] ?? EMPTY_GOAL_OWNER}
                  onChange={(next) =>
                    setAssignees((current) => ({ ...current, [item.id]: next }))
                  }
                />
              </div>
              {results?.get(item.id) ? renderResult(results.get(item.id)!) : null}
            </li>
          ))}
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