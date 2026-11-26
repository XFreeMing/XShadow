import type { IssueProperty, IssuePropertyValue, IssueType } from "@multica/core/types";

export function specializedIssuePropertyIds(issueTypes: readonly IssueType[]) {
  return new Set(issueTypes.flatMap((type) => type.fields.map((field) => field.property_id)));
}

export function issueTypeIncludesProperty(
  propertyId: string,
  specializedIds: ReadonlySet<string>,
  issueType?: IssueType | null,
) {
  return !specializedIds.has(propertyId)
    || issueType?.fields.some((field) => field.property_id === propertyId) === true;
}

export function applyIssueTypeSelection(
  nextTypeId: string | undefined,
  workspaceIssueTypes: IssueType[],
  propertyValues: Record<string, IssuePropertyValue>,
  appliedTypeDefaults: Set<string>,
): {
  issueTypeId: string | undefined;
  propertyValues: Record<string, IssuePropertyValue>;
  appliedTypeDefaults: Set<string>;
} {
  const nextType = nextTypeId
    ? workspaceIssueTypes.find((it) => it.id === nextTypeId) ?? null
    : null;
  const nextValues = { ...propertyValues };
  for (const defaultPropId of appliedTypeDefaults) {
    delete nextValues[defaultPropId];
  }
  const nextAppliedDefaults = new Set<string>();
  if (nextType) {
    for (const field of nextType.fields) {
      if (
        field.default_value !== undefined &&
        !Object.prototype.hasOwnProperty.call(nextValues, field.property_id)
      ) {
        nextValues[field.property_id] = field.default_value;
        nextAppliedDefaults.add(field.property_id);
      }
    }
  }
  return {
    issueTypeId: nextType?.id,
    propertyValues: nextValues,
    appliedTypeDefaults: nextAppliedDefaults,
  };
}

export function missingRequiredIssueTypeFields(
  selectedIssueType: IssueType | null,
  propertyValues: Record<string, IssuePropertyValue>,
): IssueType["fields"] {
  if (!selectedIssueType) return [];
  return selectedIssueType.fields.filter(
    (field) =>
      field.required &&
      !Object.prototype.hasOwnProperty.call(propertyValues, field.property_id),
  );
}

export function orderedIssueTypePropertyEntries(
  selectedIssueType: IssueType | null,
  workspaceProperties: IssueProperty[],
  propertyValues: Record<string, IssuePropertyValue>,
  customPropertyPickerId: string | null,
): Array<{ property: IssueProperty; required: boolean }> {
  const propertyById = new Map(workspaceProperties.map((prop) => [prop.id, prop]));
  const orderedEntries: Array<{ property: IssueProperty; required: boolean }> = [];
  const seenIds = new Set<string>();
  if (selectedIssueType) {
    for (const field of selectedIssueType.fields) {
      const prop = propertyById.get(field.property_id);
      if (!prop) continue;
      seenIds.add(prop.id);
      orderedEntries.push({ property: prop, required: field.required });
    }
  }
  for (const prop of workspaceProperties) {
    if (seenIds.has(prop.id)) continue;
    if (
      Object.prototype.hasOwnProperty.call(propertyValues, prop.id) ||
      customPropertyPickerId === prop.id
    ) {
      orderedEntries.push({ property: prop, required: false });
    }
  }
  return orderedEntries;
}
