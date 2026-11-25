"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import type { GridRecordOption } from "@multica/apitable-grid";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { ApiError, api } from "@multica/core/api";
import type {
  GenericCellValue,
  GenericField,
} from "@multica/core/tables";
import { useT } from "../../i18n";
import { isEditableField } from "./view-helpers";

type FormDraftValues = Record<string, string | boolean | string[]>;

function initialDraft(fields: readonly GenericField[]): FormDraftValues {
  const values: FormDraftValues = {};
  for (const field of fields) {
    if (field.kind === "checkbox") values[field.id] = false;
    else if (field.kind === "relation") values[field.id] = [];
    else values[field.id] = "";
  }
  return values;
}

export function buildFormPayload(
  fields: readonly GenericField[],
  draft: FormDraftValues,
): Record<string, GenericCellValue> {
  const payload: Record<string, GenericCellValue> = {};
  for (const field of fields) {
    if (!isEditableField(field)) continue;
    const raw = draft[field.id];
    if (field.kind === "checkbox") {
      payload[field.id] = raw === true;
    } else if (field.kind === "relation") {
      const ids = Array.isArray(raw) ? raw.filter((item): item is string => typeof item === "string") : [];
      if (ids.length > 0) payload[field.id] = ids;
    } else if (field.kind === "number") {
      const text = typeof raw === "string" ? raw.trim() : "";
      if (text !== "") {
        const parsed = Number(text);
        if (Number.isFinite(parsed)) payload[field.id] = parsed;
      }
    } else if (field.kind === "date" || field.kind === "single_select") {
      const text = typeof raw === "string" ? raw.trim() : "";
      if (text !== "") payload[field.id] = text;
    } else {
      const text = typeof raw === "string" ? raw : "";
      if (text.trim() !== "") payload[field.id] = text;
    }
  }
  return payload;
}

