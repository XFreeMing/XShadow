"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@multica/ui/components/ui/table";
import { ApiError, api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  genericSheetOptions,
  genericTableKeys,
  revisionConflictSchema,
  type GenericCellValue,
  type GenericField,
  type GenericFieldKind,
  type GenericFilter,
  type GenericRecord,
  type GenericSort,
  type GenericViewWrite,
} from "@multica/core/tables";
import { useT } from "../../i18n";
import { AppLink, useNavigation } from "../../navigation";

const EMPTY_FIELDS: GenericField[] = [];
const KINDS: GenericFieldKind[] = ["text", "number", "date", "single_select", "checkbox"];
const OPS = ["eq", "contains", "is_empty", "is_not_empty"] as const;

interface ViewDraft {
  name: string;
  columns: string[] | null;
  filter: GenericFilter | null;
  sort: GenericSort | null;
}

export function TableSheetPage({ tableId }: { tableId: string }) {
  const { t } = useT("tables");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { replace, searchParams } = useNavigation();
  const viewId = searchParams.get("view");
  const queryClient = useQueryClient();
  const sheet = useQuery(genericSheetOptions(wsId, tableId, viewId));
  const revisions = useRef(new Map<string, number>());
  const [draft, setDraft] = useState<ViewDraft | null>(null);
  const [conflict, setConflict] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [showField, setShowField] = useState(false);
  const [viewName, setViewName] = useState("");

  const active = sheet.data?.views.find((view) => view.id === sheet.data?.active_view_id) ?? sheet.data?.views[0];
  const viewStamp = active ? `${active.id}:${active.updated_at}` : "";
  useEffect(() => {
    if (!active) return;
    setDraft({
      name: active.name,
      columns: active.columns,
      filter: active.filter,
      sort: active.sort,
    });
    // Local view edits stay put until this saved view changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewStamp]);

  const refresh = async () => {
    revisions.current.clear();
    setConflict(null);
    await queryClient.invalidateQueries({ queryKey: genericTableKeys.sheetAll(wsId, tableId) });
  };

  const report = (error: unknown) => {
    const parsed = revisionConflictSchema.safeParse(error instanceof ApiError ? error.body : null);
    if (parsed.success) {
      setConflict(parsed.data.error);
      return;
    }
    setNotice(error instanceof ApiError ? error.message : t(($) => $.sheet.save_failed));
  };

  const invalidate = () => queryClient.invalidateQueries({ queryKey: genericTableKeys.sheetAll(wsId, tableId) });

  const addField = useMutation({
    mutationFn: (body: { name: string; kind: GenericFieldKind; options: string }) => {
      const config = body.kind === "single_select"
        ? {
            options: body.options.split("\n").map((line) => line.trim()).filter(Boolean).map((label, index) => ({
              id: `opt${index + 1}`,
              label,
            })),
          }
        : undefined;
      return api.createGenericField(tableId, { name: body.name, kind: body.kind, config });
    },
    onSuccess: async () => {
      setShowField(false);
      await invalidate();
    },
    onError: report,
  });

  const renameField = useMutation({
    mutationFn: (body: { fieldId: string; name: string }) => api.renameGenericField(tableId, body.fieldId, body.name),
    onSuccess: invalidate,
    onError: report,
  });

  const addRecord = useMutation({
    mutationFn: () => api.createGenericRecord(tableId),
    onSuccess: invalidate,
    onError: report,
  });

  const saveCell = useMutation({
    // Later cells in this window wait for the revision the previous save stored.
    // A 409 does not adopt the other writer's revision.
    scope: { id: `generic-table:${wsId}:${tableId}` },
    mutationFn: (body: { record: GenericRecord; fieldId: string; value: GenericCellValue }) => {
      const revision = Math.max(body.record.revision, revisions.current.get(body.record.id) ?? 0);
      return api.updateGenericRecord(tableId, body.record.id, revision, { [body.fieldId]: body.value });
    },
    onSuccess: async (record) => {
      revisions.current.set(record.id, record.revision);
      setConflict(null);
      await invalidate();
    },
    onError: report,
  });

  const saveView = useMutation({
    mutationFn: (body: GenericViewWrite & { viewId: string }) =>
      api.saveGenericView(tableId, body.viewId, body),
    onSuccess: async () => {
      setNotice(null);
      await invalidate();
    },
    onError: report,
  });

  const createView = useMutation({
    mutationFn: (name: string) => api.createGenericView(tableId, {
      name,
      columns: null,
      filter: null,
      sort: null,
    }),
    onSuccess: async (view) => {
      setViewName("");
      await invalidate();
      replace(paths.table(tableId, view.id));
    },
    onError: report,
  });

  const fields = sheet.data?.fields ?? EMPTY_FIELDS;
  const visible = useMemo(() => visibleFields(fields, draft?.columns ?? null), [fields, draft?.columns]);
  const message = sheet.error instanceof ApiError ? sheet.error.message : notice;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-12 shrink-0 items-center gap-3 px-5">
        <AppLink href={paths.tables()} className="text-body text-muted-foreground hover:text-foreground">
          {t(($) => $.sheet.back)}
        </AppLink>
        <h1 className="min-w-0 flex-1 truncate text-title-sm font-semibold">{sheet.data?.table.name ?? t(($) => $.list.title)}</h1>
        <Button type="button" variant="outline" size="sm" onClick={() => setShowField((open) => !open)}>
          {t(($) => $.sheet.add_field)}
        </Button>
        <Button type="button" size="sm" onClick={() => addRecord.mutate()} disabled={addRecord.isPending || fields.length === 0}>
          {t(($) => $.sheet.add_record)}
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-16">
        {conflict ? (
          <div className="mb-3 flex items-center justify-between gap-3 rounded-md border border-destructive px-3 py-2" role="alert">
            <p className="text-body">{conflict}</p>
            <Button type="button" variant="outline" size="sm" onClick={() => void refresh()}>
              {t(($) => $.sheet.refresh)}
            </Button>
          </div>
        ) : null}
        {message ? <p className="mb-3 text-body text-destructive">{message}</p> : null}
        {sheet.data && draft && active ? (
          <ViewEditor
            fields={fields}
            views={sheet.data.views}
            activeId={active.id}
            draft={draft}
            viewName={viewName}
            onViewName={setViewName}
            onDraft={setDraft}
            onSelectView={(id) => replace(paths.table(tableId, id))}
            onCreateView={() => {
              const name = viewName.trim();
              if (name) createView.mutate(name);
            }}
            onSave={() => {
              if (!draft.name.trim()) return;
              saveView.mutate({ ...savedView(draft), viewId: active.id });
            }}
          />
        ) : null}
        {showField ? (
          <FieldForm
            pending={addField.isPending}
            onSubmit={(body) => addField.mutate(body)}
          />
        ) : null}
        {sheet.isLoading ? <p className="text-body text-muted-foreground">{t(($) => $.list.loading)}</p> : null}
        {sheet.data && fields.length === 0 ? (
          <p className="text-body text-muted-foreground">{t(($) => $.sheet.empty_fields)}</p>
        ) : null}
        {sheet.data && fields.length > 0 ? (
          <Table>
            <TableHeader>
              <TableRow>
                {visible.map((field) => (
                  <TableHead key={field.id}>
                    <input
                      aria-label={t(($) => $.sheet.rename)}
                      defaultValue={field.name}
                      key={field.name}
                      onBlur={(event) => {
                        const name = event.target.value.trim();
                        if (name && name !== field.name) renameField.mutate({ fieldId: field.id, name });
                      }}
                      onKeyDown={(event) => {
                        if (event.key === "Enter") event.currentTarget.blur();
                      }}
                      className="w-full min-w-28 bg-transparent font-medium outline-none"
                    />
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {sheet.data.records.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={Math.max(visible.length, 1)} className="text-muted-foreground">
                    {t(($) => $.sheet.empty_records)}
                  </TableCell>
                </TableRow>
              ) : sheet.data.records.map((record) => (
                <TableRow key={record.id} data-record-id={record.id}>
                  {visible.map((field) => (
                    <TableCell key={field.id}>
                      <CellEditor
                        field={field}
                        value={record.values[field.id] ?? null}
                        onCommit={(value) => {
                          const current = record.values[field.id] ?? null;
                          if (value === current) return;
                          saveCell.mutate({ record, fieldId: field.id, value });
                        }}
                      />
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : null}
      </div>
    </div>
  );
}

function savedView(draft: ViewDraft): GenericViewWrite {
  const filter = draft.filter && (draft.filter.op === "is_empty" || draft.filter.op === "is_not_empty")
    ? { field_id: draft.filter.field_id, op: draft.filter.op }
    : draft.filter;
  return { name: draft.name.trim(), columns: draft.columns, filter, sort: draft.sort };
}

function visibleFields(fields: GenericField[], columns: string[] | null): GenericField[] {
  if (columns == null) return fields;
  const byId = new Map(fields.map((field) => [field.id, field]));
  return columns.flatMap((id) => {
    const field = byId.get(id);
    return field ? [field] : [];
  });
}

function ViewEditor({
  fields,
  views,
  activeId,
  draft,
  viewName,
  onViewName,
  onDraft,
  onSelectView,
  onCreateView,
  onSave,
}: {
  fields: GenericField[];
  views: { id: string; name: string }[];
  activeId: string;
  draft: ViewDraft;
  viewName: string;
  onViewName: (value: string) => void;
  onDraft: (draft: ViewDraft) => void;
  onSelectView: (id: string) => void;
  onCreateView: () => void;
  onSave: () => void;
}) {
  const { t } = useT("tables");
  const visible = visibleFields(fields, draft.columns).map((field) => field.id);
  const hidden = fields.filter((field) => !visible.includes(field.id));
  const ordered = [...visibleFields(fields, draft.columns), ...hidden];

  const setVisible = (ids: string[]) => onDraft({ ...draft, columns: ids });

  return (
    <section className="mb-4 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <label className="text-body">
          <span className="sr-only">{t(($) => $.sheet.view_name)}</span>
          <select
            aria-label={t(($) => $.sheet.view_name)}
            value={activeId}
            onChange={(event) => onSelectView(event.target.value)}
            className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
          >
            {views.map((view) => <option key={view.id} value={view.id}>{view.name}</option>)}
          </select>
        </label>
        <Input
          aria-label={t(($) => $.sheet.new_view)}
          value={viewName}
          onChange={(event) => onViewName(event.target.value)}
          placeholder={t(($) => $.sheet.view_name)}
          className="max-w-48"
        />
        <Button type="button" variant="outline" size="sm" onClick={onCreateView} disabled={viewName.trim() === ""}>
          {t(($) => $.sheet.new_view)}
        </Button>
        <Button type="button" size="sm" onClick={onSave}>{t(($) => $.sheet.save_view)}</Button>
      </div>
      <div className="mt-3">
        <h2 className="text-caption font-medium text-muted-foreground">{t(($) => $.sheet.columns)}</h2>
        <ul className="mt-1 space-y-1">
          {ordered.map((field) => {
            const shown = visible.includes(field.id);
            const index = visible.indexOf(field.id);
            return (
              <li key={field.id} className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={shown}
                  aria-label={field.name}
                  onChange={() => {
                    setVisible(shown ? visible.filter((id) => id !== field.id) : [...visible, field.id]);
                  }}
                />
                <span className="min-w-0 flex-1 truncate text-body">{field.name}</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  disabled={!shown || index === 0}
                  onClick={() => {
                    const next = visible.slice();
                    const [item] = next.splice(index, 1);
                    next.splice(index - 1, 0, item!);
                    setVisible(next);
                  }}
                >
                  {t(($) => $.sheet.move_up)}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  disabled={!shown || index === visible.length - 1}
                  onClick={() => {
                    const next = visible.slice();
                    const [item] = next.splice(index, 1);
                    next.splice(index + 1, 0, item!);
                    setVisible(next);
                  }}
                >
                  {t(($) => $.sheet.move_down)}
                </Button>
              </li>
            );
          })}
        </ul>
      </div>
      <FilterSort fields={fields} draft={draft} onDraft={onDraft} />
    </section>
  );
}

function FilterSort({
  fields,
  draft,
  onDraft,
}: {
  fields: GenericField[];
  draft: ViewDraft;
  onDraft: (draft: ViewDraft) => void;
}) {
  const { t } = useT("tables");
  const filterField = fields.find((field) => field.id === draft.filter?.field_id) ?? null;
  const op = draft.filter?.op ?? "eq";
  return (
    <div className="mt-3 flex flex-wrap items-end gap-2">
      <label className="text-caption text-muted-foreground">
        {t(($) => $.sheet.filter)}
        <select
          aria-label={t(($) => $.sheet.filter)}
          value={draft.filter?.field_id ?? ""}
          onChange={(event) => {
            const fieldId = event.target.value;
            onDraft({
              ...draft,
              filter: fieldId ? { field_id: fieldId, op: "eq" } : null,
            });
          }}
          className="mt-1 block h-8 rounded-md border border-border bg-transparent px-2 text-body"
        >
          <option value="">{t(($) => $.sheet.none)}</option>
          {fields.map((field) => <option key={field.id} value={field.id}>{field.name}</option>)}
        </select>
      </label>
      {draft.filter ? (
        <select
          aria-label={t(($) => $.sheet.filter)}
          value={op}
          onChange={(event) => onDraft({
            ...draft,
            filter: { field_id: draft.filter!.field_id, op: event.target.value as GenericFilter["op"] },
          })}
          className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
        >
          {OPS.filter((item) => item !== "contains" || filterField?.kind === "text").map((item) => (
            <option key={item} value={item}>{t(($) => $.sheet.ops[item])}</option>
          ))}
        </select>
      ) : null}
      {draft.filter && op !== "is_empty" && op !== "is_not_empty" ? (
        <FilterValue
          field={filterField}
          value={draft.filter.value}
          onChange={(value) => onDraft({
            ...draft,
            filter: { field_id: draft.filter!.field_id, op, value: value ?? undefined },
          })}
        />
      ) : null}
      <label className="text-caption text-muted-foreground">
        {t(($) => $.sheet.sort)}
        <select
          aria-label={t(($) => $.sheet.sort)}
          value={draft.sort?.field_id ?? ""}
          onChange={(event) => {
            const fieldId = event.target.value;
            onDraft({
              ...draft,
              sort: fieldId ? { field_id: fieldId, direction: draft.sort?.direction ?? "asc" } : null,
            });
          }}
          className="mt-1 block h-8 rounded-md border border-border bg-transparent px-2 text-body"
        >
          <option value="">{t(($) => $.sheet.none)}</option>
          {fields.map((field) => <option key={field.id} value={field.id}>{field.name}</option>)}
        </select>
      </label>
      {draft.sort ? (
        <select
          aria-label={t(($) => $.sheet.sort)}
          value={draft.sort.direction}
          onChange={(event) => onDraft({
            ...draft,
            sort: { field_id: draft.sort!.field_id, direction: event.target.value as GenericSort["direction"] },
          })}
          className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
        >
          <option value="asc">{t(($) => $.sheet.directions.asc)}</option>
          <option value="desc">{t(($) => $.sheet.directions.desc)}</option>
        </select>
      ) : null}
    </div>
  );
}

function FilterValue({
  field,
  value,
  onChange,
}: {
  field: GenericField | null;
  value: GenericCellValue | undefined;
  onChange: (value: GenericCellValue | null) => void;
}) {
  const { t } = useT("tables");
  if (field?.kind === "single_select") {
    return (
      <select
        aria-label={field.name}
        value={typeof value === "string" ? value : ""}
        onChange={(event) => onChange(event.target.value || null)}
        className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
      >
        <option value="">{t(($) => $.sheet.empty_value)}</option>
        {field.config.options?.map((option) => <option key={option.id} value={option.id}>{option.label}</option>)}
      </select>
    );
  }
  if (field?.kind === "checkbox") {
    return (
      <select
        aria-label={field.name}
        value={value === true ? "true" : value === false ? "false" : ""}
        onChange={(event) => onChange(event.target.value === "" ? null : event.target.value === "true")}
        className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
      >
        <option value="">{t(($) => $.sheet.empty_value)}</option>
        <option value="true">{t(($) => $.sheet.checked)}</option>
        <option value="false">{t(($) => $.sheet.unchecked)}</option>
      </select>
    );
  }
  return (
    <Input
      aria-label={field?.name ?? t(($) => $.sheet.filter)}
      type={field?.kind === "date" ? "date" : "text"}
      value={value == null ? "" : String(value)}
      onChange={(event) => {
        const raw = event.target.value;
        if (raw === "") onChange(null);
        else if (field?.kind === "number") {
          const parsed = Number(raw);
          if (Number.isFinite(parsed)) onChange(parsed);
        } else onChange(raw);
      }}
      className="max-w-48"
    />
  );
}

function FieldForm({
  pending,
  onSubmit,
}: {
  pending: boolean;
  onSubmit: (body: { name: string; kind: GenericFieldKind; options: string }) => void;
}) {
  const { t } = useT("tables");
  const [name, setName] = useState("");
  const [kind, setKind] = useState<GenericFieldKind>("text");
  const [options, setOptions] = useState("");
  return (
    <form
      className="mb-4 flex max-w-xl flex-col gap-2 rounded-md border border-border p-3"
      onSubmit={(event) => {
        event.preventDefault();
        if (!name.trim()) return;
        if (kind === "single_select" && options.trim() === "") return;
        onSubmit({ name: name.trim(), kind, options });
        setName("");
        setOptions("");
      }}
    >
      <Input aria-label={t(($) => $.sheet.field_name)} value={name} onChange={(event) => setName(event.target.value)} placeholder={t(($) => $.sheet.field_name)} />
      <select
        aria-label={t(($) => $.sheet.kind)}
        value={kind}
        onChange={(event) => setKind(event.target.value as GenericFieldKind)}
        className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
      >
        {KINDS.map((item) => <option key={item} value={item}>{t(($) => $.sheet.kinds[item])}</option>)}
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
      <Button type="submit" size="sm" disabled={pending || name.trim() === ""}>{t(($) => $.sheet.add)}</Button>
    </form>
  );
}

function CellEditor({
  field,
  value,
  onCommit,
}: {
  field: GenericField;
  value: GenericCellValue;
  onCommit: (value: GenericCellValue) => void;
}) {
  const { t } = useT("tables");
  const [text, setText] = useState(value == null ? "" : String(value));
  useEffect(() => {
    setText(value == null ? "" : String(value));
  }, [value]);
  if (field.kind === "checkbox") {
    const checked = value === true;
    return (
      <span className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={checked}
          ref={(node) => {
            if (node) node.indeterminate = value == null;
          }}
          aria-label={value == null ? t(($) => $.sheet.empty_value) : checked ? t(($) => $.sheet.checked) : t(($) => $.sheet.unchecked)}
          onChange={(event) => onCommit(event.target.checked)}
        />
        <Button type="button" variant="ghost" size="xs" disabled={value == null} onClick={() => onCommit(null)}>
          {t(($) => $.sheet.clear)}
        </Button>
      </span>
    );
  }
  if (field.kind === "single_select") {
    return (
      <select
        aria-label={field.name}
        value={typeof value === "string" ? value : ""}
        onChange={(event) => onCommit(event.target.value || null)}
        className="h-8 w-full min-w-28 rounded-md border border-border bg-transparent px-2 text-body"
      >
        <option value="">{t(($) => $.sheet.empty_value)}</option>
        {field.config.options?.map((option) => <option key={option.id} value={option.id}>{option.label}</option>)}
      </select>
    );
  }
  return (
    <input
      aria-label={field.name}
      type={field.kind === "date" ? "date" : "text"}
      value={field.kind === "date" && typeof value === "string" ? value : text}
      onChange={(event) => {
        setText(event.target.value);
        if (field.kind === "date") onCommit(event.target.value || null);
      }}
      onBlur={() => {
        if (field.kind === "date") return;
        if (field.kind === "number") {
          if (text.trim() === "") onCommit(null);
          else {
            const parsed = Number(text);
            if (Number.isFinite(parsed)) onCommit(parsed);
          }
          return;
        }
        onCommit(text.trim() === "" ? null : text);
      }}
      className="h-8 w-full min-w-28 rounded-md border border-border bg-transparent px-2 text-body outline-none"
    />
  );
}
