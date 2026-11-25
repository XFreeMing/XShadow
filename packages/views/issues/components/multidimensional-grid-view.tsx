"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { hashKey, useQueries } from "@tanstack/react-query";
import {
  ApitableGrid,
  type GridCellRenderContext,
  type GridCellValue,
  type GridFieldView,
  type GridLabels,
  type GridRecordView,
} from "@multica/apitable-grid";
import { Loader2, Plus } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { issueTableRowPageOptions } from "@multica/core/issues/queries";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  Issue,
  IssueTableQuerySpec,
  UpdateIssueRequest,
} from "@multica/core/types";
import { useT } from "../../i18n";
import { resolveClickIntent, useIntentNavigate } from "../../navigation";
import { runConfirmIntent } from "../actions/run-confirm-gate";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";
import { useIssuePeekClick } from "../surface/peek-context";
import type { IssueCreateDefaults } from "../surface/types";
import { AssigneePicker, StatusPicker } from "./pickers";

const GRID_PAGE_LIMIT = 50;
const INITIAL_CURSORS: Array<string | null> = [null];

type OptionalGridColumn = "status" | "assignee";

export interface MultidimensionalGridViewProps {
  serverQuery: IssueTableQuerySpec;
  onCreateIssue: (defaults?: IssueCreateDefaults) => void;
}

export function MultidimensionalGridView({
  serverQuery,
  onCreateIssue,
}: MultidimensionalGridViewProps) {
  const wsId = useWorkspaceId();
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const intentNavigate = useIntentNavigate();
  const handlePeekClick = useIssuePeekClick();
  const actions = useIssueSurfaceActionsOptional();
  const openModal = useModalStore((s) => s.open);
  const { entryOf } = useIssueStatuses(wsId);

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

  const fields = useMemo<GridFieldView[]>(
    () => [
      { id: "title", name: t(($) => $.table.columns.title), kind: "text", cancelOnEscape: true },
      { id: "status", name: t(($) => $.table.columns.status), kind: "single_select" },
      { id: "assignee", name: t(($) => $.table.columns.assignee), kind: "text" },
    ],
    [t],
  );
  // Local to this grid. The issue title is not optional, and other views keep
  // their own column settings.
  const [optionalColumns, setOptionalColumns] = useState<
    Record<OptionalGridColumn, boolean>
  >({ status: true, assignee: true });
  const setOptionalColumnVisible = useCallback(
    (column: OptionalGridColumn, visible: boolean) => {
      setOptionalColumns((current) =>
        current[column] === visible ? current : { ...current, [column]: visible },
      );
    },
    [],
  );
  const visibleFields = useMemo(
    () =>
      fields.filter((field) => {
        if (field.id === "status") return optionalColumns.status;
        if (field.id === "assignee") return optionalColumns.assignee;
        return true;
      }),
    [fields, optionalColumns],
  );

  const records = useMemo<GridRecordView[]>(
    () =>
      issues.map((issue) => ({
        id: issue.id,
        revision: issue.revision ?? 0,
        values: {
          title: issue.title,
          status: issue.status,
          assignee:
            issue.assignee_type && issue.assignee_id
              ? `${issue.assignee_type}:${issue.assignee_id}`
              : null,
        },
      })),
    [issues],
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
      return undefined;
    },
    [issueById, updateIssue],
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
              render={<Button type="button" size="sm" variant="outline" />}
            >
              {t(($) => $.table.columns.section)}
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuCheckboxItem checked disabled>
                {t(($) => $.table.columns.title)}
              </DropdownMenuCheckboxItem>
              <DropdownMenuCheckboxItem
                checked={optionalColumns.status}
                onCheckedChange={(checked) =>
                  setOptionalColumnVisible("status", checked)
                }
              >
                {t(($) => $.table.columns.status)}
              </DropdownMenuCheckboxItem>
              <DropdownMenuCheckboxItem
                checked={optionalColumns.assignee}
                onCheckedChange={(checked) =>
                  setOptionalColumnVisible("assignee", checked)
                }
              >
                {t(($) => $.table.columns.assignee)}
              </DropdownMenuCheckboxItem>
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
    </div>
  );
}
