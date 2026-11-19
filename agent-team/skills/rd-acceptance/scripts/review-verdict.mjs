#!/usr/bin/env node
// Deterministic adjudicator for the in_review acceptance step.
//
// It reads only platform-exported data: one issue record, its comments, and the
// workspace agent roster. Identities come from each comment's real author, never
// from a field an agent writes about itself. The acceptance automation engineer
// posts one machine-readable record per independent acceptance attempt; the
// quality manager posts one verdict per attempt.
//
// Input is JSON, never a live API call, so the same decision is reproducible
// from the issue's stored history.

import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { buildMemberTodo, titleCandidates, visibleViolations } from "./member-todo.mjs";

export const RECORD_MARKER = "multica-acceptance";
export const DECISION_MARKER = "multica-acceptance-decision";
export const MIN_FAILED_ROUNDS = 3;
const RESULTS = new Set(["passed", "failed", "not_verified"]);
const RECORD_KINDS = new Set(["attempt", "verdict", "exhausted"]);

const USAGE = `Usage:
  node review-verdict.mjs --issue <issue.json> --comments <comments.json> \\
    --agents <agents.json> --standard-version <id> --candidate <sha-or-version> \\
    --criteria <id,id,...> --owner-user-id <member-id> \\
    [--criteria-file <json>] [--existing-decisions <json>]... \\
    [--tester-role 验收自动化工程师] [--quality-role 质量交付经理] \\
    [--lead-roles 研发经理,项目经理] \\
    [--done-status done] [--back-status in_progress] [--blocked-status blocked] \\
    [--decision-dir <dir>] [--output json|text]

--issue              JSON from \`multica issue get <issue> --output json\`.
--comments           JSON from \`multica issue comment list <issue> --full --output json\`,
                     or any array of comment objects.
--agents             JSON from \`multica agent list --output json\`. Roles are
                     resolved by name; every record's author is checked against
                     the resolved role, so an agent cannot sign its own work.
--standard-version   Frozen acceptance criteria version under test.
--candidate          Current candidate revision. A pass for another revision
                     never ends the story.
--criteria           The complete frozen criterion id set, comma separated.
--owner-user-id      Human member who receives an escalated decision. Required,
                     so the escalation path never blocks on a second run.
--existing-decisions JSON issue list/search result; repeat to pass several.
--decision-dir       Write decision title.txt and body.md there.

Machine records live in comments as HTML comments:
  <!-- multica-acceptance {"kind":"attempt",...} -->
See references/review-entry.md for the record shapes and the step procedure.
`;

function parseArgs(argv) {
  const args = { output: "json", existingDecisions: [] };
  for (let i = 0; i < argv.length; i++) {
    const token = argv[i];
    if (token === "-h" || token === "--help") {
      args.help = true;
      continue;
    }
    if (!token.startsWith("--")) throw new Error(`unexpected argument: ${token}`);
    const eq = token.indexOf("=");
    const name = eq === -1 ? token.slice(2) : token.slice(2, eq);
    let value;
    if (eq !== -1) value = token.slice(eq + 1);
    else {
      value = argv[i + 1];
      if (value === undefined || value.startsWith("--")) throw new Error(`missing value for --${name}`);
      i++;
    }
    const key = name.replace(/-([a-z])/g, (_, c) => c.toUpperCase());
    if (key === "existingDecisions") args.existingDecisions.push(value);
    else args[key] = value;
  }
  return args;
}

function readJson(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    throw new Error(`read ${label} (${path}): ${error.message}`);
  }
}

function text(value) {
  return typeof value === "string" && value.trim() !== "";
}

function asRows(input, keys) {
  if (Array.isArray(input)) return input;
  for (const key of keys) if (Array.isArray(input?.[key])) return input[key];
  return null;
}

function timeValue(value) {
  const parsed = Date.parse(value ?? "");
  return Number.isNaN(parsed) ? null : parsed;
}

function normalizeComments(input) {
  const rows = asRows(input, ["comments", "items"]);
  if (!rows) throw new Error("comments input must be an array, {comments: [...]} or {items: [...]}");
  return rows.map((comment, index) => ({
    id: text(comment?.id) ? comment.id : `#${index}`,
    content: typeof comment?.content === "string" ? comment.content : "",
    created_at: text(comment?.created_at) ? comment.created_at : null,
    author_id: text(comment?.author_id) ? comment.author_id : null,
    author_type: text(comment?.author_type) ? comment.author_type : null,
    input_order: index,
  }));
}

// Platform order, not the order the caller's JSON happened to list comments in.
function compareRecords(a, b) {
  const at = timeValue(a.created_at);
  const bt = timeValue(b.created_at);
  if (at !== bt) {
    if (at === null) return -1;
    if (bt === null) return 1;
    return at < bt ? -1 : 1;
  }
  if (a.comment_id !== b.comment_id) return a.comment_id < b.comment_id ? -1 : 1;
  return a.order_in_comment - b.order_in_comment;
}

