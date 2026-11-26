"use client";

import { useEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { errorCode } from "@multica/core/api";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useViewStore, useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import type { GanttZoom } from "@multica/core/issues/stores/view-store";
import { projectListOptions } from "@multica/core/projects/queries";
import { propertyListOptions } from "@multica/core/properties";
import type { Issue, IssueProperty, IssueStatusCategory } from "@multica/core/types";
import { issueStatusCategory, statusColumnKeys } from "@multica/core/issues";
import { dateOnlyToUTCDate } from "@multica/core/issues/date";
import { cn } from "@multica/ui/lib/utils";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import { Button } from "@multica/ui/components/ui/button";
import { AppLink } from "../../navigation";
import {
  PEEK_TARGET_ATTR,
  useIsIssuePeeked,
  useIssuePeekActions,
  useIssuePeekLinkProps,
} from "../surface/peek-context";
import { ActorAvatar } from "../../common/actor-avatar";
import { ProjectIcon } from "../../projects/components/project-icon";
import { StatusIcon } from "./status-icon";
import { PriorityIcon } from "./priority-icon";
import { IssueActionsContextMenu } from "../actions";
import { DueDatePicker } from "./pickers/due-date-picker";
import { StartDatePicker } from "./pickers/start-date-picker";
import {
  buildGanttDateUpdateRequest,
  computeDueDateEditPatch,
  computeStartDateEditPatch,
  type GanttDatePatch,
} from "../utils/gantt-schedule";
import { sortIssues } from "../utils/sort";
import { useLocale, useT } from "../../i18n";
import { IssuePrototypePreview } from "./issue-prototype-preview";
import { CustomPropertyValueDisplay } from "./pickers/custom-property-picker";
import { resolveCardCustomProperties } from "../utils/card-custom-properties";
import { ComputedPropertyViewBoundary } from "./computed-property-view-boundary";

// Date utilities — everything is UTC-day-aligned so a `due_date` ISO string
// produced anywhere maps to exactly one column on the axis.

const MS_PER_DAY = 24 * 60 * 60 * 1000;

function startOfDayUTC(d: Date): Date {
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate()));
}

function addDays(d: Date, days: number): Date {
  return new Date(d.getTime() + days * MS_PER_DAY);
}

function daysBetween(a: Date, b: Date): number {
  return Math.round((b.getTime() - a.getTime()) / MS_PER_DAY);
}

// Issue dates arrive as date-only "YYYY-MM-DD" strings (calendar days). Anchor
// each to UTC midnight so the bar lands on exactly that day, independent of the
// viewer's timezone. See @multica/core/issues/date.
function parseDay(iso: string | null): Date | null {
  return dateOnlyToUTCDate(iso);
}

function isWeekendUTC(d: Date): boolean {
  const wd = d.getUTCDay();
  return wd === 0 || wd === 6;
}

function isMonthStartUTC(d: Date): boolean {
  return d.getUTCDate() === 1;
}

function isWeekStartUTC(d: Date): boolean {
  return d.getUTCDay() === 1; // Monday
}

// Geometry

const ROW_HEIGHT = 36;
const HEADER_HEIGHT = 56;
const ISSUE_COLUMN_WIDTH = 260;
const DATE_COLUMN_WIDTH = 108;
const LEFT_COL_WIDTH = ISSUE_COLUMN_WIDTH + 2 * DATE_COLUMN_WIDTH;
const DATE_CELL_TRIGGER = "inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded px-1 text-caption hover:bg-accent disabled:opacity-50";

const DAY_PX_BY_ZOOM: Record<GanttZoom, number> = {
  day: 36,
  week: 14,
  month: 6,
};

interface Range {
  start: Date;
  end: Date;
}

function computeRange(issues: Issue[], today: Date, zoom: GanttZoom): Range {
  const defaultPad: Record<GanttZoom, number> = {
    day: 21,
    week: 60,
    month: 180,
  };
  let minTs = today.getTime() - defaultPad[zoom] * MS_PER_DAY;
  let maxTs = today.getTime() + defaultPad[zoom] * MS_PER_DAY;
  for (const i of issues) {
    const s = parseDay(i.start_date);
    const e = parseDay(i.due_date);
    if (s && s.getTime() < minTs) minTs = s.getTime();
    if (e && e.getTime() > maxTs) maxTs = e.getTime();
    if (s && s.getTime() > maxTs) maxTs = s.getTime();
    if (e && e.getTime() < minTs) minTs = e.getTime();
  }
  const pad = Math.max(2, Math.round(defaultPad[zoom] / 6));
  return {
    start: addDays(startOfDayUTC(new Date(minTs)), -pad),
    end: addDays(startOfDayUTC(new Date(maxTs)), pad + 1),
  };
}

