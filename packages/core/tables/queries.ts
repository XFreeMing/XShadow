import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const genericTableKeys = {
  all: (wsId: string) => ["generic-tables", wsId] as const,
  list: (wsId: string) => [...genericTableKeys.all(wsId), "list"] as const,
  sheetAll: (wsId: string, tableId: string) =>
    [...genericTableKeys.all(wsId), "sheet", tableId] as const,
  sheet: (wsId: string, tableId: string, viewId: string | null) =>
    [...genericTableKeys.sheetAll(wsId, tableId), viewId] as const,
};

export function genericTableListOptions(wsId: string) {
  return queryOptions({
    queryKey: genericTableKeys.list(wsId),
    queryFn: () => api.listGenericTables(),
    enabled: !!wsId,
  });
}

export function genericSheetOptions(wsId: string, tableId: string, viewId: string | null) {
  return queryOptions({
    queryKey: genericTableKeys.sheet(wsId, tableId, viewId),
    queryFn: () => api.getGenericSheet(tableId, viewId),
    enabled: !!wsId && !!tableId,
  });
}
