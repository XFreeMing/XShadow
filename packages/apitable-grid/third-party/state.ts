/**
 * APITable <https://github.com/apitable/apitable>
 * Copyright (C) 2022 APITable Ltd. <https://apitable.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

// Local pack shape. Upstream datasheetMap routes every action that carries
// datasheetId into a per-sheet pack (datasheet.ts). This pack keeps client
// selection and editing, plus the host read-only snapshot the selectors read.

import type { ICell, ISelection } from "./types";

export type GridMirrorCellValue =
  | string
  | number
  | boolean
  | null
  | GridMirrorCellValue[];

export interface GridRecordMirror {
  id: string;
  revision: number;
  values: Record<string, GridMirrorCellValue>;
  errors?: Record<string, string>;
}

export interface GridSnapshot {
  visibleFieldIds: string[];
  visibleRecordIds: string[];
  records: Record<string, GridRecordMirror>;
  conflicts: Record<string, string>;
}

export interface GridClientState {
  selection: ISelection | null;
  isEditingCell: ICell | null;
}

export interface GridPack {
  client: GridClientState;
  snapshot: GridSnapshot;
}

export interface GridRootState {
  pageParams: { datasheetId?: string };
  datasheetMap: Record<string, GridPack>;
}

export const EMPTY_SNAPSHOT: GridSnapshot = {
  visibleFieldIds: [],
  visibleRecordIds: [],
  records: {},
  conflicts: {},
};

export const EMPTY_CLIENT: GridClientState = {
  selection: null,
  isEditingCell: null,
};
