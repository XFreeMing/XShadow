"use client";

import { useMemo, useState } from "react";
import type { GridRecordOption } from "@multica/apitable-grid";
import { Button } from "@multica/ui/components/ui/button";
import type {
  GenericCellValue,
  GenericField,
  GenericRecord,
} from "@multica/core/tables";
import { useT } from "../../i18n";
import {
  buildCalendarMonthGrid,
  buildGanttModel,
  buildOrgTree,
  formatFieldValue,
  fromDayIndex,
  parseCalendarDate,
  resolveRecordTitle,
  todayCalendarDate,
  type OrgNode,
  type ValueFormatLabels,
} from "./view-helpers";
import { GalleryCardGrid } from "../../layout/gallery-card-grid";

interface CommonViewProps {
  fields: readonly GenericField[];
  visibleFields: readonly GenericField[];
  records: readonly GenericRecord[];
  recordOptionsByTable: Readonly<Record<string, readonly GridRecordOption[]>>;
  titleFieldId?: string;
  onOpenRecord: (recordId: string) => void;
}

function useFormatLabels(): ValueFormatLabels {
  const { t } = useT("tables");
  return useMemo(
    () => ({
      checked: t(($) => $.sheet.checked),
      unchecked: t(($) => $.sheet.unchecked),
      emptyValue: t(($) => $.sheet.empty_value),
    }),
    [t],
  );
}

