import type { GoalPlanEdge } from "../types";
import { dateOnlyToUTCDate } from "../issues/date";

interface ScheduleInput {
  id: string;
  duration: number | null;
}

export function plannedDays(startDate?: string | null, dueDate?: string | null) {
  const start = dateOnlyToUTCDate(startDate);
  const end = dateOnlyToUTCDate(dueDate);
  if (!start || !end || start > end) return null;
  return (end.getTime() - start.getTime()) / 86_400_000 + 1;
}

// A missing duration changes the unit for the entire plan. Mixing calendar
// days with arbitrary one-day guesses could highlight the wrong branch.
export function scheduleGoalPlan(items: ScheduleInput[], edges: GoalPlanEdge[]) {
  const unit = items.every(item => item.duration !== null) ? "day" : "stage";
  const nodes = new Map(items.map(item => [item.id, {
    id: item.id, duration: unit === "day" ? item.duration ?? 1 : 1,
    start: 0, latestEnd: 0, pending: 0, successors: new Array<string>(),
  }]));
  for (const edge of edges) {
    const before = nodes.get(edge.prerequisite_item_id);
    const after = nodes.get(edge.dependent_item_id);
    if (!before || !after) return { kind: "invalid" } as const;
    if (edge.kind === "conditional") continue;
    before.successors.push(after.id);
    after.pending++;
  }
  const order = [...nodes.values()].filter(node => node.pending === 0);
  let length = 0;
  for (const node of order) {
    const end = node.start + node.duration;
    length = Math.max(length, end);
    for (const id of node.successors) {
      const after = nodes.get(id)!;
      after.start = Math.max(after.start, end);
      if (--after.pending === 0) order.push(after);
    }
  }
  if (order.length !== items.length) return { kind: "invalid" } as const;

  const events = new Map<number, number>();
  for (const node of [...order].reverse()) {
    node.latestEnd = length;
    for (const id of node.successors) {
      const after = nodes.get(id)!;
      node.latestEnd = Math.min(node.latestEnd, after.latestEnd - after.duration);
    }
    const end = node.start + node.duration;
    events.set(node.start, (events.get(node.start) ?? 0) + 1);
    events.set(end, (events.get(end) ?? 0) - 1);
  }
  let peak = 0;
  let active = 0;
  for (const [, delta] of [...events].sort(([a], [b]) => a - b)) {
    active += delta;
    peak = Math.max(peak, active);
  }
  const rows = [...nodes.values()].sort((a, b) => a.start - b.start).map(node => ({
    id: node.id, start: node.start, end: node.start + node.duration,
    critical: node.start + node.duration === node.latestEnd,
  }));
  return { kind: "scheduled", unit, rows, length, peak } as const;
}
