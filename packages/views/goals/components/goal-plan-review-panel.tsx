"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { goalKeys, goalPlanGenerationOptions, useSaveGoalPlan } from "@multica/core/goals";
import type { GoalPlan } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { ListChecks } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { GoalPlanTool } from "./goal-plan-tool";

export function GoalPlanReviewPanel({ plan, agents, blocked, open, onToggle }: {
  plan: GoalPlan;
  agents: readonly { id: string; name: string }[];
  blocked: boolean;
  open: boolean;
  onToggle: () => void;
}) {
  const { t } = useT("goals");
  const [reviewer, setReviewer] = useState("");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const review = plan.review;
  const ready = plan.progress?.ready_to_prepare ?? [];
  const pending = review?.state === "queued" || review?.state === "running";
  const assessment = review?.assessment;
  const proposed = review?.state === "succeeded" && assessment?.decision === "planning_gap";
  const generation = useQuery({
    ...goalPlanGenerationOptions(wsId, plan.goal_id, proposed ? review.generation_id : null),
  });
  const candidate = generation.data?.candidate;
  const save = useSaveGoalPlan();
  const start = useMutation({
    mutationFn: async () => {
      const run = await api.startGoalPlanReview(plan.goal_id, { expected_revision: plan.revision, agent_id: reviewer });
      if (!run.id) throw new Error("Unconfirmed goal review");
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: goalKeys.plan(wsId, plan.goal_id) }),
  });
  const planItem = new Map(plan.items.map((item) => [item.id, item.title]));
  const previousItems = new Map(plan.items.map((item) => [item.id, JSON.stringify(item)]));
  const adjustedItems = candidate?.items.filter((item) => previousItems.get(item.id) !== JSON.stringify(item)) ?? [];
  const decision = assessment?.decision;
  const decisionLabel = decision === "covered" ? t(($) => $.plan_review.covered)
    : decision === "planning_gap" ? t(($) => $.plan_review.gap)
    : decision === "execution_issue" ? t(($) => $.plan_review.execution_issue)
    : t(($) => $.plan_review.unverified);

  if (!review && !plan.can_manage_execution && ready.length === 0) return null;
  const status = pending ? t(($) => $.plan_review.running)
    : review?.state === "stale" ? t(($) => $.plan_review.stale)
    : review?.state === "failed" ? t(($) => $.plan_review.failed)
    : review?.state === "succeeded" && assessment ? `${decisionLabel} · r${review.base_revision}`
    : null;
  return (
    <GoalPlanTool
      label={t(($) => $.plan_review.title)}
      icon={<ListChecks aria-hidden="true" />}
      status={status}
      open={open}
      onToggle={onToggle}
    >
      <div className="grid max-w-4xl gap-3 text-label">
        {review?.state === "succeeded" && assessment && (
          <>
            <p className="break-words">{assessment.summary}</p>
            <details className="space-y-1">
              <summary className="cursor-pointer font-medium">{t(($) => $.plan_review.checks)} ({assessment.checks.length})</summary>
              <ul className="mt-2 grid gap-2">
                {assessment.checks.map((check) => (
                  <li key={check.outcome} className="border-s border-border ps-2">
                    <p className="font-medium">{check.outcome}</p>
                    {check.item_ids.length > 0 && <p className="text-caption text-muted-foreground">{check.item_ids.map((id) => planItem.get(id) ?? id).join(" · ")}</p>}
                    <p className="break-words">{check.reason}</p>
                  </li>
                ))}
              </ul>
            </details>
          </>
        )}
        {ready.length > 0 && (
          <div className="grid gap-1">
            <h4 className="font-medium">{t(($) => $.plan_review.preparation)}</h4>
            <ul className="flex flex-wrap gap-x-3 gap-y-1 text-muted-foreground">
              {ready.map((item) => <li key={item.item_id}>{item.title}</li>)}
            </ul>
          </div>
        )}
        {plan.can_manage_execution && !pending && (!review || review.state === "failed") && (
          <div className="flex flex-wrap gap-2">
            <select aria-label={t(($) => $.plan_review.reviewer)} value={reviewer} onChange={(event) => setReviewer(event.target.value)} className="max-w-full rounded-md border border-border bg-background px-2">
              <option value="">{t(($) => $.plan_review.reviewer)}</option>
              {agents.map((agent) => <option value={agent.id} key={agent.id}>{agent.name}</option>)}
            </select>
            <Button size="xs" variant="outline" disabled={!reviewer || blocked || start.isPending} onClick={() => start.mutate()}>
              {t(($) => $.plan_review.start)}
            </Button>
          </div>
        )}
        {proposed && adjustedItems.length > 0 && (
          <ul className="list-inside list-disc text-muted-foreground">
            {adjustedItems.map((item) => <li key={item.id}>{item.title}</li>)}
          </ul>
        )}
        {plan.can_manage_execution && proposed && candidate && candidate.items.length > 0 && (
          <Button className="justify-self-start" size="xs" variant="outline" disabled={blocked || save.isPending} onClick={() => save.mutate({
            goalId: plan.goal_id,
            data: {
              expected_revision: plan.revision,
              items: candidate.items,
              edges: candidate.edges,
              context_snapshot: candidate.context_snapshot,
              generation_id: review.generation_id,
            },
          })}>
            {t(($) => $.plan_review.apply)}
          </Button>
        )}
        {(start.isError || save.isError || generation.isError) && <p role="alert" className="text-destructive">{t(($) => $.plan_review.failed)}</p>}
      </div>
    </GoalPlanTool>
  );
}
