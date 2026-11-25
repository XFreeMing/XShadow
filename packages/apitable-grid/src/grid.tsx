"use client";

import { useLayoutEffect, useMemo, useState } from "react";
import { Provider, useDispatch } from "react-redux";
import { GRID_STYLE_ID, gridCss } from "./grid-css";
import { GridCell } from "./cell";
import { createGridStore, hostSnapshot, setPageDatasheet } from "./store";
import type { GridCellValue, GridConflictMark, GridFieldView, GridLabels, GridRecordView, GridRemotePatch } from "./types";
import type { GridSnapshot } from "../third-party/state";

const NO_CONFLICTS: readonly GridConflictMark[] = [];

function mirror(fields: readonly GridFieldView[], records: readonly GridRecordView[], conflicts: readonly GridConflictMark[]): GridSnapshot {
  const recordMap: GridSnapshot["records"] = {};
  for (const record of records) {
    const values: GridSnapshot["records"][string]["values"] = {};
    for (const [fieldId, value] of Object.entries(record.values)) {
      if (value !== undefined) values[fieldId] = value;
    }
    recordMap[record.id] = { id: record.id, revision: record.revision, values };
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

export function ApitableGrid({
  datasheetId,
  fields,
  records,
  remote,
  labels,
  onCellCommit,
  onRenameField,
}: {
  datasheetId: string;
  fields: readonly GridFieldView[];
  records: readonly GridRecordView[];
  remote?: GridRemotePatch;
  labels: GridLabels;
  onCellCommit: (recordId: string, fieldId: string, value: GridCellValue) => void;
  onRenameField: (fieldId: string, name: string) => void;
}) {
  const [store] = useState(createGridStore);
  const shownRecords = remote?.records ?? records;
  const conflicts = remote?.conflicts ?? NO_CONFLICTS;
  const snapshot = useMemo(
    () => mirror(fields, shownRecords, conflicts),
    [fields, shownRecords, conflicts],
  );
  const recordIds = shownRecords.map((record) => record.id);

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
      <table className="apitable-grid" data-apitable-grid="">
        <thead>
        <tr className="apitable-grid__row">
          <th className="apitable-grid__rowHead" />
          {fields.map((field) => (
            <th className="apitable-grid__fieldHeader" key={field.id}>
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
            </th>
          ))}
        </tr>
        </thead>
        <tbody>
        {shownRecords.length === 0 ? (
          <tr className="apitable-grid__empty"><td>{labels.emptyRecords}</td></tr>
        ) : shownRecords.map((record, rowIndex) => (
          <tr className="apitable-grid__row" data-record-id={record.id} key={record.id}>
            <th className="apitable-grid__rowHead" scope="row">{rowIndex + 1}</th>
            {fields.map((field) => (
              <GridCell
                key={field.id}
                datasheetId={datasheetId}
                field={field}
                recordId={record.id}
                rowIndex={rowIndex}
                recordIds={recordIds}
                labels={labels}
                onCommit={(value) => onCellCommit(record.id, field.id, value)}
              />
            ))}
          </tr>
        ))}
        </tbody>
      </table>
    </Provider>
  );
}
