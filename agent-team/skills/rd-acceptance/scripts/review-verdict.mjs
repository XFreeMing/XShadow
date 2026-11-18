#!/usr/bin/env node
// Deterministic adjudicator for the in_review acceptance step.
//
// It reads only platform-exported data: one issue record and its comments. The
// acceptance automation engineer posts one machine-readable record per
// independent acceptance attempt; the quality manager posts one verdict per
// attempt. This tool deduplicates replays, refuses to count missing evidence or
// environment errors as real failures, and decides whether the story is done,
// goes back to the delivery team, or escalates one decision to the configured
// human.
//
// Input is JSON, never a live API call, so the same decision is reproducible
// from the issue's stored history.

import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

export const RECORD_MARKER = "multica-acceptance";
export const MIN_FAILED_ROUNDS = 3;
const RESULTS = new Set(["passed", "failed", "not_verified"]);
const RECORD_KINDS = new Set(["attempt", "verdict", "exhausted"]);

const USAGE = `Usage:
  node review-verdict.mjs --issue <issue.json> --comments <comments.json> \\
    --standard-version <id> [--owner-user-id <user-id>] [--existing-decisions <json>] \\
    [--done-status done] [--back-status in_progress] [--blocked-status blocked] \\
    [--decision-dir <dir>] [--output json|text]

--issue              JSON from \`multica issue get <issue> --output json\`.
--comments           JSON from \`multica issue comment list <issue> --output json\`,
                     or any array of comment objects.
--standard-version   Frozen acceptance criteria version under test. Records for
                     other versions are reported but never counted.
--owner-user-id      The human who receives an escalated decision. Required only
                     when escalating; must not be the issue's agent creator.
--existing-decisions JSON array from \`multica issue list --metadata
                     acceptance_decision_key=<key>\`; reused instead of creating.
--decision-dir       Write decision title.txt and body.md there, ready for
                     \`multica issue create --description-file\`.
--output             json (default) or text.

Machine records live in comments as HTML comments:
  <!-- multica-acceptance {"kind":"attempt",...} -->
See references/review-entry.md for the record shapes and the step procedure.
`;

