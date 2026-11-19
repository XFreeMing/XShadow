import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { adjudicate, decisionKey, decisionKeyFromTitle, extractRecords, MIN_FAILED_ROUNDS } from "../agent-team/skills/rd-acceptance/scripts/review-verdict.mjs";
import { validateMemberTodo, visibleViolations } from "../agent-team/skills/rd-acceptance/scripts/member-todo.mjs";
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
    decision_brief: {
      subject: "PDF 引用跳转",
      problem: "读者能看到正文，但点 PDF 里的引用打不开原文，无法核对来源。",
      why_you: "本期先发还是等引用修好再发，只有你能定这个发布取舍。",
      options: [
        { label: "本期先发，标注引用暂不可跳转", consequence: "读者先看到正文；引用暂时点不开，发布说明会写明限制。", example: "回复“先发”" },
        { label: "等引用能跳转后再发", consequence: "发布时间推迟，直到 PDF 引用可以打开原文。", example: "回复“等修好”" },
      ],
    },
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

test("three real failures without a plan escalate exactly one readable decision", () => {
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"))];
  const first = run(comments, { candidate: "sha-a3" });
  assert.equal(first.outcome, "escalate");
  assert.equal(first.rounds.count, 3);
  assert.equal(first.decision.action, "create");
  assert.equal(first.decision.assignee_user_id, "user-1");
  assert.equal(first.decision.metadata_key, "acceptance_decision_key");
  assert.equal(first.decision.key, decisionKey(issue, "standard-v1"));
  // The visible title names the concrete capability and the decision, never the key.
  assert.equal(first.decision.title, "「PDF 引用跳转」需要你做一个取舍（DEV-7）");
  assert.doesNotMatch(first.decision.title, /acc-[0-9a-f]{16}|标准|候选|缺口键|SHA|这项功能/i);
  // The key still travels for search and for the create-then-metadata retry.
  assert.match(first.decision.description, new RegExp(`^decision_key: ${first.decision.key}$`, "m"));
  assert.match(first.decision.description, new RegExp(`"decision_key":"${first.decision.key}"`));
  assert.equal(first.decision.todo_key, first.decision.key);
  assert.equal(first.decision.metadata.acceptance_decision_key, first.decision.key);
  assert.match(first.status_command, /issue status DEV-7 blocked/);
  const screen = first.decision.first_screen;
  for (const needle of ["读者能看到正文，但点 PDF 里的引用打不开原文", "本期先发", "等引用能跳转后再发",
    "发布说明会写明限制", "怎样算完成", "标记为完成", "只回复不会自动接续", "之后：", "原事项会自动继续"]) {
    assert.ok(screen.includes(needle), `decision first screen is missing ${needle}`);
  }
  assert.doesNotMatch(screen, /acc-[0-9a-f]{16}|标准版本|候选|attempt|缺口键|SHA|失败标准|这项功能|需要你决定下一步/i);
  // The completion promise is backed by a real resume rule waking the lead.
  assert.equal(first.decision.resume.signal, "done");
  assert.equal(first.decision.resume.agent_id, "agent-dev");
  assert.match(first.decision.resume.command_template, /issue wakeup create DEV-7 --until-issue <新决策事项> --until-issue-state done/);
  assert.match(first.decision.resume.command_template, /--agent-id agent-dev/);
  for (const needle of ["失败标准", "第 1 轮", "第 2 轮", "第 3 轮", "尝试过的方案",
    "[DEV-7](mention://issue/issue-uuid)", "standard-v1", "sha-a3"]) {
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

test("an escalation without a specific member-facing brief goes back to the team", () => {
  const valid = exhaustedBody().decision_brief;
  const cases = [
    ["missing", Symbol("missing")],
    ["generic subject", { ...valid, subject: "这项功能" }],
    ["engineering words in the impact", { ...valid, problem: "读者点 PDF 引用打不开，候选 sha a1b2c3d4e5f6 仍未通过。" }],
    ["only one option", { ...valid, options: [valid.options[0]] }],
    ["a missing reply example", {
      ...valid,
      options: [{ label: valid.options[0].label, consequence: valid.options[0].consequence }, valid.options[1]],
    }],
    ["internal verification as the reply example", {
      ...valid,
      options: [{ ...valid.options[0], example: "先运行测试并附上日志，再回复先发" }, valid.options[1]],
    }],
    ["an engineering word in the subject", { ...valid, subject: "PDF 引用跳转（验收回执）" }],
    ["a preset default", { ...valid, options: [{ ...valid.options[0], recommended: true }, valid.options[1]] }],
  ];
  for (const [label, brief] of cases) {
    const body = exhaustedBody("a3", brief === undefined || typeof brief === "symbol" ? {} : { decision_brief: brief });
    if (typeof brief === "symbol") delete body.decision_brief;
    const result = run([...commentsOf(...threeRounds()), comment(body)], { candidate: "sha-a3" });
    assert.equal(result.outcome, "rework", label);
    assert.equal(result.decision, null, label);
    assert.match(result.next_action, /decision_brief/, label);
    assert.match(result.next_action, /不要创建决策事项/, label);
    assert.match(result.status_command, /issue status DEV-7 in_progress/, label);
  }
});

test("a legacy same-key decision is reused even without the new brief", () => {
  const body = exhaustedBody("a3");
  delete body.decision_brief;
  const key = decisionKey(issue, "standard-v1");
  const existing = {
    issues: [{
      id: "legacy-uuid", identifier: "DEV-70", status: "blocked",
      metadata: { acceptance_decision_key: key },
    }],
  };
  const result = run([...commentsOf(...threeRounds()), comment(body)], { candidate: "sha-a3", existingDecisions: [existing] });
  assert.equal(result.outcome, "reuse_decision");
  assert.equal(result.decision.existing_identifier, "DEV-70");
  assert.equal(result.decision.resume.signal, "done");
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

test("the migrated title stays readable while the old fixed title remains searchable", () => {
  const key = "acc-0123456789abcdef";
  // Historical decisions still resolve their key from the old title.
  assert.equal(decisionKeyFromTitle(`验收决策 ${key}：DEV-7 需要决定下一步`), key);
  assert.equal(decisionKeyFromTitle(`验收决策 ${key}`), key);
  // The new readable title carries no key and never invents one from prose.
  assert.equal(decisionKeyFromTitle("「交付一个可体验功能」三次没通过验收，需要你决定下一步（DEV-7）"), null);
  assert.equal(decisionKeyFromTitle("「C9 回执补验」三次没通过验收，需要你决定下一步（DEV-7）"), null);
});

test("a readable decision title is not an identity, while its body marker still is", () => {
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"))];
  const draft = run(comments, { candidate: "sha-a3" }).decision;
  assert.deepEqual(visibleViolations(draft.title), []);
  assert.deepEqual(visibleViolations(draft.first_screen), []);
  assert.deepEqual(validateMemberTodo({
    kind: "decision", todo_key: draft.todo_key, title: draft.title, description: draft.description,
  }), []);

  // A different decision with the same readable title must never be merged in.
  const otherKey = "acc-ffffffffffffffff";
  const sameTitleOtherKey = {
    issues: [{
      id: "other-uuid", identifier: "DEV-95", title: draft.title, status: "in_progress",
      metadata: { acceptance_decision_key: otherKey },
      description: `<!-- multica-acceptance-decision ${JSON.stringify({ decision_key: otherKey })} -->`,
    }],
  };
  const other = run(comments, { candidate: "sha-a3", existingDecisions: [sameTitleOtherKey] });
  assert.equal(other.outcome, "escalate");
  assert.equal(other.decision.action, "create");

  // A row that only shares the new readable title carries no key, so it is not
  // mistaken for this decision; the body marker of a real match still reuses it.
  const titleOnly = {
    issues: [{ id: "title-only", identifier: "DEV-96", title: draft.title, status: "in_progress" }],
  };
  assert.equal(run(comments, { candidate: "sha-a3", existingDecisions: [titleOnly] }).decision.action, "create");
  const bodyMarker = {
    issues: [{
      id: "body-only", identifier: "DEV-97", status: "in_progress",
      description: `需要决定\n<!-- multica-acceptance-decision ${JSON.stringify({ decision_key: draft.key })} -->`,
    }],
  };
  const reused = run(comments, { candidate: "sha-a3", existingDecisions: [bodyMarker] });
  assert.equal(reused.outcome, "reuse_decision");
  assert.equal(reused.decision.existing_identifier, "DEV-97");
});

test("different sources and standard versions keep distinct decision identities", () => {
  const baseIssue = { ...issue, title: "同样的故事标题" };
  const a = decisionKey(baseIssue, "standard-v1");
  const b = decisionKey(baseIssue, "standard-v2");
  const c = decisionKey({ ...baseIssue, id: "issue-uuid-2", identifier: "DEV-8" }, "standard-v1");
  assert.notEqual(a, b);
  assert.notEqual(a, c);
  // A decision for another standard version is not reused for this one.
  const comments = [...commentsOf(...threeRounds()), comment(exhaustedBody("a3"))];
  const otherStandard = {
    issues: [{
      id: "other-standard", identifier: "DEV-98", status: "blocked",
      metadata: { acceptance_decision_key: b },
      description: `<!-- multica-acceptance-decision ${JSON.stringify({ decision_key: b })} -->`,
    }],
  };
  const result = run(comments, { candidate: "sha-a3", existingDecisions: [otherStandard] });
  assert.equal(result.outcome, "escalate");
  assert.equal(result.decision.key, a);
  assert.equal(result.decision.action, "create");
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
    // The key stays out of the title but remains searchable in the appendix
    // and the invisible marker, so the create-then-metadata retry still works.
    assert.doesNotMatch(title, new RegExp(result.decision.key));
    assert.match(title, /「PDF 引用跳转」需要你做一个取舍/);
    assert.match(body, /本期先发/);
    assert.match(body, new RegExp(`^decision_key: ${result.decision.key}$`, "m"));
    assert.match(body, /\[DEV-7\]\(mention:\/\/issue\/issue-uuid\)/);
    assert.match(body, /第 3 轮/);
    assert.match(body, /multica-acceptance-decision/);
    const titles = readFileSync(join(dir, "decision/titles.txt"), "utf8").trim().split("\n");
    assert.equal(titles[0], result.decision.title);
    assert.equal(titles[1], `${result.decision.title}（第 2 项）`);
    assert.deepEqual(titles, result.decision.title_candidates);
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
  assert.match(entry, /decision_brief/);
  assert.match(entry, /例如「PDF 引用跳转」/);
  assert.match(entry, /until-issue-state done/);
  assert.match(entry, /不再承担身份/);
  assert.match(entry, /titles\.txt/);
  assert.match(entry, /Active duplicate issue exists/);
  assert.match(entry, /（第 2 项）/);
  assert.match(entry, /create-result\.txt/);
  assert.match(entry, /同一键的并发重试/);
  assert.match(entry, /不得声称已派发/);
  assert.match(entry, /issue get/);
});

test("the review entry registers a project-less wakeup from the Default workflow instructions", () => {
  const entry = readFileSync(join(ACCEPTANCE_DIR, "references/review-entry.md"), "utf8");
  assert.match(entry, /workflow get default/, "the registration must read the workspace's Default workflow");
  assert.match(entry, /in-review-instruction\.md/, "the Default in_review text is written to a file before registration");
  assert.match(entry, /wc -c/, "the entry must check the instruction byte size before creating the rule");
  assert.match(entry, /-le 12000/, "the entry must enforce the 12000-byte wakeup instruction limit");
  assert.match(entry, /--instruction-file \.acceptance\/in-review-instruction\.md/);
  assert.doesNotMatch(entry, /--instruction-file\s+\S*review-entry\.md/,
    "the whole review entry cannot be submitted as the wakeup instruction");
  assert.match(entry, /--instruction "\$\(cat \.acceptance\/in-review-instruction\.md\)"/,
    "the inline fallback must submit the Default in_review text, which carries the real decision member");
  assert.doesNotMatch(entry, /--instruction "事项再次进入审核中/,
    "the old hand-written fallback without the decision member must be gone");
  assert.match(entry, /真实决策接收用户/);
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

test("common rules never hold an accepted story in review for optional confirmation", () => {
  const common = readFileSync(join(ROOT, "agent-team/common.md"), "utf8");
  assert.doesNotMatch(common, /等待用户体验决定时保留 in_review/);
  assert.match(common, /明确必验的手验项/);
  assert.match(common, /可选体验或发布确认不把已通过的故事卡在 in_review/);
  assert.match(common, /仓库已有的合并授权/);
  assert.match(common, /是否更新日常客户端由用户选择/);
});

test("the planner skill arms a project-less in_review wakeup from the Default workflow's own instructions", () => {
  const skill = readFileSync(join(ROOT, "agent-team/skills/rd-project-management/SKILL.md"), "utf8");
  for (const needle of ["project_id", "issue wakeup list", "--until-status in_review", "--mode once", "--agent-id",
    "--instruction-file", "workflow get default", "issue_field", "12000", "Never infer the decision member",
    "rule is consumed", "re-arm", "has no agent handler"]) {
    assert.ok(skill.includes(needle), `planner skill is missing ${needle}`);
  }
  // The full review entry cannot travel through the wakeup instruction limit,
  // which is why the planner passes the workflow's own in_review text instead.
  const entryBytes = statSync(join(ACCEPTANCE_DIR, "references/review-entry.md")).size;
  assert.ok(entryBytes > 12000, `review-entry.md is ${entryBytes} bytes; update the planner guidance if it now fits`);
});

test("the CLI runs through a symlinked skill directory and a symlinked file", () => {
  const dir = mkdtempSync(join(tmpdir(), "review-verdict-link-"));
  try {
    symlinkSync(ACCEPTANCE_DIR, join(dir, "rd-acceptance"), "dir");
    const viaDir = execFileSync(process.execPath, [
      join(dir, "rd-acceptance/scripts/review-verdict.mjs"), "--help",
    ], { encoding: "utf8" });
    assert.match(viaDir, /Usage:/);
    assert.match(viaDir, /--owner-user-id/);

    symlinkSync(TOOL, join(dir, "review-verdict.mjs"));
    const viaFile = execFileSync(process.execPath, [join(dir, "review-verdict.mjs"), "--help"], { encoding: "utf8" });
    assert.match(viaFile, /Usage:/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});