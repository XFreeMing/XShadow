export const DEFAULT_GRID_COLUMN_ORDER = [
  "title",
  "type",
  "status",
  "assignee",
  "priority",
  "start_date",
  "due_date",
  "labels",
  "identifier",
  "project",
  "child_progress",
  "creator",
  "created_at",
  "updated_at",
] as const;

export const GRID_PROPERTY_PREFIX = "property:" as const;
export type GridColumnKey = (typeof DEFAULT_GRID_COLUMN_ORDER)[number] | `property:${string}`;

const GRID_FIXED_COLUMN_KEYS: ReadonlySet<string> = new Set(DEFAULT_GRID_COLUMN_ORDER);

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
    ...DEFAULT_GRID_COLUMN_ORDER.filter((key) => key !== "type" || hasIssueTypes),
    ...propertyIds.map(gridPropertyColumnId),
  ];
}

export function projectTableColumnsToGrid(
  tableColumns: readonly { key: string }[],
  catalog: readonly GridColumnKey[],
): { order: GridColumnKey[]; hidden: GridColumnKey[] } {
  const visibleKeys = tableColumns
    .map((column) => column.key)
    .filter(isGridColumnKey);
  const visibleSet = new Set<GridColumnKey>(["title", ...visibleKeys]);
  const fullCatalog = [...new Set<GridColumnKey>([...catalog, ...visibleKeys])];
  return {
    order: normalizeGridColumnOrder(fullCatalog, visibleKeys),
    hidden: fullCatalog.filter(
      (key) => key !== "title" && !visibleSet.has(key),
    ),
  };
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