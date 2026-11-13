import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { GoalDetailPage as GoalDetailView } from "@multica/views/goals/components";
import { isGoalUnavailableError } from "@multica/views/goals/status";
import { useWorkspaceId } from "@multica/core/hooks";
import { goalDetailOptions } from "@multica/core/goals";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function GoalDetailPage() {
  const { id } = useParams<{ id: string }>();
  const wsId = useWorkspaceId();
  // Shares the detail query with GoalDetailView below, so the title costs no
  // extra request. A goal that is gone for this viewer must not keep the last
  // loaded name — the same rule the tab strip follows; the view itself renders
  // the not-found/permission state.
  const { data: goal, error, isError } = useQuery(goalDetailOptions(wsId, id ?? ""));
  const unavailable = isError && isGoalUnavailableError(error);

  useDocumentTitle(!unavailable && goal ? goal.title : "Goal");

  if (!id) return null;
  return <GoalDetailView goalId={id} />;
}