function parseArgs(argv) {
  const args = { output: "json" };
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
    args[name.replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = value;
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

function normalizeComments(input) {
  const rows = Array.isArray(input)
    ? input
    : Array.isArray(input?.comments)
      ? input.comments
      : Array.isArray(input?.items)
        ? input.items
        : null;
  if (!rows) throw new Error("comments input must be an array, {comments: [...]} or {items: [...]}");
  return rows.map((comment, index) => ({
    index,
    id: text(comment?.id) ? comment.id : `#${index}`,
    content: typeof comment?.content === "string" ? comment.content : "",
    created_at: text(comment?.created_at) ? comment.created_at : null,
    author_id: text(comment?.author_id) ? comment.author_id : null,
    author_type: text(comment?.author_type) ? comment.author_type : null,
  }));
}

export function extractRecords(comments) {
  const attempts = [];
  const verdicts = [];
  const exhausted = [];
  const ignored = [];
  const marker = new RegExp(`<!--\\s*${RECORD_MARKER}\\s+([\\s\\S]*?)-->`, "g");

  for (const comment of normalizeComments(comments)) {
    for (const match of comment.content.matchAll(marker)) {
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
      const row = { ...record, comment_id: comment.id, created_at: comment.created_at, order: comment.index };
      if (record.kind === "exhausted") {
        exhausted.push(row);
        continue;
      }
      if (!text(record.attempt_id)) {
        ignored.push({ comment_id: comment.id, reason: `missing attempt_id on ${record.kind} record` });
        continue;
      }
      if (!RESULTS.has(record.outcome)) {
        ignored.push({ comment_id: comment.id, reason: `invalid outcome: ${String(record.outcome)}` });
        continue;
      }
      (record.kind === "attempt" ? attempts : verdicts).push(row);
    }
  }
  return { attempts, verdicts, exhausted, ignored };
}

function attemptKey(standardVersion, attemptId) {
  return `${standardVersion}\u0000${attemptId}`;
}

// A replay of the same independent attempt is the same attempt. The earliest
// record wins so a retried run cannot add a round.
function distinctAttempts(attempts) {
  const seen = new Map();
  const replays = [];
  for (const attempt of attempts) {
    const key = attemptKey(attempt.standard_version, attempt.attempt_id);
    if (seen.has(key)) replays.push({ attempt_id: attempt.attempt_id, comment_id: attempt.comment_id });
    else seen.set(key, attempt);
  }
  return { ordered: [...seen.values()].sort((a, b) => a.order - b.order), replays };
}

// A corrected verdict may supersede an earlier one; the latest wins.
function latestVerdictByAttempt(verdicts) {
  const latest = new Map();
  for (const verdict of verdicts) latest.set(attemptKey(verdict.standard_version, verdict.attempt_id), verdict);
  return latest;
}

function attemptHasRunEvidence(attempt) {
  if (!Array.isArray(attempt.evidence) || attempt.evidence.length === 0) return false;
  return attempt.evidence.every((item) => {
    if (!text(item?.criterion)) return false;
    if (item.result !== undefined && !RESULTS.has(item.result)) return false;
    return text(item.command) || text(item.log) || text(item.artifact) || text(item.attachment);
  });
}

function isPass(attempt, verdict) {
  if (!verdict || attempt.outcome !== "passed" || verdict.outcome !== "passed") return false;
  if (attempt.environment_error === true || verdict.environment_error === true) return false;
  if (!attemptHasRunEvidence(attempt)) return false;
  const criteria = verdict.criteria;
  if (!Array.isArray(criteria) || criteria.length === 0) return false;
  if (criteria.some((c) => c?.result !== "passed" || !text(c?.evidence))) return false;
  return true;
}

// Only a real, actually-failing, independently-verified attempt counts. A
// missing environment, missing evidence or a merely unverified run never does.
function isRealFailure(attempt, verdict) {
  if (!verdict || attempt.outcome !== "failed" || verdict.outcome !== "failed") return false;
  if (attempt.environment_error === true || verdict.environment_error === true) return false;
  if (!attemptHasRunEvidence(attempt)) return false;
  const criteria = verdict.criteria;
  if (!Array.isArray(criteria) || criteria.length === 0) return false;
  if (criteria.some((c) => !RESULTS.has(c?.result) || c.result === "not_verified")) return false;
  const failed = criteria.filter((c) => c.result === "failed");
  if (failed.length === 0) return false;
  return failed.every((c) => text(c.evidence));
}

function currentExhaustion(exhausted, standardVersion) {
  const rows = exhausted.filter((row) => row.standard_version === standardVersion);
  const latest = rows.at(-1);
  if (!latest) return null;
  const hasPlan = text(latest.executable_plan);
  return {
    lead: text(latest.lead) ? latest.lead : null,
    reason: text(latest.reason) ? latest.reason : null,
    executable_plan: hasPlan ? latest.executable_plan : null,
    plans: Array.isArray(latest.plans) ? latest.plans : [],
    comment_id: latest.comment_id,
    valid: text(latest.lead) && text(latest.reason) && !hasPlan && Array.isArray(latest.plans) && latest.plans.length > 0,
  };
}

export function decisionKey(issue, standardVersion) {
  const digest = createHash("sha256").update(`${issue.id}|${standardVersion}`).digest("hex").slice(0, 16);
  return `acc-${digest}`;
}

function formatCriteria(criteria) {
  return criteria
    .filter((c) => c?.result === "failed")
    .map((c) => `- ${c.criterion ?? c.id ?? "未命名标准"}：${c.evidence}`)
    .join("\n");
}

function normalizeDecisions(input) {
  const rows = Array.isArray(input)
    ? input
    : Array.isArray(input?.issues)
      ? input.issues
      : Array.isArray(input?.items)
        ? input.items
        : [];
  return rows
    .map((row) => ({
      decision_key: row?.metadata?.acceptance_decision_key ?? row?.decision_key ?? null,
      identifier: row?.identifier ?? null,
      created_at: row?.created_at ?? null,
      status: row?.status ?? null,
    }))
    .filter((row) => text(row.decision_key) && text(row.identifier));
}

function buildDecision({ issue, standardVersion, rounds, counted, exhaustion, ownerUserId }) {
  const key = decisionKey(issue, standardVersion);
  const backlink = `[${issue.identifier}](mention://issue/${issue.id})`;
  const evidence = counted
    .map((row, index) => {
      const failed = formatCriteria(row.verdict.criteria);
      return `### 第 ${index + 1} 轮（尝试 ${row.attempt.attempt_id}，候选 ${row.attempt.candidate_version ?? "未记录"}，证据评论 ${row.attempt.comment_id}）\n${failed}`;
    })
    .join("\n\n");
  const plans = exhaustion.plans
    .map((plan, index) => {
      const label = typeof plan === "string" ? plan : plan.plan ?? plan.summary ?? JSON.stringify(plan);
      const result = typeof plan === "object" && plan ? plan.result ?? plan.outcome : null;
      return `- 方案 ${index + 1}：${label}${result ? `（结果：${result}）` : ""}`;
    })
    .join("\n");
  return {
    key,
    metadata_key: "acceptance_decision_key",
    action: "create",
    existing_identifier: null,
    duplicates: [],
    assignee_user_id: ownerUserId,
    title: `决定 ${issue.identifier} 的下一步：验收连续 ${rounds} 轮未通过且团队已无可行方案`,
    description: `## 需要你决定\n\n原事项：${backlink}\n\n验收标准版本：\`${standardVersion}\`\n\n### 失败标准\n\n${evidence}\n\n### 尝试过的方案\n\n${plans}\n\n### 团队结论\n\n${exhaustion.lead}：${exhaustion.reason}\n\n### 待决定事项\n\n继续投入修复、缩小验收范围，或暂停/取消这个故事。原事项已停在 blocked，选择后由原交付负责人按决定继续。\n\n<!-- ${RECORD_MARKER}-decision ${JSON.stringify({ decision_key: key, standard_version: standardVersion, original_issue: issue.identifier })} -->`,
  };
}

export function adjudicate({ issue, comments, standardVersion, ownerUserId, existingDecisions, doneStatus = "done", backStatus = "in_progress", blockedStatus = "blocked" }) {
  if (!issue || !text(issue.id) || !text(issue.identifier)) throw new Error("issue input needs id and identifier");
  if (!text(standardVersion)) throw new Error("standardVersion is required");

  const { attempts, verdicts, exhausted, ignored } = extractRecords(comments);
  const { ordered, replays } = distinctAttempts(attempts);
  const verdictByKey = latestVerdictByAttempt(verdicts);

  const scoped = ordered.filter((attempt) => attempt.standard_version === standardVersion);
  const otherVersions = [...new Set(ordered.map((a) => a.standard_version).filter((v) => v !== standardVersion))];
  const latest = scoped.at(-1) ?? null;
  const latestVerdict = latest ? verdictByKey.get(attemptKey(latest.standard_version, latest.attempt_id)) ?? null : null;

  const counted = scoped
    .map((attempt) => ({ attempt, verdict: verdictByKey.get(attemptKey(attempt.standard_version, attempt.attempt_id)) }))
    .filter((row) => isRealFailure(row.attempt, row.verdict));
  const rounds = counted.length;
  const exhaustion = currentExhaustion(exhausted, standardVersion);

  const notCounted = scoped
    .filter((attempt) => !counted.some((row) => row.attempt.attempt_id === attempt.attempt_id))
    .map((attempt) => {
      const verdict = verdictByKey.get(attemptKey(attempt.standard_version, attempt.attempt_id));
      let reason = "尚无独立裁定";
      if (verdict) {
        if (attempt.environment_error === true || verdict.environment_error === true) reason = "环境错误，不计真实失败";
        else if (!attemptHasRunEvidence(attempt)) reason = "缺少命令、日志或产物证据";
        else if (!Array.isArray(verdict.criteria) || verdict.criteria.length === 0) reason = "裁定未逐条记录标准";
        else if (verdict.criteria.some((c) => c?.result === "not_verified")) reason = "存在未验证的标准";
        else if (verdict.outcome === "passed") reason = "该轮通过";
        else if (verdict.outcome !== "failed") reason = "不是失败裁定";
      }
      return { attempt_id: attempt.attempt_id, verdict_outcome: verdict?.outcome ?? null, reason };
    });

  const base = {
    tool: "review-verdict",
    version: 1,
    issue: { id: issue.id, identifier: issue.identifier, title: issue.title ?? "" },
    standard_version: standardVersion,
    decision_key: decisionKey(issue, standardVersion),
    rounds: { count: rounds, required: MIN_FAILED_ROUNDS, remaining: Math.max(0, MIN_FAILED_ROUNDS - rounds) },
    latest_attempt: latest ? { attempt_id: latest.attempt_id, candidate_version: latest.candidate_version ?? null, comment_id: latest.comment_id } : null,
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
    ignored,
    exhaustion: exhaustion
      ? {
          lead: exhaustion.lead,
          reason: exhaustion.reason,
          executable_plan: exhaustion.executable_plan,
          plans: exhaustion.plans,
          valid: exhaustion.valid,
        }
      : null,
    decision: null,
    status_command: null,
    next_action: null,
    outcome: null,
  };

  if (!latest) {
    return {
      ...base,
      outcome: "run_acceptance",
      next_action: "尚无本轮验收记录：先按冻结标准执行验收代码，在原事项提交一条证据评论，再交给质量交付经理裁定。不要推进状态。",
    };
  }

  if (!latestVerdict) {
    return {
      ...base,
      outcome: "awaiting_verdict",
      next_action: "已有验收证据但缺少质量交付经理的独立裁定：在原事项把证据单次交给质量交付经理，不要自行判定通过或推进状态。",
    };
  }

  if (isPass(latest, latestVerdict)) {
    return {
      ...base,
      outcome: "done",
      status_command: `"$MULTICA_CLI_PATH" issue status ${issue.identifier} ${doneStatus}`,
      next_action: "全部必验标准通过且证据齐全：由步骤负责人把原事项标记为 done。自动 done 只代表故事验收通过，不构成发布、合并或日常升级授权。",
    };
  }

  const escalate = rounds >= MIN_FAILED_ROUNDS && exhaustion?.valid;
  if (escalate) {
    if (!text(ownerUserId)) throw new Error("ownerUserId is required to escalate; pass the human from identity configuration");
    if (text(issue.creator_id) && issue.creator_id === ownerUserId && issue.creator_type !== "member") {
      throw new Error("decision owner must be the configured human, not the issue's agent creator");
    }
    const draft = buildDecision({ issue, standardVersion, rounds, counted, exhaustion, ownerUserId });
    const existing = normalizeDecisions(existingDecisions)
      .filter((row) => row.decision_key === draft.key)
      .sort((a, b) => String(a.created_at ?? "").localeCompare(String(b.created_at ?? "")) || String(a.identifier).localeCompare(String(b.identifier)));
    if (existing.length > 0) {
      draft.action = "reuse";
      draft.existing_identifier = existing[0].identifier ?? null;
      draft.duplicates = existing.slice(1).map((row) => row.identifier).filter(Boolean);
    }
    return {
      ...base,
      outcome: draft.action === "reuse" ? "reuse_decision" : "escalate",
      decision: draft,
      status_command: `"$MULTICA_CLI_PATH" issue status ${issue.identifier} ${blockedStatus}`,
      next_action: draft.action === "reuse"
        ? `已有同源决策 ${draft.existing_identifier}：不要新建事项，保留该决策并在原事项回链；多余的重复事项用 --duplicate-of 归并。`
        : "两个升级条件同时满足：同一标准版本至少三轮真实失败，且团队负责人已声明没有可执行方案。只创建一项决策事项，指派给配置中的真实用户，并写入 acceptance_decision_key 元数据用于后续去重。",
    };
  }

  const reasons = [];
  if (rounds > 0) reasons.push(`同一标准版本已累计 ${rounds} 轮真实失败`);
  if (rounds >= MIN_FAILED_ROUNDS && exhaustion?.executable_plan) reasons.push("团队仍有可执行方案，继续修复而不是升级");
  else if (rounds >= MIN_FAILED_ROUNDS && exhaustion && !exhaustion.valid) reasons.push("团队负责人的无方案声明缺少负责人、原因或已尝试方案");
  else if (rounds >= MIN_FAILED_ROUNDS) reasons.push("尚未满足升级条件：缺少团队负责人明确无可执行方案的记录");
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
    `${result.issue.identifier} | ${result.outcome} | 标准 ${result.standard_version}`,
    `真实失败轮次：${result.rounds.count}/${result.rounds.required}`,
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
  for (const required of ["issue", "comments", "standardVersion"]) {
    if (!text(args[required])) throw new Error(`--${required.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)} is required`);
  }
  const result = adjudicate({
    issue: readJson(args.issue, "issue"),
    comments: readJson(args.comments, "comments"),
    standardVersion: args.standardVersion,
    ownerUserId: args.ownerUserId,
    existingDecisions: args.existingDecisions ? readJson(args.existingDecisions, "existing decisions") : [],
    doneStatus: args.doneStatus,
    backStatus: args.backStatus,
    blockedStatus: args.blockedStatus,
  });
  if (text(args.decisionDir) && result.decision) {
    mkdirSync(args.decisionDir, { recursive: true });
    writeFileSync(`${args.decisionDir}/title.txt`, `${result.decision.title}\n`);
    writeFileSync(`${args.decisionDir}/body.md`, `${result.decision.description}\n`);
  }
  stdout.write(args.output === "text" ? formatText(result) : `${JSON.stringify(result, null, 2)}\n`);
  return 0;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  try {
    process.exitCode = main();
  } catch (error) {
    process.stderr.write(`review-verdict: ${error.message}\n`);
    process.exitCode = 2;
  }
}