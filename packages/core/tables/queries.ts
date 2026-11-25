import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const genericTableKeys = {
  all: (wsId: string) => ["generic-tables", wsId] as const,
  list: (wsId: string) => [...genericTableKeys.all(wsId), "list"] as const,
  sheetAll: (wsId: string, tableId: string) =>
    [...genericTableKeys.all(wsId), "sheet", tableId] as const,
  sheet: (wsId: string, tableId: string, viewId: string | null) =>
    [...genericTableKeys.sheetAll(wsId, tableId), viewId] as const,
  recordOptions: (wsId: string, tableId: string) =>
    [...genericTableKeys.all(wsId), "record-options", tableId] as const,
  recordComments: (wsId: string, tableId: string, recordId: string) =>
    [...genericTableKeys.all(wsId), "record-comments", tableId, recordId] as const,
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

export function genericRecordOptionsQuery(wsId: string, tableId: string) {
  return queryOptions({
    queryKey: genericTableKeys.recordOptions(wsId, tableId),
    queryFn: () => api.listGenericRecordOptions(tableId),
    enabled: !!wsId && !!tableId,
  });
}

export function genericRecordCommentsOptions(
  wsId: string,
  tableId: string,
  recordId: string,
  limit = 50,
) {
  return queryOptions({
    queryKey: genericTableKeys.recordComments(wsId, tableId, recordId),
    queryFn: () => api.listGenericRecordComments(tableId, recordId, { after: 0, limit }),
    enabled: !!wsId && !!tableId && !!recordId,
  });
}

export function genericRecordCommentsInfiniteOptions(
  wsId: string,
  tableId: string,
  recordId: string,
  limit = 50,
) {
  return infiniteQueryOptions({
    queryKey: genericTableKeys.recordComments(wsId, tableId, recordId),
    queryFn: ({ pageParam }) =>
      api.listGenericRecordComments(tableId, recordId, { after: pageParam, limit }),
    initialPageParam: 0,
    getNextPageParam: (lastPage) =>
      lastPage.has_more && lastPage.next_cursor != null ? lastPage.next_cursor : undefined,
    enabled: !!wsId && !!tableId && !!recordId,
  });
}
