"use client";

import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useDispatch, useSelector } from "react-redux";
import { setActiveCell, setEditStatus } from "../third-party/actions";
import { optionColor } from "../third-party/option-colors";
import { getActiveCell, getEditingCell, getGridSnapshot } from "../third-party/selectors";
import type { GridRootState } from "../third-party/state";
import type { GridCellValue, GridCollaborator, GridFieldView, GridLabels, GridRecordOption } from "./types";

function shown(value: GridCellValue | undefined): string {
  if (value == null || typeof value === "boolean" || Array.isArray(value)) return "";
  return String(value);
}

function hashId(id: string): number {
  let hash = 0;
  for (let i = 0; i < id.length; i += 1) {
    hash = (hash * 31 + id.charCodeAt(i)) | 0;
  }
  return Math.abs(hash);
}

function flattenValues(value: GridCellValue | undefined): Array<string | number | boolean | null> {
  if (value === undefined) return [];
  if (!Array.isArray(value)) return [value];
  const out: Array<string | number | boolean | null> = [];
  for (const item of value) {
    out.push(...flattenValues(item));
  }
  return out;
}

function formatLiteralScalar(
  value: string | number | boolean | null,
  labels: GridLabels,
): string | null {
  if (value === null) return null;
  if (typeof value === "boolean") return value ? labels.checked : labels.unchecked;
  return String(value);
}

function formatLookupScalar(
  value: string | number | boolean | null,
  labels: GridLabels,
  selectOptions: ReadonlyMap<string, string> | null,
  relationOptions: ReadonlyMap<string, string> | null,
): string | null {
  if (value === null) return null;
  if (typeof value === "boolean") return value ? labels.checked : labels.unchecked;
  if (typeof value === "number") return String(value);
  if (selectOptions) return selectOptions.get(value) ?? value;
  if (relationOptions) return relationOptions.get(value) ?? "...";
  return value;
}

