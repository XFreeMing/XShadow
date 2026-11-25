"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import {
  genericFieldKinds,
  genericRecordOptionsQuery,
  genericSheetOptions,
  genericTableListOptions,
  type GenericField,
  type GenericFieldKind,
  type GenericFieldPatch,
  type GenericFieldWrite,
} from "@multica/core/tables";
import { useT } from "../../i18n";

export function FieldCreateForm({
  wsId,
  tableId,
  fields,
  pending,
  error,
  onSubmit,
}: {
  wsId: string;
  tableId: string;
  fields: readonly GenericField[];
  pending: boolean;
  error: string | null;
  onSubmit: (body: GenericFieldWrite) => void;
}) {
  const { t } = useT("tables");
  const [name, setName] = useState("");
  const [kind, setKind] = useState<GenericFieldKind>("text");
  const [options, setOptions] = useState("");
  const [targetTableId, setTargetTableId] = useState("");
  const [bidirectional, setBidirectional] = useState(true);
  const [relationFieldId, setRelationFieldId] = useState("");
  const [targetFieldId, setTargetFieldId] = useState("");
  const [expression, setExpression] = useState("");

  const tablesQuery = useQuery({
    ...genericTableListOptions(wsId),
    enabled: Boolean(wsId && kind === "relation"),
  });
  const availableTables = useMemo(
    () => tablesQuery.data?.tables ?? [],
    [tablesQuery.data?.tables],
  );
  const isSelfRelation = kind === "relation" && targetTableId === tableId;

  const relationFields = useMemo(
    () => fields.filter((field) => field.kind === "relation"),
    [fields],
  );
  const selectedRelation = relationFields.find((field) => field.id === relationFieldId) ?? null;
  const lookupTargetTableId = selectedRelation?.config.target_table_id ?? "";
  const targetSheetQuery = useQuery({
    ...genericSheetOptions(wsId, lookupTargetTableId, null),
    enabled: Boolean(wsId && kind === "lookup" && lookupTargetTableId),
  });
  const targetFields = targetSheetQuery.data?.fields ?? [];

  const canSubmit = (() => {
    if (pending || name.trim() === "") return false;
    if (kind === "single_select") return options.trim() !== "";
    if (kind === "relation") return targetTableId !== "";
    if (kind === "lookup") return relationFieldId !== "" && targetFieldId !== "";
    if (kind === "formula") return expression.trim() !== "";
    return true;
  })();

  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault();
        if (!canSubmit) return;
        const trimmedName = name.trim();
        let config: GenericFieldWrite["config"];
        if (kind === "single_select") {
          config = {
            options: options
              .split("\n")
              .map((line) => line.trim())
              .filter(Boolean)
              .map((label, index) => ({ id: `opt${index + 1}`, label })),
          };
        } else if (kind === "relation") {
          config = { target_table_id: targetTableId, bidirectional: isSelfRelation ? false : bidirectional };
        } else if (kind === "lookup") {
          config = { relation_field_id: relationFieldId, target_field_id: targetFieldId };
        } else if (kind === "formula") {
          config = { expression: expression.trim() };
        }
        onSubmit({ name: trimmedName, kind, config });
      }}
    >
      <Input
        aria-label={t(($) => $.sheet.field_name)}
        value={name}
        onChange={(event) => setName(event.target.value)}
        placeholder={t(($) => $.sheet.field_name)}
      />
      <select
        aria-label={t(($) => $.sheet.kind)}
        value={kind}
        onChange={(event) => {
          setKind(event.target.value as GenericFieldKind);
          setTargetFieldId("");
        }}
        className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
      >
        {genericFieldKinds.map((item) => (
          <option key={item} value={item}>
            {t(($) => $.sheet.kinds[item])}
          </option>
        ))}
      </select>

      {kind === "single_select" ? (
        <textarea
          aria-label={t(($) => $.sheet.options)}
          value={options}
          onChange={(event) => setOptions(event.target.value)}
          placeholder={t(($) => $.sheet.options_hint)}
          className="min-h-20 rounded-md border border-border bg-transparent px-2 py-1 text-body"
        />
      ) : null}

      {kind === "relation" ? (
        <div className="flex flex-col gap-2">
          <select
            aria-label={t(($) => $.sheet.target_table)}
            value={targetTableId}
            onChange={(event) => {
              const nextTarget = event.target.value;
              setTargetTableId(nextTarget);
              if (nextTarget === tableId) setBidirectional(false);
            }}
            className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
          >
            <option value="">{t(($) => $.sheet.select_target_table)}</option>
            {availableTables.map((table) => (
              <option key={table.id} value={table.id}>
                {table.id === tableId ? `${table.name} (${t(($) => $.sheet.current_table)})` : table.name}
              </option>
            ))}
          </select>
          <label className="flex items-center gap-2 text-body">
            <input
              type="checkbox"
              checked={isSelfRelation ? false : bidirectional}
              disabled={isSelfRelation}
              onChange={(event) => setBidirectional(event.target.checked)}
            />
            <span className={isSelfRelation ? "text-muted-foreground" : undefined}>
              {isSelfRelation ? t(($) => $.sheet.self_relation_one_way) : t(($) => $.sheet.bidirectional)}
            </span>
          </label>
        </div>
      ) : null}

      {kind === "lookup" ? (
        <LookupConfigFields
          relationFields={relationFields}
          relationFieldId={relationFieldId}
          targetFields={targetFields}
          targetFieldId={targetFieldId}
          onRelationChange={(next) => {
            setRelationFieldId(next);
            setTargetFieldId("");
          }}
          onTargetFieldChange={setTargetFieldId}
        />
      ) : null}

      {kind === "formula" ? (
        <FormulaExpressionEditor
          fields={fields}
          expression={expression}
          onChange={setExpression}
        />
      ) : null}

      {error ? (
        <p className="text-caption text-destructive" role="alert">
          {error}
        </p>
      ) : null}

      <Button type="submit" size="sm" disabled={!canSubmit}>
        {t(($) => $.sheet.add)}
      </Button>
    </form>
  );
}

