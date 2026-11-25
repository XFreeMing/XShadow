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

// Shapes used by the selection reducer and selectors.
// Upstream: ICell and IRange in packages/core/src/model/view/range.ts;
// ISelection in packages/core/src/modules/database/store/interfaces/resource/datasheet/datasheet.ts.

import type {
  CLEAR_SELECTION,
  CLEAR_SELECTION_BUT_KEEP_CHECKED_RECORD,
  SET_ACTIVE_CELL,
  SET_FIELD_RANGES,
  SET_FILL_HANDLE_STATUS,
  SET_RECORD_SELECTION,
  SET_SELECTION,
} from "./action-types";

export interface ICell {
  recordId: string;
  fieldId: string;
}

export interface IRange {
  start: ICell;
  end: ICell;
}

export type IRecordRanges = string[];
export type IFieldRanges = string[];

export interface IFillHandleStatus {
  isActive: boolean;
  direction?: string;
  fillRange?: IRange;
}

export interface ISelection {
  ranges?: IRange[];
  activeCell?: ICell;
  recordRanges?: IRecordRanges;
  fieldRanges?: IFieldRanges;
  fillHandleStatus?: IFillHandleStatus;
}

export interface ISetSelectionAction {
  type: typeof SET_SELECTION;
  datasheetId?: string;
  payload: Partial<ISelection>;
}

export interface ISetActiveCellAction {
  type: typeof SET_ACTIVE_CELL;
  datasheetId?: string;
  payload: ISelection;
}

export interface ISetRecordRange {
  type: typeof SET_RECORD_SELECTION;
  datasheetId?: string;
  payload: IRecordRanges;
}

export interface IClearSelection {
  type: typeof CLEAR_SELECTION;
  datasheetId?: string;
}

export interface IClearSelectionButKeepCheckedRecord {
  type: typeof CLEAR_SELECTION_BUT_KEEP_CHECKED_RECORD;
  datasheetId?: string;
}

export interface ISetFieldRanges {
  type: typeof SET_FIELD_RANGES;
  datasheetId?: string;
  payload: IFieldRanges;
}

export interface ISetFillHandleStatus {
  type: typeof SET_FILL_HANDLE_STATUS;
  datasheetId?: string;
  payload: Partial<ISelection>;
}
