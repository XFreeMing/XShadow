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

export interface GridRecordView {
  id: string;
  revision: number;
  values: Record<string, GridCellValue | undefined>;
  errors?: Record<string, string>;
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
}
