import { z } from "zod";

export type GenericCellValue =
  | string
  | number
  | boolean
  | null
  | GenericCellValue[];

export const genericCellValueSchema: z.ZodType<GenericCellValue> = z.lazy(() =>
  z.union([
    z.string(),
    z.number(),
    z.boolean(),
    z.null(),
    z.array(genericCellValueSchema),
  ]),
);

export const genericFieldKinds = [
  "text",
  "number",
  "date",
  "single_select",
  "checkbox",
  "relation",
  "lookup",
  "formula",
] as const;
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
    target_table_id: z.string().optional(),
    bidirectional: z.boolean().optional(),
    reverse_field_id: z.string().optional(),
    relation_field_id: z.string().optional(),
    target_field_id: z.string().optional(),
    target_options: z.array(genericOptionSchema).optional(),
    target_relation_table_id: z.string().optional(),
    expression: z.string().optional(),
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
  errors: z.record(z.string(), z.string()).optional(),
}).loose();
export type GenericRecord = z.infer<typeof genericRecordSchema>;

export const genericRecordOptionSchema = z.object({
  id: z.string(),
  label: z.string(),
}).loose();
export type GenericRecordOption = z.infer<typeof genericRecordOptionSchema>;

export const genericRecordOptionsSchema = z.object({
  records: z.array(genericRecordOptionSchema),
}).loose();
export type GenericRecordOptions = z.infer<typeof genericRecordOptionsSchema>;

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
  group: z.string().nullable().default(null),
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

export interface GenericFieldConfigWrite {
  options?: { id: string; label: string }[];
  target_table_id?: string;
  bidirectional?: boolean;
  relation_field_id?: string;
  target_field_id?: string;
  expression?: string;
}

export interface GenericFieldWrite {
  name: string;
  kind: GenericFieldKind;
  config?: GenericFieldConfigWrite;
}

export interface GenericFieldPatch {
  name?: string;
  config?: GenericFieldConfigWrite;
}

export interface GenericViewWrite {
  name: string;
  columns: string[] | null;
  filter: GenericFilter | null;
  sort: GenericSort | null;
  group?: string | null;
}

export const genericActorSchema = z.object({
  source: z.string().default("member"),
  id: z.string().default(""),
  task_id: z.string().nullable().optional(),
  label: z.string().default(""),
  color: z.string().default("#7B67EE"),
}).loose();
export type GenericActor = z.infer<typeof genericActorSchema>;

export const genericChangeSchema = z.object({
  seq: z.number(),
  record_id: z.string(),
  record_revision: z.number(),
  fields: z.record(z.string(), genericCellValueSchema),
  actor: genericActorSchema,
}).loose();
export type GenericChange = z.infer<typeof genericChangeSchema>;

export const genericPresenceSchema = z.object({
  actor: genericActorSchema,
  record_id: z.string().nullable().default(null),
  field_id: z.string().nullable().default(null),
  online: z.boolean().default(true),
}).loose();
export type GenericPresence = z.infer<typeof genericPresenceSchema>;

export const genericChangesSchema = z.object({
  latest: z.number().default(0),
  next_since: z.number().default(0),
  changes: z.array(genericChangeSchema).default([]),
  presence: z.array(genericPresenceSchema).default([]),
}).loose();
export type GenericChanges = z.infer<typeof genericChangesSchema>;

const emptyTable = { id: "", name: "", created_at: "", updated_at: "" };
const emptyView = {
  id: "",
  name: "",
  columns: null,
  filter: null,
  sort: null,
  group: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_GENERIC_TABLE_LIST: GenericTableList = { tables: [] };
export const EMPTY_GENERIC_RECORD_OPTIONS: GenericRecordOptions = { records: [] };
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
export const EMPTY_GENERIC_CHANGES: GenericChanges = {
  latest: 0,
  next_since: 0,
  changes: [],
  presence: [],
};
export const EMPTY_GENERIC_SHEET: GenericSheet = {
  table: emptyTable,
  fields: [],
  views: [],
  records: [],
  active_view_id: null,
};