// Top axis — sticky on vertical scroll. Renders month + day/week ticks.

function GanttAxis({
  range,
  dayPx,
  zoom,
  todayOffsetDays,
  width,
}: {
  range: Range;
  dayPx: number;
  zoom: GanttZoom;
  todayOffsetDays: number;
  width: number;
}) {
  const locale = useLocale();
  const totalDays = daysBetween(range.start, range.end);

  const monthBlocks = useMemo(() => {
    const out: { label: string; left: number; width: number }[] = [];
    let cursor = startOfDayUTC(range.start);
    while (cursor.getTime() < range.end.getTime()) {
      const monthEnd = new Date(
        Date.UTC(cursor.getUTCFullYear(), cursor.getUTCMonth() + 1, 1),
      );
      const blockEnd = monthEnd.getTime() > range.end.getTime() ? range.end : monthEnd;
      const startDays = daysBetween(range.start, cursor);
      const widthDays = daysBetween(cursor, blockEnd);
      out.push({
        label: cursor.toLocaleDateString(locale, {
          month: "short",
          year: "numeric",
          timeZone: "UTC",
        }),
        left: startDays * dayPx,
        width: widthDays * dayPx,
      });
      cursor = monthEnd;
    }
    return out;
  }, [range, dayPx, locale]);

  return (
    <div
      className="relative shrink-0 border-b bg-background"
      style={{ height: HEADER_HEIGHT, width }}
    >
      {/* Month row */}
      <div className="relative h-7 border-b">
        {monthBlocks.map((b, i) => (
          <div
            key={i}
            className="absolute top-0 bottom-0 flex items-center px-2 text-caption font-medium text-foreground"
            style={{ left: b.left, width: b.width }}
          >
            {b.width > 40 && <span className="truncate">{b.label}</span>}
          </div>
        ))}
      </div>
      {/* Day / week ticks */}
      <div className="relative h-7">
        {Array.from({ length: totalDays }, (_, i) => {
          const date = addDays(range.start, i);
          const isMonth = isMonthStartUTC(date);
          const isWeek = isWeekStartUTC(date);
          const showLabel =
            zoom === "day" ||
            (zoom === "week" && isWeek) ||
            (zoom === "month" && isMonth);
          return (
            <div
              key={i}
              className={cn(
                "absolute top-0 bottom-0 flex items-center justify-center text-micro text-muted-foreground border-l",
                isMonth
                  ? "border-foreground/15"
                  : isWeek
                  ? "border-foreground/10"
                  : "border-foreground/5",
              )}
              style={{ left: i * dayPx, width: dayPx }}
            >
              {showLabel && (
                <div className="flex flex-col items-center leading-tight">
                  {zoom === "day" && (
                    <>
                      <span className="tabular-nums">{date.getUTCDate()}</span>
                      <span className="text-micro">
                        {date.toLocaleDateString(locale, {
                          weekday: "short",
                          timeZone: "UTC",
                        })}
                      </span>
                    </>
                  )}
                  {zoom === "week" && (
                    <span className="tabular-nums">{date.getUTCDate()}</span>
                  )}
                  {zoom === "month" && (
                    <span className="tabular-nums whitespace-nowrap">
                      {date.toLocaleDateString(locale, {
                        month: "short",
                        day: "numeric",
                        timeZone: "UTC",
                      })}
                    </span>
                  )}
                </div>
              )}
            </div>
          );
        })}
        {todayOffsetDays >= 0 && todayOffsetDays <= totalDays && (
          <div
            className="absolute top-0 bottom-0 w-px bg-brand"
            style={{ left: todayOffsetDays * dayPx }}
          />
        )}
      </div>
    </div>
  );
}

// Background layer — weekend shading, week/month gridlines, today line.
// Rendered once across the full timeline track height behind all bars.

