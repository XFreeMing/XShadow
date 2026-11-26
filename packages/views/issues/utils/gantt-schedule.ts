import { dateOnlyToUTCDate } from "@multica/core/issues/date";
import type { Issue, UpdateIssueRequest } from "@multica/core/types";

const MS_PER_DAY = 24 * 60 * 60 * 1000;

export interface GanttDatePatch {
  start_date?: string | null;
  due_date?: string | null;
}

function pad2(value: number): string {
  return String(value).padStart(2, "0");
}

/** Serialize a UTC-aligned Date to canonical `"YYYY-MM-DD"`. */
function utcDateToDateOnly(date: Date): string {
  return `${date.getUTCFullYear()}-${pad2(date.getUTCMonth() + 1)}-${pad2(date.getUTCDate())}`;
}

/** Shift a UTC-aligned Date by an integer number of UTC days. */
function addUTCDays(date: Date, days: number): Date {
  return new Date(date.getTime() + days * MS_PER_DAY);
}

/** Signed whole-day difference `b - a` between two UTC-midnight Dates. */
function daysBetweenUTC(a: Date, b: Date): number {
  return Math.round((b.getTime() - a.getTime()) / MS_PER_DAY);
}

/**
 * Compute a single atomic update patch when the user edits `start_date`.
 *
 * When both `start_date` and `due_date` are set (and not inverted), moving
 * `start_date` shifts `due_date` by the same UTC-day delta to preserve the
 * task's relative duration in one commit.
 */
export function computeStartDateEditPatch(
  currentStart: string | null,
  currentDue: string | null,
  nextStart: string | null,
): GanttDatePatch | null {
  if (nextStart === currentStart) return null;
  if (nextStart === null) {
    return { start_date: null };
  }

  const targetStart = dateOnlyToUTCDate(nextStart);
  if (!targetStart) return null;
  const canonicalStart = utcDateToDateOnly(targetStart);

  const prevStart = dateOnlyToUTCDate(currentStart);
  const prevDue = dateOnlyToUTCDate(currentDue);
  if (prevStart?.getTime() === targetStart.getTime()) return null;

  if (prevStart && prevDue) {
    const durationDays = daysBetweenUTC(prevStart, prevDue);
    if (durationDays >= 0) {
      const canonicalDue = utcDateToDateOnly(addUTCDays(targetStart, durationDays));
      return {
        start_date: canonicalStart,
        due_date: canonicalDue,
      };
    }
    // Previously inverted anomaly: do not carry negative span forward.
    if (targetStart.getTime() > prevDue.getTime()) {
      return {
        start_date: canonicalStart,
        due_date: canonicalStart,
      };
    }
    return { start_date: canonicalStart };
  }

  if (!prevStart && prevDue && targetStart.getTime() > prevDue.getTime()) {
    return {
      start_date: canonicalStart,
      due_date: canonicalStart,
    };
  }

  return { start_date: canonicalStart };
}

/**
 * Compute a single atomic update patch when the user edits `due_date`.
 *
 * Resizes the end date; if moved earlier than `start_date`, pulls `start_date`
 * back to `nextDue` in the same commit so the interval never inverts.
 */
export function computeDueDateEditPatch(
  currentStart: string | null,
  currentDue: string | null,
  nextDue: string | null,
): GanttDatePatch | null {
  if (nextDue === currentDue) return null;
  if (nextDue === null) {
    return { due_date: null };
  }

  const targetDue = dateOnlyToUTCDate(nextDue);
  if (!targetDue) return null;
  if (dateOnlyToUTCDate(currentDue)?.getTime() === targetDue.getTime()) return null;
  const canonicalDue = utcDateToDateOnly(targetDue);

  const prevStart = dateOnlyToUTCDate(currentStart);
  if (prevStart && targetDue.getTime() < prevStart.getTime()) {
    return {
      start_date: canonicalDue,
      due_date: canonicalDue,
    };
  }

  return { due_date: canonicalDue };
}

/**
 * Attach compare-and-swap `expected_revision` when the authoritative issue carries
 * a server revision, so stale schedule edits surface revision conflicts cleanly.
 */
export function buildGanttDateUpdateRequest(
  issue: Pick<Issue, "revision">,
  patch: GanttDatePatch,
): Partial<UpdateIssueRequest> {
  return {
    ...patch,
    ...(issue.revision !== undefined
      ? { expected_revision: issue.revision }
      : {}),
  };
}