export function EditDerivedFieldDialog({
  wsId,
  field,
  fields,
  pending,
  error,
  onClose,
  onSubmit,
}: {
  wsId: string;
  field: GenericField | null;
  fields: readonly GenericField[];
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onSubmit: (fieldId: string, body: GenericFieldPatch) => void;
}) {
  const { t } = useT("tables");
  const [name, setName] = useState("");
  const [relationFieldId, setRelationFieldId] = useState("");
  const [targetFieldId, setTargetFieldId] = useState("");
  const [expression, setExpression] = useState("");

  useEffect(() => {
    if (!field) return;
    setName(field.name);
    setRelationFieldId(field.config.relation_field_id ?? "");
    setTargetFieldId(field.config.target_field_id ?? "");
    setExpression(field.config.expression ?? "");
  }, [field]);

  const relationFields = useMemo(
    () => fields.filter((item) => item.kind === "relation"),
    [fields],
  );
  const selectedRelation = relationFields.find((item) => item.id === relationFieldId) ?? null;
  const lookupTargetTableId = selectedRelation?.config.target_table_id ?? "";
  const targetSheetQuery = useQuery({
    ...genericSheetOptions(wsId, lookupTargetTableId, null),
    enabled: Boolean(wsId && field?.kind === "lookup" && lookupTargetTableId),
  });
  const targetFields = targetSheetQuery.data?.fields ?? [];
  const otherFields = useMemo(
    () => fields.filter((item) => item.id !== field?.id),
    [fields, field?.id],
  );

  const canSave = (() => {
    if (!field || pending || name.trim() === "") return false;
    if (field.kind === "lookup") return relationFieldId !== "" && targetFieldId !== "";
    if (field.kind === "formula") return expression.trim() !== "";
    return false;
  })();

  return (
    <Dialog open={field !== null} onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.sheet.edit_field)}</DialogTitle>
        </DialogHeader>
        {field ? (
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              if (!canSave) return;
              const config = field.kind === "lookup"
                ? { relation_field_id: relationFieldId, target_field_id: targetFieldId }
                : { expression: expression.trim() };
              onSubmit(field.id, { name: name.trim(), config });
            }}
          >
            <Input
              aria-label={t(($) => $.sheet.field_name)}
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={t(($) => $.sheet.field_name)}
            />
            {field.kind === "lookup" ? (
              <LookupConfigFields
                relationFields={relationFields}
                relationFieldId={relationFieldId}
                targetFields={targetFields}
                targetFieldId={targetFieldId}
                onRelationChange={(next) => {
                  setRelationFieldId(next);
                  setTargetFieldId("");
                }}
                onTargetFieldChange={setTargetFieldId}
              />
            ) : null}
            {field.kind === "formula" ? (
              <FormulaExpressionEditor
                fields={otherFields}
                expression={expression}
                onChange={setExpression}
              />
            ) : null}
            {error ? (
              <p className="text-caption text-destructive" role="alert">
                {error}
              </p>
            ) : null}
            <DialogFooter>
              <Button type="button" variant="outline" size="sm" onClick={onClose}>
                {t(($) => $.sheet.cancel)}
              </Button>
              <Button type="submit" size="sm" disabled={!canSave}>
                {t(($) => $.sheet.save)}
              </Button>
            </DialogFooter>
          </form>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function LookupConfigFields({
  relationFields,
  relationFieldId,
  targetFields,
  targetFieldId,
  onRelationChange,
  onTargetFieldChange,
}: {
  relationFields: readonly GenericField[];
  relationFieldId: string;
  targetFields: readonly GenericField[];
  targetFieldId: string;
  onRelationChange: (id: string) => void;
  onTargetFieldChange: (id: string) => void;
}) {
  const { t } = useT("tables");
  return (
    <div className="flex flex-col gap-2">
      <select
        aria-label={t(($) => $.sheet.relation_field)}
        value={relationFieldId}
        onChange={(event) => onRelationChange(event.target.value)}
        className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
      >
        <option value="">{t(($) => $.sheet.select_relation_field)}</option>
        {relationFields.map((field) => (
          <option key={field.id} value={field.id}>
            {field.name}
          </option>
        ))}
      </select>
      {relationFields.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.sheet.no_relation_fields)}</p>
      ) : null}
      {relationFieldId ? (
        <select
          aria-label={t(($) => $.sheet.target_field)}
          value={targetFieldId}
          onChange={(event) => onTargetFieldChange(event.target.value)}
          className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
        >
          <option value="">{t(($) => $.sheet.select_target_field)}</option>
          {targetFields.map((field) => (
            <option key={field.id} value={field.id}>
              {field.name}
            </option>
          ))}
        </select>
      ) : null}
    </div>
  );
}

