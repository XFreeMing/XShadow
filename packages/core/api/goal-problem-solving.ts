import { z } from "zod";

const revision = z.number().int().positive();
const ref = z.object({ id: z.string(), revision });
const text = z.string().default("");
function list<T extends z.ZodType>(item: T) {
  return z.array(item).nullish().transform((value) => value ?? []);
}
const lines = list(z.string());
const charter = z.object({
  title: z.string(), intent: text, scope: text, acceptance_criteria: lines,
});
const goalVersion = z.object({ number: revision, charter, proposal_id: text, applied_by: text });

function history<T extends z.ZodRawShape>(detail: T) {
  return z.object({
    id: z.string(), current_revision: revision,
    revisions: z.array(z.object({ ...detail, revision })).min(1),
  });
}
const problem = history({
  statement: text, baseline: text, affected_users: lines, constraints: lines, success_criterion: text,
  status: text, authored_by: text, confirmed_by: text, reopen_reason: text,
});
const branch = history({
  title: text, problem_ref: ref, scope: text, boundary: text, rationale: text, change_reason: text,
}).extend({
  parent_branch_id: text, status: text,
  reassessments: list(z.object({
    challenged_by_result_id: z.string(), hypothesis_ref: ref, challenge_reason: text,
    resolved: z.boolean(), outcome: text, resolution_rationale: text, reopening_condition: text,
  })),
});
const hypothesis = history({
  branch_ref: ref, statement: text, falsification_criterion: text, assumptions: lines, change_reason: text,
}).extend({ branch_id: z.string() });
const protocol = history({
  hypothesis_ref: ref, predicted_result: text, sample_source: text, control_or_comparison: text,
  decision_threshold: text, stop_condition: text, status: text, authored_by: text, change_reason: text,
});
const evidence = z.object({ id: z.string(), kind: z.string(), issue_id: text, locator: text, summary: text });
const result = z.object({
  id: z.string(), batch_id: z.string(), hypothesis_ref: ref, protocol_ref: ref,
  observations: lines, evidence: list(evidence), recorded_by: text,
  review: z.object({
    verdict: z.string(), reviewer_id: text, rationale: text,
    late_for_current_revisions: z.boolean().default(false),
  }).nullish(),
});
const synthesis = history({
  base_goal_version: revision,
  claims: list(z.object({ id: z.string(), statement: text, result_ids: lines })),
  tradeoffs: lines, limitations: lines, recommendation: text, rationale: text, status: text,
  authored_by: text, reviewed_by: text, review_notes: text, reopen_reason: text,
  challenged_by: list(z.object({ result_id: z.string(), branch_id: z.string(), reason: text })),
});

export const GoalProblemSolvingSchema = z.object({
  goal_id: z.string().min(1), revision: z.number().int().nonnegative(),
  actor_id: z.string().min(1), can_decide: z.boolean(),
  current_result_ids: lines,
  state: z.object({
    goal_versions: z.array(goalVersion).min(1), goal_status: z.string(),
    problems: list(problem), branches: list(branch), hypotheses: list(hypothesis), protocols: list(protocol),
    prioritizations: list(z.object({
      id: z.string(), branch_ref: ref, disposition: text, rank: z.number().int().default(0),
      impact: text, controllability: text, cost: text, reopening_condition: text, rationale: text,
    })),
    validation_batches: list(z.object({
      id: z.string(), protocol_ref: ref, hypothesis_ref: ref, prior_batch_id: text, status: text,
      task: z.object({ issue_id: z.string(), submitter_id: text }),
    })),
    validation_results: list(result), syntheses: list(synthesis),
    decisions: list(z.object({
      id: z.string(), kind: text, goal_version: revision, synthesis_ref: ref,
      evidence_result_ids: lines, reason: text, review_trigger: text, decided_by: text,
    })),
    proposals: list(z.object({
      id: z.string(), base_goal_version: revision, synthesis_ref: ref, proposed_charter: charter,
      change_summary: text, proposed_by: text, status: text, applied_by: text,
      resulting_goal_version: z.number().int().default(0),
    })),
  }),
});

export type GoalProblemSolving = z.output<typeof GoalProblemSolvingSchema>;
export type GoalProblemState = GoalProblemSolving["state"];
export const goalProblemActions = [
  "define_problem", "confirm_problem", "revise_problem", "create_branch", "revise_branch", "reassess_branch",
  "create_hypothesis", "revise_hypothesis", "record_prioritization", "record_protocol", "approve_protocol",
  "revise_protocol", "start_validation_batch", "record_validation_result", "review_validation_result",
  "record_synthesis", "review_synthesis", "reopen_synthesis", "record_decision",
  "propose_charter_revision", "apply_charter_revision",
] as const;
export type GoalProblemAction = (typeof goalProblemActions)[number];
type CommandValue = string | number | CommandValue[] | { [key: string]: CommandValue };
export type GoalProblemCommandFields = Record<string, CommandValue>;
