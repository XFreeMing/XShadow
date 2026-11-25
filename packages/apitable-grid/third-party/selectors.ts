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

// Upstream getDatasheetPack / getDatasheetClient / getActiveCell / getEditingCell
// from packages/core/src/modules/database/store/selectors/resource/datasheet/base.ts,
// and getSelection from cell_range_calc.ts, at c9255a752.
//
// isCellVisible no longer walks view derivation. It uses the host snapshot's
// visible field and record ids. Preview-datasheet fallback is omitted.

import type { GridRootState } from "./state";
import type { ICell } from "./types";

export const getDatasheetPack = (state: GridRootState, id?: string | null) => {
  const datasheetId = id || state.pageParams?.datasheetId;
  if (!datasheetId) {
    return undefined;
  }
  return state.datasheetMap[datasheetId];
};

export const getDatasheetClient = (state: GridRootState, id?: string | null) => {
  const datasheetPack = getDatasheetPack(state, id);
  return datasheetPack?.client;
};

export const getActiveCell = (state: GridRootState, datasheetId?: string) => {
  const client = getDatasheetClient(state, datasheetId);
  const activeCell = client && client.selection && client.selection.activeCell;
  return activeCell ? activeCell : null;
};

export const getEditingCell = (state: GridRootState, datasheetId?: string) => {
  const client = getDatasheetClient(state, datasheetId);
  if (!client || !client.isEditingCell) {
    return null;
  }
  return client.isEditingCell;
};

function isCellVisible(state: GridRootState, cell: ICell) {
  const snapshot = getDatasheetPack(state)?.snapshot;
  if (!snapshot) return false;
  return snapshot.visibleRecordIds.includes(cell.recordId) && snapshot.visibleFieldIds.includes(cell.fieldId);
}

export const getSelection = (state: GridRootState) => {
  const client = getDatasheetClient(state);
  const selection = client && client.selection;

  // whether activeCell move out
  if (selection && selection.activeCell && !isCellVisible(state, selection.activeCell)) {
    return null;
  }
  // the start of the selection area and the end of the selection area are removed
  if (selection && selection.ranges) {
    const { start, end } = selection.ranges[0]!;
    if (!isCellVisible(state, start) || !isCellVisible(state, end)) {
      return null;
    }
  }
  return selection;
};

export const getGridSnapshot = (state: GridRootState, datasheetId?: string) => {
  return getDatasheetPack(state, datasheetId)?.snapshot;
};