function FormulaExpressionEditor({
  fields,
  expression,
  onChange,
}: {
  fields: readonly GenericField[];
  expression: string;
  onChange: (next: string) => void;
}) {
  const { t } = useT("tables");
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const insertField = (fieldName: string) => {
    const token = `{${fieldName}}`;
    const node = textareaRef.current;
    if (!node) {
      onChange(`${expression}${token}`);
      return;
    }
    const start = node.selectionStart ?? expression.length;
    const end = node.selectionEnd ?? expression.length;
    const next = `${expression.slice(0, start)}${token}${expression.slice(end)}`;
    onChange(next);
    const caret = start + token.length;
    window.requestAnimationFrame(() => {
      node.focus();
      node.setSelectionRange(caret, caret);
    });
  };

  return (
    <div className="flex flex-col gap-2">
      <textarea
        ref={textareaRef}
        aria-label={t(($) => $.sheet.formula_expression)}
        value={expression}
        onChange={(event) => onChange(event.target.value)}
        placeholder={t(($) => $.sheet.formula_placeholder)}
        className="min-h-20 rounded-md border border-border bg-transparent px-2 py-1 font-mono text-body"
      />
      <p className="text-caption text-muted-foreground">{t(($) => $.sheet.formula_help)}</p>
      {fields.length > 0 ? (
        <div className="flex flex-wrap gap-1" aria-label={t(($) => $.sheet.insert_field)}>
          {fields.map((field) => (
            <button
              key={field.id}
              type="button"
              className="rounded border border-border px-1.5 py-0.5 text-caption hover:bg-accent"
              onClick={() => insertField(field.name)}
            >
              {`{${field.name}}`}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export interface RelationChooserState {
  recordId: string;
  fieldId: string;
  fieldName: string;
  targetTableId: string;
  currentIds: readonly string[];
  baseRevision: number;
}

export function RelationChooserDialog({
  wsId,
  state,
  pending,
  onClose,
  onSave,
}: {
  wsId: string;
  state: RelationChooserState | null;
  pending: boolean;
  onClose: () => void;
  onSave: (nextIds: string[] | null) => void;
}) {
  const { t } = useT("tables");
  const [search, setSearch] = useState("");
  const [selectedIds, setSelectedIds] = useState<string[]>([]);

  useEffect(() => {
    if (!state) return;
    setSearch("");
    setSelectedIds([...state.currentIds]);
  }, [state]);

  const optionsQuery = useQuery({
    ...genericRecordOptionsQuery(wsId, state?.targetTableId ?? ""),
    enabled: Boolean(wsId && state?.targetTableId),
    refetchOnMount: "always",
  });

  const options = useMemo(
    () => optionsQuery.data?.records ?? [],
    [optionsQuery.data?.records],
  );
  const duplicateLabels = useMemo(() => {
    const counts = new Map<string, number>();
    for (const option of options) {
      counts.set(option.label, (counts.get(option.label) ?? 0) + 1);
    }
    return counts;
  }, [options]);
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    if (!needle) return options;
    return options.filter((option) => option.label.toLowerCase().includes(needle));
  }, [options, search]);

  const toggle = (id: string) => {
    setSelectedIds((current) =>
      current.includes(id) ? current.filter((item) => item !== id) : [...current, id],
    );
  };

  return (
    <Dialog open={state !== null} onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {state ? `${t(($) => $.sheet.link_records_title)} · ${state.fieldName}` : t(($) => $.sheet.link_records_title)}
          </DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-2">
          <Input
            aria-label={t(($) => $.sheet.search_records)}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t(($) => $.sheet.search_records)}
            autoFocus
          />
          <div className="max-h-64 overflow-y-auto rounded-md border border-border p-1" role="listbox" aria-multiselectable="true">
            {optionsQuery.isLoading ? (
              <p className="px-2 py-3 text-body text-muted-foreground">{t(($) => $.list.loading)}</p>
            ) : filtered.length === 0 ? (
              <p className="px-2 py-3 text-body text-muted-foreground">{t(($) => $.sheet.no_matching_records)}</p>
            ) : (
              filtered.map((option) => {
                const checked = selectedIds.includes(option.id);
                const isDup = (duplicateLabels.get(option.label) ?? 0) > 1;
                return (
                  <label
                    key={option.id}
                    data-option-id={option.id}
                    className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-body hover:bg-accent"
                  >
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={() => toggle(option.id)}
                    />
                    <span className="min-w-0 flex-1 truncate">{option.label}</span>
                    {isDup ? (
                      <span className="shrink-0 font-mono text-caption text-muted-foreground">
                        #{option.id.slice(0, 6)}
                      </span>
                    ) : null}
                  </label>
                );
              })
            )}
          </div>
        </div>
        <DialogFooter className="flex items-center justify-between gap-2 sm:justify-between">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={selectedIds.length === 0}
            onClick={() => setSelectedIds([])}
          >
            {t(($) => $.sheet.clear)}
          </Button>
          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" size="sm" onClick={onClose}>
              {t(($) => $.sheet.cancel)}
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={pending}
              onClick={() => onSave(selectedIds.length > 0 ? selectedIds : null)}
            >
              {t(($) => $.sheet.save)}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
