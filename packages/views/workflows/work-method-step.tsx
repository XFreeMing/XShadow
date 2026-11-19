"use client";

import { useQuery } from "@tanstack/react-query";
import type {
  WorkMethodHandler,
  WorkMethodStep,
} from "@multica/core/types/work-method";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  agentListOptions,
  memberListOptions,
  squadListOptions,
} from "@multica/core/workspace/queries";
import { useT } from "../i18n";

export type MethodRoleContext = {
  creatorName?: string;
  projectLeadName?: string;
};
export function useMethodHandlerName(roles?: MethodRoleContext) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  return (handler: WorkMethodHandler) => {
    if ("id" in handler) return getActorName(handler.kind, handler.id);
    const isCreator = handler.kind === "creator";
    const role = isCreator
      ? t(($) => $.workflows.handler.creator)
      : t(($) => $.workflows.handler.project_lead);
    const person = isCreator ? roles?.creatorName : roles?.projectLeadName;
    return person ? role + " · " + person : role;
  };
}

export function WorkMethodStepDetail({
  step,
  roles,
  showChecks = true,
}: {
  step: WorkMethodStep;
  roles?: MethodRoleContext;
  showChecks?: boolean;
}) {
  const { t } = useT("issues");
  const name = useMethodHandlerName(roles);
  const items = [
    [t(($) => $.method.inputs), step.inputs],
    [t(($) => $.method.instructions), step.instructions],
    [t(($) => $.method.output), step.output],
    [t(($) => $.method.owner), step.owner && name(step.owner)],
    [t(($) => $.method.reviewer), step.reviewer && name(step.reviewer)],
  ];
  return (
    <div className="space-y-3 text-label">
      <dl className="grid gap-2 sm:grid-cols-[auto_1fr] sm:gap-x-4">
        {items.map(
          ([label, value]) =>
            value && (
              <div key={label} className="contents">
                <dt className="text-muted-foreground">{label}</dt>
                <dd className="min-w-0 whitespace-pre-wrap break-words">
                  {value}
                </dd>
              </div>
            ),
        )}
      </dl>
      {showChecks && !!step.checks?.length && (
        <div>
          <p className="text-muted-foreground">{t(($) => $.method.checks)}</p>
          <ul className="list-disc space-y-1 pl-5">
            {step.checks.map((check, i) => (
              <li key={i} className="whitespace-pre-wrap break-words">
                {check}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

export function WorkMethodSteps({
  steps,
  current,
  roles,
}: {
  steps: WorkMethodStep[];
  current?: number;
  roles?: MethodRoleContext;
}) {
  return (
    <ol className="mt-2 divide-y divide-surface-border">
      {steps.map((step, i) => (
        <li key={i} aria-current={current === i ? "step" : undefined}>
          <details className="py-2">
            <summary className="cursor-pointer font-medium">
              {i + 1} · {step.name}
            </summary>
            <div className="mt-2 pl-4">
              <WorkMethodStepDetail step={step} roles={roles} />
            </div>
          </details>
        </li>
      ))}
    </ol>
  );
}

const valueOf = (handler?: WorkMethodHandler | null) =>
  handler
    ? "id" in handler
      ? handler.kind + "/" + handler.id
      : handler.kind
    : "";

export function WorkMethodHandlerPicker({
  label,
  wsId,
  handler,
  onChange,
}: {
  label: string;
  wsId: string;
  handler?: WorkMethodHandler | null;
  onChange: (handler: WorkMethodHandler | null) => void;
}) {
  const { t } = useT("issues");
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: squads = [] } = useQuery(squadListOptions(wsId));
  const options: {
    label: string;
    handlers: { handler: WorkMethodHandler; name: string }[];
  }[] = [
    {
      label: t(($) => $.workflows.handler.member),
      handlers: members.map((m) => ({
        handler: { kind: "member", id: m.user_id },
        name: m.name,
      })),
    },
    {
      label: t(($) => $.workflows.handler.agent),
      handlers: agents
        .filter((a) => !a.archived_at)
        .map((a) => ({ handler: { kind: "agent", id: a.id }, name: a.name })),
    },
    {
      label: t(($) => $.workflows.handler.squad),
      handlers: squads.map((s) => ({
        handler: { kind: "squad", id: s.id },
        name: s.name,
      })),
    },
  ];
  const value = valueOf(handler);
  const missing =
    handler &&
    "id" in handler &&
    !options.some((group) =>
      group.handlers.some((o) => valueOf(o.handler) === value),
    );
  return (
    <label className="grid min-w-0 gap-1 text-label">
      {label}
      <select
        value={value}
        className="min-w-0 rounded-md border border-input bg-background p-1.5"
        onChange={(e) => {
          const v = e.target.value;
          if (!v) {
            onChange(null);
            return;
          }
          if (v === "creator" || v === "project_lead") {
            onChange({ kind: v });
            return;
          }
          const next = options
            .flatMap((g) => g.handlers)
            .find((o) => valueOf(o.handler) === v);
          if (next) onChange(next.handler);
        }}
      >
        <option value="">{t(($) => $.method.unassigned)}</option>
        {missing && <option value={value}>{handler.id}</option>}
        <option value="creator">{t(($) => $.workflows.handler.creator)}</option>
        <option value="project_lead">
          {t(($) => $.workflows.handler.project_lead)}
        </option>
        {options.map((group) => (
          <optgroup key={group.label} label={group.label}>
            {group.handlers.map((item) => (
              <option value={valueOf(item.handler)} key={valueOf(item.handler)}>
                {item.name}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </label>
  );
}
