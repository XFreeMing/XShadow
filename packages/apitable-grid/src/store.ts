import { combineReducers, createStore, type Reducer } from "redux";
import { isEditingCell } from "../third-party/editing";
import { selection } from "../third-party/selection";
import {
  EMPTY_CLIENT,
  EMPTY_SNAPSHOT,
  type GridClientState,
  type GridPack,
  type GridRootState,
  type GridSnapshot,
} from "../third-party/state";

export const SET_PAGE_DATASHEET = "SET_PAGE_DATASHEET";
export const HOST_SNAPSHOT = "HOST_SNAPSHOT";

interface GridAction {
  type: string;
  datasheetId?: string;
  payload?: unknown;
}

const pageParams: Reducer<GridRootState["pageParams"], GridAction> = (state = {}, action) => {
  if (action.type === SET_PAGE_DATASHEET) {
    const payload = action.payload as { datasheetId?: string } | undefined;
    return { datasheetId: payload?.datasheetId };
  }
  return state;
};

function clientReducer(state: GridClientState = EMPTY_CLIENT, action: GridAction): GridClientState {
  const nextSelection = selection(state.selection, action as never);
  const nextEditing = isEditingCell(state.isEditingCell, action as never);
  if (nextSelection === state.selection && nextEditing === state.isEditingCell) return state;
  return { selection: nextSelection, isEditingCell: nextEditing };
}

function snapshotReducer(state: GridSnapshot = EMPTY_SNAPSHOT, action: GridAction): GridSnapshot {
  if (action.type === HOST_SNAPSHOT && action.payload) return action.payload as GridSnapshot;
  return state;
}

function packReducer(state: GridPack | undefined, action: GridAction): GridPack {
  const current = state ?? { client: EMPTY_CLIENT, snapshot: EMPTY_SNAPSHOT };
  const client = clientReducer(current.client, action);
  const snapshot = snapshotReducer(current.snapshot, action);
  if (client === current.client && snapshot === current.snapshot) return current;
  return { client, snapshot };
}

// Same routing rule as upstream datasheetMap: ignore actions without datasheetId.
export const datasheetMap: Reducer<Record<string, GridPack>, GridAction> = (state = {}, action) => {
  if (!action.datasheetId) return state;
  const current = state[action.datasheetId];
  const next = packReducer(current, action);
  if (next === current) return state;
  return { ...state, [action.datasheetId]: next };
};

const rootReducer = combineReducers({
  pageParams,
  datasheetMap,
});

export function createGridStore() {
  return createStore(rootReducer);
}

export type GridStore = ReturnType<typeof createGridStore>;

export function hostSnapshot(datasheetId: string, payload: GridSnapshot): GridAction {
  return { type: HOST_SNAPSHOT, datasheetId, payload };
}

export function setPageDatasheet(datasheetId: string): GridAction {
  return { type: SET_PAGE_DATASHEET, payload: { datasheetId } };
}