export function extractRecords(comments) {
  const rows = [];
  const ignored = [];
  const marker = new RegExp(`<!--\\s*${RECORD_MARKER}\\s+([\\s\\S]*?)-->`, "g");

  for (const comment of normalizeComments(comments)) {
    let order = 0;
    for (const match of comment.content.matchAll(marker)) {
      const position = order++;
      let record;
      try {
        record = JSON.parse(match[1]);
      } catch (error) {
        ignored.push({ comment_id: comment.id, reason: `invalid JSON: ${error.message}` });
        continue;
      }
      if (!RECORD_KINDS.has(record?.kind)) {
        ignored.push({ comment_id: comment.id, reason: `unknown record kind: ${String(record?.kind)}` });
        continue;
      }
      if (!text(record.standard_version)) {
        ignored.push({ comment_id: comment.id, reason: "missing standard_version" });
        continue;
      }
      const row = {
        ...record,
        comment_id: comment.id,
        created_at: comment.created_at,
        author_id: comment.author_id,
        author_type: comment.author_type,
        order_in_comment: position,
      };
      if (record.kind !== "exhausted" && !text(record.attempt_id)) {
        ignored.push({ comment_id: comment.id, reason: `missing attempt_id on ${record.kind} record` });
        continue;
      }
      if (record.kind !== "exhausted" && !RESULTS.has(record.outcome)) {
        ignored.push({ comment_id: comment.id, reason: `invalid outcome: ${String(record.outcome)}` });
        continue;
      }
      rows.push(row);
    }
  }
  rows.sort(compareRecords);
  return {
    attempts: rows.filter((row) => row.kind === "attempt"),
    verdicts: rows.filter((row) => row.kind === "verdict"),
    exhausted: rows.filter((row) => row.kind === "exhausted"),
    ignored,
  };
}

export function resolveRoles(agents, { testerRole = "验收自动化工程师", qualityRole = "质量交付经理", leadRoles = ["研发经理", "项目经理"] } = {}) {
  const rows = asRows(agents, ["agents", "items"]);
  if (!rows) throw new Error("agents input must be an array, {agents: [...]} or {items: [...]}");
  const pick = (name) => {
    const matches = rows.filter((row) => row?.name === name && text(row?.id));
    if (matches.length === 0) throw new Error(`workspace has no agent named ${name}`);
    if (matches.length > 1) throw new Error(`workspace has more than one agent named ${name}`);
    return { id: matches[0].id, name };
  };
  const tester = pick(testerRole);
  const quality = pick(qualityRole);
  if (tester.id === quality.id) throw new Error("tester and quality manager must be different agents");
  const leads = leadRoles.map(pick);
  return { tester, quality, leads, agentIds: new Set(rows.map((row) => row?.id).filter(text)) };
}

function attemptKey(standardVersion, attemptId) {
  return `${standardVersion}\u0000${attemptId}`;
}

// One attempt id identifies one independent acceptance of one candidate. A
// later record for the same candidate is a corrected revision of that round,
// so the latest revision wins. Reusing the id for another candidate is
// rejected: the two rounds are not comparable.
function distinctAttempts(attempts) {
  const groups = new Map();
  const rejected = [];
  for (const attempt of attempts) {
    const key = attemptKey(attempt.standard_version, attempt.attempt_id);
    const group = groups.get(key);
    if (!group) {
      groups.set(key, { candidate: attempt.candidate_version ?? null, records: [attempt] });
      continue;
    }
    if ((attempt.candidate_version ?? null) !== group.candidate) {
      rejected.push({
        kind: "attempt",
        attempt_id: attempt.attempt_id,
        comment_id: attempt.comment_id,
        reason: `attempt_id reused for another candidate (${group.candidate} vs ${attempt.candidate_version ?? "none"})`,
      });
      continue;
    }
    group.records.push(attempt);
  }
  const ordered = [];
  const replays = [];
  for (const group of groups.values()) {
    const winner = group.records.at(-1);
    ordered.push(winner);
    for (const row of group.records.slice(0, -1)) replays.push({ attempt_id: row.attempt_id, comment_id: row.comment_id });
  }
  ordered.sort(compareRecords);
  return { ordered, replays, rejected };
}

// A verdict can only judge evidence that already existed. An earlier verdict
// does not apply to a corrected attempt, so the quality manager must
// re-adjudicate; among valid verdicts the latest wins.
function latestVerdicts(verdicts, attempts) {
  const attemptByKey = new Map(attempts.map((attempt) => [attemptKey(attempt.standard_version, attempt.attempt_id), attempt]));
  const latest = new Map();
  const rejected = [];
  for (const verdict of verdicts) {
    const key = attemptKey(verdict.standard_version, verdict.attempt_id);
    const attempt = attemptByKey.get(key);
    if (!attempt) {
      rejected.push({ kind: "verdict", attempt_id: verdict.attempt_id, comment_id: verdict.comment_id, reason: "verdict has no surviving attempt" });
      continue;
    }
    if (compareRecords(verdict, attempt) <= 0) {
      rejected.push({ kind: "verdict", attempt_id: verdict.attempt_id, comment_id: verdict.comment_id, reason: "verdict precedes the attempt it judges" });
      continue;
    }
    latest.set(key, verdict);
  }
  return { latest, rejected };
}

function evidenceFor(attempt, criterion) {
  const items = Array.isArray(attempt.evidence) ? attempt.evidence : [];
  return items.filter((item) => item && (item.criterion === criterion || item.id === criterion));
}

// Text alone is not a run. Every counted item needs the command, its exit code
// and a log or artifact to inspect.
function hasRunEvidence(item) {
  return text(item.command) && Number.isInteger(item.exit_code) && (text(item.log) || text(item.artifact));
}

function criterionRunEvidence(attempt, criterion) {
  return evidenceFor(attempt, criterion).filter(hasRunEvidence);
}

function evidenceShowsFailure(attempt) {
  const items = Array.isArray(attempt.evidence) ? attempt.evidence : [];
  return items.some((item) => item?.result === "failed" || (Number.isInteger(item?.exit_code) && item.exit_code !== 0));
}

