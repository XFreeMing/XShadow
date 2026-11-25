"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApitableGrid, type GridRecordOption } from "@multica/apitable-grid";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { ApiError, api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  genericRecordOptionsQuery,
  genericSheetOptions,
  genericTableKeys,
  genericViewKinds,
  revisionConflictSchema,
  type GenericActor,
  type GenericCellValue,
  type GenericField,
  type GenericFieldPatch,
  type GenericFieldWrite,
  type GenericFilter,
  type GenericPresence,
  type GenericRecord,
  type GenericSheet,
  type GenericSort,
  type GenericView,
  type GenericViewConfig,
  type GenericViewKind,
  type GenericViewWrite,
} from "@multica/core/tables";
import { useT } from "../../i18n";
import { AppLink, useNavigation } from "../../navigation";
import {
  EditDerivedFieldDialog,
  FieldCreateForm,
  RelationChooserDialog,
  type RelationChooserState,
} from "./derived-field-dialogs";
import { FormView } from "./form-view";
import { RecordDetailDialog } from "./record-detail-dialog";
import { TableChatLauncher } from "./table-chat-launcher";
import {
  isEditableField,
  isSameTableOneWayRelation,
  resolveEffectiveViewConfig,
} from "./view-helpers";
import {
  CalendarView,
  GalleryView,
  GanttView,
  KanbanView,
  OrgChartView,
} from "./visual-views";

const EMPTY_FIELDS: GenericField[] = [];
const OPS = ["eq", "contains", "is_empty", "is_not_empty"] as const;

interface ViewDraft {
  name: string;
  kind: GenericViewKind;
  config: GenericViewConfig;
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
  const editingRef = useRef(false);
  const holdSheet = useRef(false);
  const pendingViewSaves = useRef(new Map<string, number>());
  const sheet = useQuery({
    ...genericSheetOptions(wsId, tableId, viewId),
    refetchOnMount: "always",
    refetchOnWindowFocus: () => !editingRef.current,
  });
  const revisions = useRef(new Map<string, number>());
  const [draftsByView, setDraftsByView] = useState<Record<string, ViewDraft>>({});
  const [conflict, setConflict] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [showField, setShowField] = useState(false);
  const [fieldFormError, setFieldFormError] = useState<string | null>(null);
  const [editingField, setEditingField] = useState<GenericField | null>(null);
  const [editFieldError, setEditFieldError] = useState<string | null>(null);
  const [relationChooser, setRelationChooser] = useState<RelationChooserState | null>(null);
  const [openRecordId, setOpenRecordId] = useState<string | null>(null);
  const [showNewView, setShowNewView] = useState(false);
  const [viewName, setViewName] = useState("");
  const [viewKind, setViewKind] = useState<GenericViewKind>("grid");
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [presence, setPresence] = useState<GenericPresence[]>([]);
  const [recentTick, setRecentTick] = useState(0);
  const cursor = useRef(0);
  const recent = useRef(new Map<string, GenericActor>());
  const positionRef = useRef<{ recordId: string; fieldId: string } | null>(null);

