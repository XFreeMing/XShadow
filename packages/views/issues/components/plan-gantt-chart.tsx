"use client";

import { useId, useMemo, useState } from "react";
import { plannedDays, scheduleGoalPlan } from "@multica/core/goals/plan-schedule";
import { useWorkspacePaths } from "@multica/core/paths";
import type { GoalPlan, GoalPlanEdge, Issue } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";

const ROW = 38;
const LABEL = 296;

export function PlanGanttChart({ plan, issues }: { plan: GoalPlan; issues: Issue[] }) {
  const { t } = useT("issues");
  const { t: goals } = useT("goals");
  const paths = useWorkspacePaths();
  const arrow = useId().replace(/:/g, "");
  const [selected, setSelected] = useState<string | null>(null);
  const items = useMemo(() => {
    const byId = new Map(issues.map(issue => [issue.id, issue]));
    return new Map(plan.items.map(item => {
      const issue = item.binding.issue_id ? byId.get(item.binding.issue_id) : undefined;
      return [item.id, { ...item, issue, duration: plannedDays(issue?.start_date, issue?.due_date) }];
    }));
  }, [plan.items, issues]);
  const schedule = useMemo(() => scheduleGoalPlan([...items.values()], plan.edges), [items, plan.edges]);
  if (schedule.kind === "invalid") return <p role="alert" className="p-4 text-body">{t(($) => $.gantt.invalid)}</p>;

  const { rows, length, peak, unit } = schedule;
  const width = Math.min(2000, Math.max(480, length * 98));
  const padding = 16;
  const scale = (width - 2 * padding) / Math.max(1, length);
  const axis = (n: number) => padding + n * scale;
  const percent = (x: number) => `${x / width * 100}%`;
  const inset = Math.min(5, scale / 4);
  const tickStep = Math.max(1, Math.ceil(60 / scale));
  const coords = new Map(rows.map((row, i) => [row.id, {
    ...row, left: axis(row.start) + inset, right: axis(row.end) - inset, y: (i + 0.5) * ROW,
  }]));
  const label = (id: string) => {
    const item = items.get(id);
    return item?.issue?.identifier || item?.title || goals(($) => $.plan.untitled_item);
  };
  const focus = selected && items.has(selected) ? selected : null;
  const current = focus ? items.get(focus) : undefined;
  const touches = (edge: GoalPlanEdge) => edge.prerequisite_item_id === focus || edge.dependent_item_id === focus;
  const relations = plan.edges.filter(touches);
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 px-4 pb-2 text-caption">
        <strong className="inline-flex items-center gap-2 text-brand">
          <span className="h-2 w-5 rounded-sm bg-brand" />
          {t(($) => $.gantt.critical)} · {unit === "day" ? t(($) => $.gantt.days, { value: length }) : t(($) => $.gantt.stages, { value: length })}
        </strong>
        <span>{t(($) => $.gantt.peak, { value: peak })}</span>
        <span className="inline-flex items-center gap-1.5 text-muted-foreground"><span className="w-5 border-t" />{goals(($) => $.plan.dependency_hard)}</span>
        <span className="inline-flex items-center gap-1.5 text-muted-foreground"><span className="w-5 border-t border-dashed" />{goals(($) => $.plan.dependency_conditional)}</span>
      </div>
      <p className="px-4 pb-3 text-caption text-muted-foreground">
        {unit === "day" ? t(($) => $.gantt.day_basis) : t(($) => $.gantt.stage_basis)}
      </p>
      <div className="min-h-32 shrink overflow-auto border-y">
        <div style={{ minWidth: LABEL + width }}>
          <div className="sticky top-0 z-20 flex h-8 border-b bg-background text-caption text-muted-foreground">
            <div className="sticky left-0 z-10 flex items-center border-r bg-background px-4" style={{ width: LABEL }}>{t(($) => $.gantt.header_issue)}</div>
            <div className="relative flex-1">
              {Array.from({ length: Math.ceil(length / tickStep) }, (_, i) => (
                <span key={i} className="absolute inset-y-0 flex items-center pl-2 tabular-nums" style={{ left: percent(axis(i * tickStep)) }}>
                  {unit === "stage" ? goals(($) => $.plan.stage) : t(($) => $.gantt.zoom_day)} {i * tickStep + 1}
                </span>
              ))}
            </div>
          </div>
          <div className="relative">
            <svg aria-hidden="true" viewBox={`0 0 ${width} ${rows.length * ROW}`} preserveAspectRatio="none"
              height={rows.length * ROW} className="pointer-events-none absolute top-0"
              style={{ left: LABEL, width: `calc(100% - ${LABEL}px)` }}>
              <defs><marker id={arrow} viewBox="0 0 8 8" refX="7" refY="4" markerWidth="5" markerHeight="5" orient="auto">
                <path d="M0 0 L8 4 L0 8 Z" fill="context-stroke" />
              </marker></defs>
              {plan.edges.map(edge => {
                const from = coords.get(edge.prerequisite_item_id)!;
                const to = coords.get(edge.dependent_item_id)!;
                const conditional = edge.kind === "conditional";
                const critical = !conditional && from.critical && to.critical && from.end === to.start;
                const bend = from.right + 4;
                const lane = from.y + (to.y > from.y ? 1 : -1) * ROW / 2;
                const d = to.left > from.right
                  ? `M ${from.right} ${from.y} H ${bend} V ${to.y} H ${to.left}`
                  : `M ${from.right} ${from.y} h 5 V ${lane} H ${to.left - 5} V ${to.y} H ${to.left}`;
                return <path key={edge.prerequisite_item_id + edge.dependent_item_id} d={d}
                  fill="none" stroke="currentColor" vectorEffect="non-scaling-stroke" strokeWidth={focus && touches(edge) ? 2.5 : 1.5}
                  className={critical ? "text-brand" : "text-muted-foreground"}
                  opacity={focus && !touches(edge) ? 0.15 : 0.8}
                  strokeDasharray={conditional ? "4 3" : undefined} markerEnd={`url(#${arrow})`} />;
              })}
            </svg>
            {rows.map(row => {
              const item = items.get(row.id)!;
              const pos = coords.get(row.id)!;
              const title = item.title || goals(($) => $.plan.untitled_item);
              const identifier = item.issue?.identifier;
              const caption = [identifier, title].filter(Boolean).join(" · ");
              return (
                <div key={row.id} className="flex border-b border-border/50 last:border-b-0" style={{ height: ROW }}>
                  <button type="button" title={caption} aria-pressed={focus === row.id}
                    onClick={() => setSelected(focus === row.id ? null : row.id)}
                    className={cn("sticky left-0 z-10 flex shrink-0 items-center gap-2 border-r bg-background px-4 text-left text-body hover:bg-accent",
                      focus === row.id && "bg-accent")} style={{ width: LABEL }}>
                    {identifier && <span className="shrink-0 text-caption tabular-nums text-muted-foreground">{identifier}</span>}
                    <span className="truncate">{title}</span>
                  </button>
                  <div className="relative flex-1">
                    <button type="button" aria-label={caption + (row.critical ? " · " + t(($) => $.gantt.critical) : "")}
                      aria-pressed={focus === row.id} title={caption} onClick={() => setSelected(focus === row.id ? null : row.id)}
                      className={cn("absolute top-2 h-5 overflow-hidden rounded px-1 text-micro font-medium",
                        row.critical ? "bg-brand text-brand-foreground" : "bg-secondary text-secondary-foreground ring-1 ring-inset ring-border",
                        focus === row.id && "outline-2 outline-offset-2 outline-foreground")}
                      style={{ left: percent(pos.left), width: percent(pos.right - pos.left), minWidth: 2 }}>
                      <span className="block truncate">{identifier ?? title}</span>
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      </div>
      {current && <section className="shrink-0 space-y-2 border-b p-4 text-caption" aria-live="polite">
        <div className="flex items-baseline gap-3">
          <strong className="text-body">{current.title}</strong>
          {current.issue && <AppLink className="text-brand underline" href={paths.issueDetail(current.issue.identifier || current.issue.id)}>
            {current.issue.identifier}
          </AppLink>}
        </div>
        {relations.length ? <ul className="space-y-1">{relations.map(edge => (
          <li key={edge.prerequisite_item_id + edge.dependent_item_id}>
            <strong>{label(edge.prerequisite_item_id)} → {label(edge.dependent_item_id)}</strong>
            {" · "}{edge.kind === "conditional" ? goals(($) => $.plan.dependency_conditional) : goals(($) => $.plan.dependency_hard)}
            {" · "}{edge.reason}
          </li>
        ))}</ul> : <p>{goals(($) => $.plan.dependency_unrecorded)}</p>}
        {current.coordination?.parallel_reason && <p>{current.coordination.parallel_reason}</p>}
        {!!current.coordination?.exclusive_resources.length && <p>{t(($) => $.gantt.exclusive)} · {current.coordination.exclusive_resources.join(", ")}</p>}
      </section>}
    </div>
  );
}
