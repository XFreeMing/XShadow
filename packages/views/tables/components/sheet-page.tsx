"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApitableGrid } from "@multica/apitable-grid";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { ApiError, api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  genericSheetOptions,
  genericTableKeys,
  revisionConflictSchema,
  type GenericActor,
  type GenericCellValue,
  type GenericField,
  type GenericFieldKind,
  type GenericFilter,
  type GenericPresence,
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
  group: string | null;
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
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [presence, setPresence] = useState<GenericPresence[]>([]);
  const [recentTick, setRecentTick] = useState(0);
  const cursor = useRef(0);
  const recent = useRef(new Map<string, GenericActor>());
  const positionRef = useRef<{ recordId: string; fieldId: string } | null>(null);
  const editingRef = useRef(false);
  const holdSheet = useRef(false);
  const viewSaves = useRef(0);

  const active = sheet.data?.views.find((view) => view.id === sheet.data?.active_view_id) ?? sheet.data?.views[0];
  const viewStamp = active ? `${active.id}:${active.updated_at}` : "";
  useEffect(() => {
    if (!active || viewSaves.current > 0) return;
    setDraft({
      name: active.name,
      columns: active.columns,
      filter: active.filter,
      sort: active.sort,
      group: active.group ?? null,
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
    mutationFn: (body: { recordId: string; fieldId: string; value: GenericCellValue; baseRevision: number }) => {
      const revision = body.baseRevision > 0
        ? body.baseRevision
        : revisions.current.get(body.recordId) ?? 0;
      return api.updateGenericRecord(tableId, body.recordId, revision, { [body.fieldId]: body.value });
    },
    onSuccess: async (record) => {
      revisions.current.set(record.id, record.revision);
      setConflict(null);
      if (editingRef.current) holdSheet.current = true;
      else await invalidate();
    },
    onError: report,
  });

  const saveView = useMutation({
    scope: { id: `generic-view:${wsId}:${tableId}` },
    mutationFn: (body: GenericViewWrite & { viewId: string }) =>
      api.saveGenericView(tableId, body.viewId, body),
    onError: report,
  });

  const createView = useMutation({
    mutationFn: (name: string) => api.createGenericView(tableId, {
      name,
      columns: null,
      filter: null,
      sort: null,
      group: null,
    }),
    onSuccess: async (view) => {
      setViewName("");
      await invalidate();
      replace(paths.table(tableId, view.id));
    },
    onError: report,
  });

  const changes = useQuery({
    queryKey: ["generic-changes", wsId, tableId],
    enabled: Boolean(wsId && tableId && sheet.data),
    refetchInterval: 2000,
    queryFn: () => api.listGenericChanges(tableId, cursor.current),
  });
  useEffect(() => {
    cursor.current = 0;
    recent.current.clear();
    setPresence([]);
  }, [tableId]);
  const releaseSheet = () => {
    if (!holdSheet.current) return;
    holdSheet.current = false;
    void queryClient.invalidateQueries({ queryKey: genericTableKeys.sheetAll(wsId, tableId) });
  };
  useEffect(() => {
    const page = changes.data;
    if (!page) return;
    const incoming = page.changes.filter((change) => change.seq > cursor.current);
    if (incoming.length > 0) {
      for (const change of incoming) {
        for (const fieldId of Object.keys(change.fields)) {
          recent.current.set(`${change.record_id}:${fieldId}`, change.actor);
        }
      }
      setRecentTick((tick) => tick + 1);
      if (editingRef.current) holdSheet.current = true;
      else void queryClient.invalidateQueries({ queryKey: genericTableKeys.sheetAll(wsId, tableId) });
    }
    cursor.current = Math.max(cursor.current, page.next_since);
    setPresence(page.presence);
    if (page.latest > cursor.current) {
      void queryClient.invalidateQueries({ queryKey: ["generic-changes", wsId, tableId] });
    }
  }, [changes.data, queryClient, tableId, wsId]);
  const sheetReady = Boolean(sheet.data);
  useEffect(() => {
    if (!sheetReady) return;
    const send = () => {
      const position = positionRef.current;
      void api.touchGenericPresence(tableId, {
        record_id: position?.recordId ?? null,
        field_id: position?.fieldId ?? null,
      }).then((page) => setPresence(page.presence)).catch(() => undefined);
    };
    send();
    const timer = window.setInterval(send, 2000);
    return () => window.clearInterval(timer);
  }, [sheetReady, tableId]);

  const fields = sheet.data?.fields ?? EMPTY_FIELDS;
  const visible = useMemo(() => visibleFields(fields, draft?.columns ?? null), [fields, draft?.columns]);
  const hiddenCount = draft?.columns == null ? 0 : Math.max(0, fields.length - visible.length);
  const groups = useMemo(() => {
    if (!draft?.group || !sheet.data) return null;
    const field = fields.find((item) => item.id === draft.group);
    if (!field) return null;
    const order: string[] = [];
    const buckets = new Map<string, { label: string; records: GenericRecord[] }>();
    for (const record of sheet.data.records) {
      const raw = record.values[field.id];
      const empty = raw == null || raw === "";
      const key = empty ? "" : String(raw);
      const option = field.config.options?.find((item) => item.id === raw);
      const label = empty ? t(($) => $.sheet.empty_group) : option?.label ?? String(raw);
      const bucket = buckets.get(key);
      if (bucket) bucket.records.push(record);
      else {
        order.push(key);
        buckets.set(key, { label, records: [record] });
      }
    }
    return order.map((key) => {
      const bucket = buckets.get(key)!;
      return { key, label: bucket.label, count: bucket.records.length, collapsed: collapsed[key] === true, records: bucket.records };
    });
  }, [collapsed, draft?.group, fields, sheet.data, t]);
  const actors = presence.map((item) => ({
    id: item.actor.id,
    label: item.actor.label,
    color: item.actor.color,
  })).filter((actor, index, list) => actor.id && list.findIndex((item) => item.id === actor.id) === index);
  const collaborators = [
    ...presence.filter((item) => item.record_id && item.field_id).map((item) => ({
      actorId: item.actor.id,
      label: item.actor.label,
      color: item.actor.color,
      kind: "online" as const,
      recordId: item.record_id,
      fieldId: item.field_id,
    })),
    ...[...recent.current.entries()].flatMap(([key, actor]) => {
      const split = key.indexOf(":");
      const recordId = key.slice(0, split);
      const fieldId = key.slice(split + 1);
      if (presence.some((item) => item.record_id === recordId && item.field_id === fieldId)) return [];
      return [{
        actorId: actor.id,
        label: actor.label,
        color: actor.color,
        kind: "recent" as const,
        recordId,
        fieldId,
      }];
    }),
  ];
  void recentTick;
  const persistDraft = (next: ViewDraft) => {
    setDraft(next);
    if (!active) return;
    viewSaves.current += 1;
    saveView.mutate({ ...savedView(next), viewId: active.id }, {
      onSettled: () => {
        viewSaves.current -= 1;
        if (viewSaves.current === 0) {
          setNotice(null);
          void invalidate();
        }
      },
    });
  };
  const message = sheet.error instanceof ApiError ? sheet.error.message : notice;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-12 shrink-0 items-center gap-3 px-5">
        <AppLink href={paths.tables()} className="text-body text-muted-foreground hover:text-foreground">
          {t(($) => $.sheet.back)}
        </AppLink>
        <h1 className="min-w-0 flex-1 truncate text-title-sm font-semibold">{sheet.data?.table.name ?? t(($) => $.list.title)}</h1>
        <Popover open={showField} onOpenChange={setShowField}>
          <PopoverTrigger render={<Button type="button" variant="outline" size="sm">{t(($) => $.sheet.add_field)}</Button>} />
          <PopoverContent align="end" className="w-80 p-3">
            <FieldForm pending={addField.isPending} onSubmit={(body) => addField.mutate(body)} />
          </PopoverContent>
        </Popover>
        <Button type="button" size="sm" onClick={() => addRecord.mutate()} disabled={addRecord.isPending || fields.length === 0}>
          {t(($) => $.sheet.add_record)}
        </Button>
      </div>
      {sheet.data && draft && active ? (
        <ViewBar
          fields={fields}
          views={sheet.data.views}
          activeId={active.id}
          draft={draft}
          hiddenCount={hiddenCount}
          viewName={viewName}
          onViewName={setViewName}
          onDraft={persistDraft}
          onSelectView={(id) => replace(paths.table(tableId, id))}
          onCreateView={() => {
            const name = viewName.trim();
            if (name) createView.mutate(name);
          }}
        />
      ) : null}
      <div className="min-h-0 flex-1 overflow-auto">
        {conflict ? (
          <div className="m-2 flex items-center justify-between gap-3 rounded-md border border-destructive px-3 py-2" role="alert">
            <p className="text-body">{conflict}</p>
            <Button type="button" variant="outline" size="sm" onClick={() => void refresh()}>
              {t(($) => $.sheet.refresh)}
            </Button>
          </div>
        ) : null}
        {message ? <p className="mx-2 mb-2 text-body text-destructive">{message}</p> : null}
        {sheet.isLoading ? <p className="px-3 text-body text-muted-foreground">{t(($) => $.list.loading)}</p> : null}
        {sheet.data && fields.length === 0 ? (
          <p className="px-3 text-body text-muted-foreground">{t(($) => $.sheet.empty_fields)}</p>
        ) : null}
        {sheet.data && fields.length > 0 ? (
          <ApitableGrid
            datasheetId={tableId}
            fields={visible}
            records={sheet.data.records}
            groups={groups}
            actors={actors}
            collaborators={collaborators}
            labels={{
              rename: t(($) => $.sheet.rename),
              clear: t(($) => $.sheet.clear),
              emptyRecords: t(($) => $.sheet.empty_records),
              emptyValue: t(($) => $.sheet.empty_value),
              checked: t(($) => $.sheet.checked),
              unchecked: t(($) => $.sheet.unchecked),
              emptyGroup: t(($) => $.sheet.empty_group),
              online: t(($) => $.sheet.online),
              recent: t(($) => $.sheet.recent),
            }}
            onRenameField={(fieldId, name) => renameField.mutate({ fieldId, name })}
            onActiveCell={(recordId, fieldId) => {
              positionRef.current = { recordId, fieldId };
              editingRef.current = true;
              void api.touchGenericPresence(tableId, { record_id: recordId, field_id: fieldId })
                .then((page) => setPresence(page.presence))
                .catch(() => undefined);
            }}
            onEditEnd={() => {
              positionRef.current = null;
              editingRef.current = false;
              void api.touchGenericPresence(tableId, { record_id: null, field_id: null })
                .then((page) => setPresence(page.presence))
                .catch(() => undefined);
              releaseSheet();
            }}
            onToggleGroup={(key) => setCollapsed((current) => ({ ...current, [key]: !current[key] }))}
            onCellCommit={(recordId, fieldId, value, baseRevision) => {
              saveCell.mutate({ recordId, fieldId, value, baseRevision });
            }}
          />
        ) : null}
      </div>
    </div>
  );
}

