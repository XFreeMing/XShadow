"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import {
  issueWorkMethodOptions,
  workMethodCatalogOptions,
} from "@multica/core/issue-workflows/queries";
import type {
  WorkMethodAction,
  WorkMethodRun,
} from "@multica/core/types/work-method";
import {
  WorkMethodSteps,
  WorkMethodStepDetail,
  useMethodHandlerName,
  type MethodRoleContext,
} from "../../workflows/work-method-step";
import { Button } from "@multica/ui/components/ui/button";
import { useLocale, useT } from "../../i18n";
import { PlannedDeliverySummary } from "../../goals/components/goal-plan-delivery";
import { IssueCloseout } from "./issue-closeout";
import { useActorName } from "@multica/core/workspace/hooks";

export function IssueWorkMethod({
  wsId,
  issueId,
  issueRevision,
  roles,
  children,
}: {
  wsId: string;
  issueId: string;
  issueRevision?: number;
  roles?: MethodRoleContext;
  children: ReactNode;
}) {
  const { t } = useT("issues");
  const options = issueWorkMethodOptions(wsId, issueId);
  const run = useQuery({ ...options, enabled: !!wsId && !!issueId });
  const qc = useQueryClient();
  const previousRevision = useRef(issueRevision);
  useEffect(() => {
    if (previousRevision.current !== issueRevision) {
      previousRevision.current = issueRevision;
      void qc.invalidateQueries({
        queryKey: issueWorkMethodOptions(wsId, issueId).queryKey,
      });
    }
  }, [issueId, issueRevision, qc, wsId]);

  if (run.error instanceof ApiError && run.error.status === 404)
    return <>{children}</>;
  return (
    <>
      {run.isError && (
        <div role="alert" className="mt-4 text-label text-destructive">
          {t(($) => $.method.load_error)}{" "}
          <Button variant="link" size="xs" onClick={() => void run.refetch()}>
            {t(($) => $.method.retry)}
          </Button>
        </div>
      )}
      {run.data?.state !== "closed" && run.data?.planned_delivery && <PlannedDeliverySummary delivery={run.data.planned_delivery} />}
      {run.data && (
        <MethodCard
          wsId={wsId}
          issueId={issueId}
          run={run.data}
          roles={roles}
          acceptance={run.data.method && (run.data.method.kind === "development" || run.data.planned_delivery) ? children : null}
        />
      )}
      {run.data && !run.data.method && children}
    </>
  );
}

