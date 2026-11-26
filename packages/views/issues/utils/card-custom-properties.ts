import type { Issue, IssueProperty } from "@multica/core/types";
import { isComputedPropertyType } from "@multica/core/types";

/** Custom property chips toggled on for cards, preserving picker order. */
export function resolveCardCustomProperties(
  cardPropertyIds: readonly string[],
  workspaceProperties: readonly IssueProperty[],
  issue: Issue,
  options?: { excludePropertyId?: string | null },
): IssueProperty[] {
  const excludeId = options?.excludePropertyId ?? null;
  return cardPropertyIds
    .filter((id) => id !== excludeId)
    .map((id) => workspaceProperties.find((property) => property.id === id))
    .filter((property): property is IssueProperty => {
      if (!property) return false;
      if (isComputedPropertyType(property.type)) return true;
      return issue.properties?.[property.id] !== undefined;
    });
}

export function cardPropertyIdsNeedComputedFetch(
  cardPropertyIds: readonly string[],
  workspaceProperties: readonly IssueProperty[],
): boolean {
  const selected = new Set(cardPropertyIds);
  return workspaceProperties.some(
    (property) =>
      selected.has(property.id) && isComputedPropertyType(property.type),
  );
}