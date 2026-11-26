"use client";

import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Provider, useDispatch } from "react-redux";
import { GRID_STYLE_ID, gridCss } from "./grid-css";
import { GridCell } from "./cell";
import { createGridStore, hostSnapshot, setPageDatasheet } from "./store";
import type { GridActor, GridCellRenderer, GridCellValue, GridCollaborator, GridConflictMark, GridFieldView, GridGroup, GridInsertedRow, GridLabels, GridRecordOption, GridRecordView, GridRemotePatch, GridRowSelection } from "./types";
import type { GridSnapshot } from "../third-party/state";

const NO_CONFLICTS: readonly GridConflictMark[] = [];

function mirror(fields: readonly GridFieldView[], records: readonly GridRecordView[], conflicts: readonly GridConflictMark[]): GridSnapshot {
  const recordMap: GridSnapshot["records"] = {};
  for (const record of records) {
    const values: GridSnapshot["records"][string]["values"] = {};
    for (const [fieldId, value] of Object.entries(record.values)) {
      if (value !== undefined) values[fieldId] = value;
    }
    recordMap[record.id] = {
      id: record.id,
      revision: record.revision,
      values,
      errors: record.errors,
    };
  }
  const conflictMap: Record<string, string> = {};
  for (const conflict of conflicts) {
    conflictMap[`${conflict.recordId}:${conflict.fieldId}`] = conflict.message;
  }
  return {
    visibleFieldIds: fields.map((field) => field.id),
    visibleRecordIds: records.map((record) => record.id),
    records: recordMap,
    conflicts: conflictMap,
  };
}

function Hydrator({
  datasheetId,
  snapshot,
}: {
  datasheetId: string;
  snapshot: GridSnapshot;
}) {
  const dispatch = useDispatch();
  useLayoutEffect(() => {
    dispatch(setPageDatasheet(datasheetId));
    dispatch(hostSnapshot(datasheetId, snapshot));
  }, [datasheetId, dispatch, snapshot]);
  return null;
}

function GridSelectAllHeader({ rowSelection }: { rowSelection: GridRowSelection }) {
  const ref = useRef<HTMLInputElement>(null);
  const selectedCount = rowSelection.recordIds.filter((id) =>
    rowSelection.selectedRecordIds.has(id),
  ).length;
  const checked =
    rowSelection.recordIds.length > 0 &&
    selectedCount === rowSelection.recordIds.length;
  useEffect(() => {
    if (ref.current) {
      ref.current.indeterminate = selectedCount > 0 && !checked;
    }
  }, [checked, selectedCount]);
  return (
    <input
      ref={ref}
      type="checkbox"
      className="apitable-grid__rowSelect"
      aria-label={rowSelection.selectAllLabel}
      checked={checked}
      onChange={() => rowSelection.onToggleAll(!checked)}
    />
  );
}

function GridRowCheckbox({
  checked,
  label,
  onToggle,
}: {
  checked: boolean;
  label: string;
  onToggle: (shiftKey: boolean) => void;
}) {
  return (
    <input
      type="checkbox"
      className="apitable-grid__rowSelect"
      aria-label={label}
      checked={checked}
      onClick={(event) => {
        event.stopPropagation();
        onToggle(event.shiftKey);
      }}
      onChange={() => undefined}
    />
  );
}

function typeMark(kind: string): string {
  if (kind === "number") return "#";
  if (kind === "date") return "D";
  if (kind === "single_select") return "S";
  if (kind === "checkbox") return "C";
  if (kind === "relation") return "R";
  if (kind === "lookup") return "L";
  if (kind === "formula") return "fx";
  return "A";
}

