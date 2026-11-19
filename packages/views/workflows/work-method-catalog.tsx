"use client";

import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import { workMethodCatalogOptions } from "@multica/core/issue-workflows/queries";
import {
  WorkMethodKindSchema,
  type NewWorkMethod,
  type WorkMethodKind,
} from "@multica/core/types/work-method";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";

const selectClass =
  "w-full rounded-md border border-input bg-background px-2 py-1.5 text-label";
const labelClass = "grid gap-1.5 text-label font-medium";

export function WorkMethodCatalog({
  wsId,
  canCreate,
}: {
  wsId: string;
  canCreate: boolean;
}) {
  const { t } = useT("issues");
  const query = useQuery({
    ...workMethodCatalogOptions(wsId),
    enabled: !!wsId,
  });
  const [editing, setEditing] = useState(false);
  if (query.error instanceof ApiError && query.error.status === 404)
    return null;
  return (
    <section className="mt-6 space-y-3">
      <header className="flex items-center justify-between gap-3">
        <h2 className="text-heading font-semibold">
          {t(($) => $.method.catalog)}
        </h2>
        {canCreate && !editing && (
          <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
            {t(($) => $.method.create)}
          </Button>
        )}
      </header>
      {editing && (
        <MethodEditor wsId={wsId} onClose={() => setEditing(false)} />
      )}
      {query.isError && (
        <p role="alert" className="text-label text-destructive">
          {t(($) => $.method.load_error)}{" "}
          <Button variant="link" size="xs" onClick={() => void query.refetch()}>
            {t(($) => $.method.retry)}
          </Button>
        </p>
      )}
      <div className="divide-y divide-surface-border rounded-lg border border-surface-border bg-card">
        {query.data?.map((method) => (
          <details key={method.id} className="px-4 py-3 text-label">
            <summary className="cursor-pointer font-medium">
              {method.name}{" "}
              <span className="font-normal text-muted-foreground">
                · {t(($) => $.method[method.kind])}
              </span>
            </summary>
            <ol className="mt-3 list-decimal space-y-1 pl-5">
              {method.steps.map((step, i) => (
                <li key={i}>
                  <strong>{step.name}</strong> · {step.output}
                </li>
              ))}
            </ol>
            <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
              {method.keywords.length > 0 && (
                <>
                  <dt className="text-muted-foreground">
                    {t(($) => $.method.keywords)}
                  </dt>
                  <dd>{method.keywords.join(", ")}</dd>
                </>
              )}
              <dt className="text-muted-foreground">
                {t(($) => $.method.review)}
              </dt>
              <dd>{method.review}</dd>
              <dt className="text-muted-foreground">
                {t(($) => $.method.rework_step)}
              </dt>
              <dd>{method.steps[method.rework_step]?.name}</dd>
            </dl>
          </details>
        ))}
      </div>
    </section>
  );
}

function MethodEditor({
  wsId,
  onClose,
}: {
  wsId: string;
  onClose: () => void;
}) {
  const { t } = useT("issues");
  const qc = useQueryClient();
  const [steps, setSteps] = useState([{ id: 0, name: "", output: "" }]);
  const [rework, setRework] = useState(0);
  const [kind, setKind] = useState<WorkMethodKind>("development");
  const [name, setName] = useState("");
  const [keywords, setKeywords] = useState("");
  const [review, setReview] = useState("");
  const mutation = useMutation({
    mutationFn: (draft: NewWorkMethod) => api.createWorkMethod(draft),
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
      keywords: [
        ...new Set(
          keywords
            .split(/[,，、\n]/u)
            .map((word) => word.trim())
            .filter(Boolean),
        ),
      ],
      steps: steps.map((step) => ({
        name: step.name.trim(),
        output: step.output.trim(),
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
            onClick={() =>
              setSteps([
                ...steps,
                {
                  id: Math.max(...steps.map((s) => s.id)) + 1,
                  name: "",
                  output: "",
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
            steps.some((s) => !s.name.trim() || !s.output.trim())
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
