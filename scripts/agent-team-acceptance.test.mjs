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
const template = JSON.parse(readFileSync(join(ROOT, "agent-team/team.json"), "utf8"));

const issue = {
  id: "issue-uuid", identifier: "DEV-7", title: "交付一个可体验功能", creator_id: "member-1", creator_type: "member",
};

// The roster the platform returns from `multica agent list --output json`.
// Records are attributed by these real platform identities, never by a field
// an agent writes about itself.
const agents = [
  { id: "agent-tester", name: "验收自动化工程师" },
  { id: "agent-quality", name: "质量交付经理" },
  { id: "agent-dev", name: "研发经理" },
  { id: "agent-pm", name: "项目经理" },
  { id: "agent-other", name: "其他工程师" },
];
const AUTHOR = { attempt: "agent-tester", verdict: "agent-quality", exhausted: "agent-dev" };

const BASE_TIME = Date.parse("2026-11-18T10:00:00Z");
let sequence = 0;

function record(body) {
  return `<!-- multica-acceptance ${JSON.stringify(body)} -->`;
}

function comment(body, { author, at, id } = {}) {
  sequence += 1;
  const kind = typeof body === "string" ? null : body.kind;
  return {
    id: id ?? `comment-${String(sequence).padStart(4, "0")}`,
    content: typeof body === "string" ? body : record(body),
    created_at: at ?? new Date(BASE_TIME + sequence * 1000).toISOString(),
    author_id: author === null ? null : author ?? AUTHOR[kind],
    author_type: "agent",
  };
}

function commentsOf(...bodies) {
  return bodies.map((body) => comment(body));
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

function passedAttempt(attemptId, overrides = {}) {
  return attemptBody(attemptId, {
    outcome: "passed",
    evidence: [{ criterion: "C1", command: "pnpm test", exit_code: 0, result: "passed", log: `logs/${attemptId}.log` }],
    ...overrides,
  });
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

function passedVerdict(attemptId, overrides = {}) {
  return verdictBody(attemptId, {
    outcome: "passed",
    criteria: [{ criterion: "C1", result: "passed", evidence: `logs/${attemptId}.log` }],
    ...overrides,
  });
}

function exhaustedBody(attemptId = "a3", overrides = {}) {
  return {
    kind: "exhausted",
    standard_version: "standard-v1",
    attempt_id: attemptId,
    reason: "上游依赖不可用，团队没有可执行方案",
    executable_plan: null,
    plans: [
      { plan: "升级上游依赖", result: "仍失败" },
      { plan: "隔离不可用服务", result: "仍失败" },
    ],
    ...overrides,
  };
}

function failedRound(attemptId) {
  return [attemptBody(attemptId), verdictBody(attemptId)];
}

function threeRounds() {
  return [...failedRound("a1"), ...failedRound("a2"), ...failedRound("a3")];
}

function run(comments, overrides = {}) {
  return adjudicate({
    issue,
    comments,
    agents,
    standardVersion: "standard-v1",
    candidate: "sha-a1",
    criteria: ["C1"],
    ownerUserId: "user-1",
    ...overrides,
  });
}

test("a fully evidenced pass on the current candidate ends the story with done", () => {
  const result = run([comment(passedAttempt("a1")), comment(passedVerdict("a1"))]);
  assert.equal(result.outcome, "done");
  assert.equal(result.rounds.count, 0);
  assert.match(result.status_command, /issue status DEV-7 done/);
  assert.match(result.next_action, /不是发布|不构成发布/);
});

test("an old candidate's pass never ends the story once a newer candidate is under test", () => {
  const comments = [
    comment(passedAttempt("a1")), comment(passedVerdict("a1")),
    ...commentsOf(...failedRound("a2")),
  ];
  const result = run(comments, { candidate: "sha-a2" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.latest_attempt.candidate_version, "sha-a2");
  assert.notEqual(result.outcome, "done");
});

test("a failed round returns the issue to the delivery team with a next action", () => {
  const result = run(commentsOf(...failedRound("a1")));
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 1);
  assert.equal(result.rounds.remaining, MIN_FAILED_ROUNDS - 1);
  assert.match(result.status_command, /issue status DEV-7 in_progress/);
  assert.match(result.next_action, /唤醒/);
});

test("an attempt without an independent verdict never advances or counts", () => {
  const result = run(commentsOf(attemptBody("a1")));
  assert.equal(result.outcome, "awaiting_verdict");
  assert.equal(result.status_command, null);
  assert.equal(result.rounds.count, 0);
});

test("replayed attempts and duplicate events count as one round", () => {
  const comments = commentsOf(attemptBody("a1"), attemptBody("a1"), attemptBody("a1"), verdictBody("a1"));
  const result = run(comments);
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 1);
  assert.equal(result.replays.length, 2);
});

test("plain comments and records for another standard version are ignored", () => {
  const comments = [
    comment("看起来没问题，已经跑了三次"),
    ...commentsOf(attemptBody("old", { standard_version: "standard-v0" }), verdictBody("old", { standard_version: "standard-v0" })),
  ];
  const result = run(comments);
  assert.equal(result.outcome, "run_acceptance");
  assert.deepEqual(result.other_standard_versions, ["standard-v0"]);
  assert.equal(result.rounds.count, 0);
});

test("missing evidence, environment errors and unverified criteria never count as real failures", () => {
  const comments = commentsOf(
    attemptBody("missing-evidence", { evidence: [] }),
    verdictBody("missing-evidence"),
    attemptBody("env-error", { environment_error: true }),
    verdictBody("env-error", { environment_error: true }),
    attemptBody("unverified"),
    verdictBody("unverified", { outcome: "not_verified", criteria: [{ criterion: "C1", result: "not_verified", evidence: "manual step not run" }] }),
    attemptBody("no-criteria"),
    verdictBody("no-criteria", { criteria: [] }),
  );
  const result = run(comments, { candidate: "sha-no-criteria" });
  assert.equal(result.outcome, "awaiting_verdict");
  assert.equal(result.rounds.count, 0);
  assert.equal(result.not_counted.length, 4);
  assert.match(result.not_counted.find((row) => row.attempt_id === "env-error").reason, /环境错误/);
  assert.match(result.not_counted.find((row) => row.attempt_id === "missing-evidence").reason, /缺少/);
});

test("a failed verdict that still contains an unverified criterion is not a real failure", () => {
  const result = run([
    comment(attemptBody("mix")),
    comment(verdictBody("mix", {
      criteria: [
        { criterion: "C1", result: "failed", evidence: "logs/mix.log" },
        { criterion: "C2", result: "not_verified", evidence: "手验未执行" },
      ],
    })),
  ], { candidate: "sha-mix", criteria: ["C1", "C2"] });
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 0);
  assert.match(result.not_counted[0].reason, /C2/);
});

