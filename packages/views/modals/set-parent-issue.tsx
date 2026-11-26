"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { errorCode } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  childIssuesOptions,
} from "@multica/core/issues/queries";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { IssuePickerModal } from "./issue-picker-modal";
import { useT } from "../i18n";

export function SetParentIssueModal({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("modals");
  const { t: tIssues } = useT("issues");
  const issueId = (data?.issueId as string) || "";
  const expectedRevision =
    typeof data?.expectedRevision === "number" ? data.expectedRevision : undefined;
  const extraExcludeIds = Array.isArray(data?.excludeIds)
    ? data.excludeIds.filter((value): value is string => typeof value === "string")
    : [];
  const wsId = useWorkspaceId();
  const updateIssue = useUpdateIssue();

  const { data: children = [] } = useQuery({
    ...childIssuesOptions(wsId, issueId),
    enabled: !!issueId,
  });

  const excludeIds = [
    ...new Set([issueId, ...children.map((c) => c.id), ...extraExcludeIds]),
  ];

  return (
    <IssuePickerModal
      open
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
      title={t(($) => $.set_parent.title)}
      description={t(($) => $.set_parent.description)}
      excludeIds={excludeIds}
      onSelect={(selected) => {
        updateIssue.mutate(
          {
            id: issueId,
            parent_issue_id: selected.id,
            ...(expectedRevision !== undefined
              ? { expected_revision: expectedRevision }
              : {}),
          },
          {
            onSuccess: () =>
              toast.success(
                t(($) => $.set_parent.toast_success, {
                  identifier: selected.identifier,
                }),
              ),
            onError: (err) =>
              toast.error(
                errorCode(err) === "revision_conflict"
                  ? tIssues(($) => $.revision.conflict)
                  : err instanceof Error && err.message
                    ? err.message
                    : t(($) => $.set_parent.toast_failed),
              ),
          },
        );
      }}
    />
  );
}
