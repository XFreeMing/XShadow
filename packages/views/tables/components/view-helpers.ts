import type { GridRecordOption } from "@multica/apitable-grid";
import type {
  GenericCellValue,
  GenericField,
  GenericRecord,
  GenericViewConfig,
  GenericViewKind,
} from "@multica/core/tables";

export interface ValueFormatLabels {
  checked: string;
  unchecked: string;
  emptyValue: string;
}

export function isEditableField(field: GenericField): boolean {
  return field.kind !== "lookup" && field.kind !== "formula";
}

export function isSameTableOneWayRelation(field: GenericField, tableId: string): boolean {
  return (
    field.kind === "relation" &&
    field.config.target_table_id?.toLowerCase() === tableId.toLowerCase() &&
    field.config.bidirectional !== true
  );
}

export function flattenCellValue(value: GenericCellValue): Array<string | number | boolean | null> {
  if (!Array.isArray(value)) return [value];
  const out: Array<string | number | boolean | null> = [];
  for (const item of value) {
    out.push(...flattenCellValue(item));
  }
  return out;
}

export function formatFieldValue(
  field: GenericField,
  raw: GenericCellValue | undefined,
  recordOptionsByTable: Readonly<Record<string, readonly GridRecordOption[]>>,
  labels: ValueFormatLabels,
  error?: string,
): string {
  if (error) return `#ERR: ${error}`;
  if (raw === undefined || raw === null || raw === "") return labels.emptyValue;
  if (field.kind === "checkbox") {
    return raw === true ? labels.checked : raw === false ? labels.unchecked : labels.emptyValue;
  }
  if (field.kind === "single_select" && typeof raw === "string") {
    return field.config.options?.find((opt) => opt.id === raw)?.label ?? raw;
  }
  if (field.kind === "relation" && Array.isArray(raw)) {
    const ids = raw.filter((item): item is string => typeof item === "string");
    if (ids.length === 0) return labels.emptyValue;
    const options = field.config.target_table_id
      ? new Map((recordOptionsByTable[field.config.target_table_id] ?? []).map((item) => [item.id, item.label]))
      : null;
    return ids.map((id) => options?.get(id) ?? `#${id.slice(0, 6)}`).join(", ");
  }
  if (field.kind === "lookup" && Array.isArray(raw)) {
    const flat = flattenCellValue(raw).filter((item) => item !== null && item !== "");
    if (flat.length === 0) return labels.emptyValue;
    const targetOptions = field.config.target_relation_table_id
      ? new Map((recordOptionsByTable[field.config.target_relation_table_id] ?? []).map((item) => [item.id, item.label]))
      : null;
    const selectOptions = field.config.target_options
      ? new Map(field.config.target_options.map((opt) => [opt.id, opt.label]))
      : null;
    return flat
      .map((item) => {
        if (typeof item === "boolean") return item ? labels.checked : labels.unchecked;
        if (typeof item === "string") return selectOptions?.get(item) ?? targetOptions?.get(item) ?? item;
        return String(item);
      })
      .join(", ");
  }
  if (Array.isArray(raw)) {
    const flat = flattenCellValue(raw).filter((item) => item !== null && item !== "");
    return flat.length > 0 ? flat.map(String).join(", ") : labels.emptyValue;
  }
  return String(raw);
}

export function resolveRecordTitle(
  record: GenericRecord,
  fields: readonly GenericField[],
  titleFieldId: string | undefined,
  recordOptionsByTable: Readonly<Record<string, readonly GridRecordOption[]>>,
  labels: ValueFormatLabels,
): string {
  const preferred = titleFieldId ? fields.find((item) => item.id === titleFieldId) : undefined;
  const candidates = preferred
    ? [preferred]
    : [
        ...fields.filter((item) => item.kind === "text"),
        ...fields.filter((item) => item.kind !== "text"),
      ];
  for (const field of candidates) {
    const raw = record.values[field.id];
    const isEmpty = raw === undefined || raw === null || raw === "" || (Array.isArray(raw) && raw.length === 0);
    if (!isEmpty) {
      const formatted = formatFieldValue(field, raw, recordOptionsByTable, labels, record.errors?.[field.id]);
      if (formatted && formatted !== labels.emptyValue) {
        return formatted.split("\n")[0]!.slice(0, 80);
      }
    }
  }
  return `#${record.id.slice(0, 8)}`;
}