test("text-only evidence neither passes nor counts as a real failure", () => {
  const passed = run([
    comment(attemptBody("a1", { outcome: "passed", evidence: [{ criterion: "C1", note: "手动确认通过" }] })),
    comment(passedVerdict("a1")),
  ]);
  assert.equal(passed.outcome, "awaiting_verdict");
  assert.notEqual(passed.outcome, "done");

  const failed = run([
    comment(attemptBody("a2", { evidence: [{ criterion: "C1", note: "测试失败" }] })),
    comment(verdictBody("a2")),
  ], { candidate: "sha-a2" });
  assert.equal(failed.outcome, "awaiting_verdict");
  assert.equal(failed.rounds.count, 0);
});

test("a verdict of passed over failing evidence cannot end the story", () => {
  const result = run([
    comment(attemptBody("a1", { outcome: "passed" })),
    comment(passedVerdict("a1")),
  ]);
  assert.equal(result.outcome, "awaiting_verdict");
  assert.equal(result.inconsistent, true);
  assert.match(result.next_action, /失败/);
});

test("an incomplete frozen criterion set cannot pass", () => {
  const result = run([
    comment(passedAttempt("a1")),
    comment(passedVerdict("a1")),
  ], { criteria: ["C1", "C2"] });
  assert.notEqual(result.outcome, "done");
  assert.equal(result.outcome, "awaiting_verdict");
  assert.match(result.next_action, /C2/);
});