function BackgroundLayer({
  range,
  dayPx,
  height,
  todayOffsetDays,
}: {
  range: Range;
  dayPx: number;
  height: number;
  todayOffsetDays: number;
}) {
  const totalDays = daysBetween(range.start, range.end);
  return (
    <div
      className="pointer-events-none absolute inset-0"
      style={{ height, width: totalDays * dayPx }}
    >
      {Array.from({ length: totalDays }, (_, i) => {
        const date = addDays(range.start, i);
        const weekend = isWeekendUTC(date);
        const isMonth = isMonthStartUTC(date);
        const isWeek = isWeekStartUTC(date);
        return (
          <div
            key={i}
            className="absolute top-0 bottom-0"
            style={{ left: i * dayPx, width: dayPx }}
          >
            {weekend && <div className="absolute inset-0 bg-muted/40" />}
            {(isMonth || isWeek) && (
              <div
                className={cn(
                  "absolute top-0 bottom-0 left-0 w-px",
                  isMonth ? "bg-foreground/10" : "bg-foreground/5",
                )}
              />
            )}
          </div>
        );
      })}
      {todayOffsetDays >= 0 && todayOffsetDays <= totalDays && (
        <div
          className="absolute top-0 bottom-0 w-px bg-brand/70"
          style={{ left: todayOffsetDays * dayPx }}
        />
      )}
    </div>
  );
}

// Bar color by status (uses semantic Tailwind tokens, not hardcoded colors).

// Keyed by CATEGORY, not by status key: an issue on a custom status draws in
// the color of the category it behaves as. Keying this by IssueStatus made the
// lookup `undefined` for every custom key, so the bar lost its color entirely.
// (MUL-6243)
const STATUS_BAR_BG: Record<IssueStatusCategory, string> = {
  unstarted: "bg-muted-foreground/70",
  started: "bg-warning",
  done: "bg-info",
  closed: "bg-muted-foreground/40",
};

// One row — left label cell + right timeline track with absolute bar.

