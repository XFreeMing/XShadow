"use client";

import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import { Workflow } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { WorkMethodSteps } from "../../workflows/work-method-step";
import { GoalPlanTool } from "./goal-plan-tool";

export function GoalMethodRecommendation({
  wsId,
  goalId,
  updatedAt,
  open,
  onToggle,
}: {
  wsId: string;
  goalId: string;
  updatedAt: string;
  open: boolean;
  onToggle: () => void;
}) {
  const { t } = useT("issues");
  const query = useQuery({
    queryKey: ["work-methods", wsId, "goal", goalId, updatedAt],
    queryFn: () => api.getGoalWorkMethod(goalId),
    staleTime: 0,
    retry: false,
  });
  if (query.error instanceof ApiError && query.error.status === 404) return null;
  if (!query.data && !query.isError) return null;
  const recommendation = query.data;

  return (
    <GoalPlanTool
      label={t(($) => $.method.recommended)}
      icon={<Workflow aria-hidden="true" />}
      status={query.isError ? t(($) => $.method.load_error) : recommendation?.method?.name ?? t(($) => $.method.no_match)}
      open={open}
      onToggle={onToggle}
    >
      {query.isError ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-label text-destructive">
          <span>{t(($) => $.method.load_error)}</span>
          <Button size="xs" variant="outline" onClick={() => void query.refetch()}>
            {t(($) => $.method.retry)}
          </Button>
        </div>
      ) : (
        <div className="max-w-3xl space-y-2 text-label">
          {recommendation?.reason ? <p className="break-words">{recommendation.reason}</p> : null}
          {recommendation?.method ? (
            <>
              <h3 className="font-medium">{t(($) => $.method.steps)}</h3>
              <WorkMethodSteps steps={recommendation.method.steps} />
            </>
          ) : !recommendation?.reason ? (
            <p>{t(($) => $.method.no_match)}</p>
          ) : null}
        </div>
      )}
    </GoalPlanTool>
  );
}
