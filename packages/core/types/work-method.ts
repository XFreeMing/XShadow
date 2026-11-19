import { z } from "zod";

export const WorkMethodKindSchema = z.enum([
  "development",
  "research",
  "outcome",
]);
export type WorkMethodKind = z.infer<typeof WorkMethodKindSchema>;

const nonEmpty = z.string().trim().min(1);
const index = z.number().int().nonnegative();
export const WorkMethodSchema = z
  .object({
    id: nonEmpty,
    name: nonEmpty,
    kind: WorkMethodKindSchema,
    keywords: z
      .array(z.string())
      .nullish()
      .transform((value) => value ?? []),
    steps: z.array(z.object({ name: nonEmpty, output: nonEmpty })).min(1),
    review: nonEmpty,
    rework_step: index,
  })
  .refine((method) => method.rework_step < method.steps.length);
export const WorkMethodListSchema = z.object({
  methods: z.array(WorkMethodSchema),
});
export const WorkMethodRunSchema = z
  .object({
    revision: index,
    state: z.enum(["ready", "working", "review", "closed"]),
    method: WorkMethodSchema.nullable(),
    step: index,
    reason: z.string().optional().default(""),
    history: z
      .array(
        z.object({
          action: z.string(),
          step: index,
          evidence: z
            .string()
            .nullish()
            .transform((value) => value ?? ""),
          actor: z.string(),
          at: z.string(),
        }),
      )
      .nullish()
      .transform((value) => value ?? []),
  })
  .refine((run) => {
    if (run.state === "ready") return true;
    if (!run.method) return false;
    return run.state !== "working" || run.step < run.method.steps.length;
  });

export type WorkMethod = z.infer<typeof WorkMethodSchema>;
export type NewWorkMethod = Omit<WorkMethod, "id">;
export type WorkMethodRun = z.infer<typeof WorkMethodRunSchema>;
export type WorkMethodAction = { base_revision: number } & (
  | { action: "select"; method_id: string }
  | { action: "advance"; evidence: string; method_id?: string }
  | { action: "approve"; evidence: string }
  | { action: "rework"; evidence: string; target_step?: number }
);
