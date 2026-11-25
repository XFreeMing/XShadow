import { z } from "zod";
import { AcceptanceCloseoutSchema } from "./acceptance";
import { GoalPlanDeliverySchema } from "./goal-plan-delivery";

export const WorkMethodKindSchema = z.enum([
  "development",
  "research",
  "outcome",
]);
export type WorkMethodKind = z.infer<typeof WorkMethodKindSchema>;

const nonEmpty = z.string().trim().min(1);
const index = z.number().int().nonnegative();
const actor = z.object({
  kind: z.enum(["member", "agent", "squad"]),
  id: nonEmpty,
});
const handler = z.union([
  actor,
  z.object({ kind: z.enum(["creator", "project_lead"]) }),
]);
const step = z.object({
  self_claim: z.boolean().optional(),
  acceptance_gate: z.enum(["criteria", "coverage", "candidate", "delivery"]).optional(),
  name: nonEmpty,
  output: nonEmpty,
  inputs: z.string().optional(),
  instructions: z.string().optional(),
  checks: z.array(z.string()).nullish(),
  owner: handler.nullish(),
  reviewer: handler.nullish(),
});
export type WorkMethodHandler = z.infer<typeof handler>;
export type WorkMethodStep = z.infer<typeof step>;
export const WorkMethodSchema = z
  .object({
    id: nonEmpty,
    revision: z.number().int().positive().default(1),
    name: nonEmpty,
    kind: WorkMethodKindSchema,
    keywords: z
      .array(z.string())
      .nullish()
      .transform((value) => value ?? []),
    steps: z.array(step).min(1),
    review: nonEmpty,
    rework_step: index,
  })
  .refine((method) => method.rework_step < method.steps.length);
export const WorkMethodListSchema = z.object({
  methods: z.array(WorkMethodSchema),
});
const finding = z.object({
  criterion_id: nonEmpty.optional(),
  case_id: nonEmpty.optional(),
  expected: nonEmpty,
  actual: nonEmpty,
  evidence: nonEmpty,
});
export const WorkMethodRunSchema = z
  .object({
    revision: index,
    state: z.enum(["ready", "working", "review", "closed"]),
    method: WorkMethodSchema.nullable(),
    step: index,
    reason: z.string().optional().default(""),
    current_actor: actor.nullish(),
    can_act: z.boolean().optional(),
    source: z.string().optional(),
    planned_delivery: GoalPlanDeliverySchema.nullish().catch(null),
    history: z
      .array(
        z.object({
          action: z.string(),
          candidate_failure: z.object({
            commit: z.string().regex(/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/),
            worker_task_id: nonEmpty,
            cases: z.array(nonEmpty).min(1),
            trace_revision: z.number().int().positive(),
          }).optional(),
          closeout: AcceptanceCloseoutSchema.optional().catch(undefined),
          step: index,
          from_step: index.optional(),
          previous_method: WorkMethodSchema.optional(),
          method_revision: z.number().int().positive().optional(),
          checked_items: z.array(index).nullish(),
          findings: z.array(finding).optional(),
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
export type NewWorkMethod = Omit<WorkMethod, "id" | "revision">;
export type WorkMethodRun = z.infer<typeof WorkMethodRunSchema>;
export type WorkMethodAction = { base_revision: number } & (
  | { action: "select"; method_id: string; method_revision?: number }
  | { action: "adopt_revision"; method_id: string; method_revision: number; evidence: string }
  | { action: "start"; method_id?: string; method_revision?: number }
  | {
      action: "advance";
      evidence: string;
      method_id?: string;
      method_revision?: number;
    }
  | { action: "approve"; evidence: string; checked_items?: number[] }
  | {
      action: "rework";
      evidence: string;
      target_step?: number;
      checked_items?: number[];
      findings?: z.infer<typeof finding>[];
    }
);
