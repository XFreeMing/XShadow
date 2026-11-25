// Host-owned read-only view. Server records stay in the host query.
// `remote` is the insertion point for a later turn: pass received records
// and conflict marks here. This package does not open a collaboration session.

export interface GridFieldView {
  id: string;
  name: string;
  kind: string;
  config?: {
    options?: { id: string; label: string }[];
  };
}

export interface GridRecordView {
  id: string;
  revision: number;
  values: Record<string, string | number | boolean | null | undefined>;
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
}

export type GridCellValue = string | number | boolean | null;