function MethodCard({
  wsId,
  issueId,
  run,
  roles,
  acceptance,
}: {
  wsId: string;
  issueId: string;
  run: WorkMethodRun;
  acceptance: ReactNode;
  roles?: MethodRoleContext;
}) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const locale = useLocale();
  const handlerName = useMethodHandlerName(roles);
  function actorName(actor: string) {
    const [type, id] = actor.split("/");
    return type && id ? getActorName(type, id) : actor;
  }
  const qc = useQueryClient();
  const [checkReview, setCheckReview] = useState<{
    revision: number;
    checked: number[];
  } | null>(null);
  const [choosing, setChoosing] = useState(false);
  const [selected, setSelected] = useState("");
  const [draft, setDraft] = useState<{
    value: string;
    revision: number;
    methodId: string;
    methodRevision: number;
  } | null>(null);
  const [returnStep, setReturnStep] = useState<number | null>(null);
  const catalog = useQuery({
    ...workMethodCatalogOptions(wsId),
    enabled: choosing,
  });
  const queryKey = issueWorkMethodOptions(wsId, issueId).queryKey;
  const mutation = useMutation({
    mutationFn: (op: WorkMethodAction) =>
      api.changeIssueWorkMethod(issueId, op),
    onSuccess: (next) => {
      qc.setQueryData<WorkMethodRun>(queryKey, (old) =>
        old && old.revision > next.revision ? old : next,
      );
      setDraft(null);
      setCheckReview(null);
      setChoosing(false);
      setSelected("");
      setReturnStep(null);
    },
    onError: () => {
      void qc.invalidateQueries({ queryKey });
    },
  });
  const { method } = run;
  const step = method?.steps[run.step];
  const stale =
    draft !== null &&
    (draft.revision !== run.revision ||
      draft.methodId !== method?.id ||
      draft.methodRevision !== method?.revision);
  const evidence = draft?.value.trim() ?? "";
  const disabled = stale || mutation.isPending || !evidence;
  const reviewed = run.state === "review";
  const lastStep = !!method && run.step === method.steps.length - 1;
  const startsHandoff = run.state === "ready" && !!step?.owner;
  const canAct = run.can_act !== false && !startsHandoff;
  const checked =
    checkReview?.revision === run.revision ? checkReview.checked : [];
  const checks = step?.checks ?? [];
  const reviewComplete =
    !step?.reviewer || checks.every((_, i) => checked.includes(i));
  const latest = [...run.history].reverse();
  const failedCandidateReview = reviewed && !!latest[0]?.candidate_failure;
  const closeout = run.state === "closed" ? latest[0]?.closeout : undefined;
  const delivery = latest.find((event) => event.action === "advance");
  const returned = latest.find(
    (event) => event.action === "rework" && event.step === run.step,
  );
  const returnTo =
    returnStep === null || returnStep > run.step
      ? lastStep
        ? method?.rework_step
        : run.step
      : returnStep;
  function act(action: "advance" | "approve" | "rework") {
    if (
      disabled ||
      !method ||
      !canAct ||
      (action === "approve" && (failedCandidateReview || !reviewComplete))
    )
      return;
    const base = { base_revision: run.revision, evidence };
    const verdict = checks.length
      ? { checked_items: [...checked].sort((a, b) => a - b) }
      : {};
    if (action === "advance")
      mutation.mutate({
        ...base,
        action,
        ...(run.state === "ready"
          ? { method_id: method.id, method_revision: method.revision }
          : {}),
      });
    else if (action === "rework")
      mutation.mutate({
        ...base,
        action,
        target_step: returnTo,
        ...verdict,
      });
    else mutation.mutate({ ...base, action, ...verdict });
  }
  // Reviews refer to the checklist in force when they were recorded.
  let historicalMethod = method;
  const historicalMethods = latest.map((event) => {
    const applicable = historicalMethod;
    if (event.previous_method) historicalMethod = event.previous_method;
    return applicable;
  }).reverse();
  const actionText = (action: string) => {
    switch (action) {
      case "adopt_revision":
        return t(($) => $.method.catalog);
      case "start":
        return t(($) => $.method.start);
      case "select":
        return t(($) => $.method.select);
      case "advance":
        return t(($) => $.method.advance);
      case "approve":
        return t(($) => $.method.step_pass);
      case "rework":
        return t(($) => $.method.rework);
      default:
        return action;
    }
  };
  const stateText = {
    ready: t(($) => $.method.ready),
    working: t(($) => $.method.working),
    review: t(($) => $.method.in_review),
    closed: t(($) => $.method.closed),
  }[run.state];
  const canChoose =
    run.state === "ready" ||
    (run.state === "working" &&
      run.history.every((entry) => entry.action === "select"));
  return (
    <details
      className="mt-5 rounded-lg border border-surface-border bg-card px-4 py-3"
      open={method ? true : undefined}
    >
      <summary className="cursor-pointer text-label font-medium">
        {t(($) => $.method.singular)}
        {method && (
          <>
            {" "}
            · {method.name}{" "}
            <span className="text-muted-foreground">· {stateText}</span>
          </>
        )}
      </summary>
      <div className="mt-3 grid gap-3 text-label">
        {run.state !== "closed" && run.reason && <p className="text-muted-foreground">{run.reason}</p>}
        {canChoose && !choosing && (
          <div>
            <Button
              type="button"
              size="xs"
              variant="outline"
              onClick={() => {
                setSelected(method?.id ?? "");
                setChoosing(true);
              }}
            >
              {t(($) => $.method.select)}
            </Button>
          </div>
        )}
        {choosing && canChoose && (
          <div className="flex flex-wrap items-end gap-2">
            <label className="grid min-w-48 gap-1">
              {t(($) => $.method.singular)}
              <select
                className="rounded-md border border-input bg-background p-1.5"
                value={selected}
                onChange={(e) => setSelected(e.target.value)}
              >
                <option value="">—</option>
                {catalog.data?.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
            </label>
            <Button
              size="xs"
              disabled={!selected || mutation.isPending}
              onClick={() =>
                mutation.mutate({
                  action: "select",
                  base_revision: run.revision,
                  method_id: selected,
                  method_revision: catalog.data?.find((m) => m.id === selected)
                    ?.revision,
                })
              }
            >
              {t(($) => $.method.use)}
            </Button>
            <Button
              size="xs"
              variant="ghost"
              disabled={mutation.isPending}
              onClick={() => setChoosing(false)}
            >
              {t(($) => $.acceptance.cancel)}
            </Button>
            {catalog.isError && (
              <Button
                size="xs"
                variant="link"
                onClick={() => void catalog.refetch()}
              >
                {t(($) => $.method.retry)}
              </Button>
            )}
          </div>
        )}
        {method && (
          <>
            {closeout && <IssueCloseout closeout={closeout} actorName={actorName} />}
            {acceptance}
            {run.state !== "closed" && step && (
              <>
                <div className="grid gap-2 rounded-md bg-muted/30 p-3">
                  <h4 className="font-medium">
                    {run.step + 1} · {step.name}
                  </h4>
                  <WorkMethodStepDetail
                    step={step}
                    roles={roles}
                    showChecks={!reviewed || !canAct}
                  />
                  {reviewed && lastStep && (
                    <p className="whitespace-pre-wrap">
                      {t(($) => $.method.review)} · {method.review}
                    </p>
                  )}
                </div>
                {run.current_actor && (
                  <p>
                    {t(($) => $.method.current_actor)} ·{" "}
                    {handlerName(run.current_actor)}
                  </p>
                )}
                {startsHandoff && step.owner && (
                  <div>
                    <Button
                      size="sm"
                      disabled={mutation.isPending}
                      onClick={() =>
                        mutation.mutate({
                          action: "start",
                          method_id: method.id,
                          method_revision: method.revision,
                          base_revision: run.revision,
                        })
                      }
                    >
                      {t(($) => $.method.start_handoff, {
                        name: handlerName(step.owner),
                      })}
                    </Button>
                  </div>
                )}
                {delivery && (
                  <div className="min-w-0 border-l-2 border-border pl-3">
                    <p className="text-caption font-medium text-muted-foreground">
                      {t(($) => $.method.latest_delivery)} ·{" "}
                      {method.steps[delivery.step]?.name} ·{" "}
                      {actorName(delivery.actor)}
                    </p>
                    <p className="break-words whitespace-pre-wrap">
                      {delivery.evidence}
                    </p>
                  </div>
                )}
                {returned && (
                  <div className="min-w-0 border-l-2 border-warning pl-3">
                    <p className="text-caption font-medium">
                      {t(($) => $.method.return_reason)} ·{" "}
                      {actorName(returned.actor)}
                    </p>
                    <p className="break-words whitespace-pre-wrap">
                      {returned.evidence}
                    </p>
                  </div>
                )}
                {canAct && (
                  <>
                    {reviewed && checks.length > 0 && (
                      <fieldset className="rounded-md border border-surface-border p-3">
                        <legend className="px-1 font-medium">
                          {t(($) => $.method.checks)}
                        </legend>
                        <div className="grid gap-2">
                          {checks.map((check, i) => (
                            <label key={i} className="flex items-start gap-2">
                              <input
                                type="checkbox"
                                checked={checked.includes(i)}
                                onChange={(e) =>
                                  setCheckReview({
                                    revision: run.revision,
                                    checked: e.target.checked
                                      ? [...checked, i]
                                      : checked.filter((item) => item !== i),
                                  })
                                }
                              />
                              {check}
                            </label>
                          ))}
                        </div>
                      </fieldset>
                    )}
                    <label className="grid gap-1.5">
                      {reviewed
                        ? t(($) => $.method.review_evidence)
                        : t(($) => $.method.evidence)}
                      <textarea
                        className="min-h-16 rounded-md border border-input bg-background p-2"
                        value={draft?.value ?? ""}
                        onChange={(e) =>
                          setDraft({
                            revision: draft?.revision ?? run.revision,
                            methodId: draft?.methodId ?? method.id,
                            methodRevision:
                              draft?.methodRevision ?? method.revision,
                            value: e.target.value,
                          })
                        }
                      />
                    </label>
                    {stale && (
                      <div role="alert">
                        {t(($) => $.method.stale)}{" "}
                        <Button
                          size="xs"
                          variant="link"
                          onClick={() => setDraft(null)}
                        >
                          {t(($) => $.method.restart)}
                        </Button>
                      </div>
                    )}
                    {reviewed && (
                      <label className="grid gap-1.5">
                        {t(($) => $.method.rework_step)}
                        <select
                          className="rounded-md border border-input bg-background p-1.5"
                          value={returnTo}
                          onChange={(e) =>
                            setReturnStep(Number(e.target.value))
                          }
                        >
                          {method.steps.slice(0, run.step + 1).map((s, i) => (
                            <option key={i} value={i}>
                              {i + 1} · {s.name}
                            </option>
                          ))}
                        </select>
                      </label>
                    )}
                    <div className="flex flex-wrap gap-2">
                      {reviewed ? (
                        <>
                          {!failedCandidateReview && (
                            <Button
                              size="sm"
                              disabled={disabled || !reviewComplete}
                              onClick={() => act("approve")}
                            >
                              {lastStep
                                ? t(($) => $.method.approve)
                                : t(($) => $.method.approve_next)}
                            </Button>
                          )}
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={disabled}
                            onClick={() => act("rework")}
                          >
                            {t(($) => $.method.rework)}
                          </Button>
                        </>
                      ) : (
                        <Button
                          size="sm"
                          disabled={disabled}
                          onClick={() => act("advance")}
                        >
                          {lastStep || step.reviewer
                            ? t(($) => $.method.submit_review)
                            : t(($) => $.method.advance)}
                        </Button>
                      )}
                    </div>
                  </>
                )}
              </>
            )}
            <details className="rounded-md bg-muted/20 px-3 py-2">
              <summary className="cursor-pointer">
                {t(($) => $.method.steps)}
              </summary>
              <WorkMethodSteps
                steps={method.steps}
                current={run.state === "closed" ? undefined : run.step}
                roles={roles}
              />
            </details>
            {run.history.length > 0 && (
              <details className="rounded-md bg-muted/20 px-3 py-2">
                <summary className="cursor-pointer">
                  {t(($) => $.method.history)} ({run.history.length})
                </summary>
                <ol className="mt-2 space-y-2">
                  {run.history.map((entry, i) => (
                    <li
                      key={i}
                      className="grid gap-1 border-l border-border pl-3"
                    >
                      <p>
                        {actionText(entry.action)}
                        {entry.previous_method && entry.method_revision && (
                          <> v{entry.previous_method.revision} → v{entry.method_revision}</>
                        )} ·{" "}
                        {entry.from_step !== undefined &&
                          entry.from_step !== entry.step && (
                            <>{method.steps[entry.from_step]?.name} → </>
                          )}
                        {method.steps[entry.step]?.name ?? method.name}
                      </p>
                      {entry.checked_items && (
                        <ul className="space-y-1 text-caption">
                          {historicalMethods[i]?.steps[
                            entry.from_step ?? entry.step
                          ]?.checks?.map((text, idx) => (
                            <li key={idx}>
                              {entry.checked_items?.includes(idx)
                                ? t(($) => $.method.confirmed)
                                : t(($) => $.method.unconfirmed)}{" "}
                              · {text}
                            </li>
                          ))}
                        </ul>
                      )}
                      {entry.findings?.map((finding, index) => (
                        <div key={index} className="grid gap-1 break-words text-caption">
                          {(finding.criterion_id || finding.case_id) && (
                            <p className="font-medium">{finding.criterion_id} {finding.case_id}</p>
                          )}
                          <p>{finding.expected} → {finding.actual}</p>
                          <p className="whitespace-pre-wrap">{finding.evidence}</p>
                        </div>
                      ))}
                      {entry.evidence && (
                        <p className="whitespace-pre-wrap break-words">
                          {entry.evidence}
                        </p>
                      )}
                      <p className="break-words text-caption text-muted-foreground">
                        <span title={entry.actor}>
                          {actorName(entry.actor)}
                        </span>{" "}
                        ·{" "}
                        <time dateTime={entry.at}>
                          {Number.isFinite(Date.parse(entry.at))
                            ? new Date(entry.at).toLocaleString(locale)
                            : entry.at}
                        </time>
                      </p>
                    </li>
                  ))}
                </ol>
              </details>
            )}
          </>
        )}
        {mutation.error && (
          <p role="alert" className="text-destructive">
            {t(($) => $.method.save_error)} {mutation.error.message}
          </p>
        )}
      </div>
    </details>
  );
}