  const active = sheet.data?.views.find((view) => view.id === sheet.data?.active_view_id) ?? sheet.data?.views[0];
  const draft: ViewDraft | null = useMemo(() => {
    if (!active) return null;
    const local = draftsByView[active.id];
    if (local) {
      return { ...local, kind: active.kind ?? "grid" };
    }
    return viewToDraft(active);
  }, [active, draftsByView]);

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
    mutationFn: (body: GenericFieldWrite) => api.createGenericField(tableId, body),
    onSuccess: async (created) => {
      setFieldFormError(null);
      setShowField(false);
      await invalidate();
      if (created.kind === "relation" && created.config.target_table_id) {
        await queryClient.invalidateQueries({
          queryKey: genericTableKeys.sheetAll(wsId, created.config.target_table_id),
        });
      }
    },
    onError: (error) => {
      setFieldFormError(error instanceof ApiError ? error.message : t(($) => $.sheet.save_failed));
    },
  });

  const updateField = useMutation({
    mutationFn: (body: { fieldId: string; patch: GenericFieldPatch }) =>
      api.updateGenericField(tableId, body.fieldId, body.patch),
    onSuccess: async () => {
      setEditFieldError(null);
      setEditingField(null);
      await invalidate();
    },
    onError: (error) => {
      setEditFieldError(error instanceof ApiError ? error.message : t(($) => $.sheet.save_failed));
    },
  });

  const renameField = useMutation({
    mutationFn: (body: { fieldId: string; name: string }) => api.renameGenericField(tableId, body.fieldId, body.name),
    onSuccess: invalidate,
    onError: report,
  });

  const addRecord = useMutation({
    mutationFn: () => api.createGenericRecord(tableId),
    onSuccess: async () => {
      await invalidate();
      await queryClient.invalidateQueries({ queryKey: genericTableKeys.recordOptions(wsId, tableId) });
    },
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
    onSuccess: async (record, variables) => {
      revisions.current.set(record.id, record.revision);
      setConflict(null);
      setNotice(null);
      void queryClient.invalidateQueries({ queryKey: genericTableKeys.recordOptions(wsId, tableId) });
      const field = sheet.data?.fields.find((item) => item.id === variables.fieldId);
      if (field?.kind === "relation" && field.config.target_table_id) {
        void queryClient.invalidateQueries({
          queryKey: genericTableKeys.sheetAll(wsId, field.config.target_table_id),
        });
      }
      if (editingRef.current) holdSheet.current = true;
      else await invalidate();
    },
    onError: report,
  });

  const saveView = useMutation({
    scope: { id: `generic-view:${wsId}:${tableId}` },
    mutationFn: (body: GenericViewWrite & { viewId: string }) => {
      const { viewId: targetViewId, ...payload } = body;
      return api.saveGenericView(tableId, targetViewId, payload);
    },
    onSuccess: (saved, variables) => {
      setNotice(null);
      queryClient.setQueriesData<GenericSheet>(
        { queryKey: genericTableKeys.sheetAll(wsId, tableId) },
        (current) => patchCachedSheetView(current, variables.viewId, saved),
      );
    },
    onError: report,
    onSettled: (_data, _error, variables) => {
      const remaining = (pendingViewSaves.current.get(variables.viewId) ?? 1) - 1;
      if (remaining > 0) {
        pendingViewSaves.current.set(variables.viewId, remaining);
        return;
      }
      pendingViewSaves.current.delete(variables.viewId);
      setDraftsByView((prev) => {
        if (!(variables.viewId in prev)) return prev;
        const next = { ...prev };
        delete next[variables.viewId];
        return next;
      });
      void invalidate();
    },
  });

  const createView = useMutation({
    mutationFn: (body: { name: string; kind: GenericViewKind }) => api.createGenericView(tableId, {
      name: body.name,
      kind: body.kind,
      config: {},
      columns: null,
      filter: null,
      sort: null,
      group: null,
    }),
    onSuccess: async (view) => {
      setViewName("");
      setViewKind("grid");
      setShowNewView(false);
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
    pendingViewSaves.current.clear();
    setDraftsByView({});
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
  const hasLookup = fields.some((field) => field.kind === "lookup");
  useEffect(() => {
    if (!sheetReady || !hasLookup) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === "hidden") return;
      if (editingRef.current) {
        holdSheet.current = true;
        return;
      }
      void queryClient.invalidateQueries(
        { queryKey: genericTableKeys.sheetAll(wsId, tableId) },
        { cancelRefetch: false },
      );
    }, 2000);
    return () => window.clearInterval(timer);
  }, [hasLookup, queryClient, sheetReady, tableId, wsId]);

  const relationTargetTableIds = useMemo(() => {
    const ids = new Set<string>();
    for (const field of fields) {
      if (field.kind === "relation" && field.config.target_table_id) {
        ids.add(field.config.target_table_id);
      } else if (field.kind === "lookup" && field.config.target_relation_table_id) {
        ids.add(field.config.target_relation_table_id);
      }
    }
    return [...ids];
  }, [fields]);
  const optionQueries = useQueries({
    queries: relationTargetTableIds.map((targetId) => ({
      ...genericRecordOptionsQuery(wsId, targetId),
      refetchOnMount: "always" as const,
      refetchInterval: 4000,
    })),
  });
  const recordOptionsByTable = useMemo(() => {
    const map: Record<string, readonly GridRecordOption[]> = {};
    relationTargetTableIds.forEach((targetId, index) => {
      map[targetId] = optionQueries[index]?.data?.records ?? [];
    });
    return map;
  }, [optionQueries, relationTargetTableIds]);

  const visible = useMemo(() => visibleFields(fields, draft?.columns ?? null), [fields, draft?.columns]);
  const hiddenCount = draft?.columns == null ? 0 : Math.max(0, fields.length - visible.length);
  const groups = useMemo(() => {
    if (!draft?.group || !sheet.data) return null;
    const field = fields.find((item) => item.id === draft.group);
    if (!field) return null;
    const order: string[] = [];
    const buckets = new Map<string, { label: string; records: GenericRecord[] }>();
    const targetOptions = field.config.target_table_id
      ? new Map((recordOptionsByTable[field.config.target_table_id] ?? []).map((item) => [item.id, item.label]))
      : null;
    for (const record of sheet.data.records) {
      const raw = record.values[field.id];
      const empty = raw == null || raw === "" || (Array.isArray(raw) && raw.length === 0);
      const key = empty ? "" : JSON.stringify(raw);
      let label: string;
      if (empty) {
        label = t(($) => $.sheet.empty_group);
      } else if (field.kind === "relation" && Array.isArray(raw)) {
        label = raw
          .filter((item): item is string => typeof item === "string")
          .map((id) => targetOptions?.get(id) ?? "...")
          .join(", ");
      } else if (Array.isArray(raw)) {
        label = flattenCell(raw).filter((item) => item != null).map(String).join(", ") || t(($) => $.sheet.empty_group);
      } else {
        const option = field.config.options?.find((item) => item.id === raw);
        label = option?.label ?? String(raw);
      }
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
  }, [collapsed, draft?.group, fields, recordOptionsByTable, sheet.data, t]);
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
    if (!active) return;
    const targetViewId = active.id;
    const scoped: ViewDraft = { ...next, kind: active.kind ?? "grid" };
    setDraftsByView((prev) => ({ ...prev, [targetViewId]: scoped }));
    pendingViewSaves.current.set(
      targetViewId,
      (pendingViewSaves.current.get(targetViewId) ?? 0) + 1,
    );
    saveView.mutate({ ...savedView(scoped), viewId: targetViewId });
  };
  const activeKind: GenericViewKind = active?.kind ?? "grid";
  const effectiveConfig = useMemo(
    () => resolveEffectiveViewConfig(activeKind, draft?.config ?? {}, fields, tableId),
    [activeKind, draft?.config, fields, tableId],
  );
  const openRecord = useMemo(
    () => (openRecordId ? sheet.data?.records.find((item) => item.id === openRecordId) ?? null : null),
    [openRecordId, sheet.data?.records],
  );
  const message = sheet.error instanceof ApiError ? sheet.error.message : notice;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-12 shrink-0 items-center gap-3 px-5">
        <AppLink href={paths.tables()} className="text-body text-muted-foreground hover:text-foreground">
          {t(($) => $.sheet.back)}
        </AppLink>
        <h1 className="min-w-0 flex-1 truncate text-title-sm font-semibold">{sheet.data?.table.name ?? t(($) => $.list.title)}</h1>
        {sheet.data ? (
          <TableChatLauncher
            mode="table"
            context={{
              table: sheet.data.table,
              view: active ? { id: active.id, name: draft?.name ?? active.name, kind: activeKind } : null,
              fields,
              visibleFields: visible,
              records: sheet.data.records,
              titleFieldId: effectiveConfig.titleFieldId,
              recordOptionsByTable,
            }}
          />
        ) : null}
        <Popover
          open={showField}
          onOpenChange={(open) => {
            setShowField(open);
            if (!open) setFieldFormError(null);
          }}
        >
          <PopoverTrigger render={<Button type="button" variant="outline" size="sm">{t(($) => $.sheet.add_field)}</Button>} />
          <PopoverContent align="end" className="w-80 p-3">
            <FieldCreateForm
              wsId={wsId}
              tableId={tableId}
              fields={fields}
              pending={addField.isPending}
              error={fieldFormError}
              onSubmit={(body) => {
                setFieldFormError(null);
                addField.mutate(body);
              }}
            />
          </PopoverContent>
        </Popover>
        {activeKind !== "form" ? (
          <Button type="button" size="sm" onClick={() => addRecord.mutate()} disabled={addRecord.isPending || fields.length === 0}>
            {t(($) => $.sheet.add_record)}
          </Button>
        ) : null}
      </div>
      {sheet.data && draft && active ? (
        <ViewBar
          tableId={tableId}
          fields={fields}
          views={sheet.data.views}
          activeId={active.id}
          draft={draft}
          hiddenCount={hiddenCount}
          showNewView={showNewView}
          onShowNewView={setShowNewView}
          viewName={viewName}
          viewKind={viewKind}
          onViewName={setViewName}
          onViewKind={setViewKind}
          onDraft={persistDraft}
          onSelectView={(id) => replace(paths.table(tableId, id))}
          onCreateView={() => {
            const name = viewName.trim();
            if (name) createView.mutate({ name, kind: viewKind });
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
        {sheet.data && fields.length > 0 && activeKind === "grid" ? (
          <ApitableGrid
            datasheetId={tableId}
            fields={visible}
            records={sheet.data.records}
            groups={groups}
            actors={actors}
            collaborators={collaborators}
            recordOptionsByTable={recordOptionsByTable}
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
              configureField: t(($) => $.sheet.configure_field),
              addLink: t(($) => $.sheet.add_link),
              openRecord: t(($) => $.sheet.open_record),
            }}
            onRenameField={(fieldId, name) => renameField.mutate({ fieldId, name })}
            onEditField={(gridField) => {
              const full = fields.find((item) => item.id === gridField.id) ?? null;
              setEditFieldError(null);
              setEditingField(full);
            }}
            onOpenRecord={setOpenRecordId}
            onOpenRelationCell={(recordId, gridField, currentIds, baseRevision) => {
              if (!gridField.config?.target_table_id) return;
              editingRef.current = true;
              setRelationChooser({
                recordId,
                fieldId: gridField.id,
                fieldName: gridField.name,
                targetTableId: gridField.config.target_table_id,
                currentIds,
                baseRevision,
              });
            }}
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
              const target = fields.find((item) => item.id === fieldId);
              if (target?.kind === "lookup" || target?.kind === "formula") return;
              saveCell.mutate({ recordId, fieldId, value, baseRevision });
            }}
          />
        ) : null}
        {sheet.data && fields.length > 0 && activeKind === "kanban" ? (
          <KanbanView
            fields={fields}
            visibleFields={visible}
            records={sheet.data.records}
            recordOptionsByTable={recordOptionsByTable}
            titleFieldId={effectiveConfig.titleFieldId}
            groupFieldId={effectiveConfig.groupFieldId}
            onOpenRecord={setOpenRecordId}
            onMoveRecord={(recordId, fieldId, value, baseRevision) => {
              saveCell.mutate({ recordId, fieldId, value, baseRevision });
            }}
          />
        ) : null}
        {sheet.data && fields.length > 0 && activeKind === "gallery" ? (
          <GalleryView
            fields={fields}
            visibleFields={visible}
            records={sheet.data.records}
            recordOptionsByTable={recordOptionsByTable}
            titleFieldId={effectiveConfig.titleFieldId}
            onOpenRecord={setOpenRecordId}
          />
        ) : null}
        {sheet.data && fields.length > 0 && activeKind === "calendar" ? (
          <CalendarView
            fields={fields}
            visibleFields={visible}
            records={sheet.data.records}
            recordOptionsByTable={recordOptionsByTable}
            titleFieldId={effectiveConfig.titleFieldId}
            dateFieldId={effectiveConfig.dateFieldId}
            onOpenRecord={setOpenRecordId}
          />
        ) : null}
        {sheet.data && fields.length > 0 && activeKind === "gantt" ? (
          <GanttView
            fields={fields}
            visibleFields={visible}
            records={sheet.data.records}
            recordOptionsByTable={recordOptionsByTable}
            titleFieldId={effectiveConfig.titleFieldId}
            dateFieldId={effectiveConfig.dateFieldId}
            endDateFieldId={effectiveConfig.endDateFieldId}
            onOpenRecord={setOpenRecordId}
          />
        ) : null}
        {sheet.data && fields.length > 0 && activeKind === "org_chart" ? (
          <OrgChartView
            fields={fields}
            visibleFields={visible}
            records={sheet.data.records}
            recordOptionsByTable={recordOptionsByTable}
            titleFieldId={effectiveConfig.titleFieldId}
            parentFieldId={effectiveConfig.parentFieldId}
            onOpenRecord={setOpenRecordId}
          />
        ) : null}
        {sheet.data && fields.length > 0 && active && activeKind === "form" ? (
          <FormView
            tableId={tableId}
            viewId={active.id}
            viewName={draft?.name ?? active.name}
            visibleFields={visible}
            requiredFieldIds={effectiveConfig.requiredFieldIds}
            recordOptionsByTable={recordOptionsByTable}
            onSubmitted={async () => {
              await invalidate();
              await queryClient.invalidateQueries({ queryKey: genericTableKeys.recordOptions(wsId, tableId) });
            }}
          />
        ) : null}
      </div>
      <RecordDetailDialog
        table={sheet.data?.table ?? null}
        activeView={active ? { id: active.id, name: draft?.name ?? active.name, kind: activeKind } : null}
        record={openRecord}
        fields={fields}
        visibleFields={visible}
        records={sheet.data?.records}
        titleFieldId={effectiveConfig.titleFieldId}
        recordOptionsByTable={recordOptionsByTable}
        onClose={() => setOpenRecordId(null)}
      />
      <EditDerivedFieldDialog
        wsId={wsId}
        field={editingField}
        fields={fields}
        pending={updateField.isPending}
        error={editFieldError}
        onClose={() => {
          setEditingField(null);
          setEditFieldError(null);
        }}
        onSubmit={(fieldId, patch) => {
          setEditFieldError(null);
          updateField.mutate({ fieldId, patch });
        }}
      />
      <RelationChooserDialog
        wsId={wsId}
        state={relationChooser}
        pending={saveCell.isPending}
        onClose={() => {
          setRelationChooser(null);
          editingRef.current = false;
          releaseSheet();
        }}
        onSave={(nextIds) => {
          if (!relationChooser) return;
          const current = relationChooser;
          setRelationChooser(null);
          editingRef.current = false;
          saveCell.mutate({
            recordId: current.recordId,
            fieldId: current.fieldId,
            value: nextIds,
            baseRevision: current.baseRevision,
          });
        }}
      />
    </div>
  );
}

