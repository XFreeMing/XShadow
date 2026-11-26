"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  catalogGridColumnOrder,
  gridPropertyColumnId,
  isGridColumnKey,
  isGridColumnVisible,
  normalizeGridColumnOrder,
  type GridColumnKey,
} from "@multica/core/issues/grid-columns";
import { useViewStore, useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import { hashKey, useQueries, useQuery } from "@tanstack/react-query";
import {
  ApitableGrid,
  type GridCellRenderContext,
  type GridCellValue,
  type GridFieldView,
  type GridGroup,
  type GridInsertedRow,
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
import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";
import { issueTableRowPageOptions } from "@multica/core/issues/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  Issue,
  IssueProperty,
  IssueTableQuerySpec,
  Project,
  UpdateIssueRequest,
} from "@multica/core/types";
import { isActorPropertyType, isComputedPropertyType } from "@multica/core/types";
import { ComputedIssuePropertyProvider } from "./computed-property-context";
import { useIssueSurfaceSelection } from "../surface/selection-context";
import {
  buildIssueTableCsv,
  getIssueTableSelectionRange,
  IssueTableExportIntegrityError,
} from "./table-view-model";
import { issueGridExportRow } from "./issue-grid-export";
import { issueTableGroupLabel } from "./issue-table-grouping";
import {
  useIssueTableFlatGroups,
  visibleTableGroupIssues,
} from "./use-issue-table-flat-groups";
import { useIssueTableHierarchyRows } from "./use-issue-table-hierarchy-rows";
import type { HierarchyGridFooter } from "./issue-table-server-branches";
import { useLocale, useT } from "../../i18n";
import { resolveClickIntent, useIntentNavigate } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { ProjectPicker } from "../../projects/components/project-picker";
import { runConfirmIntent } from "../actions/run-confirm-gate";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";
import { useIssuePeekClick } from "../surface/peek-context";
import { priorityLabel } from "../utils/priority-label";
import { useStatusLabel } from "../utils/status-label";
import type { IssueCreateDefaults } from "../surface/types";
import { BatchActionToolbar } from "./batch-action-toolbar";
import type { ChildProgress } from "./list-row";
import {
  AssigneePicker,
  DueDatePicker,
  LabelPicker,
  PriorityPicker,
  StartDatePicker,
  StatusPicker,
} from "./pickers";
import { CustomPropertyValueEditor } from "./pickers/custom-property-picker";
import { ProgressRing } from "./progress-ring";
import { TableIssueSearch } from "./table-view";

const GRID_PAGE_LIMIT = 50;
const INITIAL_CURSORS: Array<string | null> = [null];
const PROPERTY_COLUMN_PREFIX = "property:";

function GridLabelCell({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(false);
  const labels = issue.labels ?? [];
  if (open) {
    return (
      <LabelPicker
        issueId={issue.id}
        open
        onOpenChange={(next) => {
          if (!next) setOpen(false);
        }}
        triggerRender={<button type="button" className="flex max-w-full gap-1" />}
      />
    );
  }
  return (
    <button
      type="button"
      className="flex max-w-full items-center gap-1 overflow-hidden rounded-xs px-1 py-0.5 hover:bg-accent"
      onClick={(event) => {
        event.stopPropagation();
        setOpen(true);
      }}
    >
      {labels.length === 0 ? (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.table.empty_value)}
        </span>
      ) : (
        labels.slice(0, 3).map((label) => (
          <span
            key={label.id}
            className="inline-flex max-w-24 items-center gap-1 truncate rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-foreground"
          >
            <span
              className="size-1.5 shrink-0 rounded-full"
              style={{ backgroundColor: label.color }}
            />
            <span className="truncate">{label.name}</span>
          </span>
        ))
      )}
    </button>
  );
}

export interface MultidimensionalGridViewProps {
  serverQuery: IssueTableQuerySpec;
  childProgressMap?: Map<string, ChildProgress>;
  search?: string;
  onSearchChange?: (query: string) => void;
  onCreateIssue: (defaults?: IssueCreateDefaults) => void;
  showBatchToolbar: boolean;
  exportIssues: () => Promise<Issue[]>;
  resolveExportLookups?: (needs: {
    projects: boolean;
    childProgress: boolean;
  }) => Promise<{
    projectMap: Map<string, Project>;
    childProgressMap: Map<string, ChildProgress>;
  }>;
}

