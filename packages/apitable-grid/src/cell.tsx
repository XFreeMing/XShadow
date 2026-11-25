"use client";

import { useEffect, useState } from "react";
import type { KeyboardEvent } from "react";
import { useDispatch, useSelector } from "react-redux";
import { setActiveCell, setEditStatus } from "../third-party/actions";
import { optionColor } from "../third-party/option-colors";
import { getActiveCell, getEditingCell, getGridSnapshot } from "../third-party/selectors";
import type { GridRootState } from "../third-party/state";
import type { GridCellValue, GridFieldView, GridLabels } from "./types";

function shown(value: string | number | boolean | null | undefined): string {
  if (value == null || typeof value === "boolean") return "";
  return String(value);
}

export function GridCell({
  datasheetId,
  field,
  recordId,
  rowIndex,
  recordIds,
  labels,
  onCommit,
}: {
  datasheetId: string;
  field: GridFieldView;
  recordId: string;
  rowIndex: number;
  recordIds: readonly string[];
  labels: GridLabels;
  onCommit: (value: GridCellValue) => void;
}) {
  const dispatch = useDispatch();
  const value = useSelector((state: GridRootState) => {
    const stored = getGridSnapshot(state, datasheetId)?.records[recordId]?.values[field.id];
    return stored ?? null;
  });
  const conflict = useSelector((state: GridRootState) => {
    return getGridSnapshot(state, datasheetId)?.conflicts[`${recordId}:${field.id}`] ?? null;
  });
  const active = useSelector((state: GridRootState) => {
    const cell = getActiveCell(state, datasheetId);
    return cell?.recordId === recordId && cell.fieldId === field.id;
  });
  const editing = useSelector((state: GridRootState) => {
    const cell = getEditingCell(state, datasheetId);
    return cell?.recordId === recordId && cell.fieldId === field.id;
  });
  const [text, setText] = useState(shown(value));
  useEffect(() => {
    if (editing) return;
    setText(shown(value));
  }, [editing, value]);

  const focusCell = () => {
    dispatch(setActiveCell(datasheetId, { recordId, fieldId: field.id }));
    dispatch(setEditStatus(datasheetId, { recordId, fieldId: field.id }));
  };
  const blurCell = () => {
    dispatch(setEditStatus(datasheetId, null));
  };
  const commit = (next: GridCellValue) => {
    if (next === value) return;
    onCommit(next);
  };
  const moveVertical = (event: KeyboardEvent<HTMLElement>, delta: number) => {
    const next = recordIds[rowIndex + delta];
    if (!next) return;
    event.preventDefault();
    const root = event.currentTarget.closest("[data-apitable-grid]");
    const target = root?.querySelector<HTMLElement>(`[data-cell="${CSS.escape(next)}:${CSS.escape(field.id)}"]`);
    target?.focus();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (event.key === "ArrowDown") moveVertical(event, 1);
    if (event.key === "ArrowUp") moveVertical(event, -1);
  };

  const className = [
    "apitable-grid__cell",
    active ? "apitable-grid__selected apitable-grid__active" : "",
    conflict ? "apitable-grid__conflict" : "",
  ].filter(Boolean).join(" ");
  const options = field.config?.options ?? [];
  const selectedIndex = options.findIndex((option) => option.id === value);
  const selected = selectedIndex >= 0 ? options[selectedIndex] : null;
  const cellName = field.kind === "single_select"
    ? selected?.label
    : field.kind === "checkbox"
      ? undefined
      : shown(value) || undefined;

  if (field.kind === "checkbox") {
    const checked = value === true;
    return (
      <td
        className={className}
        aria-label={cellName}
        data-active={active ? "true" : "false"}
        data-conflict={conflict ? "true" : undefined}
        title={conflict ?? undefined}
      >
        <div className="apitable-grid__checkboxCell">
          <input
            type="checkbox"
            data-cell={`${recordId}:${field.id}`}
            checked={checked}
            ref={(node) => {
              if (node) node.indeterminate = value == null;
            }}
            aria-label={value == null ? labels.emptyValue : checked ? labels.checked : labels.unchecked}
            onFocus={focusCell}
            onBlur={blurCell}
            onKeyDown={onKeyDown}
            onChange={(event) => commit(event.target.checked)}
          />
          <button type="button" disabled={value == null} onClick={() => commit(null)}>
            {labels.clear}
          </button>
        </div>
      </td>
    );
  }

  if (field.kind === "single_select") {
    return (
      <td
        className={className}
        aria-label={cellName}
        data-active={active ? "true" : "false"}
        data-conflict={conflict ? "true" : undefined}
        title={conflict ?? undefined}
      >
        <div className="apitable-grid__optionCell">
          {selected ? (
            <span className="apitable-grid__tabItem" style={{ background: optionColor(selectedIndex) }} aria-hidden="true">
              {selected.label}
            </span>
          ) : null}
          <select
            className="apitable-grid__optionSelect"
            data-cell={`${recordId}:${field.id}`}
            aria-label={field.name}
            value={typeof value === "string" ? value : ""}
            onFocus={focusCell}
            onBlur={blurCell}
            onKeyDown={onKeyDown}
            onChange={(event) => commit(event.target.value || null)}
          >
            <option value="">{labels.emptyValue}</option>
            {options.map((option) => <option key={option.id} value={option.id}>{option.label}</option>)}
          </select>
        </div>
      </td>
    );
  }

  const date = field.kind === "date";
  return (
    <td
      className={className}
      aria-label={cellName}
      data-active={active ? "true" : "false"}
      data-conflict={conflict ? "true" : undefined}
      title={conflict ?? undefined}
    >
      <input
        className={date ? "apitable-grid__dateTime" : `apitable-grid__cellText${field.kind === "number" ? " apitable-grid__numberCell" : ""}`}
        data-cell={`${recordId}:${field.id}`}
        aria-label={field.name}
        type={date ? "date" : "text"}
        value={date && typeof value === "string" ? value : text}
        onFocus={focusCell}
        onKeyDown={onKeyDown}
        onChange={(event) => {
          setText(event.target.value);
          if (date) commit(event.target.value || null);
        }}
        onBlur={() => {
          blurCell();
          if (date) return;
          if (field.kind === "number") {
            if (text.trim() === "") commit(null);
            else {
              const parsed = Number(text);
              if (Number.isFinite(parsed)) commit(parsed);
            }
            return;
          }
          commit(text.trim() === "" ? null : text);
        }}
      />
    </td>
  );
}
