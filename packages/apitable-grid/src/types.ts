import type { ReactNode } from "react";

// Host-owned read-only view. Server records stay in the host query.
// `remote` is the insertion point for a later turn: pass received records
// and conflict marks here. This package does not open a collaboration session.

export type GridCellValue =
  | string
  | number
  | boolean
  | null
  | GridCellValue[];

export interface GridRecordOption {
  id: string;
  label: string;
}

export interface GridFieldView {
  id: string;
  name: string;
  kind: string;
  /** For text cells, Escape discards the unsaved draft. */
  cancelOnEscape?: boolean;
  config?: {
    options?: { id: string; label: string }[];
    target_table_id?: string;
    bidirectional?: boolean;
    reverse_field_id?: string;
    relation_field_id?: string;
    target_field_id?: string;
    target_options?: { id: string; label: string }[];
    target_relation_table_id?: string;
    expression?: string;
  };
}

export interface GridRecordHierarchy {
  depth: number;
  hasChildren: boolean;
  collapsed: boolean;
  toggleLabel: string;
  onToggle: () => void;
}

export interface GridRecordView {
  id: string;
  revision: number;
  values: Record<string, GridCellValue | undefined>;
  errors?: Record<string, string>;
  hierarchy?: GridRecordHierarchy;
}

export interface GridInsertedRow {
  key: string;
  afterRecordId: string | null;
  /** When groups are shown, footers render only inside the matching group. */
  groupKey?: string | null;
  content: ReactNode;
}

export interface GridConflictMark {
  recordId: string;
  fieldId: string;
  message: string;
}

export interface GridRemotePatch {
  records?: readonly GridRecordView[];
  conflicts?: readonly GridConflictMark[];
}

export interface GridLabels {
  rename: string;
  clear: string;
  emptyRecords: string;
  emptyValue: string;
  checked: string;
  unchecked: string;
  emptyGroup?: string;
  online?: string;
  recent?: string;
  configureField?: string;
  addLink?: string;
  readonly?: string;
  openRecord?: string;
  loadMore?: string;
  loadingBranch?: string;
  retryBranch?: string;
}

/** Optional row checkboxes for host-driven multi-select (e.g. task batch actions). */
export interface GridRowSelection {
  selectedRecordIds: ReadonlySet<string>;
  recordIds: readonly string[];
  selectAllLabel: string;
  selectRecordLabel: (recordId: string) => string;
  onToggleRecord: (recordId: string, shiftKey: boolean) => void;
  onToggleAll: (select: boolean) => void;
}

export interface GridCollaborator {
  actorId: string;
  label: string;
  color: string;
  kind: "online" | "recent";
  recordId: string | null;
  fieldId: string | null;
}

export interface GridActor {
  id: string;
  label: string;
  color: string;
}

export interface GridGroup {
  key: string;
  label: string;
  count: number;
  collapsed: boolean;
  records: readonly GridRecordView[];
  /** Native branch still has additional rows beyond `records`. */
  branchHasMore?: boolean;
  branchLoading?: boolean;
  branchError?: boolean;
  onLoadMore?: () => void;
}

export interface GridCellRenderContext {
  record: GridRecordView;
  field: GridFieldView;
  value: GridCellValue;
  revision: number;
  rowIndex: number;
  active: boolean;
  onActivate: () => void;
  onDeactivate: () => void;
}

export type GridCellRenderer = (context: GridCellRenderContext) => ReactNode | undefined;