function flattenCell(value: GenericCellValue): Array<string | number | boolean | null> {
  if (!Array.isArray(value)) return [value];
  const out: Array<string | number | boolean | null> = [];
  for (const item of value) {
    out.push(...flattenCell(item));
  }
  return out;
}

function viewToDraft(view: GenericView): ViewDraft {
  return {
    name: view.name,
    kind: view.kind ?? "grid",
    config: view.config ?? {},
    columns: view.columns,
    filter: view.filter,
    sort: view.sort,
    group: view.group ?? null,
  };
}

export function patchCachedSheetView(
  sheet: GenericSheet | undefined,
  viewId: string,
  saved: GenericView,
): GenericSheet | undefined {
  if (!sheet) return sheet;
  let changed = false;
  const views = sheet.views.map((view) => {
    if (view.id !== viewId) return view;
    changed = true;
    return saved;
  });
  return changed ? { ...sheet, views } : sheet;
}

function savedView(draft: ViewDraft): GenericViewWrite {
  const filter = draft.filter && (draft.filter.op === "is_empty" || draft.filter.op === "is_not_empty")
    ? { field_id: draft.filter.field_id, op: draft.filter.op }
    : draft.filter;
  return {
    name: draft.name.trim(),
    config: draft.config,
    columns: draft.columns,
    filter,
    sort: draft.sort,
    group: draft.group,
  };
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
  tableId,
  fields,
  views,
  activeId,
  draft,
  hiddenCount,
  showNewView,
  onShowNewView,
  viewName,
  viewKind,
  onViewName,
  onViewKind,
  onDraft,
  onSelectView,
  onCreateView,
}: {
  tableId: string;
  fields: GenericField[];
  views: { id: string; name: string; kind: GenericViewKind }[];
  activeId: string;
  draft: ViewDraft;
  hiddenCount: number;
  showNewView: boolean;
  onShowNewView: (open: boolean) => void;
  viewName: string;
  viewKind: GenericViewKind;
  onViewName: (value: string) => void;
  onViewKind: (value: GenericViewKind) => void;
  onDraft: (draft: ViewDraft) => void;
  onSelectView: (id: string) => void;
  onCreateView: () => void;
}) {
  const { t } = useT("tables");
  const visible = visibleFields(fields, draft.columns).map((field) => field.id);
  const hidden = fields.filter((field) => !visible.includes(field.id));
  const ordered = [...visibleFields(fields, draft.columns), ...hidden];

  const setVisible = (ids: string[]) => {
    if (draft.kind === "form" && draft.config.required_field_ids?.length) {
      const shownSet = new Set(ids);
      const nextRequired = draft.config.required_field_ids.filter((id) => shownSet.has(id));
      const nextConfig: GenericViewConfig = { ...draft.config };
      if (nextRequired.length > 0) nextConfig.required_field_ids = nextRequired;
      else delete nextConfig.required_field_ids;
      onDraft({ ...draft, columns: ids, config: nextConfig });
      return;
    }
    onDraft({ ...draft, columns: ids });
  };

  const updateConfig = (patch: Partial<GenericViewConfig>) => {
    const nextConfig: GenericViewConfig = { ...draft.config, ...patch };
    for (const [key, value] of Object.entries(nextConfig)) {
      if (value === undefined || value === "" || (Array.isArray(value) && value.length === 0)) {
        delete (nextConfig as Record<string, unknown>)[key];
      }
    }
    onDraft({ ...draft, config: nextConfig });
  };

  const singleSelectFields = fields.filter((field) => field.kind === "single_select");
  const dateFields = fields.filter((field) => field.kind === "date");
  const selfRelationFields = fields.filter((field) => isSameTableOneWayRelation(field, tableId));
  const visibleEditableFields = visibleFields(fields, draft.columns).filter(isEditableField);

  return (
    <div className="border-b border-border">
      <div className="flex h-8 items-center gap-1 overflow-x-auto px-2" role="tablist" aria-label={t(($) => $.sheet.view_name)}>
        {views.map((view) => (
          <button
            key={view.id}
            type="button"
            role="tab"
            data-view-id={view.id}
            data-view-kind={view.kind}
            aria-selected={view.id === activeId}
            className={view.id === activeId ? "h-7 shrink-0 rounded-sm bg-accent px-2 text-body" : "h-7 shrink-0 rounded-sm px-2 text-body text-muted-foreground"}
            onClick={() => onSelectView(view.id)}
          >
            {view.name}
            {view.kind !== "grid" ? (
              <span className="ml-1 text-caption text-muted-foreground">
                · {t(($) => $.sheet.view_kinds[view.kind])}
              </span>
            ) : null}
          </button>
        ))}
        <Popover open={showNewView} onOpenChange={onShowNewView}>
          <PopoverTrigger render={<Button type="button" variant="ghost" size="sm">{t(($) => $.sheet.new_view)}</Button>} />
          <PopoverContent align="start" className="flex w-72 flex-col gap-2 p-2.5">
            <Input
              aria-label={t(($) => $.sheet.view_name)}
              value={viewName}
              onChange={(event) => onViewName(event.target.value)}
              placeholder={t(($) => $.sheet.view_name)}
            />
            <select
              aria-label={t(($) => $.sheet.view_type)}
              value={viewKind}
              onChange={(event) => onViewKind(event.target.value as GenericViewKind)}
              className="h-8 rounded-md border border-border bg-transparent px-2 text-body"
            >
              {genericViewKinds.map((kind) => (
                <option key={kind} value={kind}>
                  {t(($) => $.sheet.view_kinds[kind])}
                </option>
              ))}
            </select>
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
        {draft.kind !== "form" ? (
          <>
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
          </>
        ) : null}
        {draft.kind === "grid" ? (
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
        ) : (
          <Popover>
            <PopoverTrigger render={<Button type="button" variant="ghost" size="sm">{t(($) => $.sheet.view_settings)}</Button>} />
            <PopoverContent align="start" className="flex w-72 flex-col gap-2.5 p-3">
              {draft.kind === "kanban" ? (
                <label className="flex flex-col gap-1 text-caption text-muted-foreground">
                  {t(($) => $.sheet.group_field)}
                  <select
                    aria-label={t(($) => $.sheet.group_field)}
                    value={draft.config.group_field_id ?? ""}
                    onChange={(event) => updateConfig({ group_field_id: event.target.value || undefined })}
                    className="h-8 rounded-md border border-border bg-transparent px-2 text-body text-foreground"
                  >
                    <option value="">{t(($) => $.sheet.auto_field)}</option>
                    {singleSelectFields.map((field) => (
                      <option key={field.id} value={field.id}>{field.name}</option>
                    ))}
                  </select>
                </label>
              ) : null}

              {draft.kind === "calendar" || draft.kind === "gantt" ? (
                <label className="flex flex-col gap-1 text-caption text-muted-foreground">
                  {t(($) => $.sheet.date_field)}
                  <select
                    aria-label={t(($) => $.sheet.date_field)}
                    value={draft.config.date_field_id ?? ""}
                    onChange={(event) => updateConfig({ date_field_id: event.target.value || undefined })}
                    className="h-8 rounded-md border border-border bg-transparent px-2 text-body text-foreground"
                  >
                    <option value="">{t(($) => $.sheet.auto_field)}</option>
                    {dateFields.map((field) => (
                      <option key={field.id} value={field.id}>{field.name}</option>
                    ))}
                  </select>
                </label>
              ) : null}

              {draft.kind === "gantt" ? (
                <label className="flex flex-col gap-1 text-caption text-muted-foreground">
                  {t(($) => $.sheet.end_date_field)}
                  <select
                    aria-label={t(($) => $.sheet.end_date_field)}
                    value={draft.config.end_date_field_id ?? ""}
                    onChange={(event) => updateConfig({ end_date_field_id: event.target.value || undefined })}
                    className="h-8 rounded-md border border-border bg-transparent px-2 text-body text-foreground"
                  >
                    <option value="">{t(($) => $.sheet.auto_field)}</option>
                    {dateFields.map((field) => (
                      <option key={field.id} value={field.id}>{field.name}</option>
                    ))}
                  </select>
                </label>
              ) : null}

              {draft.kind === "org_chart" ? (
                <label className="flex flex-col gap-1 text-caption text-muted-foreground">
                  {t(($) => $.sheet.parent_field)}
                  <select
                    aria-label={t(($) => $.sheet.parent_field)}
                    value={draft.config.parent_field_id ?? ""}
                    onChange={(event) => updateConfig({ parent_field_id: event.target.value || undefined })}
                    className="h-8 rounded-md border border-border bg-transparent px-2 text-body text-foreground"
                  >
                    <option value="">{t(($) => $.sheet.auto_field)}</option>
                    {selfRelationFields.map((field) => (
                      <option key={field.id} value={field.id}>{field.name}</option>
                    ))}
                  </select>
                </label>
              ) : null}

              {draft.kind !== "form" ? (
                <label className="flex flex-col gap-1 text-caption text-muted-foreground">
                  {t(($) => $.sheet.title_field)}
                  <select
                    aria-label={t(($) => $.sheet.title_field)}
                    value={draft.config.title_field_id ?? ""}
                    onChange={(event) => updateConfig({ title_field_id: event.target.value || undefined })}
                    className="h-8 rounded-md border border-border bg-transparent px-2 text-body text-foreground"
                  >
                    <option value="">{t(($) => $.sheet.auto_field)}</option>
                    {fields.map((field) => (
                      <option key={field.id} value={field.id}>{field.name}</option>
                    ))}
                  </select>
                </label>
              ) : (
                <div className="flex flex-col gap-1.5">
                  <span className="text-caption font-medium text-muted-foreground">
                    {t(($) => $.sheet.required_fields)}
                  </span>
                  {visibleEditableFields.length === 0 ? (
                    <p className="text-caption text-muted-foreground">{t(($) => $.sheet.empty_form_fields)}</p>
                  ) : (
                    <ul className="space-y-1">
                      {visibleEditableFields.map((field) => {
                        const checked = draft.config.required_field_ids?.includes(field.id) ?? false;
                        return (
                          <li key={field.id}>
                            <label className="flex cursor-pointer items-center gap-2 text-body">
                              <input
                                type="checkbox"
                                checked={checked}
                                onChange={() => {
                                  const current = draft.config.required_field_ids ?? [];
                                  const next = checked
                                    ? current.filter((id) => id !== field.id)
                                    : [...current, field.id];
                                  updateConfig({ required_field_ids: next });
                                }}
                              />
                              <span className="truncate">{field.name}</span>
                            </label>
                          </li>
                        );
                      })}
                    </ul>
                  )}
                </div>
              )}
            </PopoverContent>
          </Popover>
        )}
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
                const picked = fields.find((item) => item.id === fieldId);
                onDraft({
                  ...draft,
                  filter: fieldId
                    ? { field_id: fieldId, op: picked?.kind === "lookup" ? "is_not_empty" : "eq" }
                    : null,
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
              {OPS.filter((item) => {
                if (filterField?.kind === "lookup") {
                  return item === "is_empty" || item === "is_not_empty";
                }
                if (item === "contains") {
                  return filterField?.kind === "text" || filterField?.kind === "formula";
                }
                return true;
              }).map((item) => (
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
        } else if (field?.kind === "formula") {
          if (raw === "true") onChange(true);
          else if (raw === "false") onChange(false);
          else if (raw.trim() !== "" && Number.isFinite(Number(raw))) onChange(Number(raw));
          else onChange(raw);
        } else onChange(raw);
      }}
      className="max-w-48"
    />
  );
}

