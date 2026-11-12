"use client";

import { toast } from "sonner";
import type { Goal, Issue } from "@multica/core/types";
import { errorCode } from "@multica/core/api";
import { useLinkGoalIssue } from "@multica/core/goals";
import { useT } from "../../i18n";
import { IssuePickerModal } from "../../modals/issue-picker-modal";

/**
 * Link an existing task. Candidate search reuses the workspace task search
 * (local index), narrowed to the goal's own project — the server accepts a
 * link only when both sides belong to the same project, including "neither has
 * a project" — and the server has the final say on every conflict.
 */
export function GoalLinkTaskDialog({
  open,
  onOpenChange,
  goal,
  linkedIssueIds,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  goal: Goal;
  linkedIssueIds: string[];
}) {
  const { t } = useT("goals");
  const linkIssue = useLinkGoalIssue();

  const handleSelect = (issue: Issue) => {
    linkIssue.mutate(
      { goalId: goal.id, issueId: issue.id },
      {
        onSuccess: () => toast.success(t(($) => $.link.linked)),
        onError: (err) => {
          const code = errorCode(err);
          toast.error(
            code === "issue_already_linked"
              ? t(($) => $.link.conflict_already_linked)
              : code === "goal_project_mismatch"
                ? t(($) => $.link.conflict_project_mismatch)
                : t(($) => $.link.link_failed),
          );
        },
      },
    );
  };

  return (
    <IssuePickerModal
      open={open}
      onOpenChange={onOpenChange}
      title={t(($) => $.link.title)}
      description={t(($) => $.link.description)}
      excludeIds={linkedIssueIds}
      onSelect={handleSelect}
      isSelectable={(issue) => issue.project_id === goal.project_id}
    />
  );
}