import { z } from "zod";

export const genericCellValueSchema = z.union([z.string(), z.number(), z.boolean(), z.null()]);
export type GenericCellValue = z.infer<typeof genericCellValueSchema>;

export const genericFieldKinds = ["text", "number", "date", "single_select", "checkbox"] as const;
export type GenericFieldKind = (typeof genericFieldKinds)[number];

export const genericOptionSchema = z.object({
  id: z.string(),
  label: z.string(),
}).loose();

export const genericFieldSchema = z.object({
  id: z.string(),
  name: z.string(),
  kind: z.string(),
  config: z.object({
    options: z.array(genericOptionSchema).optional(),
  }).loose().catch({}),
  position: z.number(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();
export type GenericField = z.infer<typeof genericFieldSchema>;

export const genericRecordSchema = z.object({
  id: z.string(),
  revision: z.number(),
  created_at: z.string(),
  updated_at: z.string(),
  values: z.record(z.string(), genericCellValueSchema),
}).loose();
export type GenericRecord = z.infer<typeof genericRecordSchema>;

export const genericFilterSchema = z.object({
  field_id: z.string(),
  op: z.enum(["eq", "contains", "is_empty", "is_not_empty"]),
  value: genericCellValueSchema.optional(),
}).loose();
export type GenericFilter = z.infer<typeof genericFilterSchema>;

export const genericSortSchema = z.object({
  field_id: z.string(),
  direction: z.enum(["asc", "desc"]),
}).loose();
export type GenericSort = z.infer<typeof genericSortSchema>;

export const genericViewSchema = z.object({
  id: z.string(),
  name: z.string(),
  columns: z.array(z.string()).nullable(),
  filter: genericFilterSchema.nullable(),
  sort: genericSortSchema.nullable(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();
export type GenericView = z.infer<typeof genericViewSchema>;

export const genericTableSchema = z.object({
  id: z.string(),
  name: z.string(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();
export type GenericTable = z.infer<typeof genericTableSchema>;

export const genericTableListSchema = z.object({
  tables: z.array(genericTableSchema),
}).loose();
export type GenericTableList = z.infer<typeof genericTableListSchema>;

export const genericTableCreatedSchema = z.object({
  table: genericTableSchema,
  view: genericViewSchema,
}).loose();
export type GenericTableCreated = z.infer<typeof genericTableCreatedSchema>;

export const genericFieldResponseSchema = z.object({
  field: genericFieldSchema,
}).loose();

export const genericRecordResponseSchema = z.object({
  record: genericRecordSchema,
}).loose();
export type GenericRecordResponse = z.infer<typeof genericRecordResponseSchema>;

export const genericViewResponseSchema = z.object({
  view: genericViewSchema,
}).loose();
export type GenericViewResponse = z.infer<typeof genericViewResponseSchema>;

export const genericSheetSchema = z.object({
  table: genericTableSchema,
  fields: z.array(genericFieldSchema),
  views: z.array(genericViewSchema),
  records: z.array(genericRecordSchema),
  active_view_id: z.string().nullable(),
}).loose();
export type GenericSheet = z.infer<typeof genericSheetSchema>;

export const revisionConflictSchema = z.object({
  code: z.literal("revision_conflict"),
  error: z.string(),
  record: genericRecordSchema,
}).loose();

export interface GenericFieldWrite {
  name: string;
  kind: GenericFieldKind;
  config?: { options?: { id: string; label: string }[] };
}

export interface GenericViewWrite {
  name: string;
  columns: string[] | null;
  filter: GenericFilter | null;
  sort: GenericSort | null;
}

const emptyTable = { id: "", name: "", created_at: "", updated_at: "" };
const emptyView = {
  id: "",
  name: "",
  columns: null,
  filter: null,
  sort: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_GENERIC_TABLE_LIST: GenericTableList = { tables: [] };
export const EMPTY_GENERIC_TABLE_CREATED: GenericTableCreated = { table: emptyTable, view: emptyView };
export const EMPTY_GENERIC_FIELD: GenericField = {
  id: "",
  name: "",
  kind: "text",
  config: {},
  position: 0,
  created_at: "",
  updated_at: "",
};
export const EMPTY_GENERIC_RECORD: GenericRecord = {
  id: "",
  revision: 0,
  created_at: "",
  updated_at: "",
  values: {},
};
export const EMPTY_GENERIC_VIEW: GenericView = emptyView;
export const EMPTY_GENERIC_SHEET: GenericSheet = {
  table: emptyTable,
  fields: [],
  views: [],
  records: [],
  active_view_id: null,
};
