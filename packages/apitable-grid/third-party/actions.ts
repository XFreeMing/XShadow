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

// Upstream setActiveCell and clearSelection from
// packages/core/src/modules/database/store/actions/resource/datasheet/selection.ts.
// setEditStatus from datasheet.ts. Thunk actions that read the full root state
// (setSelection, setFillHandleStatus) are not included.

import { CLEAR_SELECTION, SET_ACTIVE_CELL, SET_EDIT_STATUS } from "./action-types";
import type { ICell } from "./types";

export const setActiveCell = (datasheetId: string, cell: ICell) => {
  const payload = cell;
  return {
    type: SET_ACTIVE_CELL,
    datasheetId,
    payload: {
      // when activate cell, selection area equals the the vector of active cell itself
      ranges: [{ start: cell, end: cell }],
      activeCell: payload,
    },
  };
};

export const clearSelection = (datasheetId: string) => {
  return {
    type: CLEAR_SELECTION,
    datasheetId,
  };
};

export const setEditStatus = (datasheetId: string, payload: { recordId: string; fieldId: string } | null) => {
  return {
    type: SET_EDIT_STATUS,
    datasheetId,
    payload,
  };
};
