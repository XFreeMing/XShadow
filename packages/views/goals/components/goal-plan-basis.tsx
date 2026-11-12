"use client";

import { Badge } from "@multica/ui/components/ui/badge";
import { useT } from "../../i18n";
import {
  hasActualBasis,
  readActualBasis,
  readInputContext,
  type PlanActualBasisView,
  type PlanInputContextView,
} from "../plan";

const MAX_RENDERED_FILES = 8;
const MAX_RENDERED_TASKS = 8;

/**
 * Shows the plan's basis. An input resource is a pointer: unless the run
 * reported reading it, it must never be presented as evidence.
 */
export function PlanBasis({
  input,
  actual,
}: {
  input?: PlanInputContextView;
  actual?: PlanActualBasisView;
}) {
  const { t } = useT("goals");
  const showInput = input && (input.projectTitle || input.resources.length > 0 || input.missing.length > 0);
  const showActual = actual && hasActualBasis(actual);

  if (!showInput && !showActual && !actual?.missing.length) {
    return (
      <p className="text-caption text-muted-foreground">
        {t(($) => $.plan.basis_actual_empty)}
      </p>
    );
  }

  return (
    <div className="space-y-2 rounded-md bg-muted/50 px-3 py-2 text-caption text-muted-foreground">
      {showInput && input ? (
        <div className="space-y-1.5" data-testid="plan-input-basis">
          <p className="font-medium text-foreground">{t(($) => $.plan.basis)}</p>
          {input.projectTitle ? (
            <p>
              <span className="text-muted-foreground">{t(($) => $.plan.basis_project)}: </span>
              {input.projectTitle}
            </p>
          ) : null}
          {input.resources.length > 0 ? (
            <div className="space-y-1">
              <p>{t(($) => $.plan.basis_resources)}</p>
              <ul className="space-y-1">
                {input.resources.map((resource) => (
                  <li
                    key={resource.id || `${resource.type}-${resource.ref}`}
                    className="flex flex-wrap items-center gap-1.5"
                  >
                    <Badge variant={resource.read ? "secondary" : "outline"}>
                      {resource.read
                        ? t(($) => $.plan.basis_resource_read)
                        : t(($) => $.plan.basis_resource_unread)}
                    </Badge>
                    <span className="text-foreground">{resource.label || resource.type}</span>
                    {resource.ref ? <span className="break-all">{resource.ref}</span> : null}
                  </li>
                ))}
              </ul>
            </div>
          ) : (
            <p>{t(($) => $.plan.basis_none)}</p>
          )}
          {input.missing.length > 0 ? (
            <p>
              <span>{t(($) => $.plan.basis_missing)}: </span>
              {input.missing.join(", ")}
            </p>
          ) : null}
          {input.readAt ? (
            <p>
              {t(($) => $.plan.basis_read_at, { time: input.readAt })}
            </p>
          ) : null}
        </div>
      ) : null}

      {showActual || actual?.missing.length ? (
        <div className="space-y-1.5" data-testid="plan-actual-basis">
          <p className="font-medium text-foreground">{t(($) => $.plan.basis_actual)}</p>
          {showActual && actual ? (
            <>
              {actual.repoSha ? (
                <p>
                  <span>{t(($) => $.plan.basis_repo_sha)}: </span>
                  <code className="break-all">{actual.repoSha}</code>
                </p>
              ) : null}
              {actual.files.length > 0 ? (
                <ul className="space-y-0.5">
                  {actual.files.slice(0, MAX_RENDERED_FILES).map((file) => (
                    <li key={file} className="break-all">
                      {file}
                    </li>
                  ))}
                </ul>
              ) : null}
              {actual.tasks.length > 0 ? (
                <ul className="space-y-0.5">
                  {actual.tasks.slice(0, MAX_RENDERED_TASKS).map((task) => (
                    <li key={task} className="break-all">
                      {task}
                    </li>
                  ))}
                </ul>
              ) : null}
              {actual.readResourceIds.length > 0 ? (
                <p>
                  <span>{t(($) => $.plan.basis_resources)}: </span>
                  {actual.readResourceIds.join(", ")}
                </p>
              ) : null}
              {actual.readAt ? (
                <p>{t(($) => $.plan.basis_read_at, { time: actual.readAt })}</p>
              ) : null}
            </>
          ) : (
            <p>{t(($) => $.plan.basis_actual_empty)}</p>
          )}
          {actual && actual.missing.length > 0 ? (
            <p>
              <span>{t(($) => $.plan.basis_missing)}: </span>
              {actual.missing.join(", ")}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** Convenience wrappers so callers do not re-parse a snapshot inline. */
export function PlanInputBasis({ snapshot }: { snapshot: Record<string, unknown> }) {
  return <PlanBasis input={readInputContext(snapshot)} />;
}

export function PlanActualBasis({ snapshot }: { snapshot: Record<string, unknown> }) {
  return <PlanBasis actual={readActualBasis(snapshot)} />;
}