export interface EffectiveViewConfig {
  groupFieldId?: string;
  titleFieldId?: string;
  dateFieldId?: string;
  endDateFieldId?: string;
  parentFieldId?: string;
  requiredFieldIds: string[];
}

export function resolveEffectiveViewConfig(
  kind: GenericViewKind,
  saved: GenericViewConfig,
  fields: readonly GenericField[],
  tableId: string,
): EffectiveViewConfig {
  const byId = new Map(fields.map((field) => [field.id, field]));
  const titleFieldId = saved.title_field_id && byId.has(saved.title_field_id)
    ? saved.title_field_id
    : fields.find((field) => field.kind === "text")?.id ?? fields[0]?.id;

  const groupFieldId = kind === "kanban"
    ? (saved.group_field_id && byId.get(saved.group_field_id)?.kind === "single_select"
        ? saved.group_field_id
        : fields.find((field) => field.kind === "single_select")?.id)
    : undefined;

  const dateFields = fields.filter((field) => field.kind === "date");
  const dateFieldId = kind === "calendar" || kind === "gantt"
    ? (saved.date_field_id && byId.get(saved.date_field_id)?.kind === "date"
        ? saved.date_field_id
        : dateFields[0]?.id)
    : undefined;

  const endDateFieldId = kind === "gantt"
    ? (saved.end_date_field_id && byId.get(saved.end_date_field_id)?.kind === "date"
        ? saved.end_date_field_id
        : dateFields.find((field) => field.id !== dateFieldId)?.id)
    : undefined;

  const parentFieldId = kind === "org_chart"
    ? (saved.parent_field_id && (() => {
        const f = byId.get(saved.parent_field_id);
        return f ? isSameTableOneWayRelation(f, tableId) : false;
      })()
        ? saved.parent_field_id
        : fields.find((field) => isSameTableOneWayRelation(field, tableId))?.id)
    : undefined;

  const requiredFieldIds = kind === "form"
    ? (saved.required_field_ids ?? []).filter((id) => {
        const f = byId.get(id);
        return Boolean(f && isEditableField(f));
      })
    : [];

  return {
    groupFieldId,
    titleFieldId,
    dateFieldId,
    endDateFieldId,
    parentFieldId,
    requiredFieldIds,
  };
}

export {
  type CalendarCell,
  type CalendarDate,
  buildCalendarMonthGrid,
  calendarGridDateRange,
  calendarMonthKey,
  dayOfWeek,
  daysInMonth,
  formatCalendarMonthLabel,
  formatIsoDate,
  fromDayIndex,
  isLeapYear,
  parseCalendarDate,
  todayCalendarDate,
  toDayIndex,
} from "../../layout/calendar-dates";

import {
  parseCalendarDate,
  todayCalendarDate,
  toDayIndex,
  type CalendarDate,
} from "../../layout/calendar-dates";

export interface GanttBar {
  record: GenericRecord;
  start: CalendarDate;
  end: CalendarDate;
  startIndex: number;
  endIndex: number;
}

export interface GanttModel {
  scheduled: GanttBar[];
  unscheduled: Array<{ record: GenericRecord; reason: "missing" | "invalid" }>;
  anchorDayIndex: number;
}

