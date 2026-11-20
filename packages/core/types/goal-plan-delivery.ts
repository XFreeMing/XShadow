import { z } from "zod";

export const GoalPlanDeliveryTypeSchema = z.enum([
  "feature",
  "defect",
  "research",
  "outcome",
]);

export const GoalPlanDeliverySchema = z.object({
  type: GoalPlanDeliveryTypeSchema,
  method_id: z.string(),
  criteria: z.array(z.object({
    id: z.string(),
    expected: z.string(),
    verification: z.string(),
  })),
});

export type GoalPlanDeliveryType = z.infer<typeof GoalPlanDeliveryTypeSchema>;
export type GoalPlanDelivery = z.infer<typeof GoalPlanDeliverySchema>;