function criteriaCoverage(verdict, criteria) {
  const rows = Array.isArray(verdict.criteria) ? verdict.criteria : [];
  const byId = new Map();
  for (const row of rows) {
    const id = row?.criterion ?? row?.id;
    if (text(id)) byId.set(id, row);
  }
  const missing = criteria.filter((id) => !byId.has(id));
  const extra = [...byId.keys()].filter((id) => !criteria.includes(id));
  const invalid = [...byId.values()].filter((row) => !RESULTS.has(row?.result) || !text(row?.evidence));
  const malformed = rows.filter((row) => !text(row?.criterion ?? row?.id)).length;
  return { byId, missing, extra, invalid, malformed, complete: missing.length === 0 && extra.length === 0 && invalid.length === 0 && malformed === 0 };
}

function planText(plan) {
  if (typeof plan === "string") return text(plan) ? plan : null;
  const label = plan?.plan ?? plan?.summary;
  return text(label) ? label : null;
}

function isPass(attempt, verdict, criteria) {
  if (!verdict || attempt.outcome !== "passed" || verdict.outcome !== "passed") return false;
  if (attempt.environment_error === true || verdict.environment_error === true) return false;
  if (evidenceShowsFailure(attempt)) return false;
  const coverage = criteriaCoverage(verdict, criteria);
  if (!coverage.complete) return false;
  return criteria.every((id) => {
    if (coverage.byId.get(id).result !== "passed") return false;
    const items = criterionRunEvidence(attempt, id);
    return items.length > 0 && items.every((item) => item.result === "passed" && item.exit_code === 0);
  });
}

// Only a real, actually-failing, independently-verified attempt counts. A
// missing environment, text-only evidence, an unverified criterion or a
// verdict that contradicts the evidence never does.
function isRealFailure(attempt, verdict, criteria) {
  if (!verdict || attempt.outcome !== "failed" || verdict.outcome !== "failed") return false;
  if (attempt.environment_error === true || verdict.environment_error === true) return false;
  const coverage = criteriaCoverage(verdict, criteria);
  if (!coverage.complete) return false;
  // An unverified criterion means the round is not a complete failure: the
  // missing check may be what would have passed, so it cannot drive an escalation.
  if ([...coverage.byId.values()].some((row) => row.result === "not_verified")) return false;
  const failed = [...coverage.byId.entries()].filter(([, row]) => row.result === "failed");
  if (failed.length === 0) return false;
  if (failed.some(([id]) => criterionRunEvidence(attempt, id).length === 0)) return false;
  if (!failed.some(([id]) => criterionRunEvidence(attempt, id).some((item) => item.result === "failed" || item.exit_code !== 0))) return false;
  return true;
}

function inconsistentVerdict(attempt, verdict, criteria) {
  if (!verdict) return null;
  const coverage = criteriaCoverage(verdict, criteria);
  if (!coverage.complete) return null;
  const runEvidenceComplete = criteria.every((id) => criterionRunEvidence(attempt, id).length > 0);
  if (!runEvidenceComplete) return null;
  if (verdict.outcome === "passed" && (attempt.outcome === "failed" || evidenceShowsFailure(attempt))) {
    return "证据显示测试失败，但裁定为通过";
  }
  if (verdict.outcome === "failed" && attempt.outcome === "passed" && !evidenceShowsFailure(attempt)) {
    return "证据没有失败记录，但裁定为失败";
  }
  return null;
}

function failureReason(attempt, verdict, criteria) {
  if (!verdict) return "尚无独立裁定";
  if (attempt.environment_error === true || verdict.environment_error === true) return "环境错误，不计真实失败";
  const coverage = criteriaCoverage(verdict, criteria);
  const missingRun = criteria.filter((id) => criterionRunEvidence(attempt, id).length === 0);
  if (missingRun.length > 0) return `缺少命令、返回码或日志证据：${missingRun.join("、")}`;
  if (coverage.missing.length > 0) return `裁定缺少冻结标准：${coverage.missing.join("、")}`;
  if (coverage.extra.length > 0) return `裁定包含冻结标准之外的标准：${coverage.extra.join("、")}`;
  if (!coverage.complete) return "裁定未为每条标准写明结果和证据位置";
  if ([...coverage.byId.values()].some((row) => row.result === "not_verified")) return "存在未验证的标准";
  if (verdict.outcome === "passed") return "该轮通过";
  if (verdict.outcome !== "failed") return "不是失败裁定";
  return "证据没有对应的失败记录";
}

export function decisionKey(issue, standardVersion) {
  const digest = createHash("sha256").update(`${issue.id}|${standardVersion}`).digest("hex").slice(0, 16);
  return `acc-${digest}`;
}

export function decisionKeyFromTitle(title) {
  // Historical decisions use 「验收决策 <key> …」. New decisions keep the key
  // out of the visible title, so their body marker and metadata are the
  // identity; this legacy pattern stays readable for migration and search.
  const match = String(title ?? "").match(/验收决策\s+(acc-[0-9a-f]{16})/);
  return match ? match[1] : null;
}

