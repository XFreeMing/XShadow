"use client";

import { useLayoutEffect, useMemo, useState } from "react";
import { Provider, useDispatch } from "react-redux";
import { GRID_STYLE_ID, gridCss } from "./grid-css";
import { GridCell } from "./cell";
import { createGridStore, hostSnapshot, setPageDatasheet } from "./store";
import type { GridActor, GridCellValue, GridCollaborator, GridConflictMark, GridFieldView, GridGroup, GridLabels, GridRecordOption, GridRecordView, GridRemotePatch } from "./types";
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
  onCellCommit,
  onRenameField,
  onEditField,
  onOpenRelationCell,
  onActiveCell,
  onEditEnd,
  onToggleGroup,
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
  onCellCommit: (recordId: string, fieldId: string, value: GridCellValue, baseRevision: number) => void;
  onRenameField: (fieldId: string, name: string) => void;
  onEditField?: (field: GridFieldView) => void;
  onOpenRelationCell?: (
    recordId: string,
    field: GridFieldView,
    currentIds: readonly string[],
    baseRevision: number,
  ) => void;
  onActiveCell?: (recordId: string, fieldId: string, revision: number) => void;
  onEditEnd?: () => void;
  onToggleGroup?: (key: string) => void;
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
  const renderRecord = (record: GridRecordView, rowIndex: number) => (
    <tr className="apitable-grid__row" data-record-id={record.id} key={record.id}>
      <th className="apitable-grid__rowHead" scope="row">{rowIndex + 1}</th>
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
            recordId={record.id}
            rowIndex={rowIndex}
            recordIds={recordIds}
            labels={labels}
            collaborator={collaboratorAt(record.id, field.id)}
            recordOptions={optionTableId ? recordOptionsByTable?.[optionTableId] : undefined}
            onOpenRelationCell={onOpenRelationCell}
            onActive={onActiveCell}
            onEditEnd={onEditEnd}
            onCommit={(value, baseRevision) => onCellCommit(record.id, field.id, value, baseRevision)}
          />
        );
      })}
    </tr>
  );

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
      <table className="apitable-grid" data-apitable-grid="">
        <thead>
        <tr className="apitable-grid__row">
          <th className="apitable-grid__rowHead" />
          {fields.map((field) => (
            <th className="apitable-grid__fieldHeader" key={field.id}>
              <span className="apitable-grid__type" aria-hidden="true">{typeMark(field.kind)}</span>
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
          return [header, ...group.records.map((record, index) => renderRecord(record, start + index))];
        }) : visibleRecords.map((record, rowIndex) => renderRecord(record, rowIndex))}
        </tbody>
      </table>
    </Provider>
  );
}
