"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  catalogGridColumnOrder,
  gridPropertyColumnId,
  isGridColumnKey,
  isGridColumnVisible,
  type GridColumnKey,
} from "@multica/core/issues/grid-columns";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { hashKey, useQueries, useQuery } from "@tanstack/react-query";
import {
  ApitableGrid,
  type GridCellRenderContext,
  type GridCellValue,
  type GridFieldView,
  type GridLabels,
  type GridRecordView,
  type GridRowSelection,
} from "@multica/apitable-grid";
import { ArrowDown, ArrowUp, ChevronDown, Download, Loader2, Plus } from "@multica/ui/icons";
import { IconButton } from "@multica/ui/components/common/icon-button";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { toast } from "sonner";
import { buildActorNameResolver, useActorName } from "@multica/core/workspace/hooks";
import {
  agentListOptions,
  memberListOptions,
  squadListOptions,
} from "@multica/core/workspace/queries";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { issueTypeListOptions } from "@multica/core/issue-types";
import { propertyListOptions } from "@multica/core/properties";
import { issueTableRowPageOptions } from "@multica/core/issues/queries";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  Issue,
  IssueProperty,
  IssueTableQuerySpec,
  UpdateIssueRequest,
} from "@multica/core/types";
import { isActorPropertyType } from "@multica/core/types";
import { useIssueSurfaceSelection } from "../surface/selection-context";
import {
  buildIssueTableCsv,
  getIssueTableSelectionRange,
  IssueTableExportIntegrityError,
} from "./table-view-model";
import { issueGridExportRow } from "./issue-grid-export";
import { useT } from "../../i18n";
import { resolveClickIntent, useIntentNavigate } from "../../navigation";
import { runConfirmIntent } from "../actions/run-confirm-gate";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";
import { useIssuePeekClick } from "../surface/peek-context";
import { useStatusLabel } from "../utils/status-label";
import type { IssueCreateDefaults } from "../surface/types";
import { BatchActionToolbar } from "./batch-action-toolbar";
import { AssigneePicker, StatusPicker } from "./pickers";
import { CustomPropertyValueEditor } from "./pickers/custom-property-picker";

const GRID_PAGE_LIMIT = 50;
const INITIAL_CURSORS: Array<string | null> = [null];
const PROPERTY_COLUMN_PREFIX = "property:";

export interface MultidimensionalGridViewProps {
  serverQuery: IssueTableQuerySpec;
  onCreateIssue: (defaults?: IssueCreateDefaults) => void;
  showBatchToolbar: boolean;
  exportIssues: () => Promise<Issue[]>;
}

