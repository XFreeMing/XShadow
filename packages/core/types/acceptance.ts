import { z } from "zod";

const revision = z.number().int().nonnegative();
const version = z.number().int().positive();
const commit = z.string().regex(/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/);
export const AcceptanceTraceSchema = z.object({
  revision,
  criteria: z.array(
    z.object({
      id: z.string().min(1),
      versions: z.array(z.string().min(1)).min(1),
      cases: z.array(
        z.object({
          id: z.string(),
          specification: z.string(),
          runs: z.array(
            z.object({
              version,
              result: z.enum(["pass", "fail"]),
              candidate: commit,
              evidence: z.string(),
            }),
          ),
        }),
      ),
      implementations: z.array(
        z.object({
          version,
          repository: z.string(),
          branch: z.string(),
          commit,
        }),
      ),
    }),
  ),
});

export type AcceptanceTrace = z.infer<typeof AcceptanceTraceSchema>;
export type AcceptanceChange = {
  base_revision: number;
  criterion_id: string;
} & (
  | { action: "criterion"; text: string }
  | { action: "case"; case_id: string; text: string }
  | {
      action: "implementation";
      repository: string;
      branch: string;
      commit: string;
    }
  | {
      action: "run";
      case_id: string;
      result: "pass" | "fail";
      candidate: string;
      evidence: string;
    }
);