// Metadata and search can return the same issue twice, and a search hit may
// carry only its fixed title. Merge by UUID/identifier and read the key from
// metadata, the body or the fixed title so one issue never duplicates itself.
function normalizeDecisions(inputs) {
  const list = Array.isArray(inputs) ? inputs : [inputs];
  const rows = list.flatMap((input) => asRows(input, ["issues", "items"]) ?? []);
  const parsed = rows
    .map((row) => {
      const description = typeof row?.description === "string" ? row.description : "";
      const marker = description.match(new RegExp(`<!--\\s*${DECISION_MARKER}\\s+([\\s\\S]*?)-->`));
      const keyLine = description.match(/decision_key:\s*(acc-[0-9a-f]{16})/);
      let bodyKey = null;
      if (marker) {
        try {
          bodyKey = JSON.parse(marker[1])?.decision_key ?? null;
        } catch {
          bodyKey = null;
        }
      }
      return {
        id: text(row?.id) ? row.id : null,
        identifier: text(row?.identifier) ? row.identifier : null,
        decision_key: row?.metadata?.acceptance_decision_key ?? bodyKey ?? (keyLine ? keyLine[1] : null) ?? decisionKeyFromTitle(row?.title),
        created_at: text(row?.created_at) ? row.created_at : null,
        status: text(row?.status) ? row.status : null,
        title: text(row?.title) ? row.title : null,
      };
    })
    .filter((row) => text(row.decision_key) && (text(row.identifier) || text(row.id)));

  const groups = [];
  const byAlias = new Map();
  for (const row of parsed) {
    const aliases = [...new Set([row.id && `id:${row.id}`, row.identifier && `number:${row.identifier}`].filter(Boolean))];
    const found = [...new Set(aliases.map((alias) => byAlias.get(alias)).filter(Boolean))];
    let group = found[0];
    if (!group) {
      group = { rows: [] };
      groups.push(group);
    }
    for (const other of found.slice(1)) {
      if (other === group) continue;
      group.rows.push(...other.rows);
      other.rows = [];
      for (const [alias, owner] of byAlias) if (owner === other) byAlias.set(alias, group);
    }
    group.rows.push(row);
    for (const alias of aliases) byAlias.set(alias, group);
  }

  return groups
    .map((group) => {
      const dated = group.rows.filter((row) => row.created_at)
        .sort((a, b) => (timeValue(a.created_at) ?? 0) - (timeValue(b.created_at) ?? 0) || a.created_at.localeCompare(b.created_at));
      return {
        decision_key: group.rows.find((row) => text(row.decision_key))?.decision_key ?? null,
        identifier: group.rows.find((row) => text(row.identifier))?.identifier ?? null,
        created_at: dated[0]?.created_at ?? null,
        status: group.rows.find((row) => text(row.status))?.status ?? null,
        title: group.rows.find((row) => text(row.title))?.title ?? null,
      };
    })
    .filter((row) => text(row.decision_key) && text(row.identifier));
}

function formatCriteria(coverage) {
  return [...coverage.byId.entries()]
    .filter(([, row]) => row.result === "failed")
    .map(([id, row]) => `- ${id}：${row.evidence}`)
    .join("\n");
}

// The person receiving the decision must learn which capability is broken and
// what each option actually delivers. The lead supplies that as
// `decision_brief`; the tool never invents a generic "this feature" fallback.
const GENERIC_SUBJECTS = new Set(["这项功能", "这个功能", "功能", "该功能", "此功能", "这个故事", "这个任务", "该事项"]);
const INTERNAL_EVIDENCE = /日志|脚本|复跑|测试报告|回执|命令行|运行测试|执行命令|构建产物|验收回执|验收结果|验收记录|复验|测试用例|测试结果|测试日志|跑(?:一遍|一下)?测试|自测/;

function normalizeDecisionBrief(brief) {
  if (!brief || typeof brief !== "object" || Array.isArray(brief)) return null;
  const subject = text(brief.subject) ? brief.subject.trim() : null;
  const problem = text(brief.problem) ? brief.problem.trim() : null;
  const why = text(brief.why_you) ? brief.why_you.trim() : null;
  const action = text(brief.action) ? brief.action.trim() : null;
  const title = text(brief.title) ? brief.title.trim() : null;
  const rows = Array.isArray(brief.options) ? brief.options : [];
  if (!subject || GENERIC_SUBJECTS.has(subject) || rows.length < 2) return null;
  if (!problem || INTERNAL_EVIDENCE.test(problem)) return null;
  for (const value of [subject, problem, why, action, title]) {
    if (value && (INTERNAL_EVIDENCE.test(value) || visibleViolations(value).length > 0)) return null;
  }
  const options = [];
  for (const row of rows) {
    const label = text(row?.label) ? row.label.trim() : null;
    const consequence = text(row?.consequence ?? row?.effect) ? String(row.consequence ?? row.effect).trim() : null;
    if (!label || !consequence || row?.default !== undefined || row?.recommended !== undefined) return null;
    if (INTERNAL_EVIDENCE.test(label) || INTERNAL_EVIDENCE.test(consequence)) return null;
    const example = text(row?.example) ? row.example.trim() : null;
    // Without a reply example the person has to invent the wording; with an
    // internal-verification example the team hands its own work to the person.
    if (!example || INTERNAL_EVIDENCE.test(example)) return null;
    for (const value of [label, consequence, example]) {
      if (value && visibleViolations(value).length > 0) return null;
    }
    options.push({ label, consequence, example });
  }
  return {
    subject, problem, options,
    ...(why ? { why_you: why } : {}),
    ...(action ? { action } : {}),
    ...(title ? { title } : {}),
  };
}

function decisionResume(issue, exhaustion) {
  return {
    signal: "done",
    agent_id: exhaustion.lead_agent_id,
    agent_name: exhaustion.lead,
    instruction: "成员已在决策事项完成选择：读取该事项的最新结论和所选选项，按选择继续、缩小范围或停止原事项，并在原事项记录处理结果。",
    command_template: `"$MULTICA_CLI_PATH" issue wakeup create ${issue.identifier} --until-issue <新决策事项> --until-issue-state done --mode once --agent-id ${exhaustion.lead_agent_id} --instruction-file .acceptance/decision-resume-instruction.md`,
  };
}

