import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";
import type {
  IssueProperty,
  IssueTableGroupDescriptor,
  IssueTableGroupSpec,
  Project,
} from "@multica/core/types";

export function tableGroupSpec(grouping: string): IssueTableGroupSpec {
  if (grouping === "status") return { kind: "status" };
  if (grouping === "assignee") return { kind: "assignee" };
  if (grouping === "project") return { kind: "project" };
  const propertyId = propertyIdFromViewKey(grouping);
  if (propertyId) return { kind: "property", property_id: propertyId };
  return { kind: "none" };
}

export interface IssueTableGroupLabelContext {
  resolveStatusLabel: (status: string) => string;
  getActorName: (type: string, id: string) => string;
  groupProjectMap: ReadonlyMap<string, Project>;
  propertyById: ReadonlyMap<string, IssueProperty>;
  translate: {
    unassigned: string;
    noProject: string;
    noParent: string;
    noValue: string;
    valueUnavailable: string;
    customPropertyTrue: string;
    customPropertyFalse: string;
  };
}

/** Native `/groups` descriptor labels shared by TableView and the issue grid. */
export function issueTableGroupLabel(
  descriptor: IssueTableGroupDescriptor,
  {
    resolveStatusLabel,
    getActorName,
    groupProjectMap,
    propertyById,
    translate,
  }: IssueTableGroupLabelContext,
): string {
  const value = descriptor.value;
  if (value.kind === "status") {
    return resolveStatusLabel(value.status);
  }
  if (value.kind === "assignee") {
    return value.actor
      ? getActorName(value.actor.type, value.actor.id)
      : translate.unassigned;
  }
  if (value.kind === "project") {
    if (!value.project_id) return translate.noProject;
    return (
      groupProjectMap.get(value.project_id)?.title ?? translate.valueUnavailable
    );
  }
  if (value.kind === "parent") {
    if (value.value_state === "unset") return translate.noParent;
    return value.parent?.title ?? translate.valueUnavailable;
  }
  if (value.value_state === "unset") return translate.noValue;
  if (value.value_state === "unavailable") return translate.valueUnavailable;
  const property = propertyById.get(value.property_id);
  if (typeof value.value === "boolean") {
    return value.value
      ? translate.customPropertyTrue
      : translate.customPropertyFalse;
  }
  return (
    property?.config.options?.find((option) => option.id === value.value)
      ?.name ?? String(value.value ?? "")
  );
}