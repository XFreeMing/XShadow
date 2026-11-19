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
import { Button } from "@multica/ui/components/ui/button";
import { useLocale, useT } from "../../i18n";
import { useActorName } from "@multica/core/workspace/hooks";

export function IssueWorkMethod({
  wsId,
  issueId,
  issueRevision,
  children,
}: {
  wsId: string;
  issueId: string;
  issueRevision?: number;
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
      {run.data && <MethodCard wsId={wsId} issueId={issueId} run={run.data} />}
      {run.data &&
        (!run.data.method || run.data.method.kind === "development") &&
        children}
    </>
  );
}

function MethodCard({
  wsId,
  issueId,
  run,
}: {
  wsId: string;
  issueId: string;
  run: WorkMethodRun;
}) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const locale = useLocale();
  function actorName(actor: string) {
    const [type, id] = actor.split("/");
    return type && id ? getActorName(type, id) : actor;
  }
  const qc = useQueryClient();
  const [choosing, setChoosing] = useState(false);
  const [selected, setSelected] = useState("");
  const [draft, setDraft] = useState<{
    value: string;
    revision: number;
    methodId: string;
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
    (draft.revision !== run.revision || draft.methodId !== method?.id);
  const evidence = draft?.value.trim() ?? "";
  const disabled = stale || mutation.isPending || !evidence;
  const reviewed = run.state === "review";
  function act(action: "advance" | "approve" | "rework") {
    if (disabled || !method) return;
    const base = { base_revision: run.revision, evidence };
    if (action === "advance")
      mutation.mutate({
        ...base,
        action,
        ...(run.state === "ready" ? { method_id: method.id } : {}),
      });
    else if (action === "rework")
      mutation.mutate({
        ...base,
        action,
        target_step: returnStep ?? method.rework_step,
      });
    else mutation.mutate({ ...base, action });
  }
  const actionText = (action: string) => {
    switch (action) {
      case "select":
        return t(($) => $.method.select);
      case "advance":
        return t(($) => $.method.advance);
      case "approve":
        return t(($) => $.method.approve);
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
        {run.reason && <p className="text-muted-foreground">{run.reason}</p>}
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
            {run.state !== "closed" && (
              <>
                <div className="rounded-md bg-muted/30 p-3">
                  <h4 className="font-medium">
                    {reviewed ? t(($) => $.method.review) : step?.name}
                  </h4>
                  <p className="whitespace-pre-wrap break-words text-muted-foreground">
                    {reviewed ? method.review : step?.output}
                  </p>
                </div>
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
                        value: e.target.value,
                      })
                    }
                  />
                </label>
                {stale && (
                  <div role="alert">
                    {t(($) => $.method.stale)}{" "}
                    <Button
                      variant="link"
                      size="xs"
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
                      value={returnStep ?? method.rework_step}
                      onChange={(e) => setReturnStep(Number(e.target.value))}
                    >
                      {method.steps.map((s, i) => (
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
                      <Button
                        size="sm"
                        disabled={disabled}
                        onClick={() => act("approve")}
                      >
                        {t(($) => $.method.approve)}
                      </Button>
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
                      disabled={disabled || !step}
                      onClick={() => act("advance")}
                    >
                      {run.step === method.steps.length - 1
                        ? t(($) => $.method.submit_review)
                        : t(($) => $.method.advance)}
                    </Button>
                  )}
                </div>
              </>
            )}
            <details className="rounded-md bg-muted/20 px-3 py-2">
              <summary className="cursor-pointer">
                {t(($) => $.method.steps)}
              </summary>
              <ol className="mt-2 list-decimal space-y-1 pl-5">
                {method.steps.map((s, i) => (
                  <li
                    key={i}
                    aria-current={
                      !reviewed && run.state !== "closed" && i === run.step
                        ? "step"
                        : undefined
                    }
                  >
                    <strong>{s.name}</strong> · {s.output}
                  </li>
                ))}
              </ol>
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
                        {actionText(entry.action)} ·{" "}
                        {method.steps[entry.step]?.name ?? method.name}
                      </p>
                      {entry.evidence && (
                        <p className="whitespace-pre-wrap break-words">
                          {entry.evidence}
                        </p>
                      )}
                      <p className="break-words text-caption text-muted-foreground">
                        <span title={entry.actor}>{actorName(entry.actor)}</span> ·{" "}
                        <time dateTime={entry.at}>{Number.isFinite(Date.parse(entry.at)) ? new Date(entry.at).toLocaleString(locale) : entry.at}</time>
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