export function MultidimensionalGridView({
  serverQuery,
  onCreateIssue,
  showBatchToolbar,
  exportIssues,
}: MultidimensionalGridViewProps) {
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const paths = useWorkspacePaths();
  const intentNavigate = useIntentNavigate();
  const handlePeekClick = useIssuePeekClick();
  const actions = useIssueSurfaceActionsOptional();
  const openModal = useModalStore((s) => s.open);
  const { entryOf } = useIssueStatuses(wsId);
  const resolveStatusLabel = useStatusLabel(wsId);
  const selection = useIssueSurfaceSelection();
  const selectionAnchorRef = useRef<string | null>(null);
  const [exporting, setExporting] = useState<"all" | "selected" | null>(null);
  const issueTypesQuery = useQuery(issueTypeListOptions(wsId, true));
  const propertiesQuery = useQuery(propertyListOptions(wsId));
  const workspaceIssueTypes = issueTypesQuery.data ?? [];
  const workspaceProperties = propertiesQuery.data ?? [];
  const gridCatalogReady =
    issueTypesQuery.isSuccess && propertiesQuery.isSuccess;

  const issueTypeById = useMemo(
    () => new Map(workspaceIssueTypes.map((it) => [it.id, it])),
    [workspaceIssueTypes],
  );
  const propertyById = useMemo(
    () => new Map(workspaceProperties.map((p) => [p.id, p])),
    [workspaceProperties],
  );

  const queryIdentity = useMemo(
    () => hashKey([wsId, serverQuery]),
    [wsId, serverQuery],
  );
  const [cursorState, setCursorState] = useState<{
    identity: string;
    cursors: Array<string | null>;
  }>(() => ({ identity: queryIdentity, cursors: INITIAL_CURSORS }));
  const cursors =
    cursorState.identity === queryIdentity
      ? cursorState.cursors
      : INITIAL_CURSORS;

  const pageQueries = useQueries({
    queries: cursors.map((cursor) => ({
      ...issueTableRowPageOptions(wsId, {
        query: serverQuery,
        group: { kind: "none" },
        group_key: null,
        hierarchy: { enabled: false },
        parent_id: null,
        page: { limit: GRID_PAGE_LIMIT, cursor },
      }),
      // Do not carry placeholder rows across query or scope transitions.
      placeholderData: undefined,
    })),
  });

  const {
    issues,
    total,
    nextCursor,
    validPrefixLength,
    activeTailQuery,
  } = useMemo(() => {
    const deduped: Issue[] = [];
    const seen = new Set<string>();
    let resolvedTotal = 0;
    let resolvedNextCursor: string | null = null;
    let prefixLength = 1;
    let tail: (typeof pageQueries)[number] | undefined;

    for (let i = 0; i < pageQueries.length; i += 1) {
      const query = pageQueries[i];
      if (!query) break;
      if (i > 0) {
        // Parent-to-child cursor invariant: keep a loaded/pending tail only
        // while its cursor still matches its predecessor's current next_cursor.
        if (cursors[i] !== resolvedNextCursor) break;
        prefixLength = i + 1;
        tail = query;
      }
      const data = query.data;
      if (!data) break;
      if (i === 0) resolvedTotal = data.total;
      for (const row of data.rows) {
        if (seen.has(row.issue.id)) continue;
        seen.add(row.issue.id);
        deduped.push(row.issue);
      }
      resolvedNextCursor = data.next_cursor;
    }

    return {
      issues: deduped,
      total: resolvedTotal,
      nextCursor: resolvedNextCursor,
      validPrefixLength: prefixLength,
      activeTailQuery: tail,
    };
  }, [cursors, pageQueries]);

  const hasStaleTail = validPrefixLength < cursors.length;
  useEffect(() => {
    if (!hasStaleTail) return;
    setCursorState((prev) => {
      if (
        prev.identity !== queryIdentity ||
        prev.cursors.length <= validPrefixLength
      ) {
        return prev;
      }
      return {
        identity: queryIdentity,
        cursors: prev.cursors.slice(0, validPrefixLength),
      };
    });
  }, [hasStaleTail, queryIdentity, validPrefixLength]);

  const issueById = useMemo(
    () => new Map(issues.map((issue) => [issue.id, issue])),
    [issues],
  );

  const headQuery = pageQueries[0];
  const isHeadLoading = Boolean(headQuery && headQuery.isPending && !headQuery.data);
  const isHeadError = Boolean(headQuery && headQuery.isError && !headQuery.data);
  const isTailLoading = Boolean(
    activeTailQuery &&
      (activeTailQuery.isPending || activeTailQuery.isFetching) &&
      !activeTailQuery.data,
  );
  const isTailError = Boolean(
    activeTailQuery && activeTailQuery.isError && !activeTailQuery.data,
  );
  const validCursors = cursors.slice(0, validPrefixLength);
  const canLoadMore = Boolean(
    nextCursor &&
      !validCursors.includes(nextCursor) &&
      !isTailLoading &&
      !isTailError,
  );

  const handleLoadMore = useCallback(() => {
    if (!nextCursor) return;
    setCursorState((prev) => {
      const current =
        prev.identity === queryIdentity
          ? prev.cursors.slice(0, validPrefixLength)
          : [null];
      if (current.includes(nextCursor)) return prev;
      return { identity: queryIdentity, cursors: [...current, nextCursor] };
    });
  }, [nextCursor, queryIdentity, validPrefixLength]);

  const openIssue = useCallback(
    (issue: Issue, event?: React.MouseEvent) => {
      if (handlePeekClick(issue.id, event)) {
        if (event?.shiftKey) window.getSelection()?.removeAllRanges();
        return;
      }
      intentNavigate(
        paths.issueDetail(issue.id),
        event ? resolveClickIntent(event) : "push",
        issue.identifier,
      );
    },
    [handlePeekClick, intentNavigate, paths],
  );

  const handleOpenRecord = useCallback(
    (recordId: string) => {
      const issue = issueById.get(recordId);
      if (issue) openIssue(issue);
    },
    [issueById, openIssue],
  );

  const updateIssue = useCallback(
    (
      issue: Issue,
      updates: Partial<UpdateIssueRequest>,
      expectedRevision?: number,
    ) => {
      const keys = Object.keys(updates);
      if (keys.length === 0) return;
      if (
        keys.length === 1 &&
        updates.status !== undefined &&
        updates.status === issue.status
      ) {
        return;
      }
      if (
        keys.length === 2 &&
        Object.prototype.hasOwnProperty.call(updates, "assignee_type") &&
        Object.prototype.hasOwnProperty.call(updates, "assignee_id") &&
        updates.assignee_type === issue.assignee_type &&
        updates.assignee_id === issue.assignee_id
      ) {
        return;
      }
      const intent = runConfirmIntent(issue, updates, { entryOf });
      if (intent) {
        openModal("issue-run-confirm", intent);
        return;
      }
      const revision = expectedRevision ?? issue.revision;
      actions?.updateIssue(issue.id, {
        ...updates,
        ...(revision !== undefined ? { expected_revision: revision } : {}),
      });
    },
    [actions, entryOf, openModal],
  );

  const handleCellCommit = useCallback(
    (
      recordId: string,
      fieldId: string,
      value: GridCellValue,
      baseRevision: number,
    ) => {
      if (fieldId !== "title") return;
      const issue = issueById.get(recordId);
      if (!issue) return;
      const nextTitle = typeof value === "string" ? value.trim() : "";
      if (!nextTitle || nextTitle === issue.title) return;
      updateIssue(
        issue,
        { title: nextTitle, title_base: issue.title },
        issue.revision !== undefined ? baseRevision : undefined,
      );
    },
    [issueById, updateIssue],
  );

  const gridColumnOrder = useViewStore((state) => state.gridColumnOrder);
  const gridHiddenColumnIds = useViewStore((state) => state.gridHiddenColumnIds);
  const setGridColumnHidden = useViewStore((state) => state.setGridColumnHidden);
  const moveGridColumn = useViewStore((state) => state.moveGridColumn);
  const syncGridColumnCatalog = useViewStore((state) => state.syncGridColumnCatalog);

  const catalogOrder = useMemo(
    () =>
      catalogGridColumnOrder(
        workspaceIssueTypes.length > 0,
        workspaceProperties.map((prop) => prop.id),
      ),
    [workspaceIssueTypes.length, workspaceProperties],
  );

  useEffect(() => {
    if (!gridCatalogReady) return;
    syncGridColumnCatalog(catalogOrder);
  }, [catalogOrder, gridCatalogReady, syncGridColumnCatalog]);

  const hiddenColumnIds = useMemo(
    () => new Set(gridHiddenColumnIds),
    [gridHiddenColumnIds],
  );

  const fieldsById = useMemo(() => {
    const byId = new Map<string, GridFieldView>();
    byId.set("title", {
      id: "title",
      name: t(($) => $.table.columns.title),
      kind: "text",
      cancelOnEscape: true,
    });
    if (workspaceIssueTypes.length > 0) {
      byId.set("type", {
        id: "type",
        name: t(($) => $.detail.prop_type),
        kind: "text",
      });
    }
    byId.set("status", {
      id: "status",
      name: t(($) => $.table.columns.status),
      kind: "single_select",
    });
    byId.set("assignee", {
      id: "assignee",
      name: t(($) => $.table.columns.assignee),
      kind: "text",
    });
    for (const prop of workspaceProperties) {
      byId.set(gridPropertyColumnId(prop.id), {
        id: gridPropertyColumnId(prop.id),
        name: prop.name,
        kind: "text",
      });
    }
    return byId;
  }, [t, workspaceIssueTypes.length, workspaceProperties]);

  const menuFields = useMemo(
    () =>
      gridColumnOrder
        .map((columnId) => fieldsById.get(columnId))
        .filter((field): field is GridFieldView => Boolean(field)),
    [fieldsById, gridColumnOrder],
  );

  const visibleFields = useMemo(
    () =>
      gridColumnOrder
        .filter((columnId) => isGridColumnVisible(columnId, hiddenColumnIds))
        .map((columnId) => fieldsById.get(columnId))
        .filter((field): field is GridFieldView => Boolean(field)),
    [fieldsById, gridColumnOrder, hiddenColumnIds],
  );

  const exportColumnKeys = useMemo(
    () =>
      visibleFields
        .map((field) => field.id)
        .filter((id): id is GridColumnKey => isGridColumnKey(id)),
    [visibleFields],
  );

  const recordIds = useMemo(() => issues.map((issue) => issue.id), [issues]);
  const selectedIssues = useMemo(
    () => issues.filter((issue) => selection.selectedIds.has(issue.id)),
    [issues, selection.selectedIds],
  );

  const handleRecordSelection = useCallback(
    (recordId: string, shiftKey: boolean) => {
      const range = shiftKey
        ? getIssueTableSelectionRange(
            recordIds,
            selectionAnchorRef.current,
            recordId,
          )
        : null;
      if (range) {
        if (selection.selectedIds.has(recordId)) selection.deselect(range);
        else selection.select(range);
        return;
      }
      selection.toggle(recordId);
      selectionAnchorRef.current = recordId;
    },
    [recordIds, selection],
  );

  useEffect(() => {
    if (selection.selectedIds.size === 0) selectionAnchorRef.current = null;
  }, [selection.selectedIds]);

  const rowSelection = useMemo<GridRowSelection>(
    () => ({
      selectedRecordIds: selection.selectedIds,
      recordIds,
      selectAllLabel: t(($) => $.table.select_all),
      selectRecordLabel: (recordId) => {
        const issue = issueById.get(recordId);
        return t(($) => $.table.select_issue, {
          identifier: issue?.identifier ?? recordId,
        });
      },
      onToggleRecord: handleRecordSelection,
      onToggleAll: (select) => {
        if (select) selection.select(recordIds);
        else selection.deselect(recordIds);
      },
    }),
    [handleRecordSelection, issueById, recordIds, selection, t],
  );

  const handleExport = useCallback(
    async (mode: "all" | "selected") => {
      setExporting(mode);
      try {
        const exportProperties = gridCatalogReady
          ? workspaceProperties
          : (await queryClient.fetchQuery(
              propertyListOptions(wsId),
            )) as unknown as IssueProperty[];
        const exportPropertyById = new Map(
          exportProperties.map((property) => [property.id, property]),
        );
        const exportColumnKeysResolved = exportColumnKeys.filter((key) => {
          if (!key.startsWith(PROPERTY_COLUMN_PREFIX)) return true;
          const propertyId = key.slice(PROPERTY_COLUMN_PREFIX.length);
          return exportPropertyById.has(propertyId);
        });
        const needsActors =
          exportColumnKeysResolved.includes("assignee") ||
          exportColumnKeysResolved.some((key) => {
            if (!key.startsWith(PROPERTY_COLUMN_PREFIX)) return false;
            const property = exportPropertyById.get(
              key.slice(PROPERTY_COLUMN_PREFIX.length),
            );
            return property ? isActorPropertyType(property.type) : false;
          });
        const exportActorName: (type: string, id: string) => string = needsActors
          ? await Promise.all([
              queryClient.fetchQuery(memberListOptions(wsId)),
              queryClient.fetchQuery(agentListOptions(wsId)),
              queryClient.fetchQuery(squadListOptions(wsId)),
            ]).then(([members, agents, squads]) =>
              buildActorNameResolver({ members, agents, squads }),
            )
          : getActorName;
        const rows =
          mode === "all" ? await exportIssues() : selectedIssues;
        const headers = exportColumnKeysResolved.map((key) => {
          if (isGridColumnKey(key)) {
            const field = fieldsById.get(key);
            if (field) return field.name;
          }
          return "";
        });
        const csvRows = rows.map((issue) =>
          issueGridExportRow(issue, exportColumnKeysResolved, {
            resolveStatusLabel,
            issueTypeName: issue.issue_type_id
              ? issueTypeById.get(issue.issue_type_id)?.name ?? null
              : null,
            propertyById: exportPropertyById,
            getActorName: exportActorName,
          }),
        );
        const csv = buildIssueTableCsv(headers, csvRows);
        const blob = new Blob(["\uFEFF", csv], { type: "text/csv;charset=utf-8" });
        const url = URL.createObjectURL(blob);
        const anchor = document.createElement("a");
        anchor.href = url;
        const filenamePrefix = mode === "all" ? "issues" : "issues-selected";
        anchor.download = `${filenamePrefix}-${new Date().toISOString().slice(0, 10)}.csv`;
        anchor.click();
        URL.revokeObjectURL(url);
        toast.success(t(($) => $.table.export_success, { count: rows.length }));
      } catch (error) {
        toast.error(
          error instanceof Error &&
            !(error instanceof IssueTableExportIntegrityError) &&
            error.message
            ? error.message
            : t(($) => $.table.export_failed),
        );
      } finally {
        setExporting(null);
      }
    },
    [
      exportColumnKeys,
      exportIssues,
      fieldsById,
      getActorName,
      issueTypeById,
      queryClient,
      resolveStatusLabel,
      selectedIssues,
      t,
      wsId,
      gridCatalogReady,
      workspaceProperties,
    ],
  );

  const records = useMemo<GridRecordView[]>(
    () =>
      issues.map((issue) => {
        const typeName = issue.issue_type_id
          ? issueTypeById.get(issue.issue_type_id)?.name ?? null
          : null;
        const propValues: Record<string, GridCellValue> = {};
        for (const prop of workspaceProperties) {
          const val = issue.properties?.[prop.id];
          propValues[`${PROPERTY_COLUMN_PREFIX}${prop.id}`] =
            val === undefined
              ? null
              : Array.isArray(val)
                ? val.join(", ")
                : val;
        }
        return {
          id: issue.id,
          revision: issue.revision ?? 0,
          values: {
            title: issue.title,
            ...(workspaceIssueTypes.length > 0 ? { type: typeName } : {}),
            status: issue.status,
            assignee:
              issue.assignee_type && issue.assignee_id
                ? `${issue.assignee_type}:${issue.assignee_id}`
                : null,
            ...propValues,
          },
        };
      }),
    [issues, issueTypeById, workspaceIssueTypes.length, workspaceProperties],
  );

  const labels = useMemo<GridLabels>(
    () => ({
      rename: t(($) => $.table.rename_title),
      clear: t(($) => $.table.search_clear),
      emptyRecords: t(($) => $.table.empty),
      emptyValue: t(($) => $.table.empty_value),
      checked: "true",
      unchecked: "false",
      openRecord: t(($) => $.member_answer.open_issue),
    }),
    [t],
  );

  const renderCell = useCallback(
    ({ record, field, onActivate }: GridCellRenderContext) => {
      const issue = issueById.get(record.id);
      if (!issue) return undefined;
      if (field.id === "type") {
        const issueType = issue.issue_type_id
          ? issueTypeById.get(issue.issue_type_id)
          : undefined;
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-2 text-caption"
            onFocus={onActivate}
          >
            {issueType ? (
              <span className="inline-flex max-w-full items-center truncate rounded-full bg-muted/60 px-2 py-0.5 text-micro font-medium text-foreground">
                {issueType.name}
              </span>
            ) : (
              <span className="text-muted-foreground">{t(($) => $.table.empty_value)}</span>
            )}
          </div>
        );
      }
      if (field.id === "status") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-1.5"
            onFocus={onActivate}
          >
            <StatusPicker
              status={issue.status}
              projectId={issue.project_id}
              issue={issue}
              align="start"
              onUpdate={(updates) => updateIssue(issue, updates)}
            />
          </div>
        );
      }
      if (field.id === "assignee") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-1.5"
            onFocus={onActivate}
          >
            <AssigneePicker
              assigneeType={issue.assignee_type}
              assigneeId={issue.assignee_id}
              align="start"
              onUpdate={(updates) => updateIssue(issue, updates)}
            />
          </div>
        );
      }
      if (field.id.startsWith(PROPERTY_COLUMN_PREFIX)) {
        const propertyId = field.id.slice(PROPERTY_COLUMN_PREFIX.length);
        const property = propertyById.get(propertyId);
        if (!property) return undefined;
        const issueType = issue.issue_type_id
          ? issueTypeById.get(issue.issue_type_id)
          : undefined;
        const isRequired = Boolean(
          issueType?.fields.some((f) => f.property_id === propertyId && f.required),
        );
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-2"
            onFocus={onActivate}
          >
            <CustomPropertyValueEditor
              issue={issue}
              property={property}
              required={isRequired}
            />
          </div>
        );
      }
      return undefined;
    },
    [issueById, issueTypeById, propertyById, t, updateIssue],
  );

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <div className="flex items-center justify-between gap-2 border-b border-border/60 px-4 py-2">
        <span className="text-caption text-muted-foreground tabular-nums">
          {issues.length}
          {total > issues.length ? ` / ${total}` : ""}
        </span>
        <div className="flex items-center gap-2">
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={exporting !== null}
                />
              }
            >
              {exporting ? (
                <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              ) : (
                <Download className="size-3.5" aria-hidden="true" />
              )}
              {t(($) => $.table.export)}
              <ChevronDown className="size-3" aria-hidden="true" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-52">
              <DropdownMenuItem onClick={() => void handleExport("all")}>
                <Download className="size-3.5" aria-hidden="true" />
                {t(($) => $.table.export_all)}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={selectedIssues.length === 0}
                onClick={() => void handleExport("selected")}
              >
                <Download className="size-3.5" aria-hidden="true" />
                {t(($) => $.table.export_selected, {
                  count: selectedIssues.length,
                })}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <DropdownMenu>
            <DropdownMenuTrigger
              render={<Button type="button" size="sm" variant="outline" />}
            >
              {t(($) => $.table.columns.section)}
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuCheckboxItem checked disabled>
                {t(($) => $.table.columns.title)}
              </DropdownMenuCheckboxItem>
              {menuFields
                .filter(
                  (field): field is GridFieldView & { id: GridColumnKey } =>
                    field.id !== "title" && isGridColumnKey(field.id),
                )
                .map((field) => {
                  const columnKey = field.id;
                  const position = gridColumnOrder.indexOf(columnKey);
                  const moveUpDisabled = position <= 1;
                  const moveDownDisabled =
                    position < 0 || position >= gridColumnOrder.length - 1;
                  return (
                    <div
                      key={field.id}
                      className="flex items-center gap-0.5 pr-1"
                      data-column-id={field.id}
                    >
                      <DropdownMenuCheckboxItem
                        className="min-w-0 flex-1"
                        checked={isGridColumnVisible(columnKey, hiddenColumnIds)}
                        onCheckedChange={(checked) =>
                          setGridColumnHidden(columnKey, !checked)
                        }
                      >
                        {field.name}
                      </DropdownMenuCheckboxItem>
                      <IconButton
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        label={t(($) => $.table.columns.move_up, { column: field.name })}
                        disabled={moveUpDisabled}
                        onClick={() => moveGridColumn(columnKey, -1)}
                      >
                        <ArrowUp className="size-3.5" aria-hidden="true" />
                      </IconButton>
                      <IconButton
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        label={t(($) => $.table.columns.move_down, { column: field.name })}
                        disabled={moveDownDisabled}
                        onClick={() => moveGridColumn(columnKey, 1)}
                      >
                        <ArrowDown className="size-3.5" aria-hidden="true" />
                      </IconButton>
                    </div>
                  );
                })}
            </DropdownMenuContent>
          </DropdownMenu>
          <Button size="sm" variant="outline" onClick={() => onCreateIssue()}>
            <Plus className="size-3.5" aria-hidden="true" />
            {t(($) => $.views.new_issue)}
          </Button>
        </div>
      </div>

      {isHeadLoading ? (
        <div className="flex-1 space-y-2 p-4" aria-busy="true">
          {Array.from({ length: 6 }).map((_, index) => (
            <Skeleton key={index} className="h-8 w-full" />
          ))}
        </div>
      ) : isHeadError ? (
        <div
          role="alert"
          className="flex items-center gap-2 p-4 text-body text-destructive"
        >
          <span>{t(($) => $.table.load_failed)}</span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => void headQuery?.refetch()}
          >
            {t(($) => $.table.load_failed_retry)}
          </Button>
        </div>
      ) : (
        <div className="flex-1 min-h-0 overflow-auto">
          <ApitableGrid
            datasheetId={`issues:${wsId}:${queryIdentity}`}
            fields={visibleFields}
            records={records}
            labels={labels}
            renderCell={renderCell}
            onCellCommit={handleCellCommit}
            onOpenRecord={handleOpenRecord}
            rowSelection={rowSelection}
            stickyHeader
          />

          <div className="flex items-center justify-center px-4 py-3">
            {isTailError ? (
              <Button
                size="sm"
                variant="ghost"
                className="text-destructive"
                onClick={() => void activeTailQuery?.refetch()}
              >
                {t(($) => $.table.load_more_failed_retry)}
              </Button>
            ) : isTailLoading ? (
              <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground">
                <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
                {t(($) => $.table.loading_branch)}
              </span>
            ) : canLoadMore ? (
              <Button size="sm" variant="outline" onClick={handleLoadMore}>
                {t(($) => $.table.load_more)}
              </Button>
            ) : issues.length > 0 && total > GRID_PAGE_LIMIT ? (
              <span className="text-caption text-muted-foreground">
                {t(($) => $.table.no_more)}
              </span>
            ) : null}
          </div>
        </div>
      )}
      {showBatchToolbar && <BatchActionToolbar issues={issues} />}
    </div>
  );
}