function buildDecision({ issue, standardVersion, candidate, rounds, counted, exhaustion, ownerUserId, brief }) {
  const key = decisionKey(issue, standardVersion);
  const backlink = `[${issue.identifier}](mention://issue/${issue.id})`;
  const evidence = counted
    .map((row, index) => {
      const failed = formatCriteria(row.coverage);
      return `第 ${index + 1} 轮（尝试 ${row.attempt.attempt_id}，候选 ${row.attempt.candidate_version}，证据评论 ${row.attempt.comment_id}）\n${failed}`;
    })
    .join("\n\n");
  const plans = exhaustion.plans
    .map((plan, index) => {
      const label = planText(plan);
      if (!label) return null;
      const result = typeof plan === "object" && plan ? plan.result ?? plan.outcome : null;
      return `方案 ${index + 1}：${label}${result ? `（结果：${result}）` : ""}`;
    })
    .filter(Boolean)
    .join("\n");
  const todo = buildMemberTodo({
    kind: "decision",
    title: brief.title ?? `「${brief.subject}」需要你做一个取舍（${issue.identifier}）`,
    recipient: { id: ownerUserId },
    source: { identifier: issue.identifier, id: issue.id },
    request: "acceptance-decision",
    completion_signal: "done",
    decision_key: key,
    problem: brief.problem,
    why_you: brief.why_you ?? "这一步怎么取舍，只有你能决定。",
    action: brief.action ?? `选一个接下来怎么处理「${brief.subject}」的做法。`,
    where: "直接在本事项回复你的选择。",
    options: brief.options,
    submit: "回复选项名称或序号。",
    technical: {
      原事项: backlink,
      当前候选: candidate,
      验收标准版本: standardVersion,
      累计失败: `${rounds} 个不同候选`,
      失败标准: evidence,
      尝试过的方案: plans || "（无）",
      团队结论: `${exhaustion.lead}：${exhaustion.reason}`,
    },
    markers: [
      `<!-- ${DECISION_MARKER} ${JSON.stringify({ decision_key: key, standard_version: standardVersion, candidate, original_issue: issue.identifier })} -->`,
    ],
  });
  return {
    key,
    todo_key: todo.todo_key,
    metadata_key: "acceptance_decision_key",
    metadata: todo.metadata,
    action: "create",
    existing_identifier: null,
    duplicates: [],
    assignee_user_id: ownerUserId,
    title: todo.title,
    title_candidates: titleCandidates(todo.title),
    description: todo.description,
    first_screen: todo.first_screen,
    resume: decisionResume(issue, exhaustion),
  };
}

function normalizeCriteria(input) {
  const rows = Array.isArray(input) ? input : String(input ?? "").split(",");
  const criteria = [...new Set(rows.map((row) => String(row).trim()).filter(Boolean))];
  if (criteria.length === 0) throw new Error("criteria must list at least one frozen criterion id");
  return criteria;
}