export function FormView({
  tableId,
  viewId,
  viewName,
  visibleFields,
  requiredFieldIds,
  recordOptionsByTable,
  onSubmitted,
}: {
  tableId: string;
  viewId: string;
  viewName: string;
  visibleFields: readonly GenericField[];
  requiredFieldIds: readonly string[];
  recordOptionsByTable: Readonly<Record<string, readonly GridRecordOption[]>>;
  onSubmitted: () => Promise<void> | void;
}) {
  const { t } = useT("tables");
  const editableFields = useMemo(
    () => visibleFields.filter(isEditableField),
    [visibleFields],
  );
  const requiredSet = useMemo(() => new Set(requiredFieldIds), [requiredFieldIds]);

  const fieldSignature = editableFields.map((f) => f.id).join(",");
  const [draft, setDraft] = useState<FormDraftValues>(() => initialDraft(editableFields));
  const [error, setError] = useState<string | null>(null);
  const [submittedNotice, setSubmittedNotice] = useState(false);

  useEffect(() => {
    setDraft((prev) => {
      const base = initialDraft(editableFields);
      for (const field of editableFields) {
        if (prev[field.id] !== undefined) base[field.id] = prev[field.id]!;
      }
      return base;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewId, fieldSignature]);

  const submitMutation = useMutation({
    mutationFn: (values: Record<string, GenericCellValue>) =>
      api.submitGenericForm(tableId, viewId, values),
    onSuccess: async () => {
      setError(null);
      setSubmittedNotice(true);
      setDraft(initialDraft(editableFields));
      await onSubmitted();
    },
    onError: (err) => {
      setSubmittedNotice(false);
      setError(err instanceof ApiError ? err.message : t(($) => $.sheet.save_failed));
    },
  });

  if (editableFields.length === 0) {
    return (
      <div className="p-6 text-body text-muted-foreground" data-view-empty="form">
        {t(($) => $.sheet.empty_form_fields)}
      </div>
    );
  }

  return (
    <div className="mx-auto w-full max-w-xl p-6" data-view-kind="form">
      <div className="rounded-lg border border-border bg-background p-5 shadow-2xs">
        <h2 className="mb-4 text-title-sm font-semibold">{viewName}</h2>
        <form
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            setError(null);
            setSubmittedNotice(false);
            const payload = buildFormPayload(editableFields, draft);
            submitMutation.mutate(payload);
          }}
        >
          {editableFields.map((field) => {
            const isRequired = requiredSet.has(field.id);
            const inputId = `form-field-${field.id}`;
            const raw = draft[field.id];
            return (
              <div key={field.id} className="flex flex-col gap-1.5" data-form-field={field.id}>
                <label htmlFor={inputId} className="text-body font-medium">
                  {field.name}
                  {isRequired ? <span className="ml-1 text-destructive" aria-label={t(($) => $.sheet.required)}>*</span> : null}
                </label>

                {field.kind === "text" ? (
                  <Input
                    id={inputId}
                    value={typeof raw === "string" ? raw : ""}
                    onChange={(event) => setDraft((prev) => ({ ...prev, [field.id]: event.target.value }))}
                  />
                ) : field.kind === "number" ? (
                  <Input
                    id={inputId}
                    type="number"
                    step="any"
                    value={typeof raw === "string" ? raw : ""}
                    onChange={(event) => setDraft((prev) => ({ ...prev, [field.id]: event.target.value }))}
                  />
                ) : field.kind === "date" ? (
                  <Input
                    id={inputId}
                    type="date"
                    value={typeof raw === "string" ? raw : ""}
                    onChange={(event) => setDraft((prev) => ({ ...prev, [field.id]: event.target.value }))}
                  />
                ) : field.kind === "checkbox" ? (
                  <label className="flex items-center gap-2 text-body">
                    <input
                      id={inputId}
                      type="checkbox"
                      checked={raw === true}
                      onChange={(event) => setDraft((prev) => ({ ...prev, [field.id]: event.target.checked }))}
                    />
                    <span className="text-muted-foreground">
                      {raw === true ? t(($) => $.sheet.checked) : t(($) => $.sheet.unchecked)}
                    </span>
                  </label>
                ) : field.kind === "single_select" ? (
                  <select
                    id={inputId}
                    value={typeof raw === "string" ? raw : ""}
                    onChange={(event) => setDraft((prev) => ({ ...prev, [field.id]: event.target.value }))}
                    className="h-9 rounded-md border border-border bg-transparent px-2 text-body"
                  >
                    <option value="">{t(($) => $.sheet.none)}</option>
                    {(field.config.options ?? []).map((opt) => (
                      <option key={opt.id} value={opt.id}>
                        {opt.label}
                      </option>
                    ))}
                  </select>
                ) : field.kind === "relation" ? (
                  <div className="max-h-40 space-y-1 overflow-y-auto rounded-md border border-border p-2">
                    {((field.config.target_table_id ? recordOptionsByTable[field.config.target_table_id] : null) ?? []).length === 0 ? (
                      <p className="text-caption text-muted-foreground">{t(($) => $.sheet.no_matching_records)}</p>
                    ) : (
                      ((field.config.target_table_id ? recordOptionsByTable[field.config.target_table_id] : null) ?? []).map((opt) => {
                        const selected = Array.isArray(raw) && raw.includes(opt.id);
                        return (
                          <label key={opt.id} className="flex cursor-pointer items-center gap-2 text-body">
                            <input
                              type="checkbox"
                              checked={selected}
                              onChange={() => {
                                setDraft((prev) => {
                                  const current = Array.isArray(prev[field.id]) ? (prev[field.id] as string[]) : [];
                                  const next = current.includes(opt.id)
                                    ? current.filter((item) => item !== opt.id)
                                    : [...current, opt.id];
                                  return { ...prev, [field.id]: next };
                                });
                              }}
                            />
                            <span className="truncate">{opt.label}</span>
                          </label>
                        );
                      })
                    )}
                  </div>
                ) : null}
              </div>
            );
          })}

          {error ? (
            <p className="text-body text-destructive" role="alert">
              {error}
            </p>
          ) : null}
          {submittedNotice ? (
            <p className="text-body text-emerald-600 dark:text-emerald-400" role="status">
              {t(($) => $.sheet.form_submitted)}
            </p>
          ) : null}

          <div className="flex items-center justify-end gap-2 pt-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                setDraft(initialDraft(editableFields));
                setError(null);
                setSubmittedNotice(false);
              }}
            >
              {t(($) => $.sheet.clear)}
            </Button>
            <Button type="submit" size="sm" disabled={submitMutation.isPending}>
              {t(($) => $.sheet.submit_form)}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
