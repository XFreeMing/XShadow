export type GridColumnKey =
  | "title"
  | "type"
  | "status"
  | "assignee"
  | `property:${string}`;

export const GRID_PROPERTY_PREFIX = "property:" as const;

const GRID_FIXED_COLUMN_KEYS: ReadonlySet<string> = new Set([
  "title",
  "type",
  "status",
  "assignee",
]);

export const DEFAULT_GRID_COLUMN_ORDER: readonly GridColumnKey[] = [
  "title",
  "type",
  "status",
  "assignee",
];

export function isGridColumnKey(value: unknown): value is GridColumnKey {
  if (typeof value !== "string" || value.length === 0) return false;
  if (GRID_FIXED_COLUMN_KEYS.has(value)) return true;
  if (value.startsWith(GRID_PROPERTY_PREFIX)) {
    return value.length > GRID_PROPERTY_PREFIX.length;
  }
  return false;
}

export function gridPropertyColumnId(propertyId: string): GridColumnKey {
  return `${GRID_PROPERTY_PREFIX}${propertyId}`;
}

export function catalogGridColumnOrder(
  hasIssueTypes: boolean,
  propertyIds: readonly string[],
): GridColumnKey[] {
  return [
    "title",
    ...(hasIssueTypes ? (["type"] as const) : []),
    "status",
    "assignee",
    ...propertyIds.map((id) => gridPropertyColumnId(id)),
  ];
}

export function normalizeGridColumnOrder(
  catalog: readonly GridColumnKey[],
  order: readonly GridColumnKey[] | undefined,
): GridColumnKey[] {
  const catalogSet = new Set(catalog);
  const merged: GridColumnKey[] = ["title"];
  const seen = new Set<string>(["title"]);

  for (const key of order ?? []) {
    if (key === "title") continue;
    if (!isGridColumnKey(key) || !catalogSet.has(key) || seen.has(key)) continue;
    merged.push(key);
    seen.add(key);
  }

  for (const key of catalog) {
    if (key === "title" || seen.has(key)) continue;
    merged.push(key);
    seen.add(key);
  }

  return merged;
}

export function normalizeGridHiddenColumnIds(
  catalog: readonly GridColumnKey[],
  hidden: readonly GridColumnKey[] | undefined,
): GridColumnKey[] {
  const catalogSet = new Set(catalog);
  return [...new Set((hidden ?? []).filter(
    (key) => isGridColumnKey(key) && key !== "title" && catalogSet.has(key),
  ))];
}

export function mergeGridColumnState(
  catalog: readonly GridColumnKey[],
  order: readonly GridColumnKey[] | undefined,
  hidden: readonly GridColumnKey[] | undefined,
): { order: GridColumnKey[]; hidden: GridColumnKey[] } {
  return {
    order: normalizeGridColumnOrder(catalog, order),
    hidden: normalizeGridHiddenColumnIds(catalog, hidden),
  };
}

export function isGridColumnVisible(
  columnId: GridColumnKey,
  hidden: ReadonlySet<GridColumnKey>,
): boolean {
  return columnId === "title" || !hidden.has(columnId);
}