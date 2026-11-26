import {
  GRID_PROPERTY_PREFIX,
  type GridColumnKey,
} from "@multica/core/issues/grid-columns";
import type { Issue, IssuePriority, IssueProperty, Project } from "@multica/core/types";
import type { ChildProgress } from "./list-row";
import { propertyDisplayValue } from "./table-view-model";

export function issueGridExportRow(
  issue: Issue,
  columnKeys: readonly GridColumnKey[],
  ctx: {
    resolveStatusLabel: (status: string) => string;
    resolvePriorityLabel?: (priority: IssuePriority) => string;
    issueTypeName: string | null;
    propertyById: ReadonlyMap<string, IssueProperty>;
    getActorName: (type: string, id: string) => string;
    projectMap?: ReadonlyMap<string, Project>;
    childProgressMap?: ReadonlyMap<string, ChildProgress>;
  },
): string[] {
  return columnKeys.map((key) => {
    switch (key) {
      case "title":
        return issue.title;
      case "identifier":
        return issue.identifier;
      case "type":
        return ctx.issueTypeName ?? "";
      case "status":
        return ctx.resolveStatusLabel(issue.status);
      case "priority":
        return ctx.resolvePriorityLabel
          ? ctx.resolvePriorityLabel(issue.priority)
          : issue.priority;
      case "assignee":
        return issue.assignee_type && issue.assignee_id
          ? ctx.getActorName(issue.assignee_type, issue.assignee_id)
          : "";
      case "labels":
        return (issue.labels ?? []).map((label) => label.name).join(", ");
      case "project":
        return issue.project_id
          ? ctx.projectMap?.get(issue.project_id)?.title ?? ""
          : "";
      case "start_date":
        return issue.start_date ?? "";
      case "due_date":
        return issue.due_date ?? "";
      case "created_at":
        return issue.created_at;
      case "updated_at":
        return issue.updated_at;
      case "child_progress": {
        const progress = ctx.childProgressMap?.get(issue.id);
        return progress ? `${progress.done}/${progress.total}` : "";
      }
      case "creator":
        return ctx.getActorName(issue.creator_type, issue.creator_id);
      default:
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
    }
  });
}
