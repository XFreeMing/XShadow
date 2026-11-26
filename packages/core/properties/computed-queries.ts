import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { IssueComputedPropertyBag } from "../types";

const COMPUTED_REFRESH_MS = 5_000;
const MAX_ISSUE_IDS_PER_REQUEST = 200;

export const issueComputedPropertyKeys = {
  all: (wsId: string) => ["issue-computed-properties", wsId] as const,
  project: (wsId: string, issueIds: readonly string[]) =>
    [
      ...issueComputedPropertyKeys.all(wsId),
      [...issueIds].sort().join("\0"),
    ] as const,
};

export function issueComputedPropertiesOptions(
  wsId: string,
  issueIds: readonly string[],
) {
  const ids = [...new Set(issueIds.filter(Boolean))];
  return queryOptions({
    queryKey: issueComputedPropertyKeys.project(wsId, ids),
    queryFn: async () => {
      const properties: Record<string, IssueComputedPropertyBag> = {};
      for (let start = 0; start < ids.length; start += MAX_ISSUE_IDS_PER_REQUEST) {
        const batch = ids.slice(start, start + MAX_ISSUE_IDS_PER_REQUEST);
        const result = await api.projectIssueProperties(batch);
        Object.assign(properties, result.properties);
      }
      return properties;
    },
    enabled: Boolean(wsId) && ids.length > 0,
    refetchInterval: ids.length > 0 ? COMPUTED_REFRESH_MS : false,
  });
}
