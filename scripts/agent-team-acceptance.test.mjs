import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { adjudicate, decisionKey, extractRecords, MIN_FAILED_ROUNDS } from "../agent-team/skills/rd-acceptance/scripts/review-verdict.mjs";
import { skillFiles } from "./agent-team-template.mjs";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const ACCEPTANCE_DIR = join(ROOT, "agent-team/skills/rd-acceptance");
const TOOL = join(ACCEPTANCE_DIR, "scripts/review-verdict.mjs");
const REVIEW_ENTRY = join(ACCEPTANCE_DIR, "references/review-entry.md");
const template = JSON.parse(readFileSync(join(ROOT, "agent-team/team.json"), "utf8"));

const issue = {
  id: "issue-uuid", identifier: "DEV-7", title: "交付一个可体验功能", creator_id: "member-1", creator_type: "member",
};

function record(body) {
  return `<!-- multica-acceptance ${JSON.stringify(body)} -->`;
}

function attemptBody(attemptId, overrides = {}) {
  return {
    kind: "attempt",
    standard_version: "standard-v1",
    attempt_id: attemptId,
    candidate_version: `sha-${attemptId}`,
    outcome: "failed",
    environment_error: false,
    evidence: [{ criterion: "C1", command: "pnpm test", exit_code: 1, result: "failed", log: `logs/${attemptId}.log` }],
    ...overrides,
  };
}

function verdictBody(attemptId, overrides = {}) {
  return {
    kind: "verdict",
    standard_version: "standard-v1",
    attempt_id: attemptId,
    outcome: "failed",
    criteria: [{ criterion: "C1", result: "failed", evidence: `logs/${attemptId}.log` }],
    ...overrides,
  };
}

function exhaustedBody(overrides = {}) {
  return {
    kind: "exhausted",
    standard_version: "standard-v1",
    lead: "研发经理",
    reason: "上游依赖不可用，团队没有可执行方案",
    executable_plan: null,
    plans: [
      { plan: "升级上游依赖", result: "仍失败" },
      { plan: "隔离不可用服务", result: "仍失败" },
    ],
    ...overrides,
  };
}

function commentsOf(...bodies) {
  return bodies.map((body, index) => ({
    id: `comment-${index + 1}`,
    content: record(body),
    created_at: `2026-11-18T10:0${index}:00Z`,
    author_type: "agent",
  }));
}

function failedRound(attemptId) {
  return [attemptBody(attemptId), verdictBody(attemptId)];
}

test("a fully evidenced pass ends the story with done", () => {
  const comments = commentsOf(
    attemptBody("a1", { outcome: "passed", evidence: [{ criterion: "C1", command: "pnpm test", exit_code: 0, result: "passed", log: "logs/a1.log" }] }),
    verdictBody("a1", { outcome: "passed", criteria: [{ criterion: "C1", result: "passed", evidence: "logs/a1.log" }] }),
  );
  const result = adjudicate({ issue, comments, standardVersion: "standard-v1" });
  assert.equal(result.outcome, "done");
  assert.equal(result.rounds.count, 0);
  assert.match(result.status_command, /issue status DEV-7 done/);
  assert.match(result.next_action, /不是发布|不构成发布/);
});