export function ApitableGrid({
  datasheetId,
  fields,
  records,
  groups,
  actors,
  collaborators,
  recordOptionsByTable,
  remote,
  labels,
  renderCell,
  onCellCommit,
  onRenameField,
  onEditField,
  onOpenRelationCell,
  onOpenRecord,
  onActiveCell,
  onEditEnd,
  onToggleGroup,
  insertedRows,
  stickyHeader,
  rowSelection,
}: {
  datasheetId: string;
  fields: readonly GridFieldView[];
  records: readonly GridRecordView[];
  groups?: readonly GridGroup[] | null;
  actors?: readonly GridActor[];
  collaborators?: readonly GridCollaborator[];
  recordOptionsByTable?: Readonly<Record<string, readonly GridRecordOption[]>>;
  remote?: GridRemotePatch;
  labels: GridLabels;
  renderCell?: GridCellRenderer;
  onCellCommit: (recordId: string, fieldId: string, value: GridCellValue, baseRevision: number) => void;
  onRenameField?: (fieldId: string, name: string) => void;
  onEditField?: (field: GridFieldView) => void;
  onOpenRelationCell?: (
    recordId: string,
    field: GridFieldView,
    currentIds: readonly string[],
    baseRevision: number,
  ) => void;
  onOpenRecord?: (recordId: string) => void;
  onActiveCell?: (recordId: string, fieldId: string, revision: number) => void;
  onEditEnd?: () => void;
  onToggleGroup?: (key: string) => void;
  insertedRows?: readonly GridInsertedRow[];
  stickyHeader?: boolean;
  rowSelection?: GridRowSelection;
}) {
  const [store] = useState(createGridStore);
  const shownRecords = remote?.records ?? records;
  const conflicts = remote?.conflicts ?? NO_CONFLICTS;
  const snapshot = useMemo(
    () => mirror(fields, shownRecords, conflicts),
    [fields, shownRecords, conflicts],
  );
  const visibleRecords = groups
    ? groups.flatMap((group) => group.collapsed ? [] : group.records)
    : shownRecords;
  const recordIds = visibleRecords.map((record) => record.id);
  const collaboratorAt = (recordId: string, fieldId: string) => {
    const online = collaborators?.find((item) => item.kind === "online" && item.recordId === recordId && item.fieldId === fieldId);
    if (online) return online;
    return collaborators?.find((item) => item.kind === "recent" && item.recordId === recordId && item.fieldId === fieldId) ?? null;
  };
  const insertedAfter = useMemo(() => {
    const map = new Map<string | null, Map<string | null, GridInsertedRow[]>>();
    for (const row of insertedRows ?? []) {
      const groupKey = row.groupKey ?? null;
      const group = map.get(groupKey) ?? new Map<string | null, GridInsertedRow[]>();
      const bucket = group.get(row.afterRecordId) ?? [];
      bucket.push(row);
      group.set(row.afterRecordId, bucket);
      map.set(groupKey, group);
    }
    return map;
  }, [insertedRows]);

  const renderInserted = (row: GridInsertedRow) => (
    <tr className="apitable-grid__branch" data-insert-key={row.key} key={row.key}>
      <td colSpan={fields.length + 1}>{row.content}</td>
    </tr>
  );

  const renderRecord = (record: GridRecordView, rowIndex: number) => (
    <tr className="apitable-grid__row" data-record-id={record.id} key={record.id}>
      <th className="apitable-grid__rowHead" scope="row">
        <div
          className="apitable-grid__rowHeadInner"
          style={
            record.hierarchy
              ? { paddingLeft: record.hierarchy.depth * 12 + 4 }
              : undefined
          }
        >
          {record.hierarchy?.hasChildren ? (
            <button
              type="button"
              className="apitable-grid__hierarchyToggle"
              aria-expanded={!record.hierarchy.collapsed}
              aria-label={record.hierarchy.toggleLabel}
              onClick={(event) => {
                event.stopPropagation();
                record.hierarchy?.onToggle();
              }}
            >
              {record.hierarchy.collapsed ? "+" : "–"}
            </button>
          ) : record.hierarchy ? (
            <span className="apitable-grid__hierarchySpacer" aria-hidden="true" />
          ) : null}
          {rowSelection ? (
            <GridRowCheckbox
              checked={rowSelection.selectedRecordIds.has(record.id)}
              label={rowSelection.selectRecordLabel(record.id)}
              onToggle={(shiftKey) => rowSelection.onToggleRecord(record.id, shiftKey)}
            />
          ) : null}
          {onOpenRecord ? (
            <button
              type="button"
              className="apitable-grid__rowOpen"
              aria-label={`${labels.openRecord ?? "Open record"} ${rowIndex + 1}`}
              title={labels.openRecord ?? "Open record"}
              onClick={() => onOpenRecord(record.id)}
            >
              {rowIndex + 1}
            </button>
          ) : (
            <span className="apitable-grid__rowIndex">{rowIndex + 1}</span>
          )}
        </div>
      </th>
      {fields.map((field) => {
        const optionTableId = field.kind === "relation"
          ? field.config?.target_table_id
          : field.kind === "lookup"
            ? field.config?.target_relation_table_id
            : undefined;
        return (
          <GridCell
            key={field.id}
            datasheetId={datasheetId}
            field={field}
            record={record}
            recordId={record.id}
            rowIndex={rowIndex}
            recordIds={recordIds}
            labels={labels}
            collaborator={collaboratorAt(record.id, field.id)}
            recordOptions={optionTableId ? recordOptionsByTable?.[optionTableId] : undefined}
            renderCell={renderCell}
            onOpenRelationCell={onOpenRelationCell}
            onActive={onActiveCell}
            onEditEnd={onEditEnd}
            onCommit={(value, baseRevision) => onCellCommit(record.id, field.id, value, baseRevision)}
          />
        );
      })}
    </tr>
  );

  const renderRows = (records: readonly GridRecordView[], start: number, groupKey: string | null) => {
    const rows: ReactNode[] = [];
    const inserted = insertedAfter.get(groupKey);
    const pushInserted = (afterId: string | null) => {
      for (const row of inserted?.get(afterId) ?? []) rows.push(renderInserted(row));
    };
    pushInserted(null);
    for (const [index, record] of records.entries()) {
      rows.push(renderRecord(record, start + index));
      pushInserted(record.id);
    }
    return rows;
  };

  useLayoutEffect(() => {
    let style = document.getElementById(GRID_STYLE_ID);
    if (!style) {
      style = document.createElement("style");
      style.id = GRID_STYLE_ID;
      document.head.appendChild(style);
    }
    style.textContent = gridCss;
  }, []);

  return (
    <Provider store={store}>
      <Hydrator datasheetId={datasheetId} snapshot={snapshot} />
      {actors && actors.length > 0 ? (
        <div className="apitable-grid__actors" aria-label={labels.online ?? "online"}>
          {actors.map((actor) => (
            <span className="apitable-grid__actor" data-actor={actor.label} key={actor.id} style={{ color: actor.color, borderColor: actor.color }}>
              {actor.label}
            </span>
          ))}
        </div>
      ) : null}
      <table
        className={
          stickyHeader
            ? rowSelection
              ? "apitable-grid apitable-grid--sticky-head apitable-grid--row-selection"
              : "apitable-grid apitable-grid--sticky-head"
            : rowSelection
              ? "apitable-grid apitable-grid--row-selection"
              : "apitable-grid"
        }
        data-apitable-grid=""
      >
        <thead>
        <tr className="apitable-grid__row">
          <th className="apitable-grid__rowHead">
            {rowSelection ? <GridSelectAllHeader rowSelection={rowSelection} /> : null}
          </th>
          {fields.map((field) => (
            <th className="apitable-grid__fieldHeader" key={field.id}>
              <span className="apitable-grid__type" aria-hidden="true">{typeMark(field.kind)}</span>
              {onRenameField ? (
                <input
                  className="apitable-grid__fieldName"
                  aria-label={labels.rename}
                  defaultValue={field.name}
                  key={field.name}
                  onBlur={(event) => {
                    const name = event.target.value.trim();
                    if (name && name !== field.name) onRenameField(field.id, name);
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") event.currentTarget.blur();
                  }}
                />
              ) : (
                <span className="apitable-grid__fieldName">{field.name}</span>
              )}
              {(field.kind === "lookup" || field.kind === "formula") && onEditField ? (
                <button
                  type="button"
                  className="apitable-grid__fieldAction"
                  aria-label={`${labels.configureField ?? "Configure"} ${field.name}`}
                  title={labels.configureField ?? "Configure"}
                  onClick={() => onEditField(field)}
                >
                  {labels.configureField ?? "Edit"}
                </button>
              ) : null}
            </th>
          ))}
        </tr>
        </thead>
        <tbody>
        {visibleRecords.length === 0 && !groups?.length ? (
          <tr className="apitable-grid__empty"><td>{labels.emptyRecords}</td></tr>
        ) : groups ? groups.flatMap((group) => {
          const header = (
            <tr className="apitable-grid__group" data-group={group.key} key={`group:${group.key}`}>
              <td colSpan={fields.length + 1}>
                <button type="button" aria-expanded={!group.collapsed} onClick={() => onToggleGroup?.(group.key)}>
                  {group.collapsed ? "+" : "–"} {group.label} ({group.count})
                </button>
              </td>
            </tr>
          );
          if (group.collapsed) return [header];
          const start = visibleRecords.findIndex((record) => record.id === group.records[0]?.id);
          const rows = renderRows(group.records, start, group.key);
          const branchTail =
            group.branchError || (group.branchHasMore && group.records.length > 0)
              ? [
                  <tr className="apitable-grid__groupBranch" data-group={group.key} key={`group-more:${group.key}`}>
                    <td colSpan={fields.length + 1}>
                      <button
                        type="button"
                        disabled={group.branchLoading}
                        onClick={() => group.onLoadMore?.()}
                      >
                        {group.branchLoading
                          ? (labels.loadingBranch ?? "…")
                          : group.branchError
                            ? (labels.retryBranch ?? "Retry")
                            : (labels.loadMore ?? "Load more")}
                      </button>
                    </td>
                  </tr>,
                ]
              : [];
          return [header, ...rows, ...branchTail];
        }) : renderRows(visibleRecords, 0, null)}
        </tbody>
      </table>
    </Provider>
  );
}