export function GridCell({
  datasheetId,
  field,
  recordId,
  rowIndex,
  recordIds,
  labels,
  collaborator,
  recordOptions,
  onCommit,
  onOpenRelationCell,
  onActive,
  onEditEnd,
}: {
  datasheetId: string;
  field: GridFieldView;
  recordId: string;
  rowIndex: number;
  recordIds: readonly string[];
  labels: GridLabels;
  collaborator?: GridCollaborator | null;
  recordOptions?: readonly GridRecordOption[];
  onCommit: (value: GridCellValue, baseRevision: number) => void;
  onOpenRelationCell?: (
    recordId: string,
    field: GridFieldView,
    currentIds: readonly string[],
    baseRevision: number,
  ) => void;
  onActive?: (recordId: string, fieldId: string, revision: number) => void;
  onEditEnd?: () => void;
}) {
  const dispatch = useDispatch();
  const value = useSelector((state: GridRootState) => {
    const stored = getGridSnapshot(state, datasheetId)?.records[recordId]?.values[field.id];
    return stored ?? null;
  });
  const cellError = useSelector((state: GridRootState) => {
    return getGridSnapshot(state, datasheetId)?.records[recordId]?.errors?.[field.id] ?? null;
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
  const revision = useSelector((state: GridRootState) => {
    return getGridSnapshot(state, datasheetId)?.records[recordId]?.revision ?? 0;
  });
  const origin = useRef<{ value: GridCellValue; revision: number }>({ value: null, revision: 0 });
  const [text, setText] = useState(shown(value));
  useEffect(() => {
    if (editing) return;
    setText(shown(value));
  }, [editing, value]);

  const focusCell = () => {
    origin.current = { value: value ?? null, revision };
    dispatch(setActiveCell(datasheetId, { recordId, fieldId: field.id }));
    dispatch(setEditStatus(datasheetId, { recordId, fieldId: field.id }));
    onActive?.(recordId, field.id, revision);
  };
  const blurCell = () => {
    dispatch(setEditStatus(datasheetId, null));
    onEditEnd?.();
  };
  const commit = (next: GridCellValue) => {
    if (Object.is(next, origin.current.value)) return;
    onCommit(next, origin.current.revision);
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
    collaborator ? `apitable-grid__${collaborator.kind}` : "",
  ].filter(Boolean).join(" ");
  const mark = collaborator ? (
    <span
      className={`apitable-grid__mark apitable-grid__mark--${collaborator.kind}`}
      style={{ color: collaborator.color, borderColor: collaborator.color }}
      data-collaborator={collaborator.kind}
    >
      {collaborator.label}
    </span>
  ) : null;
  const options = field.config?.options ?? [];
  const selectedIndex = options.findIndex((option) => option.id === value);
  const selected = selectedIndex >= 0 ? options[selectedIndex] : null;
  const cellName = field.kind === "single_select"
    ? selected?.label
    : field.kind === "checkbox"
      ? undefined
      : shown(value) || undefined;

  const cellProps = {
    className,
    "aria-label": cellName,
    "data-active": active ? "true" : "false",
    "data-conflict": conflict ? "true" : undefined,
    "data-error": cellError ? "true" : undefined,
    "data-collaborator": collaborator?.kind,
    "data-collaborator-name": collaborator?.label,
    title: conflict ?? cellError ?? undefined,
    style: collaborator ? { boxShadow: `inset 0 0 0 2px ${collaborator.color}` } : undefined,
  };

  if (field.kind === "relation") {
    const linkedIds = Array.isArray(value)
      ? value.filter((item): item is string => typeof item === "string")
      : [];
    const optionMap = new Map((recordOptions ?? []).map((item) => [item.id, item.label]));
    const chipLabels = linkedIds.map((id) => ({
      id,
      label: optionMap.get(id) ?? "...",
    }));
    const buttonLabel = chipLabels.length > 0
      ? `${field.name}: ${chipLabels.map((item) => item.label).join(", ")}`
      : `${field.name}: ${labels.addLink ?? labels.emptyValue}`;
    return (
      <td {...cellProps}>
        {mark}
        <button
          type="button"
          className="apitable-grid__relationCell"
          data-cell={`${recordId}:${field.id}`}
          aria-label={buttonLabel}
          onFocus={() => {
            dispatch(setActiveCell(datasheetId, { recordId, fieldId: field.id }));
            onActive?.(recordId, field.id, revision);
          }}
          onBlur={blurCell}
          onKeyDown={onKeyDown}
          onClick={() => onOpenRelationCell?.(recordId, field, linkedIds, revision)}
        >
          {chipLabels.length > 0 ? chipLabels.map((item) => (
            <span
              key={item.id}
              className="apitable-grid__relationChip"
              style={{ background: optionColor(hashId(item.id)) }}
              title={item.label}
            >
              {item.label}
            </span>
          )) : (
            <span className="apitable-grid__relationPlaceholder">
              {labels.addLink ?? "+"}
            </span>
          )}
        </button>
      </td>
    );
  }

  if (field.kind === "lookup" || field.kind === "formula") {
    const isNumber = field.kind === "formula" && typeof value === "number";
    const selectOptions = field.kind === "lookup" && field.config?.target_options
      ? new Map(field.config.target_options.map((option) => [option.id, option.label]))
      : null;
    const relationOptions = field.kind === "lookup" && field.config?.target_relation_table_id
      ? new Map((recordOptions ?? []).map((option) => [option.id, option.label]))
      : null;
    const items = field.kind === "lookup"
      ? flattenValues(value)
          .map((item) => formatLookupScalar(item, labels, selectOptions, relationOptions))
          .filter((item): item is string => item !== null && item !== "")
      : [];
    const formulaDisplay = field.kind === "formula"
      ? (Array.isArray(value)
          ? flattenValues(value)
              .map((item) => formatLiteralScalar(item, labels))
              .filter((item): item is string => item !== null && item !== "")
              .join(", ")
          : (formatLiteralScalar(value, labels) ?? ""))
      : "";
    return (
      <td {...cellProps}>
        {mark}
        <div
          tabIndex={0}
          role="textbox"
          aria-readonly="true"
          aria-label={field.name}
          data-cell={`${recordId}:${field.id}`}
          data-readonly="true"
          className={`apitable-grid__readonlyCell${isNumber ? " apitable-grid__readonlyCell--number" : ""}`}
          onFocus={() => {
            dispatch(setActiveCell(datasheetId, { recordId, fieldId: field.id }));
          }}
          onKeyDown={(event) => {
            if (event.key === "Delete" || event.key === "Backspace") {
              event.preventDefault();
              return;
            }
            onKeyDown(event);
          }}
        >
          {cellError ? (
            <span className="apitable-grid__cellError" role="status" title={cellError}>
              ! {cellError}
            </span>
          ) : field.kind === "lookup" ? (
            items.map((item, idx) => (
              <span key={`${idx}:${item}`} className="apitable-grid__lookupChip" title={item}>
                {item}
              </span>
            ))
          ) : (
            <span className="apitable-grid__readonlyText" title={formulaDisplay || undefined}>
              {formulaDisplay}
            </span>
          )}
        </div>
      </td>
    );
  }

  if (field.kind === "checkbox") {
    const checked = value === true;
    return (
      <td {...cellProps}>
        {mark}
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
      <td {...cellProps}>
        {mark}
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
    <td {...cellProps}>
      {mark}
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
