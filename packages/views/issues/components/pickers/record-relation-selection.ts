import { MAX_ISSUE_PROPERTY_SELECT_VALUES } from "@multica/core/types";

/** Drops ids that no longer appear in the loaded target-table options, then toggles one row. */
export function nextRecordRelationAfterToggle(
  currentValue: readonly string[],
  optionId: string,
  availableIds: ReadonlySet<string>,
  max = MAX_ISSUE_PROPERTY_SELECT_VALUES,
): string[] {
  const base = currentValue.filter((id) => availableIds.has(id.toLowerCase()));
  const normalizedOption = optionId.toLowerCase();
  const selected = base.some((id) => id.toLowerCase() === normalizedOption);
  if (selected) {
    return base.filter((id) => id.toLowerCase() !== normalizedOption);
  }
  if (base.length >= max) {
    return base;
  }
  return [...base, optionId];
}

export function visibleRecordRelationLabels(
  ids: readonly string[],
  labelById: ReadonlyMap<string, string>,
): { id: string; label: string }[] {
  return ids
    .map((id) => {
      const label = labelById.get(id.toLowerCase());
      return label ? { id, label } : null;
    })
    .filter((entry): entry is { id: string; label: string } => entry !== null);
}