export function MultidimensionalGridView({
  serverQuery,
  childProgressMap,
  search = "",
  onSearchChange,
  onCreateIssue,
  showBatchToolbar,
  exportIssues,
  resolveExportLookups,
}: MultidimensionalGridViewProps) {
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const { t } = useT("issues");
  const locale = useLocale();
  const { getActorName } = useActorName();
  const paths = useWorkspacePaths();
  const intentNavigate = useIntentNavigate();
  const handlePeekClick = useIssuePeekClick();
  const actions = useIssueSurfaceActionsOptional();
  const openModal = useModalStore((s) => s.open);
  const { entryOf } = useIssueStatuses(wsId);
  const resolveStatusLabel = useStatusLabel(wsId);
  const resolvePriorityLabel = useCallback(
    (priority: string) => priorityLabel(priority, t),
    [t],
  );
  const storeApi = useViewStoreApi();
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

  const tableGrouping = useViewStore((state) => state.tableGrouping);
  const setTableGrouping = useViewStore((state) => state.setTableGrouping);
  const tableCollapsedGroups = useViewStore((state) => state.tableCollapsedGroups);
  const toggleTableGroupCollapsed = useViewStore(
    (state) => state.toggleTableGroupCollapsed,
  );
  const tableHierarchy = useViewStore((state) => state.tableHierarchy);
  const tableCollapsedParents = useViewStore(
    (state) => state.tableCollapsedParents,
  );
  const toggleTableParentCollapsed = useViewStore(
    (state) => state.toggleTableParentCollapsed,
  );
  const groupablePropertyIds = useMemo(
    () =>
      new Set(
        workspaceProperties
          .filter((property) =>
            ["select", "checkbox"].includes(property.type),
          )
          .map((property) => property.id),
      ),
    [workspaceProperties],
  );
  const groupingPropertyId = propertyIdFromViewKey(tableGrouping);
  const effectiveTableGrouping =
    groupingPropertyId && !groupablePropertyIds.has(groupingPropertyId)
      ? "none"
      : tableGrouping;
  useEffect(() => {
    if (
      !groupingPropertyId ||
      !propertiesQuery.isSuccess ||
      groupablePropertyIds.has(groupingPropertyId)
    ) {
      return;
    }
    setTableGrouping("none");
    toast.info(t(($) => $.table.group_property_unavailable));
  }, [
    groupingPropertyId,
    groupablePropertyIds,
    propertiesQuery.isSuccess,
    setTableGrouping,
    t,
  ]);

  const usesServerGrouping = effectiveTableGrouping !== "none";
  const effectiveHierarchy = tableHierarchy;
  const groupBranches = useIssueTableFlatGroups({
    wsId,
    serverQuery,
    tableGrouping: effectiveTableGrouping,
    collapsedGroupKeys: tableCollapsedGroups,
    rowsEnabled: usesServerGrouping && !tableHierarchy,
  });
  const groupKeys = useMemo(
    () => groupBranches.descriptors.map((descriptor) => descriptor.key),
    [groupBranches.descriptors],
  );
  const collapsedGroupSet = useMemo(
    () => new Set(tableCollapsedGroups),
    [tableCollapsedGroups],
  );
  const groupProjectsQuery = useQuery({
    ...projectListOptions(wsId),
    enabled: usesServerGrouping && effectiveTableGrouping === "project",
  });
  const groupProjectMap = useMemo(
    () =>
      new Map(
        (groupProjectsQuery.data ?? []).map((project) => [project.id, project]),
      ),
    [groupProjectsQuery.data],
  );

  const hierarchyState = useIssueTableHierarchyRows({
    wsId,
    serverQuery,
    serverGroupSpec: groupBranches.serverGroupSpec,
    tableHierarchy: true,
    usesServerGrouping,
    groupKeys,
    collapsedGroupKeys: tableCollapsedGroups,
    collapsedParentKeys: tableCollapsedParents,
    enabled: effectiveHierarchy,
  });

  const pageQueries = useQueries({
    queries: effectiveHierarchy || usesServerGrouping
      ? []
      : cursors.map((cursor) => ({
          ...issueTableRowPageOptions(wsId, {
            query: serverQuery,
            group: { kind: "none" },
            group_key: null,
            hierarchy: { enabled: false },
            parent_id: null,
            page: { limit: GRID_PAGE_LIMIT, cursor },
          }),
          placeholderData: undefined,
        })),
  });

  const flatPageState = useMemo(() => {
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

  const issues = effectiveHierarchy
    ? hierarchyState.issues
    : usesServerGrouping
      ? visibleTableGroupIssues(groupBranches, collapsedGroupSet)
      : flatPageState.issues;
  const total = effectiveHierarchy
    ? usesServerGrouping
      ? groupBranches.total
      : hierarchyState.total
    : usesServerGrouping
      ? groupBranches.total
      : flatPageState.total;
  const nextCursor = flatPageState.nextCursor;
  const validPrefixLength = flatPageState.validPrefixLength;
  const activeTailQuery = flatPageState.activeTailQuery;

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
  const isHeadLoading = effectiveHierarchy
    ? usesServerGrouping
      ? groupBranches.enabled &&
        groupBranches.isLoading &&
        groupBranches.descriptors.length === 0
      : hierarchyState.isHeadLoading
    : usesServerGrouping
      ? groupBranches.enabled &&
        groupBranches.isLoading &&
        groupBranches.descriptors.length === 0
      : Boolean(headQuery && headQuery.isPending && !headQuery.data);
  const isHeadError = effectiveHierarchy
    ? usesServerGrouping
      ? groupBranches.isError && groupBranches.descriptors.length === 0
      : hierarchyState.isHeadError
    : usesServerGrouping
      ? groupBranches.isError && groupBranches.descriptors.length === 0
      : Boolean(headQuery && headQuery.isError && !headQuery.data);
  const isTailLoading = Boolean(
    activeTailQuery &&
      (activeTailQuery.isPending || activeTailQuery.isFetching) &&
      !activeTailQuery.data,
  );
  const isTailError = Boolean(
    activeTailQuery && activeTailQuery.isError && !activeTailQuery.data,
  );
  const validCursors = cursors.slice(0, validPrefixLength);
  const canLoadMore = !usesServerGrouping && !tableHierarchy && Boolean(
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

  const viewMode = useViewStore((state) => state.viewMode);
  const tableColumns = useViewStore((state) => state.tableColumns);
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
    if (!gridCatalogReady || viewMode === "table") return;
    syncGridColumnCatalog(catalogOrder);
  }, [catalogOrder, gridCatalogReady, syncGridColumnCatalog, viewMode]);

  const effectiveGridColumnOrder = useMemo(
    () =>
      gridCatalogReady
        ? normalizeGridColumnOrder(catalogOrder, gridColumnOrder)
        : gridColumnOrder,
    [catalogOrder, gridCatalogReady, gridColumnOrder],
  );

  const hiddenColumnIds = useMemo(() => {
    const hidden = new Set<GridColumnKey>(gridHiddenColumnIds);
    if (viewMode === "table") {
      const legacyVisibleKeys = new Set<string>(
        tableColumns.map((column) => column.key),
      );
      for (const columnId of effectiveGridColumnOrder) {
        if (
          columnId.startsWith(PROPERTY_COLUMN_PREFIX) &&
          !legacyVisibleKeys.has(columnId)
        ) {
          hidden.add(columnId);
        }
      }
    }
    return hidden;
  }, [effectiveGridColumnOrder, gridHiddenColumnIds, tableColumns, viewMode]);

  const ensureGridColumnStateSynced = useCallback(() => {
    if (storeApi.getState().viewMode !== "table") return;
    storeApi.setState({
      viewMode: "grid",
      gridColumnOrder: effectiveGridColumnOrder,
      gridHiddenColumnIds: [...hiddenColumnIds],
    });
  }, [effectiveGridColumnOrder, hiddenColumnIds, storeApi]);

  const handleToggleGridColumnHidden = useCallback(
    (columnKey: GridColumnKey, hidden: boolean) => {
      ensureGridColumnStateSynced();
      setGridColumnHidden(columnKey, hidden);
    },
    [ensureGridColumnStateSynced, setGridColumnHidden],
  );

  const handleMoveGridColumn = useCallback(
    (columnKey: GridColumnKey, direction: -1 | 1) => {
      ensureGridColumnStateSynced();
      moveGridColumn(columnKey, direction);
    },
    [ensureGridColumnStateSynced, moveGridColumn],
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
    byId.set("priority", {
      id: "priority",
      name: t(($) => $.table.columns.priority),
      kind: "single_select",
    });
    byId.set("start_date", {
      id: "start_date",
      name: t(($) => $.table.columns.start_date),
      kind: "text",
    });
    byId.set("due_date", {
      id: "due_date",
      name: t(($) => $.table.columns.due_date),
      kind: "text",
    });
    byId.set("labels", {
      id: "labels",
      name: t(($) => $.table.columns.labels),
      kind: "text",
    });
    byId.set("identifier", {
      id: "identifier",
      name: t(($) => $.table.columns.identifier),
      kind: "text",
    });
    byId.set("project", {
      id: "project",
      name: t(($) => $.table.columns.project),
      kind: "text",
    });
    byId.set("child_progress", {
      id: "child_progress",
      name: t(($) => $.table.columns.child_progress),
      kind: "text",
    });
    byId.set("creator", {
      id: "creator",
      name: t(($) => $.table.columns.creator),
      kind: "text",
    });
    byId.set("created_at", {
      id: "created_at",
      name: t(($) => $.table.columns.created_at),
      kind: "text",
    });
    byId.set("updated_at", {
      id: "updated_at",
      name: t(($) => $.table.columns.updated_at),
      kind: "text",
    });
    for (const prop of workspaceProperties) {
      byId.set(gridPropertyColumnId(prop.id), {
        id: gridPropertyColumnId(prop.id),
        name: prop.name,
        kind: "text",
      });
    }
    for (const { key, width } of tableColumns) {
      const field = byId.get(key);
      if (field && width !== undefined && Number.isFinite(width) && width > 0) {
        field.width = width;
      }
    }
    return byId;
  }, [t, tableColumns, workspaceIssueTypes.length, workspaceProperties]);

  const menuFields = useMemo(
    () =>
      effectiveGridColumnOrder
        .map((columnId) => fieldsById.get(columnId))
        .filter((field): field is GridFieldView => Boolean(field)),
    [effectiveGridColumnOrder, fieldsById],
  );

  const visibleFields = useMemo(
    () =>
      effectiveGridColumnOrder
        .filter((columnId) => isGridColumnVisible(columnId, hiddenColumnIds))
        .map((columnId) => fieldsById.get(columnId))
        .filter((field): field is GridFieldView => Boolean(field)),
    [effectiveGridColumnOrder, fieldsById, hiddenColumnIds],
  );

  const exportColumnKeys = useMemo(
    () =>
      visibleFields
        .map((field) => field.id)
        .filter((id): id is GridColumnKey => isGridColumnKey(id)),
    [visibleFields],
  );

  const recordIds = useMemo(() => issues.map((issue) => issue.id), [issues]);

  useEffect(() => {
    const visibleIds = new Set(recordIds);
    const stale = [...selection.selectedIds].filter((id) => !visibleIds.has(id));
    if (stale.length > 0) selection.deselect(stale);
  }, [recordIds, selection]);

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
          exportColumnKeysResolved.includes("creator") ||
          exportColumnKeysResolved.some((key) => {
            if (!key.startsWith(PROPERTY_COLUMN_PREFIX)) return false;
            const property = exportPropertyById.get(
              key.slice(PROPERTY_COLUMN_PREFIX.length),
            );
            return property ? isActorPropertyType(property.type) : false;
          });
        const needsProjects = exportColumnKeysResolved.includes("project");
        const needsChildProgress =
          exportColumnKeysResolved.includes("child_progress");
        const exportLookups =
          (needsProjects || needsChildProgress) && resolveExportLookups
            ? await resolveExportLookups({
                projects: needsProjects,
                childProgress: needsChildProgress,
              })
            : {
                projectMap: groupProjectMap,
                childProgressMap,
              };
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
            resolvePriorityLabel,
            issueTypeName: issue.issue_type_id
              ? issueTypeById.get(issue.issue_type_id)?.name ?? null
              : null,
            propertyById: exportPropertyById,
            getActorName: exportActorName,
            projectMap: exportLookups.projectMap,
            childProgressMap: exportLookups.childProgressMap,
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
      groupProjectMap,
      childProgressMap,
      issueTypeById,
      queryClient,
      resolveExportLookups,
      resolvePriorityLabel,
      resolveStatusLabel,
      selectedIssues,
      t,
      wsId,
      gridCatalogReady,
      workspaceProperties,
    ],
  );

  const buildIssueGridRecord = useCallback(
    (
      issue: Issue,
      hierarchy?: GridRecordView["hierarchy"],
    ): GridRecordView => {
      const typeName = issue.issue_type_id
        ? issueTypeById.get(issue.issue_type_id)?.name ?? null
        : null;
      const progress = childProgressMap?.get(issue.id);
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
        hierarchy,
        values: {
          title: issue.title,
          identifier: issue.identifier,
          ...(workspaceIssueTypes.length > 0 ? { type: typeName } : {}),
          status: issue.status,
          priority: issue.priority,
          assignee:
            issue.assignee_type && issue.assignee_id
              ? `${issue.assignee_type}:${issue.assignee_id}`
              : null,
          labels:
            (issue.labels ?? []).map((label) => label.name).join(", ") || null,
          project: issue.project_id,
          start_date: issue.start_date,
          due_date: issue.due_date,
          created_at: issue.created_at,
          updated_at: issue.updated_at,
          child_progress: progress ? `${progress.done}/${progress.total}` : null,
          creator: `${issue.creator_type}:${issue.creator_id}`,
          ...propValues,
        },
      };
    },
    [childProgressMap, issueTypeById, workspaceIssueTypes.length, workspaceProperties],
  );

  const hierarchyToggleLabel = t(($) => $.table.toggle_sub_issues);

  const renderHierarchyFooter = useCallback(
    (item: HierarchyGridFooter) => (
      <div className="flex items-center gap-2 pl-6">
        {item.state === "loading" ? (
          <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground">
            <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
            {t(($) => $.table.loading_branch)}
          </span>
        ) : item.state === "error" ? (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="text-destructive"
            onClick={() => item.onAction?.()}
          >
            {t(($) => $.table.load_more_failed_retry)}
          </Button>
        ) : (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => item.onAction?.()}
          >
            {t(($) => $.table.load_more)}
          </Button>
        )}
      </div>
    ),
    [t],
  );

  const { records, insertedRows } = useMemo(() => {
    if (!effectiveHierarchy) {
      return {
        records: issues.map((issue) => buildIssueGridRecord(issue)),
        insertedRows: [] as GridInsertedRow[],
      };
    }
    if (usesServerGrouping) {
      return {
        records: [] as GridRecordView[],
        insertedRows: hierarchyState.gridItems.flatMap((item) =>
          item.kind === "footer"
            ? [
                {
                  key: item.key,
                  groupKey: item.groupKey,
                  afterRecordId: item.insertAfterIssueId,
                  content: renderHierarchyFooter(item),
                },
              ]
            : [],
        ),
      };
    }
    const ordered: GridRecordView[] = [];
    const branchRows: GridInsertedRow[] = [];
    for (const item of hierarchyState.gridItems) {
      if (item.kind === "issue") {
        ordered.push(
          buildIssueGridRecord(item.issue, {
            depth: item.depth,
            hasChildren: item.hasChildren,
            collapsed: item.collapsed,
            toggleLabel: hierarchyToggleLabel,
            onToggle: () => toggleTableParentCollapsed(item.issue.id),
          }),
        );
        continue;
      }
      branchRows.push({
        key: item.key,
        groupKey: item.groupKey,
        afterRecordId: item.insertAfterIssueId,
        content: renderHierarchyFooter(item),
      });
    }
    return { records: ordered, insertedRows: branchRows };
  }, [
    buildIssueGridRecord,
    effectiveHierarchy,
    hierarchyState.gridItems,
    hierarchyToggleLabel,
    issues,
    renderHierarchyFooter,
    toggleTableParentCollapsed,
    usesServerGrouping,
  ]);

  const recordById = useMemo(
    () => new Map(records.map((record) => [record.id, record])),
    [records],
  );

  const groupLabelContext = useMemo(
    () => ({
      resolveStatusLabel,
      getActorName,
      groupProjectMap,
      propertyById,
      translate: {
        unassigned: t(($) => $.table.unassigned),
        noProject: t(($) => $.swimlane.no_project),
        noParent: t(($) => $.swimlane.no_parent),
        noValue: t(($) => $.table.no_value),
        valueUnavailable: t(($) => $.table.value_unavailable),
        customPropertyTrue: t(($) => $.pickers.custom_property.true_label),
        customPropertyFalse: t(($) => $.pickers.custom_property.false_label),
      },
    }),
    [getActorName, groupProjectMap, propertyById, resolveStatusLabel, t],
  );

  const gridGroups = useMemo<GridGroup[] | null>(() => {
    if (!usesServerGrouping || groupBranches.descriptors.length === 0) {
      return null;
    }
    return groupBranches.descriptors.map((descriptor) => {
      if (effectiveHierarchy) {
        const groupRecords = hierarchyState.gridItems.flatMap((item) =>
          item.kind === "issue" && item.groupKey === descriptor.key
            ? [
                buildIssueGridRecord(item.issue, {
                  depth: item.depth,
                  hasChildren: item.hasChildren,
                  collapsed: item.collapsed,
                  toggleLabel: hierarchyToggleLabel,
                  onToggle: () => toggleTableParentCollapsed(item.issue.id),
                }),
              ]
            : [],
        );
        return {
          key: descriptor.key,
          label: issueTableGroupLabel(descriptor, groupLabelContext),
          count: descriptor.count,
          collapsed: collapsedGroupSet.has(descriptor.key),
          records: groupRecords,
        };
      }
      const branchIssues = groupBranches.issuesByKey[descriptor.key] ?? [];
      const page = groupBranches.pagination[descriptor.key];
      return {
        key: descriptor.key,
        label: issueTableGroupLabel(descriptor, groupLabelContext),
        count: descriptor.count,
        collapsed: collapsedGroupSet.has(descriptor.key),
        records: branchIssues
          .map((issue) => recordById.get(issue.id))
          .filter((record): record is GridRecordView => Boolean(record)),
        branchHasMore: page?.hasMore,
        branchLoading: page?.isLoading || page?.isFetching,
        branchError: page?.isError,
        onLoadMore: page?.isError ? page.retry : page?.loadMore,
      };
    });
  }, [
    buildIssueGridRecord,
    collapsedGroupSet,
    effectiveHierarchy,
    groupBranches.descriptors,
    groupBranches.issuesByKey,
    groupBranches.pagination,
    groupLabelContext,
    hierarchyState.gridItems,
    hierarchyToggleLabel,
    recordById,
    toggleTableParentCollapsed,
    usesServerGrouping,
  ]);

  const labels = useMemo<GridLabels>(
    () => ({
      rename: t(($) => $.table.rename_title),
      clear: t(($) => $.table.search_clear),
      emptyRecords: t(($) => $.table.empty),
      emptyValue: t(($) => $.table.empty_value),
      checked: "true",
      unchecked: "false",
      openRecord: t(($) => $.member_answer.open_issue),
      loadMore: t(($) => $.table.load_more),
      retryBranch: t(($) => $.table.load_more_failed_retry),
      loadingBranch: t(($) => $.table.loading_branch),
    }),
    [t],
  );

  const renderCell = useCallback(
    ({ record, field, onActivate }: GridCellRenderContext) => {
      const issue = issueById.get(record.id);
      if (!issue) return undefined;
      if (field.id === "identifier") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-2 text-caption text-muted-foreground tabular-nums"
            onFocus={onActivate}
          >
            <span className="truncate">{issue.identifier}</span>
          </div>
        );
      }
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
      if (field.id === "priority") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-1.5"
            onFocus={onActivate}
          >
            <PriorityPicker
              priority={issue.priority}
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
      if (field.id === "labels") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-1.5"
            onFocus={onActivate}
          >
            <GridLabelCell issue={issue} />
          </div>
        );
      }
      if (field.id === "project") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-1.5"
            onFocus={onActivate}
          >
            <ProjectPicker
              projectId={issue.project_id}
              onUpdate={(updates) => updateIssue(issue, updates)}
              triggerRender={
                <button
                  type="button"
                  className="flex max-w-full items-center gap-1.5 rounded-xs px-1 py-0.5 hover:bg-accent"
                />
              }
            />
          </div>
        );
      }
      if (field.id === "start_date") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-1.5"
            onFocus={onActivate}
          >
            <StartDatePicker
              startDate={issue.start_date}
              onUpdate={(updates) => updateIssue(issue, updates)}
            />
          </div>
        );
      }
      if (field.id === "due_date") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-1.5"
            onFocus={onActivate}
          >
            <DueDatePicker
              dueDate={issue.due_date}
              onUpdate={(updates) => updateIssue(issue, updates)}
            />
          </div>
        );
      }
      if (field.id === "created_at" || field.id === "updated_at") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-2 text-caption text-muted-foreground"
            onFocus={onActivate}
          >
            <span className="truncate">
              {new Intl.DateTimeFormat(locale, {
                month: "short",
                day: "numeric",
                year: "numeric",
              }).format(new Date(issue[field.id]))}
            </span>
          </div>
        );
      }
      if (field.id === "child_progress") {
        const progress = childProgressMap?.get(issue.id);
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-2 text-caption text-muted-foreground"
            onFocus={onActivate}
          >
            {progress ? (
              <span className="inline-flex items-center gap-1.5">
                <ProgressRing done={progress.done} total={progress.total} size={15} />
                {progress.done}/{progress.total}
              </span>
            ) : (
              <span>{t(($) => $.table.empty_value)}</span>
            )}
          </div>
        );
      }
      if (field.id === "creator") {
        return (
          <div
            className="flex h-full w-full min-w-0 items-center px-2 text-caption"
            onFocus={onActivate}
          >
            <span className="flex min-w-0 items-center gap-1.5">
              <ActorAvatar
                actorType={issue.creator_type}
                actorId={issue.creator_id}
                size="sm"
              />
              <span className="truncate">
                {getActorName(issue.creator_type, issue.creator_id)}
              </span>
            </span>
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
    [childProgressMap, getActorName, issueById, issueTypeById, locale, propertyById, t, updateIssue],
  );

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <div className="flex items-center justify-between gap-2 border-b border-border/60 px-4 py-2">
        <div className="flex items-center gap-3">
          {onSearchChange && (
            <TableIssueSearch
              value={search}
              onChange={onSearchChange}
              placeholder={t(($) => $.table.search_placeholder)}
              clearLabel={t(($) => $.table.search_clear)}
            />
          )}
          <span className="text-caption text-muted-foreground tabular-nums">
            {issues.length}
            {total > issues.length ? ` / ${total}` : ""}
          </span>
        </div>
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
                  const position = effectiveGridColumnOrder.indexOf(columnKey);
                  const moveUpDisabled = position <= 1;
                  const moveDownDisabled =
                    position < 0 || position >= effectiveGridColumnOrder.length - 1;
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
                          handleToggleGridColumnHidden(columnKey, !checked)
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
                        onClick={() => handleMoveGridColumn(columnKey, -1)}
                      >
                        <ArrowUp className="size-3.5" aria-hidden="true" />
                      </IconButton>
                      <IconButton
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        label={t(($) => $.table.columns.move_down, { column: field.name })}
                        disabled={moveDownDisabled}
                        onClick={() => handleMoveGridColumn(columnKey, 1)}
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
            onClick={() =>
              void (
                effectiveHierarchy
                  ? hierarchyState.retryRoot()
                  : usesServerGrouping
                    ? groupBranches.retryGroups()
                    : headQuery?.refetch()
              )
            }
          >
            {t(($) => $.table.load_failed_retry)}
          </Button>
        </div>
      ) : (
        <div className="flex-1 min-h-0 overflow-auto">
          <ComputedIssuePropertyProvider
            wsId={wsId}
            issueIds={workspaceProperties.some((property) => isComputedPropertyType(property.type)) ? recordIds : []}
          >
          <ApitableGrid
            datasheetId={`issues:${wsId}:${queryIdentity}:${effectiveTableGrouping}:${effectiveHierarchy ? "hierarchy" : "flat"}`}
            fields={visibleFields}
            records={records}
            groups={usesServerGrouping ? gridGroups : null}
            insertedRows={effectiveHierarchy ? insertedRows : undefined}
            labels={labels}
            renderCell={renderCell}
            onCellCommit={handleCellCommit}
            onOpenRecord={handleOpenRecord}
            onToggleGroup={toggleTableGroupCollapsed}
            rowSelection={rowSelection}
            stickyHeader
          />
          </ComputedIssuePropertyProvider>

          <div className="flex items-center justify-center px-4 py-3">
            {usesServerGrouping && groupBranches.hasMoreGroups ? (
              <Button
                size="sm"
                variant="outline"
                disabled={groupBranches.isLoadingMoreGroups}
                onClick={() => groupBranches.loadMoreGroups()}
              >
                {groupBranches.isLoadingMoreGroups ? (
                  <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
                ) : null}
                {t(($) => $.table.load_more)}
              </Button>
            ) : isTailError ? (
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
