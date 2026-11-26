import {
  GRID_PROPERTY_PREFIX,
  type GridColumnKey,
} from "@multica/core/issues/grid-columns";
import type { Issue, IssueProperty } from "@multica/core/types";
import { propertyDisplayValue } from "./table-view-model";

export function issueGridExportRow(
  issue: Issue,
  columnKeys: readonly GridColumnKey[],
  ctx: {
    resolveStatusLabel: (status: string) => string;
    issueTypeName: string | null;
    propertyById: ReadonlyMap<string, IssueProperty>;
    getActorName: (type: string, id: string) => string;
  },
): string[] {
  return columnKeys.map((key) => {
    if (key === "title") return issue.title;
    if (key === "type") return ctx.issueTypeName ?? "";
    if (key === "status") return ctx.resolveStatusLabel(issue.status);
    if (key === "assignee") {
      return issue.assignee_type && issue.assignee_id
        ? ctx.getActorName(issue.assignee_type, issue.assignee_id)
        : "";
    }
    if (key.startsWith(GRID_PROPERTY_PREFIX)) {
      const propertyId = key.slice(GRID_PROPERTY_PREFIX.length);
      const property = ctx.propertyById.get(propertyId);
      if (!property) return "";
      return propertyDisplayValue(
        property,
        issue.properties[propertyId],
        ctx.getActorName,
        ctx.resolveStatusLabel,
      );
    }
    return "";
  });
}