export function buildGanttModel(
  records: readonly GenericRecord[],
  startFieldId: string | undefined,
  endFieldId: string | undefined,
): GanttModel {
  const scheduled: GanttBar[] = [];
  const unscheduled: Array<{ record: GenericRecord; reason: "missing" | "invalid" }> = [];
  if (!startFieldId) {
    return {
      scheduled,
      unscheduled: records.map((record) => ({ record, reason: "missing" })),
      anchorDayIndex: toDayIndex(todayCalendarDate()),
    };
  }
  for (const record of records) {
    const rawStart = record.values[startFieldId];
    const start = parseCalendarDate(rawStart);
    if (!start) {
      unscheduled.push({
        record,
        reason: rawStart == null || rawStart === "" ? "missing" : "invalid",
      });
      continue;
    }
    let end = start;
    if (endFieldId) {
      const rawEnd = record.values[endFieldId];
      if (rawEnd != null && rawEnd !== "") {
        const parsedEnd = parseCalendarDate(rawEnd);
        if (!parsedEnd || toDayIndex(parsedEnd) < toDayIndex(start)) {
          unscheduled.push({ record, reason: "invalid" });
          continue;
        }
        end = parsedEnd;
      }
    }
    scheduled.push({
      record,
      start,
      end,
      startIndex: toDayIndex(start),
      endIndex: toDayIndex(end),
    });
  }
  const anchorDayIndex = scheduled.length > 0
    ? Math.min(...scheduled.map((bar) => bar.startIndex))
    : toDayIndex(todayCalendarDate());
  return { scheduled, unscheduled, anchorDayIndex };
}

export interface OrgNode {
  record: GenericRecord;
  children: OrgNode[];
  status: "normal" | "cycle" | "missing_parent";
}

export function buildOrgTree(
  records: readonly GenericRecord[],
  parentFieldId: string | undefined,
): OrgNode[] {
  const byId = new Map<string, GenericRecord>(records.map((rec) => [rec.id, rec]));
  const parentOf = new Map<string, string | null>();
  const statusOf = new Map<string, "normal" | "cycle" | "missing_parent">();

  for (const record of records) {
    let parentId: string | null = null;
    if (parentFieldId) {
      const raw = record.values[parentFieldId];
      if (Array.isArray(raw)) {
        const first = raw.find((item): item is string => typeof item === "string");
        if (first) parentId = first;
      }
    }
    if (parentId === record.id) {
      parentOf.set(record.id, null);
      statusOf.set(record.id, "cycle");
    } else if (parentId && !byId.has(parentId)) {
      parentOf.set(record.id, null);
      statusOf.set(record.id, "missing_parent");
    } else {
      parentOf.set(record.id, parentId);
      statusOf.set(record.id, "normal");
    }
  }

  // Detect and break multi-node cycles deterministically so every record renders safely.
  const visited = new Set<string>();
  for (const record of records) {
    if (visited.has(record.id)) continue;
    const path: string[] = [];
    const onPath = new Map<string, number>();
    let current: string | null = record.id;
    while (current && !visited.has(current)) {
      const cycleStart = onPath.get(current);
      if (cycleStart !== undefined) {
        // Break the cycle at the earliest record in the cycle so all nodes stay visible.
        const cycleMember = path[cycleStart]!;
        parentOf.set(cycleMember, null);
        statusOf.set(cycleMember, "cycle");
        break;
      }
      onPath.set(current, path.length);
      path.push(current);
      current = parentOf.get(current) ?? null;
    }
    for (const id of path) visited.add(id);
  }

  const childrenMap = new Map<string, GenericRecord[]>();
  const roots: GenericRecord[] = [];
  for (const record of records) {
    const pid = parentOf.get(record.id) ?? null;
    if (!pid) {
      roots.push(record);
    } else {
      const list = childrenMap.get(pid);
      if (list) list.push(record);
      else childrenMap.set(pid, [record]);
    }
  }

  const emitted = new Set<string>();
  const buildNode = (record: GenericRecord): OrgNode => {
    emitted.add(record.id);
    const kids = (childrenMap.get(record.id) ?? []).filter((child) => !emitted.has(child.id));
    return {
      record,
      status: statusOf.get(record.id) ?? "normal",
      children: kids.map(buildNode),
    };
  };

  const result = roots.map(buildNode);
  // Safety net: ensure any unvisited record is still appended as a root.
  for (const record of records) {
    if (!emitted.has(record.id)) {
      result.push(buildNode(record));
    }
  }
  return result;
}
