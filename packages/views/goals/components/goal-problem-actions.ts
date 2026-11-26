import type {
  GoalProblemAction, GoalProblemCommandFields, GoalProblemSolving,
} from "@multica/core/goals";
import type { Issue } from "@multica/core/types";

export const problemDomains = ["problem", "decomposition", "priority", "validation", "analysis", "synthesis", "decision"] as const;
export type ProblemDomain = (typeof problemDomains)[number];
type Option = { value: string; label: string };
export type ProblemField = {
  name: string;
  mode?: "text" | "paragraph" | "list" | "number" | "select" | "multi" | "revision";
  options?: Option[];
  translated?: boolean;
  optional?: boolean;
  initial?: string;
};
type CommandFields = GoalProblemCommandFields;
export type ProblemAction = {
  action: GoalProblemAction;
  fields: ProblemField[];
  build?: (fields: CommandFields) => CommandFields;
  disabled?: boolean;
};

export function currentRevision<T extends { revision: number }>(item: { current_revision: number; revisions: T[] }) {
  return item.revisions.find((version) => version.revision === item.current_revision);
}

const field = (name: string, mode: ProblemField["mode"] = "text", optional = false): ProblemField => ({ name, mode, optional });
const paragraph = (name: string, optional = false) => field(name, "paragraph", optional);
const lines = (name: string, optional = false) => field(name, "list", optional);
const select = (name: string, options: Option[], mode: ProblemField["mode"] = "select", optional = false): ProblemField =>
  ({ name, options, mode, optional });
const choices = (name: string, values: string[]): ProblemField => ({
  ...select(name, values.map((value) => ({ value, label: value }))), translated: true,
});
const ref = (name: string, options: Option[]) => select(name, options, "revision");
const revisionOption = (id: string, revision: number, label: string): Option =>
  ({ value: `${revision}:${id}`, label: `${label} · v${revision}` });
const unique = (name: string) => ({ [name]: crypto.randomUUID() });
const decisionKinds = ["retain", "revise", "pause", "stop"];
const levels = ["low", "medium", "high"];