// ---------------------------------------------------------------------------
// Kanban View
// ---------------------------------------------------------------------------
export function KanbanView({
  fields,
  visibleFields,
  records,
  recordOptionsByTable,
  titleFieldId,
  groupFieldId,
  onOpenRecord,
  onMoveRecord,
}: CommonViewProps & {
  groupFieldId?: string;
  onMoveRecord: (recordId: string, fieldId: string, value: GenericCellValue, baseRevision: number) => void;
}) {
  const { t } = useT("tables");
  const labels = useFormatLabels();
  const [dragRecordId, setDragRecordId] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);

  const groupField = groupFieldId
    ? fields.find((field) => field.id === groupFieldId && field.kind === "single_select") ?? null
    : null;

  if (!groupField) {
    return (
      <div className="p-6 text-body text-muted-foreground" data-view-empty="kanban">
        {t(($) => $.sheet.empty_kanban_field)}
      </div>
    );
  }

  const options = groupField.config.options ?? [];
  const validOptionIds = new Set(options.map((opt) => opt.id));
  const unassignedKey = "__unassigned__";
  const columns: Array<{ key: string; optionId: string | null; label: string; cards: GenericRecord[] }> = [
    ...options.map((opt) => ({
      key: opt.id,
      optionId: opt.id,
      label: opt.label,
      cards: [] as GenericRecord[],
    })),
    {
      key: unassignedKey,
      optionId: null,
      label: t(($) => $.sheet.unassigned),
      cards: [] as GenericRecord[],
    },
  ];
  const byKey = new Map(columns.map((col) => [col.key, col]));

  for (const record of records) {
    const raw = record.values[groupField.id];
    const bucketKey = typeof raw === "string" && validOptionIds.has(raw) ? raw : unassignedKey;
    byKey.get(bucketKey)!.cards.push(record);
  }

  const detailFields = visibleFields.filter((f) => f.id !== titleFieldId && f.id !== groupField.id).slice(0, 4);

  const handleDrop = (targetOptionId: string | null) => {
    setDropTarget(null);
    if (!dragRecordId) return;
    const record = records.find((item) => item.id === dragRecordId);
    setDragRecordId(null);
    if (!record) return;
    const current = record.values[groupField.id] ?? null;
    if (current === targetOptionId) return;
    onMoveRecord(record.id, groupField.id, targetOptionId, record.revision);
  };

  return (
    <div className="flex h-full gap-3 overflow-x-auto p-3" data-view-kind="kanban">
      {columns.map((column) => (
        <section
          key={column.key}
          data-kanban-column={column.key}
          aria-label={column.label}
          onDragOver={(event) => {
            event.preventDefault();
            if (dropTarget !== column.key) setDropTarget(column.key);
          }}
          onDragLeave={() => {
            if (dropTarget === column.key) setDropTarget(null);
          }}
          onDrop={(event) => {
            event.preventDefault();
            handleDrop(column.optionId);
          }}
          className={`flex w-72 shrink-0 flex-col rounded-md border border-border bg-muted/20 ${
            dropTarget === column.key ? "ring-2 ring-primary/50" : ""
          }`}
        >
          <header className="flex items-center justify-between border-b border-border px-3 py-2">
            <span className="truncate text-body font-medium">{column.label}</span>
            <span className="rounded bg-muted px-1.5 py-0.5 text-caption text-muted-foreground">
              {column.cards.length}
            </span>
          </header>
          <div className="flex flex-1 flex-col gap-2 overflow-y-auto p-2">
            {column.cards.length === 0 ? (
              <p className="px-1 py-2 text-caption text-muted-foreground">{t(($) => $.sheet.empty_records)}</p>
            ) : (
              column.cards.map((record) => {
                const title = resolveRecordTitle(record, fields, titleFieldId, recordOptionsByTable, labels);
                const currentOption = typeof record.values[groupField.id] === "string"
                  ? (record.values[groupField.id] as string)
                  : "";
                return (
                  <article
                    key={record.id}
                    data-record-id={record.id}
                    draggable
                    onDragStart={() => setDragRecordId(record.id)}
                    onDragEnd={() => {
                      setDragRecordId(null);
                      setDropTarget(null);
                    }}
                    className="rounded-md border border-border bg-background p-2.5 shadow-2xs"
                  >
                    <button
                      type="button"
                      className="block w-full truncate text-left text-body font-medium hover:underline"
                      onClick={() => onOpenRecord(record.id)}
                    >
                      {title}
                    </button>
                    {detailFields.length > 0 ? (
                      <dl className="mt-1.5 space-y-1">
                        {detailFields.map((field) => (
                          <div key={field.id} className="flex items-baseline justify-between gap-2 text-caption">
                            <dt className="shrink-0 text-muted-foreground">{field.name}</dt>
                            <dd className="min-w-0 truncate text-right">
                              {formatFieldValue(field, record.values[field.id], recordOptionsByTable, labels, record.errors?.[field.id])}
                            </dd>
                          </div>
                        ))}
                      </dl>
                    ) : null}
                    <div className="mt-2 flex items-center justify-between gap-2 border-t border-border/60 pt-1.5">
                      <label className="flex min-w-0 flex-1 items-center gap-1.5 text-caption text-muted-foreground">
                        <span className="shrink-0">{t(($) => $.sheet.move_to_group)}</span>
                        <select
                          aria-label={`${t(($) => $.sheet.move_to_group)} ${title}`}
                          value={validOptionIds.has(currentOption) ? currentOption : ""}
                          onChange={(event) => {
                            const nextVal = event.target.value || null;
                            if (nextVal !== (record.values[groupField.id] ?? null)) {
                              onMoveRecord(record.id, groupField.id, nextVal, record.revision);
                            }
                          }}
                          className="h-6 min-w-0 flex-1 rounded border border-border bg-transparent px-1 text-caption text-foreground"
                        >
                          <option value="">{t(($) => $.sheet.unassigned)}</option>
                          {options.map((opt) => (
                            <option key={opt.id} value={opt.id}>
                              {opt.label}
                            </option>
                          ))}
                        </select>
                      </label>
                    </div>
                  </article>
                );
              })
            )}
          </div>
        </section>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Gallery View
// ---------------------------------------------------------------------------
export function GalleryView({
  fields,
  visibleFields,
  records,
  recordOptionsByTable,
  titleFieldId,
  onOpenRecord,
}: CommonViewProps) {
  const { t } = useT("tables");
  const labels = useFormatLabels();

  if (records.length === 0) {
    return <GalleryCardGrid empty={t(($) => $.sheet.empty_records)} />;
  }

  const cardFields = visibleFields.filter((f) => f.id !== titleFieldId);

  return (
    <GalleryCardGrid>
      {records.map((record) => {
        const title = resolveRecordTitle(record, fields, titleFieldId, recordOptionsByTable, labels);
        return (
          <article
            key={record.id}
            data-record-id={record.id}
            className="flex flex-col rounded-md border border-border bg-background p-3 shadow-2xs transition-colors hover:border-foreground/30"
          >
            <button
              type="button"
              className="mb-2 block w-full truncate text-left text-body font-semibold hover:underline"
              onClick={() => onOpenRecord(record.id)}
            >
              {title}
            </button>
            <dl className="space-y-1.5">
              {cardFields.map((field) => (
                <div key={field.id} className="flex flex-col gap-0.5 text-caption">
                  <dt className="text-muted-foreground">{field.name}</dt>
                  <dd className="truncate text-foreground">
                    {formatFieldValue(field, record.values[field.id], recordOptionsByTable, labels, record.errors?.[field.id])}
                  </dd>
                </div>
              ))}
            </dl>
          </article>
        );
      })}
    </GalleryCardGrid>
  );
}

// ---------------------------------------------------------------------------
// Calendar View
// ---------------------------------------------------------------------------
const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;

export function CalendarView({
  fields,
  records,
  recordOptionsByTable,
  titleFieldId,
  dateFieldId,
  onOpenRecord,
}: CommonViewProps & {
  dateFieldId?: string;
}) {
  const { t } = useT("tables");
  const labels = useFormatLabels();
  const today = useMemo(() => todayCalendarDate(), []);

  const dateField = dateFieldId
    ? fields.find((field) => field.id === dateFieldId && field.kind === "date") ?? null
    : null;

  const initialAnchor = useMemo(() => {
    if (dateField) {
      for (const rec of records) {
        const parsed = parseCalendarDate(rec.values[dateField.id]);
        if (parsed) return { year: parsed.year, month: parsed.month };
      }
    }
    return { year: today.year, month: today.month };
  }, [dateField, records, today.month, today.year]);

  const [cursor, setCursor] = useState<{ year: number; month: number }>(() => initialAnchor);
  const [selectedIso, setSelectedIso] = useState<string | null>(null);

  if (!dateField) {
    return (
      <div className="p-6 text-body text-muted-foreground" data-view-empty="calendar">
        {t(($) => $.sheet.empty_date_field)}
      </div>
    );
  }

  const eventsByIso = new Map<string, GenericRecord[]>();
  const undated: GenericRecord[] = [];
  for (const record of records) {
    const parsed = parseCalendarDate(record.values[dateField.id]);
    if (!parsed) {
      undated.push(record);
      continue;
    }
    const list = eventsByIso.get(parsed.iso);
    if (list) list.push(record);
    else eventsByIso.set(parsed.iso, [record]);
  }

  const cells = buildCalendarMonthGrid(cursor.year, cursor.month);
  const monthLabel = `${cursor.year}-${String(cursor.month).padStart(2, "0")}`;

  const stepMonth = (delta: number) => {
    setCursor((prev) => {
      const nextIndex = prev.year * 12 + (prev.month - 1) + delta;
      return { year: Math.floor(nextIndex / 12), month: (nextIndex % 12) + 1 };
    });
  };

  const selectedEvents = selectedIso ? (eventsByIso.get(selectedIso) ?? []) : [];

  return (
    <div className="flex h-full flex-col gap-2 p-3" data-view-kind="calendar">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5">
          <Button type="button" variant="outline" size="xs" onClick={() => stepMonth(-1)}>
            {t(($) => $.sheet.prev)}
          </Button>
          <Button type="button" variant="outline" size="xs" onClick={() => setCursor({ year: today.year, month: today.month })}>
            {t(($) => $.sheet.today)}
          </Button>
          <Button type="button" variant="outline" size="xs" onClick={() => stepMonth(1)}>
            {t(($) => $.sheet.next)}
          </Button>
          <span className="ml-2 text-body font-semibold" data-calendar-month={monthLabel}>
            {monthLabel}
          </span>
        </div>
        {undated.length > 0 ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.sheet.unscheduled)}: {undated.length}
          </span>
        ) : null}
      </div>

      <div className="grid grid-cols-7 border-l border-t border-border">
        {WEEKDAYS.map((day) => (
          <div
            key={day}
            className="border-b border-r border-border bg-muted/30 px-2 py-1 text-caption font-medium text-muted-foreground"
          >
            {day}
          </div>
        ))}
        {cells.map((cell) => {
          const dayEvents = eventsByIso.get(cell.date.iso) ?? [];
          const isToday = cell.date.iso === today.iso;
          const isSelected = cell.date.iso === selectedIso;
          return (
            <div
              key={cell.date.iso}
              data-calendar-date={cell.date.iso}
              onClick={() => setSelectedIso(cell.date.iso)}
              className={`min-h-24 border-b border-r border-border p-1.5 ${
                cell.inMonth ? "bg-background" : "bg-muted/20 text-muted-foreground"
              } ${isSelected ? "ring-1 ring-inset ring-primary" : ""}`}
            >
              <div className="mb-1 flex items-center justify-between">
                <span
                  className={`inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1 text-caption ${
                    isToday ? "bg-primary text-primary-foreground font-semibold" : ""
                  }`}
                >
                  {cell.date.day}
                </span>
              </div>
              <div className="space-y-1">
                {dayEvents.map((record) => {
                  const title = resolveRecordTitle(record, fields, titleFieldId, recordOptionsByTable, labels);
                  return (
                    <button
                      key={record.id}
                      type="button"
                      data-record-id={record.id}
                      onClick={(event) => {
                        event.stopPropagation();
                        onOpenRecord(record.id);
                      }}
                      className="block w-full truncate rounded bg-primary/10 px-1.5 py-0.5 text-left text-caption text-foreground hover:bg-primary/20"
                    >
                      {title}
                    </button>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>

      {selectedIso ? (
        <div className="rounded-md border border-border bg-muted/10 p-2.5" data-calendar-selected={selectedIso}>
          <div className="mb-1.5 text-caption font-medium text-muted-foreground">
            {selectedIso} ({selectedEvents.length})
          </div>
          {selectedEvents.length === 0 ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.sheet.empty_records)}</p>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {selectedEvents.map((record) => (
                <button
                  key={record.id}
                  type="button"
                  onClick={() => onOpenRecord(record.id)}
                  className="rounded border border-border bg-background px-2 py-1 text-caption hover:bg-accent"
                >
                  {resolveRecordTitle(record, fields, titleFieldId, recordOptionsByTable, labels)}
                </button>
              ))}
            </div>
          )}
        </div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Gantt View
// ---------------------------------------------------------------------------
const GANTT_WINDOW_DAYS = 28;

export function GanttView({
  fields,
  records,
  recordOptionsByTable,
  titleFieldId,
  dateFieldId,
  endDateFieldId,
  onOpenRecord,
}: CommonViewProps & {
  dateFieldId?: string;
  endDateFieldId?: string;
}) {
  const { t } = useT("tables");
  const labels = useFormatLabels();

  const startField = dateFieldId
    ? fields.find((field) => field.id === dateFieldId && field.kind === "date") ?? null
    : null;

  const model = useMemo(
    () => buildGanttModel(records, startField?.id, endDateFieldId),
    [endDateFieldId, records, startField?.id],
  );

  const [windowStart, setWindowStart] = useState<number>(() => model.anchorDayIndex);

  if (!startField) {
    return (
      <div className="p-6 text-body text-muted-foreground" data-view-empty="gantt">
        {t(($) => $.sheet.empty_date_field)}
      </div>
    );
  }

  const windowEnd = windowStart + GANTT_WINDOW_DAYS - 1;
  const headerDays = Array.from({ length: GANTT_WINDOW_DAYS }, (_, idx) => fromDayIndex(windowStart + idx));
  const rangeLabel = `${headerDays[0]!.iso} ~ ${headerDays[headerDays.length - 1]!.iso}`;

  return (
    <div className="flex h-full flex-col gap-3 p-3" data-view-kind="gantt">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5">
          <Button type="button" variant="outline" size="xs" onClick={() => setWindowStart((v) => v - 14)}>
            {t(($) => $.sheet.prev)}
          </Button>
          <Button type="button" variant="outline" size="xs" onClick={() => setWindowStart(model.anchorDayIndex)}>
            {t(($) => $.sheet.today)}
          </Button>
          <Button type="button" variant="outline" size="xs" onClick={() => setWindowStart((v) => v + 14)}>
            {t(($) => $.sheet.next)}
          </Button>
          <span className="ml-2 font-mono text-caption text-muted-foreground" data-gantt-range={rangeLabel}>
            {rangeLabel}
          </span>
        </div>
      </div>

      <div className="overflow-x-auto rounded-md border border-border">
        <div className="min-w-[920px]">
          <div className="grid border-b border-border bg-muted/30" style={{ gridTemplateColumns: `200px repeat(${GANTT_WINDOW_DAYS}, minmax(24px, 1fr))` }}>
            <div className="border-r border-border px-2 py-1.5 text-caption font-medium text-muted-foreground">
              {t(($) => $.sheet.title_field)}
            </div>
            {headerDays.map((day) => (
              <div
                key={day.iso}
                data-gantt-day={day.iso}
                title={day.iso}
                className="border-r border-border/50 px-0.5 py-1.5 text-center font-mono text-[10px] text-muted-foreground"
              >
                {day.day === 1 ? `${day.month}/${day.day}` : day.day}
              </div>
            ))}
          </div>

          {model.scheduled.length === 0 ? (
            <p className="p-3 text-body text-muted-foreground">{t(($) => $.sheet.empty_records)}</p>
          ) : (
            model.scheduled.map((bar) => {
              const title = resolveRecordTitle(bar.record, fields, titleFieldId, recordOptionsByTable, labels);
              const visibleStart = Math.max(bar.startIndex, windowStart);
              const visibleEnd = Math.min(bar.endIndex, windowEnd);
              const inWindow = visibleStart <= visibleEnd;
              const colStart = visibleStart - windowStart + 2;
              const colSpan = visibleEnd - visibleStart + 1;
              return (
                <div
                  key={bar.record.id}
                  data-record-id={bar.record.id}
                  className="grid items-center border-b border-border/60 last:border-b-0"
                  style={{ gridTemplateColumns: `200px repeat(${GANTT_WINDOW_DAYS}, minmax(24px, 1fr))` }}
                >
                  <button
                    type="button"
                    onClick={() => onOpenRecord(bar.record.id)}
                    className="truncate border-r border-border px-2 py-2 text-left text-caption font-medium hover:underline"
                    title={`${title} (${bar.start.iso} ~ ${bar.end.iso})`}
                  >
                    {title}
                  </button>
                  {inWindow ? (
                    <button
                      type="button"
                      data-gantt-bar={bar.record.id}
                      onClick={() => onOpenRecord(bar.record.id)}
                      title={`${bar.start.iso} ~ ${bar.end.iso}`}
                      style={{ gridColumn: `${colStart} / span ${colSpan}` }}
                      className="mx-0.5 h-6 truncate rounded bg-primary/80 px-1.5 text-left text-caption text-primary-foreground hover:bg-primary"
                    >
                      {title}
                    </button>
                  ) : (
                    <div
                      style={{ gridColumn: `2 / span ${GANTT_WINDOW_DAYS}` }}
                      className="px-2 text-caption text-muted-foreground"
                    >
                      {bar.start.iso} ~ {bar.end.iso}
                    </div>
                  )}
                </div>
              );
            })
          )}
        </div>
      </div>

      {model.unscheduled.length > 0 ? (
        <div className="rounded-md border border-border bg-muted/10 p-2.5" data-gantt-unscheduled="">
          <div className="mb-1.5 text-caption font-medium text-muted-foreground">
            {t(($) => $.sheet.unscheduled)} ({model.unscheduled.length})
          </div>
          <div className="flex flex-wrap gap-1.5">
            {model.unscheduled.map(({ record, reason }) => (
              <button
                key={record.id}
                type="button"
                data-record-id={record.id}
                onClick={() => onOpenRecord(record.id)}
                className="flex items-center gap-1.5 rounded border border-border bg-background px-2 py-1 text-caption hover:bg-accent"
              >
                <span>{resolveRecordTitle(record, fields, titleFieldId, recordOptionsByTable, labels)}</span>
                {reason === "invalid" ? (
                  <span className="text-destructive">({t(($) => $.sheet.invalid_date_range)})</span>
                ) : null}
              </button>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Org Chart View
// ---------------------------------------------------------------------------
export function OrgChartView({
  fields,
  visibleFields,
  records,
  recordOptionsByTable,
  titleFieldId,
  parentFieldId,
  onOpenRecord,
}: CommonViewProps & {
  parentFieldId?: string;
}) {
  const { t } = useT("tables");
  const labels = useFormatLabels();

  const parentField = parentFieldId
    ? fields.find((field) => field.id === parentFieldId && field.kind === "relation") ?? null
    : null;

  const tree = useMemo(
    () => buildOrgTree(records, parentField?.id),
    [parentField?.id, records],
  );

  if (!parentField) {
    return (
      <div className="p-6 text-body text-muted-foreground" data-view-empty="org_chart">
        {t(($) => $.sheet.empty_parent_field)}
      </div>
    );
  }

  if (records.length === 0) {
    return (
      <div className="p-6 text-body text-muted-foreground" data-view-kind="org_chart">
        {t(($) => $.sheet.empty_records)}
      </div>
    );
  }

  const summaryFields = visibleFields.filter((f) => f.id !== titleFieldId && f.id !== parentField.id).slice(0, 2);

  const renderNode = (node: OrgNode) => {
    const title = resolveRecordTitle(node.record, fields, titleFieldId, recordOptionsByTable, labels);
    return (
      <li key={node.record.id} className="flex flex-col items-start" data-org-node={node.record.id}>
        <article className="min-w-52 max-w-xs rounded-md border border-border bg-background p-2.5 shadow-2xs">
          <div className="flex items-center justify-between gap-2">
            <button
              type="button"
              onClick={() => onOpenRecord(node.record.id)}
              className="truncate text-left text-body font-medium hover:underline"
            >
              {title}
            </button>
            {node.status === "cycle" ? (
              <span className="shrink-0 rounded bg-destructive/10 px-1.5 py-0.5 text-[10px] text-destructive">
                {t(($) => $.sheet.org_cycle)}
              </span>
            ) : node.status === "missing_parent" ? (
              <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                {t(($) => $.sheet.org_parent_hidden)}
              </span>
            ) : null}
          </div>
          {summaryFields.length > 0 ? (
            <dl className="mt-1 space-y-0.5">
              {summaryFields.map((field) => (
                <div key={field.id} className="flex items-baseline justify-between gap-2 text-caption">
                  <dt className="shrink-0 text-muted-foreground">{field.name}</dt>
                  <dd className="min-w-0 truncate">
                    {formatFieldValue(field, node.record.values[field.id], recordOptionsByTable, labels, node.record.errors?.[field.id])}
                  </dd>
                </div>
              ))}
            </dl>
          ) : null}
        </article>
        {node.children.length > 0 ? (
          <ul className="mt-2 ml-5 space-y-2 border-l-2 border-border pl-4">
            {node.children.map(renderNode)}
          </ul>
        ) : null}
      </li>
    );
  };

  return (
    <div className="h-full overflow-auto p-4" data-view-kind="org_chart">
      <ul className="flex flex-wrap items-start gap-6">
        {tree.map(renderNode)}
      </ul>
    </div>
  );
}