function savedView(draft: ViewDraft): GenericViewWrite {
  const filter = draft.filter && (draft.filter.op === "is_empty" || draft.filter.op === "is_not_empty")
    ? { field_id: draft.filter.field_id, op: draft.filter.op }
    : draft.filter;
  return { name: draft.name.trim(), columns: draft.columns, filter, sort: draft.sort, group: draft.group };
}

function visibleFields(fields: GenericField[], columns: string[] | null): GenericField[] {
  if (columns == null) return fields;
  const byId = new Map(fields.map((field) => [field.id, field]));
  return columns.flatMap((id) => {
    const field = byId.get(id);
    return field ? [field] : [];
  });
}

function ViewBar({
  fields,
  views,
  activeId,
  draft,
  hiddenCount,
  viewName,
  onViewName,
  onDraft,
  onSelectView,
  onCreateView,
}: {
  fields: GenericField[];
  views: { id: string; name: string }[];
  activeId: string;
  draft: ViewDraft;
  hiddenCount: number;
  viewName: string;
  onViewName: (value: string) => void;
  onDraft: (draft: ViewDraft) => void;
  onSelectView: (id: string) => void;
  onCreateView: () => void;
}) {
  const { t } = useT("tables");
  const visible = visibleFields(fields, draft.columns).map((field) => field.id);
  const hidden = fields.filter((field) => !visible.includes(field.id));
  const ordered = [...visibleFields(fields, draft.columns), ...hidden];

  const setVisible = (ids: string[]) => onDraft({ ...draft, columns: ids });

  return (
    <div className="border-b border-border">
      <div className="flex h-8 items-center gap-1 overflow-x-auto px-2" role="tablist" aria-label={t(($) => $.sheet.view_name)}>
        {views.map((view) => (
          <button
            key={view.id}
            type="button"
            role="tab"
            aria-selected={view.id === activeId}
            className={view.id === activeId ? "h-7 shrink-0 rounded-sm bg-accent px-2 text-body" : "h-7 shrink-0 rounded-sm px-2 text-body text-muted-foreground"}
            onClick={() => onSelectView(view.id)}
          >
            {view.name}
          </button>
        ))}
        <Popover>
          <PopoverTrigger render={<Button type="button" variant="ghost" size="sm">{t(($) => $.sheet.new_view)}</Button>} />
          <PopoverContent align="start" className="flex w-64 items-center gap-2 p-2">
            <Input
              aria-label={t(($) => $.sheet.view_name)}
              value={viewName}
              onChange={(event) => onViewName(event.target.value)}
              placeholder={t(($) => $.sheet.view_name)}
            />
            <Button type="button" size="sm" onClick={onCreateView} disabled={viewName.trim() === ""}>
              {t(($) => $.sheet.add)}
            </Button>
          </PopoverContent>
        </Popover>
      </div>
      <div className="flex h-8 items-center gap-1 border-t border-border px-2">
        <Popover>
          <PopoverTrigger render={<Button type="button" variant="ghost" size="sm">{t(($) => $.sheet.columns)}{hiddenCount > 0 ? ` ${hiddenCount}` : ""}</Button>} />
          <PopoverContent align="start" className="w-72 p-2">
            <ul className="space-y-1">
              {ordered.map((field) => {
                const shown = visible.includes(field.id);
                const index = visible.indexOf(field.id);
                return (
                  <li key={field.id} className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={shown}
                      aria-label={field.name}
                      onChange={() => setVisible(shown ? visible.filter((id) => id !== field.id) : [...visible, field.id])}
                    />
                    <span className="min-w-0 flex-1 truncate text-body">{field.name}</span>
                    <Button type="button" variant="ghost" size="xs" disabled={!shown || index === 0} onClick={() => {
                      const next = visible.slice();
                      const [item] = next.splice(index, 1);
                      next.splice(index - 1, 0, item!);
                      setVisible(next);
                    }}>{t(($) => $.sheet.move_up)}</Button>
                    <Button type="button" variant="ghost" size="xs" disabled={!shown || index === visible.length - 1} onClick={() => {
                      const next = visible.slice();
                      const [item] = next.splice(index, 1);
                      next.splice(index + 1, 0, item!);
                      setVisible(next);
                    }}>{t(($) => $.sheet.move_down)}</Button>
                  </li>
                );
              })}
            </ul>
          </PopoverContent>
        </Popover>
        <Popover>
          <PopoverTrigger render={<Button type="button" variant="ghost" size="sm">{t(($) => $.sheet.filter)}{draft.filter ? " 1" : ""}</Button>} />
          <PopoverContent align="start" className="w-80 p-2">
            <FilterSort fields={fields} draft={draft} onDraft={onDraft} part="filter" />
          </PopoverContent>
        </Popover>
        <Popover>
          <PopoverTrigger render={<Button type="button" variant="ghost" size="sm">{t(($) => $.sheet.sort)}{draft.sort ? " 1" : ""}</Button>} />
          <PopoverContent align="start" className="w-72 p-2">
            <FilterSort fields={fields} draft={draft} onDraft={onDraft} part="sort" />
          </PopoverContent>
        </Popover>
        <Popover>
          <PopoverTrigger render={<Button type="button" variant="ghost" size="sm">{t(($) => $.sheet.group)}{draft.group ? " 1" : ""}</Button>} />
          <PopoverContent align="start" className="w-64 p-2">
            <select
              aria-label={t(($) => $.sheet.group)}
              value={draft.group ?? ""}
              onChange={(event) => onDraft({ ...draft, group: event.target.value || null })}
              className="h-8 w-full rounded-md border border-border bg-transparent px-2 text-body"
            >
              <option value="">{t(($) => $.sheet.none)}</option>
              {fields.map((field) => <option key={field.id} value={field.id}>{field.name}</option>)}
            </select>
          </PopoverContent>
        </Popover>
      </div>
    </div>
  );
}

function FilterSort({
  fields,
  draft,
  onDraft,
  part,
}: {
  fields: GenericField[];
  draft: ViewDraft;
  onDraft: (draft: ViewDraft) => void;
  part: "filter" | "sort";
}) {
  const { t } = useT("tables");
  const filterField = fields.find((field) => field.id === draft.filter?.field_id) ?? null;
  const op = draft.filter?.op ?? "eq";
  return (
    <div className="flex flex-wrap items-end gap-2">
      {part === "filter" ? (
        <>
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
        </>
      ) : null}
      {part === "sort" ? (
        <>
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
        </>
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
      className="flex flex-col gap-2"
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

