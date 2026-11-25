import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const issueTypeKeys = {
  all: (wsId: string) => ["issue-types", wsId] as const,
  list: (wsId: string, includeArchived = false) =>
    [...issueTypeKeys.all(wsId), "list", includeArchived] as const,
};

export function issueTypeListOptions(wsId: string, includeArchived = false) {
  return queryOptions({
    queryKey: issueTypeKeys.list(wsId, includeArchived),
    queryFn: () => api.listIssueTypes(includeArchived),
    select: (data) => data.issue_types,
  });
}
