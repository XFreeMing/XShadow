import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { issueTypeKeys } from "./queries";
import type { CreateIssueTypeRequest, UpdateIssueTypeRequest } from "../types";

export function useCreateIssueType() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateIssueTypeRequest) => api.createIssueType(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueTypeKeys.all(wsId) });
    },
  });
}

export function useUpdateIssueType() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateIssueTypeRequest) =>
      api.updateIssueType(id, data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueTypeKeys.all(wsId) });
    },
  });
}