test("a failed round returns the issue to the delivery team with a next action", () => {
  const comments = commentsOf(...failedRound("a1"));
  const result = adjudicate({ issue, comments, standardVersion: "standard-v1", backStatus: "in_progress" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 1);
  assert.equal(result.rounds.remaining, MIN_FAILED_ROUNDS - 1);
  assert.match(result.status_command, /issue status DEV-7 in_progress/);
  assert.match(result.next_action, /唤醒/);
});

test("an attempt without an independent verdict never advances or counts", () => {
  const result = adjudicate({ issue, comments: commentsOf(attemptBody("a1")), standardVersion: "standard-v1" });
  assert.equal(result.outcome, "awaiting_verdict");
  assert.equal(result.status_command, null);
  assert.equal(result.rounds.count, 0);
});

test("replayed attempts and duplicate events count as one round", () => {
  const comments = commentsOf(
    attemptBody("a1"), attemptBody("a1"), attemptBody("a1"), verdictBody("a1"),
  );
  const result = adjudicate({ issue, comments, standardVersion: "standard-v1" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 1);
  assert.equal(result.replays.length, 2);
});

test("plain comments and records for another standard version are ignored", () => {
  const comments = [
    { id: "chat", content: "看起来没问题，已经跑了三次", created_at: "2026-11-18T10:00:00Z" },
    ...commentsOf(attemptBody("old", { standard_version: "standard-v0" }), verdictBody("old", { standard_version: "standard-v0" })),
  ];
  const result = adjudicate({ issue, comments, standardVersion: "standard-v1" });
  assert.equal(result.outcome, "run_acceptance");
  assert.deepEqual(result.other_standard_versions, ["standard-v0"]);
  assert.equal(result.rounds.count, 0);
});

test("missing evidence, unverified criteria and environment errors never count as real failures", () => {
  const comments = commentsOf(
    attemptBody("missing-evidence", { evidence: [] }),
    verdictBody("missing-evidence"),
    attemptBody("env-error", { environment_error: true }),
    verdictBody("env-error", { environment_error: true }),
    attemptBody("unverified"),
    verdictBody("unverified", { criteria: [{ criterion: "C1", result: "not_verified", evidence: "manual step not run" }] }),
    attemptBody("no-criteria"),
    verdictBody("no-criteria", { criteria: [] }),
  );
  const result = adjudicate({ issue, comments, standardVersion: "standard-v1" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 0);
  assert.equal(result.not_counted.length, 4);
  assert.match(result.not_counted.find((row) => row.attempt_id === "env-error").reason, /环境错误/);
  assert.match(result.not_counted.find((row) => row.attempt_id === "missing-evidence").reason, /缺少/);
});

test("three real failures with an executable plan keep fixing instead of escalating", () => {
  const comments = commentsOf(
    ...failedRound("a1"), ...failedRound("a2"), ...failedRound("a3"),
    exhaustedBody({ executable_plan: "把不可用服务替换为本地桩，并补一条回归测试" }),
  );
  const result = adjudicate({ issue, comments, standardVersion: "standard-v1", ownerUserId: "user-1" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 3);
  assert.equal(result.decision, null);
  assert.match(result.next_action, /仍有可执行方案/);
});

test("three real failures without an executable plan escalate exactly one decision ", () => {
  const comments = commentsOf(
    ...failedRound("a1"), ...failedRound("a2"), ...failedRound("a3"), exhaustedBody(),
  );
  const first = adjudicate({ issue, comments, standardVersion: "standard-v1", ownerUserId: "user-1" });
  assert.equal(first.outcome, "escalate");
  assert.equal(first.rounds.count, 3);
  assert.equal(first.decision.action, "create");
  assert.equal(first.decision.assignee_user_id, "user-1");
  assert.equal(first.decision.metadata_key, "acceptance_decision_key");
  assert.equal(first.decision.key, decisionKey(issue, "standard-v1"));
  assert.match(first.status_command, /issue status DEV-7 blocked/);
  for (const needle of ["失败标准", "第 1 轮", "第 2 轮", "第 3 轮", "尝试过的方案", "待决定事项", "[DEV-7](mention://issue/issue-uuid)"]) {
    assert.ok(first.decision.description.includes(needle), `decision body is missing ${needle}`);
  }

  const existing = {
    issues: [
      { identifier: "DEV-90", created_at: "2026-11-18T09:00:00Z", metadata: { acceptance_decision_key: first.decision.key } },
      { identifier: "DEV-91", created_at: "2026-11-18T09:05:00Z", metadata: { acceptance_decision_key: first.decision.key } },
    ],
  };
  const second = adjudicate({ issue, comments, standardVersion: "standard-v1", ownerUserId: "user-1", existingDecisions: existing });
  assert.equal(second.outcome, "reuse_decision");
  assert.equal(second.decision.action, "reuse");
  assert.equal(second.decision.existing_identifier, "DEV-90");
  assert.deepEqual(second.decision.duplicates, ["DEV-91"]);
});

test("exhaustion needs a lead, a reason and attempted plans before it can escalate", () => {
  const comments = commentsOf(
    ...failedRound("a1"), ...failedRound("a2"), ...failedRound("a3"),
    exhaustedBody({ lead: "", reason: "x", plans: [{ plan: "p", result: "r" }] }),
  );
  const result = adjudicate({ issue, comments, standardVersion: "standard-v1", ownerUserId: "user-1" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.exhaustion.valid, false);
});

test("the decision owner must be the configured human, never the issue's agent creator", () => {
  const comments = commentsOf(...failedRound("a1"), ...failedRound("a2"), ...failedRound("a3"), exhaustedBody());
  assert.throws(
    () => adjudicate({ issue: { ...issue, creator_id: "agent-1", creator_type: "agent" }, comments, standardVersion: "standard-v1", ownerUserId: "agent-1" }),
    /configured human/,
  );
  assert.throws(
    () => adjudicate({ issue, comments, standardVersion: "standard-v1" }),
    /ownerUserId is required/,
  );
});

test("extractRecords reports malformed markers instead of counting them", () => {
  const { attempts, verdicts, ignored } = extractRecords([
    { id: "bad-json", content: "<!-- multica-acceptance {not json} -->" },
    { id: "bad-kind", content: record({ kind: "summary", standard_version: "standard-v1" }) },
    { id: "bad-outcome", content: record({ kind: "attempt", standard_version: "standard-v1", attempt_id: "a", outcome: "maybe" }) },
    { id: "ok", content: record(attemptBody("a1")) },
  ]);
  assert.equal(attempts.length, 1);
  assert.equal(verdicts.length, 0);
  assert.equal(ignored.length, 3);
});

test("the CLI reads platform exports and prints json and text verdicts", () => {
  const dir = mkdtempSync(join(tmpdir(), "review-verdict-"));
  try {
    writeFileSync(join(dir, "issue.json"), JSON.stringify(issue));
    writeFileSync(join(dir, "comments.json"), JSON.stringify({ comments: commentsOf(...failedRound("a1")) }));
    const json = JSON.parse(execFileSync(process.execPath, [
      TOOL, "--issue", join(dir, "issue.json"), "--comments", join(dir, "comments.json"),
      "--standard-version", "standard-v1", "--output", "json",
    ], { encoding: "utf8" }));
    assert.equal(json.outcome, "rework");
    assert.equal(json.rounds.count, 1);

    const text = execFileSync(process.execPath, [
      TOOL, "--issue", join(dir, "issue.json"), "--comments", join(dir, "comments.json"),
      "--standard-version", "standard-v1", "--output", "text",
    ], { encoding: "utf8" });
    assert.match(text, /DEV-7 \| rework/);

    assert.throws(() => execFileSync(process.execPath, [TOOL, "--issue", join(dir, "issue.json")], { encoding: "utf8", stdio: "pipe" }), /required/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("the CLI writes a ready-to-file decision body for the escalation", () => {
  const dir = mkdtempSync(join(tmpdir(), "review-verdict-decision-"));
  try {
    writeFileSync(join(dir, "issue.json"), JSON.stringify(issue));
    writeFileSync(join(dir, "comments.json"), JSON.stringify(commentsOf(
      ...failedRound("a1"), ...failedRound("a2"), ...failedRound("a3"), exhaustedBody(),
    )));
    const result = JSON.parse(execFileSync(process.execPath, [
      TOOL, "--issue", join(dir, "issue.json"), "--comments", join(dir, "comments.json"),
      "--standard-version", "standard-v1", "--owner-user-id", "user-1",
      "--decision-dir", join(dir, "decision"), "--output", "json",
    ], { encoding: "utf8" }));
    assert.equal(result.outcome, "escalate");
    const title = readFileSync(join(dir, "decision/title.txt"), "utf8");
    const body = readFileSync(join(dir, "decision/body.md"), "utf8");
    assert.match(title, /DEV-7/);
    assert.match(body, /\[DEV-7\]\(mention:\/\/issue\/issue-uuid\)/);
    assert.match(body, /第 3 轮/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("the acceptance skill bundle ships the review entry and the adjudication tool", () => {
  const files = skillFiles(ACCEPTANCE_DIR);
  const paths = files.map((file) => file.path);
  assert.ok(paths.includes("references/review-entry.md"));
  assert.ok(paths.includes("scripts/review-verdict.mjs"));
  const entry = files.find((file) => file.path === "references/review-entry.md").content;
  for (const needle of ["review-verdict.mjs", "--until-status in_review", "attempt_id", "environment_error",
    "acceptance_decision_key", "--duplicate-of", "--owner-user-id", "in_progress", "blocked", "不是发布"]) {
    assert.ok(entry.includes(needle), `review entry is missing ${needle}`);
  }
  assert.doesNotMatch(entry, /\/home\/|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|token|password/i);
  const skill = readFileSync(join(ACCEPTANCE_DIR, "SKILL.md"), "utf8");
  assert.match(skill, /references\/review-entry\.md/);
  assert.match(skill, /review-verdict\.mjs/);
});

test("quality, acceptance automation and the project manager carry the in-review contract", () => {
  const by = Object.fromEntries(template.agents.map((agent) => [agent.key, agent]));
  const automation = by["acceptance-automation"];
  assert.match(automation.description, /审核中/);
  assert.match(automation.instructions, /review-entry\.md/);
  assert.match(automation.instructions, /三轮/);
  assert.match(automation.instructions, /真实失败/);
  assert.match(automation.instructions, /不是发布/);

  const quality = by.quality;
  assert.match(quality.description, /裁定/);
  assert.match(quality.instructions, /不推进事项状态/);
  assert.match(quality.instructions, /未验证/);

  const pm = by.pm;
  assert.match(pm.description, /审核中/);
  assert.match(pm.instructions, /三轮/);
  assert.match(pm.instructions, /blocked/);
  assert.match(pm.instructions, /批量完成/);
});