export function adjudicate({
  issue,
  comments,
  agents,
  standardVersion,
  candidate,
  criteria,
  ownerUserId,
  existingDecisions = [],
  testerRole = "验收自动化工程师",
  qualityRole = "质量交付经理",
  leadRoles = ["研发经理", "项目经理"],
  doneStatus = "done",
  backStatus = "in_progress",
  blockedStatus = "blocked",
}) {
  if (!issue || !text(issue.id) || !text(issue.identifier)) throw new Error("issue input needs id and identifier");
  if (!text(standardVersion)) throw new Error("standardVersion is required");
  if (!text(candidate)) throw new Error("candidate is required");
  const criteriaIds = normalizeCriteria(criteria);
  if (!text(ownerUserId)) throw new Error("ownerUserId is required; pass the human from identity configuration");
  const roles = resolveRoles(agents, { testerRole, qualityRole, leadRoles });
  if (roles.agentIds.has(ownerUserId)) throw new Error("decision owner must be a human member, not an agent");
  if (issue.creator_type !== "member" && issue.creator_id === ownerUserId) {
    throw new Error("decision owner must be the configured human, not the issue's agent creator");
  }

  const { attempts: allAttempts, verdicts: allVerdicts, exhausted: allExhausted, ignored } = extractRecords(comments);
  const rejected = [];
  const keep = (row, roleId, label) => {
    if (row.author_type !== "agent" || row.author_id !== roleId) {
      rejected.push({ comment_id: row.comment_id, kind: row.kind, reason: `${label} must be authored by the platform role, not a self-declared field` });
      return false;
    }
    return true;
  };
  const attempts = allAttempts.filter((row) => keep(row, roles.tester.id, "acceptance attempt"));
  const verdicts = allVerdicts.filter((row) => keep(row, roles.quality.id, "verdict"));
  const leadIds = new Set(roles.leads.map((lead) => lead.id));
  const exhausted = allExhausted.filter((row) => {
    if (row.author_type !== "agent" || !leadIds.has(row.author_id)) {
      rejected.push({ comment_id: row.comment_id, kind: row.kind, reason: "no-plan declaration must be authored by a team lead platform role" });
      return false;
    }
    return true;
  });

  const { ordered, replays, rejected: attemptRejections } = distinctAttempts(attempts);
  const { latest: verdictByKey, rejected: verdictRejections } = latestVerdicts(verdicts, ordered);
  const latestVerdictFor = (attempt) => verdictByKey.get(attemptKey(attempt.standard_version, attempt.attempt_id)) ?? null;

  const scoped = ordered.filter((attempt) => attempt.standard_version === standardVersion);
  const otherVersions = [...new Set(ordered.map((a) => a.standard_version).filter((v) => v !== standardVersion))];
  const current = scoped.filter((attempt) => attempt.candidate_version === candidate);
  const latest = current.at(-1) ?? null;
  const latestVerdict = latest ? latestVerdictFor(latest) : null;

  const countedAll = scoped
    .map((attempt) => ({ attempt, verdict: latestVerdictFor(attempt) }))
    .filter((row) => isRealFailure(row.attempt, row.verdict, criteriaIds))
    .map((row) => ({ ...row, coverage: criteriaCoverage(row.verdict, criteriaIds) }));
  const countedCandidates = new Set(countedAll.map((row) => row.attempt.candidate_version));
  const rounds = countedCandidates.size;
  const counted = countedAll.filter((row, index, rows) => rows.findIndex((other) => other.attempt.candidate_version === row.attempt.candidate_version) === index);
  // A no-plan declaration must come after the independent verdict that failed
  // the current candidate, not merely after the attempt: evidence posted before
  // adjudication cannot justify giving up.
  const currentFailures = countedAll.filter((row) => row.attempt.candidate_version === candidate);
  const currentFailure = currentFailures.at(-1) ?? null;

  const exhaustionRows = exhausted.filter((row) => row.standard_version === standardVersion);
  const exhaustionRow = exhaustionRows.at(-1) ?? null;
  const exhaustion = exhaustionRow
    ? {
        lead_agent_id: exhaustionRow.author_id,
        lead: roles.leads.find((lead) => lead.id === exhaustionRow.author_id)?.name ?? null,
        reason: text(exhaustionRow.reason) ? exhaustionRow.reason : null,
        executable_plan: text(exhaustionRow.executable_plan) ? exhaustionRow.executable_plan : null,
        plans: Array.isArray(exhaustionRow.plans) ? exhaustionRow.plans : [],
        decision_brief: exhaustionRow.decision_brief ?? null,
        names_attempt: exhaustionRow.attempt_id ?? null,
        comment_id: exhaustionRow.comment_id,
        after_latest_failure: Boolean(currentFailure) && compareRecords(exhaustionRow, currentFailure.verdict) > 0,
        names_latest_failure: Boolean(currentFailure) && exhaustionRow.attempt_id === currentFailure.attempt.attempt_id,
        brief_valid: false,
        valid: false,
      }
    : null;
  if (exhaustion) {
    const namedPlans = exhaustion.plans.map(planText).filter(Boolean);
    exhaustion.valid = Boolean(
      exhaustion.after_latest_failure &&
      exhaustion.names_latest_failure &&
      !exhaustion.executable_plan &&
      text(exhaustion.reason) &&
      namedPlans.length === exhaustion.plans.length &&
      namedPlans.length > 0,
    );
    exhaustion.brief_valid = Boolean(normalizeDecisionBrief(exhaustion.decision_brief));
  }

  const notCounted = scoped
    .filter((attempt) => !countedAll.some((row) => row.attempt.attempt_id === attempt.attempt_id))
    .map((attempt) => {
      const verdict = latestVerdictFor(attempt);
      return {
        attempt_id: attempt.attempt_id,
        candidate_version: attempt.candidate_version ?? null,
        verdict_outcome: verdict?.outcome ?? null,
        reason: failureReason(attempt, verdict, criteriaIds),
      };
    });

  const base = {
    tool: "review-verdict",
    version: 2,
    issue: { id: issue.id, identifier: issue.identifier, title: issue.title ?? "" },
    standard_version: standardVersion,
    candidate,
    criteria: criteriaIds,
    decision_key: decisionKey(issue, standardVersion),
    roles: {
      tester: roles.tester,
      quality: roles.quality,
      leads: roles.leads,
    },
    rounds: { count: rounds, required: MIN_FAILED_ROUNDS, remaining: Math.max(0, MIN_FAILED_ROUNDS - rounds) },
    latest_attempt: latest ? { attempt_id: latest.attempt_id, candidate_version: latest.candidate_version, comment_id: latest.comment_id } : null,
    latest_verdict: latestVerdict ? { outcome: latestVerdict.outcome, comment_id: latestVerdict.comment_id } : null,
    counted_failures: counted.map((row) => ({
      attempt_id: row.attempt.attempt_id,
      candidate_version: row.attempt.candidate_version ?? null,
      comment_id: row.attempt.comment_id,
      verdict_comment_id: row.verdict.comment_id,
    })),
    not_counted: notCounted,
    replays,
    other_standard_versions: otherVersions,
    ignored: [...ignored, ...rejected, ...attemptRejections, ...verdictRejections],
    exhaustion,
    wake_tester: null,
    decision: null,
    status_command: null,
    next_action: null,
    inconsistent: false,
    outcome: null,
  };

  if (!latest) {
    return {
      ...base,
      outcome: "run_acceptance",
      next_action: `候选 ${candidate} 尚无本轮验收记录：先按冻结标准执行验收代码，在原事项提交一条证据评论，再交给质量交付经理裁定。不要推进状态。`,
    };
  }

  if (!latestVerdict) {
    return {
      ...base,
      outcome: "awaiting_verdict",
      next_action: "已有验收证据但缺少质量交付经理的独立裁定：用有效 mention 实际唤醒质量交付经理，不要自行判定通过或推进状态。",
    };
  }

  const inconsistency = inconsistentVerdict(latest, latestVerdict, criteriaIds);
  if (inconsistency) {
    return {
      ...base,
      outcome: "awaiting_verdict",
      inconsistent: true,
      next_action: `${inconsistency}：不推进状态，交质量交付经理按冻结标准重新裁定。`,
    };
  }

  if (isPass(latest, latestVerdict, criteriaIds)) {
    return {
      ...base,
      outcome: "done",
      status_command: `"$MULTICA_CLI_PATH" issue status ${issue.identifier} ${doneStatus}`,
      next_action: "当前候选的全部冻结标准通过且证据齐全：由步骤负责人把原事项标记为 done。自动 done 只代表故事验收通过，不构成发布、合并或日常升级授权。",
    };
  }

  if (latestVerdict.outcome === "passed") {
    const coverage = criteriaCoverage(latestVerdict, criteriaIds);
    const missingRun = criteriaIds.filter((id) => criterionRunEvidence(latest, id).length === 0);
    const gaps = [];
    if (missingRun.length > 0) gaps.push(`缺少命令、返回码或日志：${missingRun.join("、")}`);
    if (coverage.missing.length > 0) gaps.push(`裁定缺少冻结标准：${coverage.missing.join("、")}`);
    if (coverage.extra.length > 0) gaps.push(`裁定含冻结标准之外的条目：${coverage.extra.join("、")}`);
    if (coverage.invalid.length > 0) gaps.push("部分标准没有写明结果或证据位置");
    return {
      ...base,
      outcome: "awaiting_verdict",
      next_action: `本轮裁定为通过，但证据不足以结项（${gaps.join("；") || "裁定与证据不一致"}）。不推进状态，也不计失败轮次：把缺失的命令、返回码、日志或标准项补进同一次验收，再由质量交付经理重新裁定。`,
    };
  }

  if (!isRealFailure(latest, latestVerdict, criteriaIds)) {
    const env = latest.environment_error === true || latestVerdict.environment_error === true;
    const coverage = criteriaCoverage(latestVerdict, criteriaIds);
    const unverified = [...coverage.byId.values()].some((row) => row.result === "not_verified");
    if (env || unverified || latestVerdict.outcome === "not_verified") {
      return {
        ...base,
        outcome: "rework",
        status_command: `"$MULTICA_CLI_PATH" issue status ${issue.identifier} ${backStatus}`,
        next_action: "本轮不计真实失败（环境错误或存在未验证项）：回到原交付团队补齐运行环境或必验手验项，重新执行同一冻结标准；下一轮验收重新提交证据，不用重放累计轮次。",
      };
    }
    return {
      ...base,
      outcome: "awaiting_verdict",
      next_action: "裁定为失败，但证据或冻结标准不完整，不能累计为真实失败：补齐逐条标准的命令、返回码、日志和结果后，交质量交付经理重新裁定，不推进状态。",
    };
  }

  if (rounds >= MIN_FAILED_ROUNDS && exhaustion?.valid) {
    const key = decisionKey(issue, standardVersion);
    // After rework the issue sits in_progress, so a late no-plan declaration
    // does not enter the review step by itself. The acceptance engineer must be
    // woken to recompute this round; otherwise a satisfied escalation is never
    // evaluated and no decision is created.
    const inReview = String(issue.status ?? "") === "in_review";
    const wakeTester = inReview ? null : {
      required: true,
      agent_id: roles.tester.id,
      mention: `[@${roles.tester.name}](mention://agent/${roles.tester.id})`,
      instruction: "无解声明发生在非审核状态：先实际唤醒验收自动化工程师重算上一轮证据，再由它创建决策；不要因为事项仍在 in_progress 就跳过。",
    };
    const existing = normalizeDecisions(existingDecisions)
      .filter((row) => row.decision_key === key)
      .sort((a, b) => (timeValue(a.created_at) ?? Number.MAX_SAFE_INTEGER) - (timeValue(b.created_at) ?? Number.MAX_SAFE_INTEGER)
        || String(a.identifier).localeCompare(String(b.identifier)));
    if (existing.length > 0) {
      // A decision created before this change stays reusable even though its
      // lead never supplied a member-facing brief.
      const reused = existing[0];
      const draft = {
        key,
        todo_key: key,
        metadata_key: "acceptance_decision_key",
        metadata: { acceptance_decision_key: key },
        action: "reuse",
        existing_identifier: reused.identifier ?? null,
        existing_status: reused.status ?? null,
        duplicates: existing.slice(1).map((row) => row.identifier).filter(Boolean),
        assignee_user_id: ownerUserId,
        title: reused.title ?? null,
        description: null,
        first_screen: null,
        resume: decisionResume(issue, exhaustion),
      };
      return {
        ...base,
        outcome: "reuse_decision",
        decision: draft,
        wake_tester: wakeTester,
        status_command: `"$MULTICA_CLI_PATH" issue status ${issue.identifier} ${blockedStatus}`,
        next_action: `已有同源决策 ${draft.existing_identifier}：不要新建事项，保留该决策并在原事项回链；多余的重复事项用 --duplicate-of 归并。`,
      };
    }

    const brief = normalizeDecisionBrief(exhaustion.decision_brief);
    if (!brief) {
      return {
        ...base,
        outcome: "rework",
        status_command: `"$MULTICA_CLI_PATH" issue status ${issue.identifier} ${backStatus}`,
        next_action: "升级条件已满足，但团队没有给出用户能看懂的 decision_brief：缺少具体坏掉的能力、用户受到的影响，或两种以上有依据的取舍及其后果。不要创建决策事项，也不要退回“这项功能 / 需要你决定下一步”的通用说法。回到原事项，由负责人在同一 attempt_id 的 exhausted 记录里补 decision_brief（subject、problem、options 及各自 consequence），再实际唤醒验收自动化工程师重算。",
      };
    }

    const draft = buildDecision({ issue, standardVersion, candidate, rounds, counted, exhaustion, ownerUserId, brief });
    return {
      ...base,
      outcome: "escalate",
      decision: draft,
      wake_tester: wakeTester,
      status_command: `"$MULTICA_CLI_PATH" issue status ${issue.identifier} ${blockedStatus}`,
      next_action: `${wakeTester ? "无解声明写在非审核状态，先实际唤醒验收自动化工程师重算本轮证据；确认无误后" : ""}两个升级条件同时满足：同一标准版本至少三个不同候选真实失败，且团队负责人已对当前候选的失败裁定声明没有可执行方案，并给出了用户能看懂的具体影响和取舍。只创建一项决策事项（标题写具体能力和待决定动作，不含 decision_key；键在正文末尾技术附录与不可见标记里），指派给配置中的真实用户；标题被不同键的活跃事项占用时，按 decision.title_candidates 读回命中事项后同键复用、异键加自然序号重试，不用 --allow-duplicate。创建后必须在原事项登记 decision.resume.command_template 对应的 until-issue done 规则并读回，否则不要向用户承诺自动继续。`,
    };
  }

  const reasons = [];
  if (rounds > 0) reasons.push(`同一标准版本已累计 ${rounds} 个候选真实失败`);
  if (rounds >= MIN_FAILED_ROUNDS && exhaustion?.executable_plan) reasons.push("团队仍有可执行方案，继续修复而不是升级");
  else if (rounds >= MIN_FAILED_ROUNDS && exhaustion && !exhaustion.valid) {
    if (!exhaustion.after_latest_failure) reasons.push("无方案声明早于当前候选最新失败的独立裁定，或已被更新的记录取代");
    else if (!exhaustion.names_latest_failure) reasons.push("无方案声明没有指明当前候选最新失败的那一轮");
    else reasons.push("无方案声明缺少负责人身份、原因或已尝试方案");
  } else if (rounds >= MIN_FAILED_ROUNDS) reasons.push("尚未满足升级条件：缺少团队负责人针对当前候选失败裁定明确无可执行方案的记录");
  const nextAction = latestVerdict.next_action ?? latest.next_action;
  return {
    ...base,
    outcome: "rework",
    status_command: `"$MULTICA_CLI_PATH" issue status ${issue.identifier} ${backStatus}`,
    next_action: `${reasons.join("；") || "本轮未通过或未验证"}。回到 ${backStatus} 交给原交付团队，并在原事项实际唤醒负责人修复；下一候选版本使用新的 attempt_id，事项再次进入审核中时重新执行验收。${nextAction ? `本轮下一动作：${nextAction}` : ""}`,
  };
}