export function problemActions(snapshot: GoalProblemSolving, issues: Issue[]): Record<ProblemDomain, ProblemAction[]> {
  const { state, actor_id: actor, can_decide: canDecide } = snapshot;
  const goal = state.goal_versions.at(-1);
  const base = goal?.number ?? 1;
  const problems = state.problems.flatMap((item) => {
    const value = currentRevision(item);
    return value ? [{ item, value }] : [];
  });
  const branches = state.branches.flatMap((item) => {
    const value = currentRevision(item);
    return value ? [{ item, value }] : [];
  });
  const hypotheses = state.hypotheses.flatMap((item) => {
    const value = currentRevision(item);
    return value ? [{ item, value }] : [];
  });
  const protocols = state.protocols.flatMap((item) => {
    const value = currentRevision(item);
    return value ? [{ item, value }] : [];
  });
  const syntheses = state.syntheses.flatMap((item) => {
    const value = currentRevision(item);
    return value ? [{ item, value }] : [];
  });
  const problemRefs = (predicate: (typeof problems)[number]["value"]["status"]) =>
    problems.filter(({ value }) => value.status === predicate).map(({ item, value }) => revisionOption(item.id, value.revision, value.statement));
  const branchRefs = branches.map(({ item, value }) => revisionOption(item.id, value.revision, value.title));
  const hypothesisRefs = hypotheses.map(({ item, value }) => revisionOption(item.id, value.revision, value.statement));
  const protocolRefs = (status: string) => protocols.filter(({ value }) => value.status === status)
    .map(({ item, value }) => revisionOption(item.id, value.revision, value.predicted_result));
  const resultOptions = (results: typeof state.validation_results) =>
    results.map((result) => ({ value: result.id, label: result.observations[0] || result.id }));
  const currentResultIDs = new Set(snapshot.current_result_ids);
  const reviewedResults = resultOptions(state.validation_results.filter((result) => currentResultIDs.has(result.id)));
  const synthRefs = (status: string) => syntheses.filter(({ value }) => value.status === status && value.base_goal_version === base)
    .map(({ item, value }) => revisionOption(item.id, value.revision, value.rationale));
  const taskOptions = issues.map((issue) => ({ value: issue.id, label: `${issue.identifier} ${issue.title}` }));

  const problemFields = [
    paragraph("statement"), field("baseline"), lines("affected_users"), lines("constraints"), paragraph("success_criterion"),
  ];
  const branchFields = [ref("problem_ref", problemRefs("confirmed")), field("title"), paragraph("scope"), field("boundary"), paragraph("rationale")];
  const hypothesisFields = [ref("branch_ref", branchRefs), paragraph("statement"), paragraph("falsification_criterion"), lines("assumptions", true)];
  const protocolFields = [
    ref("hypothesis_ref", hypothesisRefs), field("predicted_result"), paragraph("sample_source"),
    paragraph("control_or_comparison"), field("decision_threshold"), field("stop_condition"),
  ];
  const synthesisFields = [
    paragraph("claim"), select("result_ids", reviewedResults, "multi"), lines("tradeoffs"),
    lines("limitations"), choices("recommendation", decisionKinds), paragraph("rationale"),
  ];
  const toSynthesis = ({ claim, result_ids, ...data }: CommandFields) => ({
    ...data, base_goal_version: base, claims: [{ ...unique("id"), statement: claim ?? "", result_ids: result_ids ?? [] }],
  });
  const approved = synthRefs("published");
  return {
    problem: [
      { action: "define_problem", fields: problemFields, build: (data) => ({ ...data, ...unique("problem_id") }) },
      { action: "confirm_problem", fields: [ref("problem_ref", problemRefs("draft"))] },
      { action: "revise_problem", fields: [
        select("problem_id", problems.map(({ item, value }) => ({ value: item.id, label: value.statement }))),
        ...problemFields, paragraph("reopen_reason"),
      ] },
    ],
    decomposition: [
      {
        action: "create_branch", fields: [
          ...branchFields, select("parent_branch_id", branches.map(({ item, value }) => ({ value: item.id, label: value.title })), "select", true),
        ], build: (data) => ({ ...data, ...unique("branch_id") }),
      },
      {
        action: "revise_branch", fields: [
          select("branch_id", branches.map(({ item, value }) => ({ value: item.id, label: value.title }))),
          ...branchFields, paragraph("change_reason"),
        ],
      },
      { action: "create_hypothesis", fields: hypothesisFields, build: (data) => ({ ...data, ...unique("hypothesis_id") }) },
      {
        action: "revise_hypothesis", fields: [
          select("hypothesis_id", hypotheses.map(({ item, value }) => ({ value: item.id, label: value.statement }))),
          ...hypothesisFields, paragraph("change_reason"),
        ],
      },
      {
        action: "reassess_branch", fields: [
          ref("branch_ref", branches.filter(({ item }) => item.reassessments.some((entry) => !entry.resolved))
            .map(({ item, value }) => revisionOption(item.id, value.revision, value.title))),
          select("challenged_by_result_id", resultOptions(state.validation_results.filter((result) => result.review?.verdict === "refuted"))),
          choices("outcome", ["revise_scope", "replace_hypothesis", "defer_branch", "retire_branch"]),
          paragraph("rationale"), field("new_title", "text", true), paragraph("new_scope", true),
          field("new_boundary", "text", true), field("reopening_condition", "text", true),
        ],
      },
    ],
    priority: [{
      action: "record_prioritization", fields: [
        ref("branch_ref", branchRefs), choices("disposition", ["prioritize", "defer"]),
        field("rank", "number", true), choices("impact", levels), choices("controllability", levels), choices("cost", levels),
        field("reopening_condition", "text", true), paragraph("rationale"),
        select("reconsiders_prioritization_id", state.prioritizations.filter((item) => item.disposition === "defer")
          .map((item) => ({ value: item.id, label: item.rationale })), "select", true),
      ], build: (data) => ({ ...data, ...unique("prioritization_id") }),
    }],
    validation: [
      { action: "record_protocol", fields: protocolFields, build: (data) => ({ ...data, ...unique("protocol_id") }) },
      { action: "approve_protocol", fields: [ref("protocol_ref", protocolRefs("draft"))] },
      {
        action: "revise_protocol", fields: [
          select("protocol_id", protocols.map(({ item, value }) => ({ value: item.id, label: value.predicted_result }))),
          ...protocolFields, paragraph("change_reason"),
        ],
      },
      {
        action: "start_validation_batch", fields: [
          ref("hypothesis_ref", hypothesisRefs), ref("protocol_ref", protocolRefs("approved")),
          select("issue_id", taskOptions, "select", true), field("new_task_title", "text", true),
          select("prior_batch_id", state.validation_batches.filter((item) =>
            state.validation_results.some((result) => result.batch_id === item.id && result.review?.verdict === "insufficient")).map((item) => ({
            value: item.id, label: taskOptions.find((task) => task.value === item.task.issue_id)?.label ?? item.id,
          })), "select", true),
        ], build: ({ issue_id, ...data }) => ({ ...data, ...unique("batch_id"), task: { issue_id: issue_id ?? "" } }),
      },
    ],
    analysis: [
      {
        action: "record_validation_result", fields: [
          select("batch_id", state.validation_batches.filter((item) => item.status === "running").map((item) => ({
            value: item.id, label: taskOptions.find((task) => task.value === item.task.issue_id)?.label ?? item.id,
          }))),
          lines("observations"), field("locator"), paragraph("summary"),
        ], build: ({ locator, summary, ...data }) => ({
          ...data, ...unique("result_id"), evidence: [{ ...unique("id"), kind: "artifact", locator: locator ?? "", summary: summary ?? "" }],
        }),
      },
      {
        action: "review_validation_result", fields: [
          select("result_id", resultOptions(state.validation_results.filter((item) => !item.review && item.recorded_by !== actor &&
            !state.validation_batches.some((batch) => batch.id === item.batch_id && batch.task.submitter_id === actor)))),
          choices("verdict", ["supported", "refuted", "insufficient"]), paragraph("rationale"),
        ],
      },
    ],
    synthesis: [
      { action: "record_synthesis", fields: synthesisFields, build: (data) => ({ ...toSynthesis(data), ...unique("synthesis_id") }) },
      { action: "review_synthesis", fields: [ref("synthesis_ref", synthRefs("draft")), paragraph("review_notes", true)] },
      {
        action: "reopen_synthesis", fields: [
          select("synthesis_id", syntheses.map(({ item, value }) => ({ value: item.id, label: value.rationale }))),
          ...synthesisFields, paragraph("reopen_reason"),
        ], build: toSynthesis,
      },
    ],
    decision: [
      {
        action: "record_decision", disabled: !canDecide, fields: [
          ref("synthesis_ref", approved), choices("kind", decisionKinds), paragraph("reason"),
          select("evidence_result_ids", reviewedResults, "multi"), field("review_trigger"),
        ], build: (data) => ({ ...data, ...unique("decision_id") }),
      },
      {
        action: "propose_charter_revision", fields: [
          ref("synthesis_ref", approved),
          select("decision_id", state.decisions.filter((item) => item.goal_version === base && item.kind === "revise")
            .map((item) => ({ value: item.id, label: item.reason })), "select", true),
          { ...field("title"), initial: goal?.charter.title }, { ...paragraph("intent"), initial: goal?.charter.intent },
          { ...paragraph("scope"), initial: goal?.charter.scope },
          { ...lines("acceptance_criteria"), initial: goal?.charter.acceptance_criteria.join("\n") }, paragraph("change_summary"),
        ], build: ({ title, intent, scope, acceptance_criteria, ...data }) => ({
          ...data, ...unique("proposal_id"), base_goal_version: base,
          proposed_charter: { title: title ?? "", intent: intent ?? "", scope: scope ?? "", acceptance_criteria: acceptance_criteria ?? [] },
        }),
      },
      {
        action: "apply_charter_revision", disabled: !canDecide,
        fields: [select("proposal_id", state.proposals.filter((item) => item.status === "open" && item.base_goal_version === base)
          .map((item) => ({ value: item.id, label: item.change_summary })))],
      },
    ],
  };
}

export function canOpenAction(action: ProblemAction) {
  return !action.disabled && action.fields.every((input) => input.optional || !input.options || input.options.length > 0);
}

export function readProblemForm(form: HTMLFormElement, action: ProblemAction): CommandFields {
  const data = new FormData(form);
  const fields: CommandFields = {};
  for (const input of action.fields) {
    const entries = data.getAll(input.name).filter((item): item is string => typeof item === "string");
    const value = entries[0]?.trim();
    if (!value) continue;
    switch (input.mode) {
      case "revision": {
        const separator = value.indexOf(":");
        fields[input.name] = { revision: Number(value.slice(0, separator)), id: value.slice(separator + 1) };
        break;
      }
      case "list": fields[input.name] = value.split("\n").map((line) => line.trim()).filter(Boolean); break;
      case "multi": fields[input.name] = entries; break;
      case "number": fields[input.name] = Number(value); break;
      default: fields[input.name] = value;
    }
  }
  return action.build ? action.build(fields) : fields;
}
