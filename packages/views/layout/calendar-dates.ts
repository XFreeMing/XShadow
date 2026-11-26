// Pure YYYY-MM-DD calendar arithmetic (no Date timezone slip).

export interface CalendarDate {
  year: number;
  month: number; // 1..12
  day: number; // 1..31
  iso: string;
}

export function isLeapYear(year: number): boolean {
  return (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0;
}

export function daysInMonth(year: number, month: number): number {
  switch (month) {
    case 1:
    case 3:
    case 5:
    case 7:
    case 8:
    case 10:
    case 12:
      return 31;
    case 4:
    case 6:
    case 9:
    case 11:
      return 30;
    case 2:
      return isLeapYear(year) ? 29 : 28;
    default:
      return 0;
  }
}

export function formatIsoDate(year: number, month: number, day: number): string {
  return `${String(year).padStart(4, "0")}-${String(month).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
}

export function parseCalendarDate(raw: unknown): CalendarDate | null {
  if (typeof raw !== "string") return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(raw.trim());
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  if (month < 1 || month > 12) return null;
  const maxDay = daysInMonth(year, month);
  if (day < 1 || day > maxDay) return null;
  return { year, month, day, iso: formatIsoDate(year, month, day) };
}

/** Converts YYYY-MM-DD to a UTC epoch day index (exact integer, no DST/timezone offset). */
export function toDayIndex(date: CalendarDate): number {
  return Math.floor(Date.UTC(date.year, date.month - 1, date.day) / 86_400_000);
}

export function fromDayIndex(dayIndex: number): CalendarDate {
  const dt = new Date(dayIndex * 86_400_000);
  const year = dt.getUTCFullYear();
  const month = dt.getUTCMonth() + 1;
  const day = dt.getUTCDate();
  return { year, month, day, iso: formatIsoDate(year, month, day) };
}

export function todayCalendarDate(): CalendarDate {
  const now = new Date();
  const year = now.getFullYear();
  const month = now.getMonth() + 1;
  const day = now.getDate();
  return { year, month, day, iso: formatIsoDate(year, month, day) };
}

export function dayOfWeek(date: CalendarDate): number {
  // 0 = Sunday .. 6 = Saturday
  return new Date(Date.UTC(date.year, date.month - 1, date.day)).getUTCDay();
}

export interface CalendarCell {
  date: CalendarDate;
  inMonth: boolean;
}

export function buildCalendarMonthGrid(year: number, month: number): CalendarCell[] {
  const first: CalendarDate = { year, month, day: 1, iso: formatIsoDate(year, month, 1) };
  const startOffset = dayOfWeek(first);
  const startDayIndex = toDayIndex(first) - startOffset;
  const totalDays = daysInMonth(year, month);
  const totalCells = startOffset + totalDays > 35 ? 42 : 35;
  const cells: CalendarCell[] = [];
  for (let i = 0; i < totalCells; i++) {
    const date = fromDayIndex(startDayIndex + i);
    cells.push({ date, inMonth: date.year === year && date.month === month });
  }
  return cells;
}

/** Inclusive start and exclusive end for the visible month grid (date-only). */
export function calendarGridDateRange(year: number, month: number): { start: string; end: string } {
  const cells = buildCalendarMonthGrid(year, month);
  const first = cells[0]?.date;
  const last = cells[cells.length - 1]?.date;
  if (!first || !last) {
    const iso = formatIsoDate(year, month, 1);
    return { start: iso, end: iso };
  }
  return { start: first.iso, end: fromDayIndex(toDayIndex(last) + 1).iso };
}

/** Format a calendar month anchor without local-TZ day slip (e.g. LA seeing October for November UTC). */
export function formatCalendarMonthLabel(year: number, month: number, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  }).format(new Date(Date.UTC(year, month - 1, 1)));
}

export function calendarMonthKey(year: number, month: number): string {
  return `${year}-${String(month).padStart(2, "0")}`;
}