test("the same candidate under different attempt ids counts one round", () => {
  const comments = [
    comment(attemptBody("a1", { candidate_version: "sha-x" })), comment(verdictBody("a1")),
    comment(attemptBody("a2", { candidate_version: "sha-x" })), comment(verdictBody("a2")),
  ];
  const result = run(comments, { candidate: "sha-x" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 1);
});

test("records are ordered by platform time, not by the order the input array lists them", () => {
  const comments = [
    comment(verdictBody("a2"), { at: "2026-11-18T10:03:00Z" }),
    comment(attemptBody("a2"), { at: "2026-11-18T10:02:00Z" }),
    comment(passedVerdict("a1"), { at: "2026-11-18T10:01:00Z" }),
    comment(passedAttempt("a1"), { at: "2026-11-18T10:00:00Z" }),
  ];
  const result = run(comments, { candidate: "sha-a2" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.latest_attempt.attempt_id, "a2");
});

test("identity comes from the real comment author, so nobody can sign their own acceptance", () => {
  const selfSigned = run([
    comment(attemptBody("a1")),
    comment(verdictBody("a1"), { author: "agent-tester" }),
  ]);
  assert.equal(selfSigned.outcome, "awaiting_verdict");
  assert.ok(selfSigned.ignored.some((row) => /platform role/.test(row.reason)));

  const wrongExecutor = run([
    comment(attemptBody("a1"), { author: "agent-quality" }),
    comment(verdictBody("a1")),
  ]);
  assert.equal(wrongExecutor.outcome, "run_acceptance");

  const anonymous = run([
    comment(attemptBody("a1"), { author: null }),
    comment(verdictBody("a1")),
  ]);
  assert.equal(anonymous.outcome, "run_acceptance");
});

test("a no-plan statement only counts when a responsible lead wrote it", () => {
  const byQuality = run(
    [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"), { author: "agent-quality" })],
    { candidate: "sha-a3" },
  );
  assert.equal(byQuality.outcome, "rework");
  assert.equal(byQuality.exhaustion, null);
  assert.ok(byQuality.ignored.some((row) => /team lead/.test(row.reason)));

  const byPm = run(
    [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"), { author: "agent-pm" })],
    { candidate: "sha-a3" },
  );
  assert.equal(byPm.outcome, "escalate");
  assert.equal(byPm.exhaustion.lead, "项目经理");
});

test("three real failures with an executable plan keep fixing instead of escalating", () => {
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a3", { executable_plan: "把不可用服务替换为本地桩，并补一条回归测试" }))];
  const result = run(comments, { candidate: "sha-a3" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 3);
  assert.equal(result.decision, null);
  assert.match(result.next_action, /仍有可执行方案/);
});

test("a stale no-plan statement, or one naming an older round, cannot escalate", () => {
  const comments = [
    comment(attemptBody("a1"), { at: "2026-11-18T10:00:00Z" }), comment(verdictBody("a1"), { at: "2026-11-18T10:01:00Z" }),
    comment(attemptBody("a2"), { at: "2026-11-18T10:02:00Z" }), comment(verdictBody("a2"), { at: "2026-11-18T10:03:00Z" }),
    comment(attemptBody("a3"), { at: "2026-11-18T10:04:00Z" }), comment(verdictBody("a3"), { at: "2026-11-18T10:05:00Z" }),
  ];
  const stale = run([...comments, comment(exhaustedBody("a3"), { at: "2026-11-18T10:03:30Z" })], { candidate: "sha-a3" });
  assert.equal(stale.outcome, "rework");
  assert.equal(stale.exhaustion.after_latest_failure, false);

  const olderRound = run([...comments, comment(exhaustedBody("a1"), { at: "2026-11-18T10:06:00Z" })], { candidate: "sha-a3" });
  assert.equal(olderRound.outcome, "rework");
  assert.equal(olderRound.exhaustion.names_latest_failure, false);

  const updatedPlan = run([
    ...comments,
    comment(exhaustedBody("a3"), { at: "2026-11-18T10:06:00Z" }),
    comment(exhaustedBody("a3", { executable_plan: "改用离线桩验证" }), { at: "2026-11-18T10:07:00Z" }),
  ], { candidate: "sha-a3" });
  assert.equal(updatedPlan.outcome, "rework");
  assert.equal(updatedPlan.exhaustion.valid, false);
  assert.match(updatedPlan.next_action, /仍有可执行方案/);
});

test("a corrected revision of the same round replaces the earlier one instead of the earliest winning", () => {
  const comments = [
    comment(attemptBody("a1"), { at: "2026-11-18T10:00:00Z" }),
    comment(verdictBody("a1"), { at: "2026-11-18T10:01:00Z" }),
    comment(passedAttempt("a1"), { at: "2026-11-18T10:02:00Z" }),
    comment(passedVerdict("a1"), { at: "2026-11-18T10:03:00Z" }),
  ];
  const result = run(comments);
  assert.equal(result.outcome, "done");
  assert.equal(result.rounds.count, 0);
  assert.equal(result.replays.length, 1);
});

test("supplementary evidence for one attempt stays one round and needs re-adjudication", () => {
  const comments = [
    comment(attemptBody("a1", { evidence: [{ criterion: "C1", command: "pnpm test", exit_code: 1, result: "failed", log: "logs/a1.log" }] }), { at: "2026-11-18T10:00:00Z" }),
    comment(attemptBody("a1", { evidence: [{ criterion: "C1", command: "pnpm test", exit_code: 1, result: "failed", log: "logs/a1.log", artifact: "reports/a1.xml" }] }), { at: "2026-11-18T10:01:00Z" }),
    comment(verdictBody("a1"), { at: "2026-11-18T10:02:00Z" }),
  ];
  const result = run(comments);
  assert.equal(result.outcome, "rework");
  assert.equal(result.rounds.count, 1);
  assert.equal(result.replays.length, 1);
});

test("a verdict posted before its attempt cannot judge it", () => {
  const result = run([
    comment(passedVerdict("a1"), { at: "2026-11-18T10:00:00Z" }),
    comment(passedAttempt("a1"), { at: "2026-11-18T10:01:00Z" }),
  ]);
  assert.equal(result.outcome, "awaiting_verdict");
  assert.ok(result.ignored.some((row) => /precedes the attempt/.test(row.reason)));
});

test("an attempt id reused for another candidate is rejected, not merged into that round", () => {
  const result = run([
    comment(attemptBody("a1", { candidate_version: "sha-a1" }), { at: "2026-11-18T10:00:00Z" }),
    comment(verdictBody("a1"), { at: "2026-11-18T10:01:00Z" }),
    comment(attemptBody("a1", { candidate_version: "sha-a2" }), { at: "2026-11-18T10:02:00Z" }),
  ], { candidate: "sha-a2" });
  assert.equal(result.outcome, "run_acceptance");
  assert.equal(result.rounds.count, 1);
  assert.ok(result.ignored.some((row) => /another candidate/.test(row.reason)));
});

test("a no-plan statement before that round's independent verdict cannot escalate", () => {
  const comments = [
    comment(attemptBody("a1"), { at: "2026-11-18T10:00:00Z" }), comment(verdictBody("a1"), { at: "2026-11-18T10:01:00Z" }),
    comment(attemptBody("a2"), { at: "2026-11-18T10:02:00Z" }), comment(verdictBody("a2"), { at: "2026-11-18T10:03:00Z" }),
    comment(attemptBody("a3"), { at: "2026-11-18T10:04:00Z" }),
    comment(exhaustedBody("a3"), { at: "2026-11-18T10:05:00Z" }),
    comment(verdictBody("a3"), { at: "2026-11-18T10:06:00Z" }),
  ];
  const result = run(comments, { candidate: "sha-a3" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.exhaustion.after_latest_failure, false);
  assert.equal(result.rounds.count, 3);
});

test("a no-plan statement must name the current candidate's latest failure", () => {
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a2"), { at: "2026-11-18T12:00:00Z" })];
  const result = run(comments, { candidate: "sha-a3" });
  assert.equal(result.outcome, "rework");
  assert.equal(result.exhaustion.after_latest_failure, true);
  assert.equal(result.exhaustion.names_latest_failure, false);
});

test("a late no-plan declaration outside review wakes the tester to recompute", () => {
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"))];
  const late = run(comments, { candidate: "sha-a3", issue: { ...issue, status: "in_progress" } });
  assert.equal(late.outcome, "escalate");
  assert.equal(late.wake_tester.required, true);
  assert.equal(late.wake_tester.agent_id, "agent-tester");
  assert.match(late.wake_tester.mention, /mention:\/\/agent\/agent-tester/);
  assert.match(late.next_action, /唤醒验收自动化工程师/);

  const inReview = run(comments, { candidate: "sha-a3", issue: { ...issue, status: "in_review" } });
  assert.equal(inReview.outcome, "escalate");
  assert.equal(inReview.wake_tester, null);
});

test("three real failures without a plan escalate exactly one fixed-key decision", () => {
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"))];
  const first = run(comments, { candidate: "sha-a3" });
  assert.equal(first.outcome, "escalate");
  assert.equal(first.rounds.count, 3);
  assert.equal(first.decision.action, "create");
  assert.equal(first.decision.assignee_user_id, "user-1");
  assert.equal(first.decision.metadata_key, "acceptance_decision_key");
  assert.equal(first.decision.key, decisionKey(issue, "standard-v1"));
  assert.match(first.decision.title, new RegExp(first.decision.key));
  assert.ok(first.decision.description.startsWith(`decision_key: ${first.decision.key}`));
  assert.ok(first.decision.description.includes(`"decision_key":"${first.decision.key}"`));
  assert.match(first.status_command, /issue status DEV-7 blocked/);
  for (const needle of ["失败标准", "第 1 轮", "第 2 轮", "第 3 轮", "尝试过的方案", "待决定事项", "[DEV-7](mention://issue/issue-uuid)"]) {
    assert.ok(first.decision.description.includes(needle), `decision body is missing ${needle}`);
  }

  const metadataMatch = {
    issues: [
      { identifier: "DEV-90", created_at: "2026-11-18T09:00:00Z", metadata: { acceptance_decision_key: first.decision.key } },
      { identifier: "DEV-91", created_at: "2026-11-18T09:05:00Z", metadata: { acceptance_decision_key: first.decision.key } },
    ],
  };
  const second = run(comments, { candidate: "sha-a3", existingDecisions: [metadataMatch] });
  assert.equal(second.outcome, "reuse_decision");
  assert.equal(second.decision.action, "reuse");
  assert.equal(second.decision.existing_identifier, "DEV-90");
  assert.deepEqual(second.decision.duplicates, ["DEV-91"]);

  // A decision created moments before its metadata was written is found by the
  // key in the body, so the retry does not create a second issue.
  const bodyOnly = {
    issues: [{
      identifier: "DEV-88",
      created_at: "2026-11-18T08:00:00Z",
      description: `需要决定\n<!-- multica-acceptance-decision ${JSON.stringify({ decision_key: first.decision.key })} -->`,
    }],
  };
  const third = run(comments, { candidate: "sha-a3", existingDecisions: [bodyOnly] });
  assert.equal(third.outcome, "reuse_decision");
  assert.equal(third.decision.existing_identifier, "DEV-88");
});

test("metadata and search returning the same decision never duplicate it", () => {
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"))];
  const key = run(comments, { candidate: "sha-a3" }).decision.key;
  const metadata = {
    issues: [{
      id: "decision-uuid", identifier: "DEV-90", created_at: "2026-11-18T09:00:00Z", status: "in_progress",
      metadata: { acceptance_decision_key: key },
    }],
  };
  const search = {
    issues: [{ id: "decision-uuid", identifier: "DEV-90", title: `验收决策 ${key} 三轮失败`, status: "in_progress" }],
  };
  const result = run(comments, { candidate: "sha-a3", existingDecisions: [metadata, search] });
  assert.equal(result.outcome, "reuse_decision");
  assert.equal(result.decision.existing_identifier, "DEV-90");
  assert.equal(result.decision.existing_status, "in_progress");
  assert.deepEqual(result.decision.duplicates, []);
});

test("a title-only search hit of an already closed decision is reused without creating another", () => {
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"))];
  const key = run(comments, { candidate: "sha-a3" }).decision.key;
  const searchOnly = {
    issues: [{ id: "closed-decision-uuid", identifier: "DEV-77", title: `验收决策 ${key}`, status: "cancelled" }],
  };
  const result = run(comments, { candidate: "sha-a3", existingDecisions: [searchOnly] });
  assert.equal(result.outcome, "reuse_decision");
  assert.equal(result.decision.existing_identifier, "DEV-77");
  assert.equal(result.decision.existing_status, "cancelled");
  assert.deepEqual(result.decision.duplicates, []);
});

test("the decision owner must be a configured human, never an agent", () => {
  const comments = commentsOf(...threeRounds());
  assert.throws(() => run(comments, { ownerUserId: undefined }), /ownerUserId is required/);
  assert.throws(() => run(comments, { ownerUserId: "agent-tester" }), /human member/);
  assert.throws(
    () => run(comments, { issue: { ...issue, creator_id: "user-x", creator_type: "agent" }, ownerUserId: "user-x" }),
    /configured human/,
  );
});

test("missing role names fail loudly instead of silently trusting records", () => {
  assert.throws(
    () => adjudicate({
      issue, comments: commentsOf(...failedRound("a1")), agents: agents.filter((row) => row.name !== "质量交付经理"),
      standardVersion: "standard-v1", candidate: "sha-a1", criteria: ["C1"], ownerUserId: "user-1",
    }),
    /no agent named 质量交付经理/,
  );
});

test("extractRecords reports malformed markers instead of counting them", () => {
  const { attempts, verdicts, ignored } = extractRecords([
    { id: "bad-json", content: "<!-- multica-acceptance {not json} -->" },
    { id: "bad-kind", content: record({ kind: "summary", standard_version: "standard-v1" }) },
    { id: "bad-outcome", content: record({ kind: "attempt", standard_version: "standard-v1", attempt_id: "a", outcome: "maybe" }) },
    { id: "ok", content: record(attemptBody("a1")), author_id: "agent-tester", author_type: "agent" },
  ]);
  assert.equal(attempts.length, 1);
  assert.equal(verdicts.length, 0);
  assert.equal(ignored.length, 3);
});

function exportInputs(dir, comments) {
  writeFileSync(join(dir, "issue.json"), JSON.stringify(issue));
  writeFileSync(join(dir, "comments.json"), JSON.stringify({ comments }));
  writeFileSync(join(dir, "agents.json"), JSON.stringify(agents));
  return [
    "--issue", join(dir, "issue.json"),
    "--comments", join(dir, "comments.json"),
    "--agents", join(dir, "agents.json"),
    "--standard-version", "standard-v1",
  ];
}

test("the CLI requires every frozen input and prints json and text verdicts", () => {
  const dir = mkdtempSync(join(tmpdir(), "review-verdict-"));
  try {
    const args = exportInputs(dir, commentsOf(...failedRound("a1")));
    const json = JSON.parse(execFileSync(process.execPath, [
      TOOL, ...args, "--candidate", "sha-a1", "--criteria", "C1", "--owner-user-id", "user-1", "--output", "json",
    ], { encoding: "utf8" }));
    assert.equal(json.outcome, "rework");
    assert.equal(json.rounds.count, 1);

    const text = execFileSync(process.execPath, [
      TOOL, ...args, "--candidate", "sha-a1", "--criteria", "C1", "--owner-user-id", "user-1", "--output", "text",
    ], { encoding: "utf8" });
    assert.match(text, /DEV-7 \| rework/);

    assert.throws(
      () => execFileSync(process.execPath, [TOOL, "--issue", join(dir, "issue.json")], { encoding: "utf8", stdio: "pipe" }),
      /required/,
    );
    assert.throws(
      () => execFileSync(process.execPath, [TOOL, ...args, "--candidate", "sha-a1", "--criteria", "C1"], { encoding: "utf8", stdio: "pipe" }),
      /owner-user-id is required/,
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("the CLI writes a ready-to-file decision body containing its key", () => {
  const dir = mkdtempSync(join(tmpdir(), "review-verdict-decision-"));
  try {
    const args = exportInputs(dir, [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"))]);
    const result = JSON.parse(execFileSync(process.execPath, [
      TOOL, ...args, "--candidate", "sha-a3", "--criteria", "C1", "--owner-user-id", "user-1",
      "--decision-dir", join(dir, "decision"), "--output", "json",
    ], { encoding: "utf8" }));
    assert.equal(result.outcome, "escalate");
    const title = readFileSync(join(dir, "decision/title.txt"), "utf8");
    const body = readFileSync(join(dir, "decision/body.md"), "utf8");
    assert.match(title, new RegExp(result.decision.key));
    assert.match(body, new RegExp(`^decision_key: ${result.decision.key}`));
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
  for (const needle of ["review-verdict.mjs", "--until-status in_review", "--agents", "--candidate", "--criteria",
    "--owner-user-id", "--instruction", "--type string", "attempt_id", "environment_error", "author_id",
    "acceptance_decision_key", "--duplicate-of", "in_progress", "blocked", "不是发布"]) {
    assert.ok(entry.includes(needle), `review entry is missing ${needle}`);
  }
  assert.doesNotMatch(entry, /\/home\/|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|token|password/i);
  const skill = readFileSync(join(ACCEPTANCE_DIR, "SKILL.md"), "utf8");
  assert.match(skill, /references\/review-entry\.md/);
  assert.match(skill, /review-verdict\.mjs/);
});

test("the template's role names match the adjudicator's defaults", () => {
  const names = template.agents.map((agent) => agent.name);
  for (const name of ["验收自动化工程师", "质量交付经理", "研发经理", "项目经理"]) {
    assert.ok(names.includes(name), `template has no agent named ${name}`);
  }
});

test("quality, acceptance automation and the project manager carry the in-review contract", () => {
  const by = Object.fromEntries(template.agents.map((agent) => [agent.key, agent]));
  const automation = by["acceptance-automation"];
  assert.match(automation.description, /审核中/);
  assert.match(automation.instructions, /review-entry\.md/);
  assert.match(automation.instructions, /三轮/);
  assert.match(automation.instructions, /真实失败/);
  assert.match(automation.instructions, /不是发布/);
  assert.match(automation.instructions, /author_id/);
  assert.match(automation.instructions, /--candidate/);
  assert.match(automation.instructions, /--criteria/);
  assert.match(automation.instructions, /--owner-user-id/);

  const quality = by.quality;
  assert.match(quality.description, /裁定/);
  assert.match(quality.instructions, /不能给自己的验收记录签字/);
  assert.match(quality.instructions, /主动接续/);
  assert.match(quality.instructions, /未验证/);

  const pm = by.pm;
  assert.match(pm.description, /审核中/);
  assert.match(pm.instructions, /三轮/);
  assert.match(pm.instructions, /blocked/);
  assert.match(pm.instructions, /批量完成/);
});

test("the review entry locates the tool under every provider's skill directory", () => {
  const entry = readFileSync(join(ACCEPTANCE_DIR, "references/review-entry.md"), "utf8");
  for (const needle of [".claude/skills", "CODEX_HOME/skills", "codex-home/skills", ".grok/skills", ".agent_context/skills"]) {
    assert.ok(entry.includes(needle), `review entry does not look in ${needle}`);
  }
  assert.match(entry, /最新一条有效修订/);
  assert.match(entry, /独立失败裁定/);
  assert.match(entry, /实际唤醒验收自动化工程师/);
  assert.match(entry, /固定标题/);
  assert.match(entry, /issue get/);
});

test("the quality manager's own skill carries the copyable verdict record and the active hand-back", () => {
  const skill = readFileSync(join(ROOT, "agent-team/skills/rd-verify/SKILL.md"), "utf8");
  for (const needle of ['"kind":"verdict"', '"standard_version"', '"attempt_id"', '"criteria"',
    '"environment_error"', "mention://agent/", "Never leave the issue waiting"]) {
    assert.ok(skill.includes(needle), `rd-verify is missing ${needle}`);
  }
  assert.match(skill, /adjudicate the revision again/i);
  assert.doesNotMatch(skill, /见 rd-acceptance|follow rd-acceptance/);
});

test("the lead's coordination skill carries the no-plan record and the late wake", () => {
  const skill = readFileSync(join(ROOT, "agent-team/skills/rd-coordinate/SKILL.md"), "utf8");
  for (const needle of ['"kind":"exhausted"', '"attempt_id"', '"executable_plan"', "in_progress",
    "valid mention", "acceptance automation engineer"]) {
    assert.ok(skill.includes(needle), `rd-coordinate is missing ${needle}`);
  }
  assert.match(skill, /predates that round's verdict/);
  assert.match(skill, /keep the team fixing/);
});

test("the template sends the quality manager to its own verdict format and the lead to the late wake", () => {
  const by = Object.fromEntries(template.agents.map((agent) => [agent.key, agent]));
  assert.match(by.quality.instructions, /rd-verify/);
  assert.match(by.quality.instructions, /verdict 记录格式/);
  assert.match(by["acceptance-automation"].instructions, /实际唤醒你重算/);
  assert.match(by.pm.instructions, /实际唤醒验收自动化工程师/);
});