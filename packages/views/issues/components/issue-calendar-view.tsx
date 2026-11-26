"use client";

import { useCallback, useMemo, useState } from "react";
import { Loader2 } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Issue, IssueTableQuerySpec } from "@multica/core/types";
import {
  buildCalendarMonthGrid,
  calendarGridDateRange,
  calendarMonthKey,
  formatCalendarMonthLabel,
  parseCalendarDate,
  todayCalendarDate,
} from "../../layout/calendar-dates";
import { useT, useLocale } from "../../i18n";
import { resolveClickIntent, useIntentNavigate } from "../../navigation";
import { useIssuePeekClick } from "../surface/peek-context";
import { useIssueTableFlatRowPages } from "./use-issue-table-flat-row-pages";

const CALENDAR_PAGE_LIMIT = 50;
const WEEKDAYS = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"] as const;

export interface IssueCalendarViewProps {
  serverQuery: IssueTableQuerySpec;
}

function dueDateAxisQuery(
  base: IssueTableQuerySpec,
  start: string,
  end: string,
): IssueTableQuerySpec {
  return {
    ...base,
    filters: {
      ...base.filters,
      issue_date_bound: { field: "due_date", start, end },
    },
  };
}

export function IssueCalendarView({ serverQuery }: IssueCalendarViewProps) {
  const wsId = useWorkspaceId();
  const locale = useLocale();
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const handlePeekClick = useIssuePeekClick();
  const intentNavigate = useIntentNavigate();
  const today = useMemo(() => todayCalendarDate(), []);
  const [cursor, setCursor] = useState(() => ({ year: today.year, month: today.month }));

  const gridWindow = useMemo(
    () => calendarGridDateRange(cursor.year, cursor.month),
    [cursor.month, cursor.year],
  );
  const boundedQuery = useMemo(
    () => dueDateAxisQuery(serverQuery, gridWindow.start, gridWindow.end),
    [gridWindow.end, gridWindow.start, serverQuery],
  );

  const {
    issues,
    isHeadLoading,
    isHeadError,
    isTailLoading,
    isTailError,
    canLoadMore,
    retryHead,
    retryTail,
  } = useIssueTableFlatRowPages(wsId, boundedQuery, CALENDAR_PAGE_LIMIT, {
    autoLoadAll: true,
  });

  const eventsByIso = useMemo(() => {
    const map = new Map<string, Issue[]>();
    for (const issue of issues) {
      const parsed = parseCalendarDate(issue.due_date);
      if (!parsed) continue;
      const list = map.get(parsed.iso);
      if (list) list.push(issue);
      else map.set(parsed.iso, [issue]);
    }
    return map;
  }, [issues]);

  const cells = useMemo(
    () => buildCalendarMonthGrid(cursor.year, cursor.month),
    [cursor.month, cursor.year],
  );

  const monthLabel = useMemo(
    () => formatCalendarMonthLabel(cursor.year, cursor.month, locale),
    [cursor.month, cursor.year, locale],
  );
  const monthKey = useMemo(() => calendarMonthKey(cursor.year, cursor.month), [cursor.month, cursor.year]);

  const stepMonth = useCallback((delta: number) => {
    setCursor((prev) => {
      const nextIndex = prev.year * 12 + (prev.month - 1) + delta;
      return { year: Math.floor(nextIndex / 12), month: (nextIndex % 12) + 1 };
    });
  }, []);

  const goToday = useCallback(() => {
    setCursor({ year: today.year, month: today.month });
  }, [today.month, today.year]);

  const openIssue = useCallback(
    (issue: Issue, event: React.MouseEvent) => {
      if (handlePeekClick(issue.id, event)) {
        if (event.shiftKey) globalThis.window?.getSelection()?.removeAllRanges();
        return;
      }
      intentNavigate(
        paths.issueDetail(issue.id),
        resolveClickIntent(event),
        issue.identifier,
      );
    },
    [handlePeekClick, intentNavigate, paths],
  );

  const weekdayLabels = useMemo(
    () => WEEKDAYS.map((key) => t(($) => $.calendar.weekdays[key])),
    [t],
  );

  const isPaging = isTailLoading || isTailError || canLoadMore;
  const isPartial = !isHeadLoading && !isHeadError && issues.length > 0 && isPaging;
  const isSettledEmpty = !isHeadLoading && !isHeadError && issues.length === 0 && !isPaging;

  const statusSlot = (() => {
    if (isHeadLoading) {
      return (
        <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground" data-calendar-status="loading">
          <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          {t(($) => $.table.loading_branch)}
        </span>
      );
    }
    if (isHeadError) {
      return (
        <span className="inline-flex items-center gap-1.5 text-caption text-destructive" data-calendar-status="error">
          <span>{t(($) => $.table.load_failed)}</span>
          <Button size="sm" variant="outline" onClick={retryHead}>
            {t(($) => $.table.load_failed_retry)}
          </Button>
        </span>
      );
    }
    if (isPartial) {
      return (
        <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground" data-calendar-status="partial">
          {isTailError ? (
            <Button size="sm" variant="ghost" className="h-auto px-1 py-0 text-destructive" onClick={retryTail}>
              {t(($) => $.table.load_more_failed_retry)}
            </Button>
          ) : (
            <>
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              {t(($) => $.calendar.loading_more)}
            </>
          )}
        </span>
      );
    }
    if (isSettledEmpty) {
      return (
        <span className="text-caption text-muted-foreground" data-view-empty="calendar" data-calendar-status="empty">
          {t(($) => $.calendar.empty_month)}
        </span>
      );
    }
    if (issues.length > 0) {
      return (
        <span className="text-caption text-muted-foreground" data-calendar-status="success">
          {t(($) => $.calendar.issue_count, { count: issues.length })}
        </span>
      );
    }
    return null;
  })();

  return (
    <div
      className="flex flex-1 min-h-0 flex-col overflow-hidden"
      data-view-kind="calendar"
      aria-busy={isHeadLoading || isPartial ? true : undefined}
    >
      <div className="flex shrink-0 items-center justify-between gap-2 border-b border-border px-3 py-2">
        <div className="flex items-center gap-1.5">
          <Button type="button" variant="outline" size="xs" onClick={() => stepMonth(-1)}>
            {t(($) => $.calendar.prev)}
          </Button>
          <Button type="button" variant="outline" size="xs" onClick={goToday}>
            {t(($) => $.calendar.today)}
          </Button>
          <Button type="button" variant="outline" size="xs" onClick={() => stepMonth(1)}>
            {t(($) => $.calendar.next)}
          </Button>
          <span
            className="ml-2 text-body font-semibold"
            data-calendar-month={monthKey}
            data-calendar-month-label={monthLabel}
          >
            {monthLabel}
          </span>
        </div>
        {statusSlot}
      </div>

      <div className="flex-1 min-h-0 overflow-auto p-3">
        {isHeadLoading ? (
          <div className="grid grid-cols-7 gap-px">
            {Array.from({ length: 35 }).map((_, index) => (
              <Skeleton key={index} className="min-h-24 rounded-none" />
            ))}
          </div>
        ) : isHeadError ? (
          <div role="alert" className="p-4 text-body text-destructive">
            {t(($) => $.table.load_failed)}
          </div>
        ) : (
          <div className="grid grid-cols-7 border-l border-t border-border">
            {weekdayLabels.map((day, index) => (
              <div
                key={WEEKDAYS[index]}
                className="border-b border-r border-border bg-muted/30 px-2 py-1 text-caption font-medium text-muted-foreground"
              >
                {day}
              </div>
            ))}
            {cells.map((cell) => {
              const dayEvents = eventsByIso.get(cell.date.iso) ?? [];
              const isToday = cell.date.iso === today.iso;
              return (
                <div
                  key={cell.date.iso}
                  data-calendar-date={cell.date.iso}
                  className={`flex min-h-24 flex-col border-b border-r border-border p-1.5 ${
                    cell.inMonth ? "bg-background" : "bg-muted/20 text-muted-foreground"
                  }`}
                >
                  <div className="mb-1 flex shrink-0 items-center justify-between">
                    <span
                      className={`inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1 text-caption ${
                        isToday ? "bg-primary text-primary-foreground font-semibold" : ""
                      }`}
                    >
                      {cell.date.day}
                    </span>
                    {dayEvents.length > 0 ? (
                      <span className="text-caption tabular-nums text-muted-foreground">{dayEvents.length}</span>
                    ) : null}
                  </div>
                  <div className="min-h-0 max-h-28 flex-1 space-y-1 overflow-y-auto overscroll-y-contain">
                    {dayEvents.map((issue) => (
                      <button
                        key={issue.id}
                        type="button"
                        data-issue-id={issue.id}
                        onClick={(event) => openIssue(issue, event)}
                        className="block w-full truncate rounded bg-primary/10 px-1.5 py-0.5 text-left text-caption text-foreground hover:bg-primary/20"
                      >
                        {issue.identifier} {issue.title}
                      </button>
                    ))}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}