function ScheduledRow({
  issue,
  range,
  dayPx,
  totalDays,
  onPreview,
  workspaceProperties,
  cardPropertyIds,
}: {
  issue: Issue;
  range: Range;
  dayPx: number;
  totalDays: number;
  onPreview: () => void;
  workspaceProperties: IssueProperty[];
  cardPropertyIds: readonly string[];
}) {
  const { t } = useT("issues");
  const locale = useLocale();
  const p = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const { colorOf, iconOf } = useIssueStatuses(wsId);
  const { data: projects = [] } = useQuery({
    ...projectListOptions(wsId),
    enabled: !!issue.project_id,
  });
  const project = issue.project_id ? projects.find((pr) => pr.id === issue.project_id) : undefined;
  const cardCustomProperties = resolveCardCustomProperties(
    cardPropertyIds,
    workspaceProperties,
    issue,
  );
  const updateIssue = useUpdateIssue();
  const commitDates = (patch: GanttDatePatch | null) => {
    if (!patch || updateIssue.isPending) return;
    updateIssue.mutate(
      { id: issue.id, ...buildGanttDateUpdateRequest(issue, patch) },
      {
        onError: (error) => toast.error(
          errorCode(error) === "revision_conflict"
            ? t(($) => $.revision.conflict)
            : t(($) => $.detail.update_failed),
        ),
      },
    );
  };
  const peeked = useIsIssuePeeked(issue.id);
  const peekLinkProps = useIssuePeekLinkProps(issue.id);
  const linkProps = {
    ...peekLinkProps,
    "aria-haspopup": "dialog" as const,
    onClick: (event: MouseEvent<HTMLAnchorElement>) => {
      if (event.defaultPrevented) return;
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
        peekLinkProps.onClick?.(event);
        return;
      }
      event.preventDefault();
      onPreview();
    },
  };

  const start = parseDay(issue.start_date);
  const due = parseDay(issue.due_date);

  // start > due is a data anomaly (backend only validates RFC3339, not order).
  // Normalize to min/max so the row still draws something, and flag it so the
  // user notices instead of seeing a silently empty row.
  const inverted =
    start !== null && due !== null && start.getTime() > due.getTime();
  const rangeStart = start && due ? (inverted ? due : start) : (start ?? due);
  const rangeEnd = start && due ? (inverted ? start : due) : (start ?? due);

  let bar: { left: number; width: number; isMarker: boolean } | null = null;
  if (rangeStart && rangeEnd) {
    const s = Math.max(daysBetween(range.start, rangeStart), 0);
    const e = Math.min(daysBetween(range.start, rangeEnd) + 1, totalDays);
    if (e > s) {
      const isSingle = !start || !due;
      if (isSingle) {
        bar = { left: s * dayPx, width: Math.max(dayPx, 12), isMarker: true };
      } else {
        bar = { left: s * dayPx, width: (e - s) * dayPx, isMarker: false };
      }
    }
  }

  const fmt = (d: Date) =>
    d.toLocaleDateString(locale, {
      month: "short",
      day: "numeric",
      year: "numeric",
      timeZone: "UTC",
    });

  return (
    <IssueActionsContextMenu issue={issue}>
      <div
        {...{ [PEEK_TARGET_ATTR]: issue.id }}
        data-gantt-issue-id={issue.id}
        className="flex border-b border-foreground/5 hover:bg-accent/30 transition-colors"
        style={{ height: ROW_HEIGHT }}
      >
        {/* The task and date cells stay in view while the timeline scrolls. */}
        <div className="sticky left-0 z-[1] flex shrink-0 bg-background" style={{ width: LEFT_COL_WIDTH }}>
          <AppLink
            href={p.issueDetail(issue.id)}
            newTabTitle={issue.identifier}
            className={cn(
              "flex shrink-0 items-center gap-2 border-r px-3 text-body min-w-0",
              peeked &&
                "bg-[color-mix(in_oklab,var(--brand)_6%,var(--background))] shadow-[inset_2px_0_0_var(--brand)]",
            )}
            style={{ width: ISSUE_COLUMN_WIDTH }}
            {...linkProps}
          >
            <StatusIcon
              status={issue.status}
              color={colorOf(issue.status)}
              icon={iconOf(issue.status)}
              category={issueStatusCategory(issue) ?? undefined}
              className="h-3.5 w-3.5"
            />
            <PriorityIcon priority={issue.priority} />
            <span className="w-14 shrink-0 text-caption text-muted-foreground tabular-nums truncate">
              {issue.identifier}
            </span>
            <span className="truncate flex-1">{issue.title}</span>
            {project && <ProjectIcon project={project} size="sm" />}
            {issue.assignee_type && issue.assignee_id && (
              <ActorAvatar
                actorType={issue.assignee_type}
                actorId={issue.assignee_id}
                size="sm"
                enableHoverCard
              />
            )}
          </AppLink>
          <div className="flex shrink-0 items-center border-r px-2" style={{ width: DATE_COLUMN_WIDTH }}>
            <StartDatePicker
              startDate={issue.start_date}
              triggerRender={
                <button className={DATE_CELL_TRIGGER} aria-label={t(($) => $.gantt.header_start_date)} disabled={updateIssue.isPending} />
              }
              onUpdate={({ start_date }) => commitDates(computeStartDateEditPatch(
                issue.start_date, issue.due_date, start_date ?? null,
              ))}
            />
          </div>
          <div className="flex shrink-0 items-center border-r px-2" style={{ width: DATE_COLUMN_WIDTH }}>
            <DueDatePicker
              dueDate={issue.due_date}
              triggerRender={
                <button className={DATE_CELL_TRIGGER} aria-label={t(($) => $.gantt.header_due_date)} disabled={updateIssue.isPending} />
              }
              onUpdate={({ due_date }) => commitDates(computeDueDateEditPatch(
                issue.start_date, issue.due_date, due_date ?? null,
              ))}
            />
          </div>
        </div>
        {/* Timeline track */}
        <div
          className="relative shrink-0"
          style={{ width: totalDays * dayPx }}
        >
          {bar && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <AppLink
                    href={p.issueDetail(issue.id)}
                    newTabTitle={issue.identifier}
                    {...linkProps}
                    aria-label={`${issue.identifier} · ${issue.title}`}
                    className={cn(
                      "absolute top-1/2 -translate-y-1/2 transition-opacity hover:opacity-90",
                      bar.isMarker
                        ? "h-3 w-3 rotate-45 rounded-[2px]"
                        : "h-5 rounded-md",
                      STATUS_BAR_BG[issueStatusCategory(issue) ?? "unstarted"],
                      inverted && "ring-2 ring-destructive ring-offset-1 ring-offset-background",
                    )}
                    style={{ left: bar.left, width: bar.width }}
                  >
                    {!bar.isMarker && bar.width > 60 && (
                      <span className="block truncate px-2 py-[2px] text-micro leading-4 text-white">
                        {issue.title}
                      </span>
                    )}
                  </AppLink>
                }
              />
              <TooltipContent side="top">
                <div className="flex flex-col gap-0.5 text-caption">
                  <span className="font-medium">{issue.title}</span>
                  <span className="text-muted-foreground">
                    {start ? fmt(start) : "—"} → {due ? fmt(due) : "—"}
                  </span>
                  {inverted && (
                    <span className="text-destructive">
                      {t(($) => $.gantt.inverted_dates_warning)}
                    </span>
                  )}
                  {cardCustomProperties.map((property) => (
                    <span key={property.id} className="flex items-center gap-2">
                      <span className="shrink-0 text-muted-foreground">
                        {property.name}
                      </span>
                      <CustomPropertyValueDisplay
                        property={property}
                        value={issue.properties?.[property.id]}
                        issueId={issue.id}
                      />
                    </span>
                  ))}
                </div>
              </TooltipContent>
            </Tooltip>
          )}
        </div>
      </div>
    </IssueActionsContextMenu>
  );
}

