import { z } from "zod";

const revision = z.number().int().nonnegative();
const version = z.number().int().positive();
const commit = z.string().regex(/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/);
const sha256 = z.string().regex(/^[a-f0-9]{64}$/);
const result = z.enum(["pass", "fail"]);
const acceptanceRun = z.object({
  version,
  case_version: version.optional(),
  run_kind: z.string().optional(),
  expected_result: result.optional(),
  result,
  actor: z.string().optional(),
  task_id: z.string().optional(),
  recorded_at: z.string().optional(),
  candidate: commit,
  evidence: z.string(),
});
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
          artifact_sha256: sha256.optional(),
          previous_specifications: z.array(z.object({
            text: z.string(),
            artifact_sha256: sha256.optional(),
          })).optional(),
          runs: z.array(acceptanceRun),
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
      case_version?: number;
      artifact_sha256?: string;
      candidate: string;
      evidence: string;
    }
);

export const AcceptanceCloseoutSchema = z.object({
  trace_revision: revision,
  reconstructed: z.boolean().optional(),
  criteria: z.array(z.object({
    id: z.string(),
    version,
    summary: z.string(),
    candidate: commit.optional(),
    passed: z.boolean(),
    evidence: z.array(acceptanceRun.extend({
      case_id: z.string(),
      run_number: version,
      attachment_ids: z.array(z.string().uuid()).optional(),
    })),
  })),
});
export type AcceptanceCloseout = z.infer<typeof AcceptanceCloseoutSchema>;