export function formatText(result) {
  const lines = [
    `${result.issue.identifier} | ${result.outcome} | 标准 ${result.standard_version} | 候选 ${result.candidate}`,
    `真实失败候选数：${result.rounds.count}/${result.rounds.required}`,
  ];
  if (result.latest_attempt) lines.push(`最近尝试：${result.latest_attempt.attempt_id}（裁定 ${result.latest_verdict?.outcome ?? "未出"}）`);
  if (result.status_command) lines.push(`状态：${result.status_command}`);
  if (result.next_action) lines.push(`下一步：${result.next_action}`);
  if (result.decision) lines.push(`决策：${result.decision.action} ${result.decision.existing_identifier ?? result.decision.key}`);
  return `${lines.join("\n")}\n`;
}

export function main(argv = process.argv.slice(2), stdout = process.stdout) {
  const args = parseArgs(argv);
  if (args.help) {
    stdout.write(USAGE);
    return 0;
  }
  for (const required of ["issue", "comments", "agents", "standardVersion", "candidate", "criteria", "ownerUserId"]) {
    if (!text(args[required])) throw new Error(`--${required.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)} is required`);
  }
  const criteria = text(args.criteriaFile) ? readJson(args.criteriaFile, "criteria") : args.criteria;
  const result = adjudicate({
    issue: readJson(args.issue, "issue"),
    comments: readJson(args.comments, "comments"),
    agents: readJson(args.agents, "agents"),
    standardVersion: args.standardVersion,
    candidate: args.candidate,
    criteria,
    ownerUserId: args.ownerUserId,
    existingDecisions: args.existingDecisions.map((path) => readJson(path, "existing decisions")),
    testerRole: args.testerRole,
    qualityRole: args.qualityRole,
    leadRoles: text(args.leadRoles) ? args.leadRoles.split(",").map((row) => row.trim()).filter(Boolean) : undefined,
    doneStatus: args.doneStatus,
    backStatus: args.backStatus,
    blockedStatus: args.blockedStatus,
  });
  if (text(args.decisionDir) && result.decision) {
    mkdirSync(args.decisionDir, { recursive: true });
    writeFileSync(`${args.decisionDir}/title.txt`, `${result.decision.title}\n`);
    writeFileSync(`${args.decisionDir}/titles.txt`, `${(result.decision.title_candidates ?? [result.decision.title]).join("\n")}\n`);
    writeFileSync(`${args.decisionDir}/body.md`, `${result.decision.description}\n`);
  }
  stdout.write(args.output === "text" ? formatText(result) : `${JSON.stringify(result, null, 2)}\n`);
  return 0;
}

// The workbench and development checkout expose the skill through symlinked
// directories, so argv[1] and import.meta.url may name the same file by
// different paths. Compare resolved paths; a real module import still stays
// silent because argv[1] is then the test runner.
function isEntryPoint() {
  if (!process.argv[1]) return false;
  try {
    return realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return import.meta.url === pathToFileURL(process.argv[1]).href;
  }
}

if (isEntryPoint()) {
  try {
    process.exitCode = main();
  } catch (error) {
    process.stderr.write(`review-verdict: ${error.message}\n`);
    process.exitCode = 2;
  }
}