// GanttView — public component

export function GanttView({ issues }: { issues: Issue[] }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const [previewId, setPreviewId] = useState<string | null>(null);
  const zoom = useViewStore((s) => s.ganttZoom);
  const showCompleted = useViewStore((s) => s.ganttShowCompleted);
  const cardPropertyIds = useViewStore((s) => s.cardPropertyIds);
  const sortBy = useViewStore((s) => s.sortBy);
  const sortDirection = useViewStore((s) => s.sortDirection);
  const act = useViewStoreApi().getState();
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(wsId));
  // Board order for `sort=status`, archived included: an issue can still sit on
  // an archived status and has to rank with the rest (MUL-7379).
  const statusCatalog = useIssueStatuses(useWorkspaceId());
  const statusOrder = useMemo(() => statusColumnKeys(statusCatalog, true), [statusCatalog]);

  const today = useMemo(() => startOfDayUTC(new Date()), []);
  const dayPx = DAY_PX_BY_ZOOM[zoom];

  // `issues` is already the canvas set: the surface applies the shared
  // filters, drops undated rows, and honours `ganttShowCompleted` before
  // handing it over (see `ganttCanvasRows` in use-issue-surface-data.ts).
  // Those rules used to live here, which meant the header chip could count
  // rows this canvas would never draw (MUL-4884). Keep this view a renderer:
  // it orders rows, it does not decide which ones exist.
  const scheduled = useMemo(() => {
    // "position" makes no sense on a gantt — default to start_date asc when
    // the user hasn't picked a more specific sort.
    const sortField = sortBy === "position" ? "start_date" : sortBy;
    return sortIssues(issues, sortField, sortDirection, statusOrder);
  }, [issues, sortBy, sortDirection, statusOrder]);
  const visibleIssueIds = useMemo(
    () => scheduled.map((issue) => issue.id),
    [scheduled],
  );
  const previewIssue = scheduled.find((issue) => issue.id === previewId);

  // Side peek steps through the rows top to bottom.
  const peek = useIssuePeekActions();
  useEffect(() => {
    peek?.publishColumns([scheduled.map((issue) => issue.id)]);
  }, [peek, scheduled]);
  useEffect(() => () => peek?.publishColumns(null), [peek]);

  const range = useMemo(
    () => computeRange(scheduled, today, zoom),
    [scheduled, today, zoom],
  );
  const totalDays = daysBetween(range.start, range.end);
  const timelineWidth = totalDays * dayPx;
  const todayOffsetDays = daysBetween(range.start, today);

  const empty = scheduled.length === 0;
  const scrollRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const el = scrollRef.current;
    if (!el || empty) return;
    const timelineViewport = Math.max(0, el.clientWidth - LEFT_COL_WIDTH);
    // The task sheet is sticky, so only the timeline viewport can show today.
    el.scrollLeft = Math.max(0, todayOffsetDays * dayPx - Math.min(240, timelineViewport / 2));
  }, [todayOffsetDays, dayPx, empty]);

  const ganttBody = (
    <div className="flex flex-col flex-1 min-h-0">
      {/* Toolbar */}
      <div className="flex h-9 shrink-0 items-center gap-2 border-b px-3">
        <div className="inline-flex items-center rounded-md border border-foreground/10 p-0.5">
          {([
            { value: "day", label: t(($) => $.gantt.zoom_day) },
            { value: "week", label: t(($) => $.gantt.zoom_week) },
            { value: "month", label: t(($) => $.gantt.zoom_month) },
          ] as const).map((opt) => (
            <Button
              key={opt.value}
              size="sm"
              variant={zoom === opt.value ? "secondary" : "ghost"}
              aria-pressed={zoom === opt.value}
              className={cn(
                "h-6 px-2 text-caption",
                zoom !== opt.value && "text-muted-foreground",
              )}
              onClick={() => act.setGanttZoom(opt.value)}
            >
              {opt.label}
            </Button>
          ))}
        </div>
        <div className="flex-1" />
        <Button
          size="sm"
          variant={showCompleted ? "secondary" : "outline"}
          className={cn(
            "h-7 text-caption",
            !showCompleted && "text-muted-foreground",
          )}
          onClick={act.toggleGanttShowCompleted}
        >
          {t(($) => $.gantt.show_completed)}
        </Button>
      </div>

      {/* Body — single scroll container drives both vertical + horizontal */}
      {empty ? (
        <div className="flex min-h-0 flex-1 items-center justify-center text-body text-muted-foreground">
          {t(($) => $.gantt.empty)}
        </div>
      ) : <div ref={scrollRef} className="flex-1 min-h-0 overflow-auto">
        <div style={{ minWidth: LEFT_COL_WIDTH + timelineWidth }}>
          {/* Sticky header row */}
          <div className="sticky top-0 z-20 flex">
            <div
              className="sticky left-0 z-30 flex shrink-0 border-b bg-background"
              style={{ width: LEFT_COL_WIDTH, height: HEADER_HEIGHT }}
            >
              {[
                { title: t(($) => $.gantt.header_issue), width: ISSUE_COLUMN_WIDTH },
                { title: t(($) => $.gantt.header_start_date), width: DATE_COLUMN_WIDTH },
                { title: t(($) => $.gantt.header_due_date), width: DATE_COLUMN_WIDTH },
              ].map(({ title, width }) => (
                <div key={title} className="flex shrink-0 items-end border-r px-2 pb-1.5 text-micro font-medium text-muted-foreground" style={{ width }}>
                  {title}
                </div>
              ))}
            </div>
            <GanttAxis
              range={range}
              dayPx={dayPx}
              zoom={zoom}
              todayOffsetDays={todayOffsetDays}
              width={timelineWidth}
            />
          </div>

          {/* Scheduled rows + background overlay */}
          <div className="relative">
            {/* Background gridlines + today line spanning all rows. Positioned
                starting after the left label column. */}
            <div
              className="pointer-events-none absolute top-0"
              style={{ left: LEFT_COL_WIDTH, width: timelineWidth }}
            >
              <BackgroundLayer
                range={range}
                dayPx={dayPx}
                height={scheduled.length * ROW_HEIGHT}
                todayOffsetDays={todayOffsetDays}
              />
            </div>
            {scheduled.map((issue) => (
              <ScheduledRow
                key={issue.id}
                issue={issue}
                range={range}
                dayPx={dayPx}
                totalDays={totalDays}
                onPreview={() => setPreviewId(issue.id)}
                workspaceProperties={workspaceProperties}
                cardPropertyIds={cardPropertyIds}
              />
            ))}
          </div>
        </div>
      </div>}
      {previewIssue && (
        <IssuePrototypePreview key={previewIssue.id} issue={previewIssue} onClose={() => setPreviewId(null)} />
      )}
    </div>
  );

  return (
    <ComputedPropertyViewBoundary
      wsId={wsId}
      issueIds={visibleIssueIds}
      cardPropertyIds={cardPropertyIds}
      workspaceProperties={workspaceProperties}
    >
      {ganttBody}
    </ComputedPropertyViewBoundary>
  );
}
