"use client";

import { useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { goalListPagesOptions, goalPlanOptions } from "@multica/core/goals/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { ganttBoundIssuesOptions } from "@multica/core/issues/queries";
import type { Goal } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { useT } from "../../i18n";
import { PlanGanttChart } from "./plan-gantt-chart";

export type GanttMode = "calendar" | "plan";

export function IssueGanttPanel({ mode, setMode, projectId, children }: {
  mode: GanttMode;
  setMode: (mode: GanttMode) => void;
  projectId?: string;
  children: ReactNode;
}) {
  const { t } = useT("issues");
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 gap-1 border-b px-3 py-1.5">
        {([
          ["calendar", t(($) => $.gantt.calendar)],
          ["plan", t(($) => $.gantt.goal_plan)],
        ] as const).map(([value, label]) => (
          <Button key={value} size="sm" variant={mode === value ? "secondary" : "ghost"}
            aria-pressed={mode === value} onClick={() => setMode(value)}>{label}</Button>
        ))}
      </div>
      {mode === "calendar" ? children : <PlanPicker projectId={projectId} />}
    </div>
  );
}

function PlanPicker({ projectId }: { projectId?: string }) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const query = useInfiniteQuery(goalListPagesOptions(wsId, { projectId }));
  const goals = query.data?.pages.flatMap(page => page.goals).filter(goal => goal.project_id) ?? [];
  const [selectedId, setSelectedId] = useState("");
  const goal = goals.find(goal => goal.id === selectedId) ?? goals[0];
  const choices = goals.map(goal => ({ value: goal.id, label: goal.title }));
  if (query.isLoading) return <Spinner className="m-4" />;
  if (query.isError && !query.isFetchNextPageError) {
    return <Retry onRetry={() => void query.refetch()} />;
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-3 px-4 py-3">
        {goal ? (
          <Select value={goal.id} items={choices} onValueChange={id => setSelectedId(id ?? "")}>
            <SelectTrigger size="sm" aria-label={t(($) => $.list.title)} className="max-w-full">
              <SelectValue className="truncate" />
            </SelectTrigger>
            <SelectContent>{choices.map(choice => (
              <SelectItem key={choice.value} value={choice.value}>{choice.label}</SelectItem>
            ))}</SelectContent>
          </Select>
        ) : <span className="text-body text-muted-foreground">{t(($) => $.plan.project_required)}</span>}
        {query.hasNextPage && <Button size="sm" variant="outline" disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}>
          {query.isFetchNextPageError ? t(($) => $.list.retry) : t(($) => $.list.load_more)}
        </Button>}
      </div>
      {goal && <SavedPlan key={goal.id} goal={goal} wsId={wsId} />}
    </div>
  );
}

function Retry({ onRetry }: { onRetry: () => void }) {
  const { t } = useT("goals");
  return <div className="flex items-center gap-2 p-4 text-body" role="alert">
    {t(($) => $.plan.load_failed)}
    <Button size="sm" variant="outline" onClick={onRetry}>{t(($) => $.plan.retry)}</Button>
  </div>;
}

function SavedPlan({ wsId, goal }: { wsId: string; goal: Goal }) {
  const { t } = useT("issues");
  const query = useQuery(goalPlanOptions(wsId, goal.id));
  const plan = query.data;
  const confirmed = plan?.goal_id === goal.id && plan?.project_id === goal.project_id;
  const boundIds = confirmed ? plan.items.flatMap(item => item.binding.issue_id ?? []) : [];
  const issues = useQuery(ganttBoundIssuesOptions(wsId, goal.project_id, boundIds));
  if (query.isLoading) return <Spinner className="m-4" />;
  if (!confirmed || query.isError) return <Retry onRetry={() => void query.refetch()} />;
  if (!plan.items.length) return <p className="px-4 py-3 text-body text-muted-foreground">{t(($) => $.gantt.empty_plan)}</p>;
  if (issues.isLoading) return <Spinner className="m-4" />;
  if (issues.isError) return <Retry onRetry={() => void issues.refetch()} />;
  return <PlanGanttChart plan={plan} issues={issues.data ?? []} />;
}
