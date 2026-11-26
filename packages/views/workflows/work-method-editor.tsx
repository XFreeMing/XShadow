"use client";

import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import { workMethodCatalogOptions } from "@multica/core/issue-workflows/queries";
import {
  WorkMethodKindSchema,
  type NewWorkMethod,
  type WorkMethodKind,
  type WorkMethodStep,
  type WorkMethod,
  type WorkMethodHandler,
} from "@multica/core/types/work-method";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";
import { WorkMethodHandlerPicker } from "./work-method-step";

const selectClass =
  "w-full rounded-md border border-input bg-background px-2 py-1.5 text-label";
const labelClass = "grid gap-1.5 text-label font-medium";

export function WorkMethodEditor({
  wsId,
  original,
  onClose,
}: {
  wsId: string;
  original: WorkMethod;
  onClose: () => void;
}) {
  const { t } = useT("issues");
  const qc = useQueryClient();
  type DraftStep = Pick<WorkMethodStep, "acceptance_gate" | "self_claim" | "require_preflight" | "table_gate"> & {
    id: number;
    name: string;
    output: string;
    inputs: string;
    instructions: string;
    checks: string;
    owner: WorkMethodHandler | null;
    reviewer: WorkMethodHandler | null;
  };
  const emptyStep = {
    name: "",
    output: "",
    inputs: "",
    instructions: "",
    checks: "",
    owner: null,
    reviewer: null,
  };
  const [steps, setSteps] = useState<DraftStep[]>(() =>
    original.steps.map((step, i) => ({
          id: i,
          acceptance_gate: step.acceptance_gate,
          self_claim: step.self_claim,
          require_preflight: step.require_preflight,
          table_gate: step.table_gate,
          name: step.name,
          output: step.output,
          inputs: step.inputs ?? "",
          instructions: step.instructions ?? "",
          checks: step.checks?.join("\n") ?? "",
          owner: step.owner ?? null,
          reviewer: step.reviewer ?? null,
        })),
  );
  const [rework, setRework] = useState(original.rework_step);
  const [kind, setKind] = useState<WorkMethodKind>(
    original.kind,
  );
  const [name, setName] = useState(original.name);
  const [keywords, setKeywords] = useState(original.keywords.join(", "));
  const [review, setReview] = useState(original.review);
  const changeStep = (id: number, patch: Partial<Omit<DraftStep, "id">>) =>
    setSteps((current) =>
      current.map((s) => (s.id === id ? { ...s, ...patch } : s)),
    );
  const mutation = useMutation({
    mutationFn: (draft: NewWorkMethod) =>
      api.updateWorkMethod(original.id, original.revision, draft),
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409)
        void qc.invalidateQueries({
          queryKey: workMethodCatalogOptions(wsId).queryKey,
        });
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["work-methods", wsId] });
      onClose();
    },
  });
  function removeStep(id: number) {
    const next = steps.filter((step) => step.id !== id);
    setSteps(next);
    if (rework === id && next[0]) setRework(next[0].id);
  }
  function submit(e: FormEvent) {
    e.preventDefault();
    mutation.mutate({
      name: name.trim(),
      kind,
      ...(original.plan_task ? { plan_task: original.plan_task } : {}),
      ...(original.rework_task ? { rework_task: original.rework_task } : {}),
      ...(kind === "development" && original.github_delivery ? { github_delivery: original.github_delivery } : {}),
      keywords: [
        ...new Set(
          keywords
            .split(/[,，、\n]/u)
            .map((word) => word.trim())
            .filter(Boolean),
        ),
      ],
      steps: steps.map((step) => ({
        ...(step.table_gate ? { table_gate: step.table_gate } : {}),
        ...(kind === "development" && step.acceptance_gate === "coverage" && step.require_preflight
          ? { require_preflight: true }
          : {}),
        ...(kind === "development" && step.acceptance_gate ? { acceptance_gate: step.acceptance_gate } : {}),
        ...(kind === "development" && step.acceptance_gate === "candidate" && step.owner?.kind === "squad" && step.self_claim
          ? { self_claim: true }
          : {}),
        name: step.name.trim(),
        output: step.output.trim(),
        ...(step.inputs.trim() ? { inputs: step.inputs.trim() } : {}),
        ...(step.instructions.trim()
          ? { instructions: step.instructions.trim() }
          : {}),
        ...(step.checks.trim()
          ? {
              checks: step.checks
                .split("\n")
                .map((s) => s.trim())
                .filter(Boolean),
            }
          : {}),
        ...(step.owner && step.reviewer
          ? { owner: step.owner, reviewer: step.reviewer }
          : {}),
      })),
      review: review.trim(),
      rework_step: steps.findIndex((step) => step.id === rework),
    });
  }
  return (
    <form
      onSubmit={submit}
      className="grid gap-4 rounded-lg border border-surface-border bg-card p-4"
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <label className={labelClass}>
          {t(($) => $.method.name)}
          <Input
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label className={labelClass}>
          {t(($) => $.method.kind)}
          <select
            className={selectClass}
            value={kind}
            onChange={(e) => {
              const parsed = WorkMethodKindSchema.safeParse(e.target.value);
              if (parsed.success) setKind(parsed.data);
            }}
          >
            {WorkMethodKindSchema.options.map((value) => (
              <option value={value} key={value}>
                {t(($) => $.method[value])}
              </option>
            ))}
          </select>
        </label>
      </div>
      <label className={labelClass}>
        {t(($) => $.method.keywords)}
        <Input
          value={keywords}
          onChange={(e) => setKeywords(e.target.value)}
          placeholder={t(($) => $.method.keyword_hint)}
        />
      </label>
      <div className="grid gap-3">
        {steps.map((step, i) => (
          <fieldset
            key={step.id}
            className="grid gap-2 rounded-md border border-surface-border p-3"
          >
            <legend className="px-1 text-caption font-medium">
              {t(($) => $.method.step_label)} {i + 1}
            </legend>
            <div className="grid gap-2 sm:grid-cols-2">
              {(["name", "output"] as const).map((field) => (
                <label key={field} className={labelClass}>
                  {field === "name"
                    ? t(($) => $.method.step_name)
                    : t(($) => $.method.output)}
                  <Input
                    required
                    value={step[field]}
                    onChange={(e) =>
                      setSteps(
                        steps.map((current) =>
                          current.id === step.id
                            ? { ...current, [field]: e.target.value }
                            : current,
                        ),
                      )
                    }
                  />
                </label>
              ))}
            </div>
            {(["inputs", "instructions", "checks"] as const).map((field) => (
              <label key={field} className={labelClass}>
                {t(($) => $.method[field])}
                <textarea
                  className={selectClass}
                  value={step[field]}
                  placeholder={
                    field === "checks"
                      ? t(($) => $.method.check_hint)
                      : undefined
                  }
                  onChange={(e) =>
                    changeStep(step.id, { [field]: e.target.value })
                  }
                />
              </label>
            ))}
            <div className="grid gap-2 sm:grid-cols-2">
              <WorkMethodHandlerPicker
                wsId={wsId}
                label={t(($) => $.method.owner)}
                handler={step.owner}
                onChange={(owner) => changeStep(step.id, { owner })}
              />
              <WorkMethodHandlerPicker
                wsId={wsId}
                label={t(($) => $.method.reviewer)}
                handler={step.reviewer}
                onChange={(reviewer) => changeStep(step.id, { reviewer })}
              />
            </div>
            {Boolean(step.owner) !== Boolean(step.reviewer) && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.method.pair_hint)}
              </p>
            )}
            {steps.length > 1 && (
              <div>
                <Button
                  type="button"
                  size="xs"
                  variant="ghost"
                  onClick={() => removeStep(step.id)}
                >
                  {t(($) => $.method.remove)}
                </Button>
              </div>
            )}
          </fieldset>
        ))}
        <div>
          <Button
            type="button"
            size="xs"
            variant="outline"
            disabled={steps.length >= 32}
            onClick={() =>
              setSteps([
                ...steps,
                {
                  id: Math.max(...steps.map((s) => s.id)) + 1,
                  ...emptyStep,
                },
              ])
            }
          >
            {t(($) => $.method.add_step)}
          </Button>
        </div>
      </div>
      <label className={labelClass}>
        {t(($) => $.method.review)}
        <Input
          required
          value={review}
          onChange={(e) => setReview(e.target.value)}
        />
      </label>
      <label className={labelClass}>
        {t(($) => $.method.rework_step)}
        <select
          className={selectClass}
          value={rework}
          onChange={(e) => setRework(Number(e.target.value))}
        >
          {steps.map((step, i) => (
            <option key={step.id} value={step.id}>
              {i + 1} · {step.name}
            </option>
          ))}
        </select>
      </label>
      {mutation.isError && (
        <p role="alert" className="text-label text-destructive">
          {t(($) => $.method.save_error)} {mutation.error.message}
        </p>
      )}
      <footer className="flex gap-2">
        <Button
          size="sm"
          type="submit"
          disabled={
            mutation.isPending ||
            !name.trim() ||
            !review.trim() ||
            steps.some(
              (s) =>
                !s.name.trim() ||
                !s.output.trim() ||
                Boolean(s.owner) !== Boolean(s.reviewer),
            )
          }
        >
          {t(($) => $.acceptance.save)}
        </Button>
        <Button
          size="sm"
          type="button"
          variant="ghost"
          disabled={mutation.isPending}
          onClick={onClose}
        >
          {t(($) => $.acceptance.cancel)}
        </Button>
      </footer>
    </form>
  );
}
