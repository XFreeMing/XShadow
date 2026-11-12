import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { apiClient, initializeTeam, loadTemplate, main, parseArgs, selectRuntimes } from "./agent-team.mjs";
import { buildContext, formatContext, readLiveContext, readTaskDiagnosis } from "./agent-team-context.mjs";
import { applySyncPlan, formatSyncReport, prepareSync } from "./agent-team-sync.mjs";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const template = loadTemplate(join(ROOT, "agent-team/team.json"));

function fixture(idPrefix = "destination") {
  let next = 1;
  const state = {
    agents: [], skills: [], squads: [], members: {}, bindings: {}, calls: [],
    issues: [], tasks: [], wakeups: [],
    runtimes: Object.keys(template.runtime_limits).map((provider) => ({ id: `target-${provider}`, provider, name: provider, status: "online", daemon_id: "test-machine", max_concurrent_tasks: null })),
  };
  const modelLists = Object.fromEntries(state.runtimes.map((r) => [r.id, [...new Set(template.agents.filter((a) => a.provider === r.provider).map((a) => a.model))].map((id) => ({ id, thinking: { supported_levels: [{ value: "high" }] }, supports_explicit_standard_service_tier: true }))]));
  const mutations = () => state.calls.filter(([method, path]) => method !== "GET" && !path.endsWith("/models"));
  const api = async (method, input, body) => {
    state.calls.push([method, input, structuredClone(body)]);
    const path = input.split("?")[0];
    const parts = path.split("/").filter(Boolean);
    if (path === "/api/me") return { id: "owner" };
    if (path === "/api/issues") {
      assert.equal(method, "GET");
      return { issues: structuredClone(state.issues), total: state.issues.length };
    }
    if (path === "/api/agent-task-snapshot") {
      assert.equal(method, "GET");
      return structuredClone(state.tasks);
    }
    if (path === "/api/issue-wakeups") {
      assert.equal(method, "GET");
      const params = new URL(input, "http://fixture").searchParams;
      const offset = Number(params.get("offset") ?? 0), limit = Number(params.get("limit") ?? 50);
      return { items: structuredClone(state.wakeups.slice(offset, offset + limit)), total: state.wakeups.length, counts: {} };
    }
    if (parts[1] === "runtimes") {
      if (method === "GET" && parts.length === 2) return structuredClone(state.runtimes);
      if (parts[3] === "models") return { status: "completed", supported: true, models: structuredClone(modelLists[parts[2]]) };
      if (method === "PATCH") return Object.assign(state.runtimes.find((r) => r.id === parts[2]), body);
    }
    const kind = parts[1], id = parts[2];
    if (!["agents", "skills", "squads"].includes(kind)) throw new Error(`Unexpected endpoint ${method} ${path}`);
    const rows = state[kind];
    if (!id) {
      if (method === "GET") return structuredClone(rows);
      if (method === "POST") {
        assert.ok(!rows.some((r) => r.name === body.name), `Duplicate ${kind}: ${body.name}`);
        const item = { id: `${idPrefix}-${next++}`, owner_id: "owner", ...structuredClone(body) };
        rows.push(item);
        if (kind === "agents") state.bindings[item.id] = [...(body.skill_ids ?? [])];
        if (kind === "squads") state.members[item.id] = [{ member_id: body.leader_id, member_type: "agent", role: "leader" }];
        return structuredClone(item);
      }
    }
    const row = rows.find((r) => r.id === id);
    assert.ok(row, `Missing ${kind} ${id}`);
    if (kind === "agents" && parts[3] === "skills") {
      if (method === "GET") return state.bindings[id].map((id) => ({ id }));
      assert.equal(method, "POST"); assert.equal(parts[4], "add");
      state.bindings[id] = [...new Set([...state.bindings[id], ...body.skill_ids])];
      return null;
    }
    if (kind === "squads" && parts[3] === "members") {
      if (method === "GET") return structuredClone(state.members[id]);
      if (method === "POST") { state.members[id].push(structuredClone(body)); return body; }
      assert.equal(method, "PATCH"); assert.equal(parts[4], "role");
      Object.assign(state.members[id].find((m) => m.member_id === body.member_id && m.member_type === body.member_type), body);
      return body;
    }
    if (method === "GET") return structuredClone(row);
    assert.equal(method, "PUT");
    Object.assign(row, structuredClone(body));
    return structuredClone(row);
  };
  return { state, modelLists, mutations, api };
}

const options = { daemonId: "test-machine", log() {} };

function openIssue(overrides) {
  return {
    id: "issue-fixture", workspace_id: "fixture-workspace", number: 1, identifier: "DEV-1", title: "fixture",
    status: "in_progress", status_category: "in_progress", assignee_type: "agent", assignee_id: "unassigned",
    parent_issue_id: null, stage: null, ...overrides,
  };
}

function wakeupRule(overrides) {
  return {
    id: "wakeup-fixture", issue_id: "issue-fixture", kind: "event", mode: "once",
    event_types: ["issue.status_changed"], enabled: true, issue_closed: false,
    fire_count: 0, max_fires: null, expires_at: null, timed_out_at: null,
    paused_reason: null, last_error: null, active_runs: 0, source: "member", rule: null, ...overrides,
  };
}

test("task diagnosis separates runs, waiting parents, parked work, blockers and missing handoffs", async () => {
  const f = fixture();
  await initializeTeam(f.api, template, options);
  const agent = f.state.agents[0], squad = f.state.squads[0];
  f.state.issues = [
    openIssue({ id: "run", identifier: "DEV-10", assignee_id: agent.id }),
    openIssue({ id: "parent", identifier: "DEV-11", assignee_type: "squad", assignee_id: squad.id }),
    openIssue({ id: "child-a", identifier: "DEV-12", parent_issue_id: "parent", assignee_id: agent.id }),
    openIssue({ id: "child-b", identifier: "DEV-13", parent_issue_id: "parent", assignee_id: agent.id }),
    openIssue({ id: "backlog", identifier: "DEV-14", status: "backlog", status_category: "backlog", assignee_id: agent.id }),
    openIssue({ id: "parked", identifier: "DEV-15", status: "backlog", status_category: "backlog", assignee_type: null, assignee_id: null }),
    openIssue({ id: "blocked", identifier: "DEV-16", status: "blocked", status_category: "blocked", assignee_id: agent.id }),
    openIssue({ id: "review", identifier: "DEV-17", status: "in_review", status_category: "in_review", assignee_id: agent.id }),
    openIssue({ id: "idle", identifier: "DEV-18", assignee_id: agent.id }),
    openIssue({ id: "event", identifier: "DEV-19", assignee_id: agent.id }),
  ];
  f.state.tasks = [
    { id: "t1", issue_id: "run", agent_id: agent.id, status: "running" },
    { id: "t2", issue_id: "run", agent_id: agent.id, status: "queued" },
    { id: "t3", issue_id: "run", agent_id: agent.id, status: "completed" },
    { id: "t4", issue_id: "idle", agent_id: agent.id, status: "failed" },
  ];
  f.state.wakeups = [wakeupRule({ id: "w1", issue_id: "child-b" }), wakeupRule({ id: "w2", issue_id: "event" })];
  const live = await readLiveContext(f.api, template);
  const start = f.state.calls.length;
  const report = await readTaskDiagnosis(f.api, live, { now: Date.parse("2026-11-12T10:00:00Z") });
  const by = Object.fromEntries(report.issues.map((i) => [i.identifier, i]));

  assert.equal(report.status, "verified");
  assert.equal(by["DEV-10"].category, "active_run");
  assert.equal(by["DEV-10"].run_counts.running, 1);
  assert.equal(by["DEV-10"].run_counts.queued, 1);
  assert.equal(by["DEV-10"].run_counts.completed, undefined);
  assert.equal(by["DEV-10"].owner, agent.name);
  assert.equal(by["DEV-11"].category, "waiting_children");
  assert.deepEqual(by["DEV-11"].children.map((c) => c.identifier), ["DEV-12", "DEV-13"]);
  assert.equal(by["DEV-11"].children.find((c) => c.identifier === "DEV-12").category, "none");
  assert.equal(by["DEV-11"].children.find((c) => c.identifier === "DEV-12").progressing, false);
  assert.equal(by["DEV-11"].children.find((c) => c.identifier === "DEV-13").category, "continuation");
  assert.match(by["DEV-11"].facts.join("；"), /1 项不会自行推进/);
  assert.match(by["DEV-11"].facts.join("；"), /父项自身无有效接续/);
  assert.equal(by["DEV-14"].category, "backlog_assigned");
  assert.equal(by["DEV-15"].category, "backlog_unassigned");
  assert.equal(by["DEV-16"].category, "blocked");
  assert.equal(by["DEV-17"].category, "awaiting_review");
  assert.equal(by["DEV-18"].category, "no_continuation");
  assert.equal(by["DEV-19"].category, "waiting_continuation");
  assert.match(by["DEV-19"].facts.join("；"), /外部条件未验证/);
  assert.equal(report.counts.active_runs, 2);
  assert.equal(report.summary.active_run, 1);

  // One workspace-wide read per source — never one request per issue — and read-only.
  const calls = f.state.calls.slice(start);
  assert.equal(calls.length, 3);
  assert.ok(calls.every(([method]) => method === "GET"));
  const text = formatContext({
    version: 1, observed_at: "t", target: { profile: "fixture" }, tasks: report,
    template: { agents: [], squads: [], common: "" },
    live: { status: "verified", agents: [], squads: [], differences: [], runtimes: [] },
  });
  assert.match(text, /## 任务诊断/);
  assert.match(text, /DEV-11/);
  assert.match(text, /等待子项/);
  assert.match(text, /业务状态不等于活动 run/);
});

test("expired, consumed and errored wakeups are not reported as usable continuations", async () => {
  const f = fixture();
  await initializeTeam(f.api, template, options);
  const agent = f.state.agents[0];
  const now = Date.parse("2026-11-12T10:00:00Z");
  f.state.issues = [
    openIssue({ id: "expired", identifier: "DEV-20", assignee_id: agent.id }),
    openIssue({ id: "consumed", identifier: "DEV-21", assignee_id: agent.id }),
    openIssue({ id: "errored", identifier: "DEV-22", assignee_id: agent.id }),
    openIssue({ id: "overdue", identifier: "DEV-23", assignee_id: agent.id }),
    openIssue({ id: "scheduled", identifier: "DEV-24", assignee_id: agent.id }),
    openIssue({ id: "paused", identifier: "DEV-25", assignee_id: agent.id }),
  ];
  f.state.wakeups = [
    wakeupRule({ id: "w1", issue_id: "expired", expires_at: "2026-11-12T09:00:00Z" }),
    wakeupRule({ id: "w2", issue_id: "consumed", mode: "once", fire_count: 1 }),
    wakeupRule({ id: "w3", issue_id: "errored", last_error: "cannot resolve PR link" }),
    wakeupRule({ id: "w4", issue_id: "overdue", kind: "at", event_types: [], next_fire_at: "2026-11-12T09:30:00Z" }),
    wakeupRule({ id: "w5", issue_id: "scheduled", kind: "at", event_types: [], next_fire_at: "2026-11-12T12:00:00Z" }),
    wakeupRule({ id: "w6", issue_id: "paused", paused_reason: "capacity" }),
  ];
  const live = await readLiveContext(f.api, template);
  const report = await readTaskDiagnosis(f.api, live, { now });
  const by = Object.fromEntries(report.issues.map((i) => [i.identifier, i]));

  for (const [identifier, pattern] of [["DEV-20", /已过期/], ["DEV-21", /一次性规则已触发/], ["DEV-22", /规则错误/], ["DEV-23", /已到期但未触发/], ["DEV-25", /已暂停/]]) {
    assert.equal(by[identifier].category, "no_continuation", identifier);
    assert.match(by[identifier].facts.join("；"), pattern, identifier);
  }
  assert.equal(by["DEV-24"].category, "waiting_continuation");
  assert.match(by["DEV-24"].facts.join("；"), /已配置定时唤醒/);
});

test("--tasks is explicit, read-only, and a failed query stays unverified instead of looking idle", async (t) => {
  const dir = mkdtempSync(join(tmpdir(), "agent-team-tasks-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  mkdirSync(join(dir, "fixture"));
  writeFileSync(join(dir, "fixture/config.json"), JSON.stringify({ server_url: "http://example.test", workspace_id: "fixture-workspace", token: "hidden-token" }));
  const f = fixture();
  await initializeTeam(f.api, template, options);
  const agent = f.state.agents[0];
  f.state.issues = [openIssue({ id: "run", identifier: "DEV-10", assignee_id: agent.id })];
  f.state.tasks = [{ id: "t1", issue_id: "run", agent_id: agent.id, status: "running" }];
  const deps = { root: dir, env: {}, client: () => f.api };

  const defaultStart = f.state.calls.length;
  const plain = await buildContext(template, { profile: "fixture", profilesDir: dir }, deps);
  assert.equal(plain.tasks, undefined);
  assert.ok(f.state.calls.slice(defaultStart).every(([, path]) => !["/api/issues", "/api/agent-task-snapshot", "/api/issue-wakeups"].some((p) => path.split("?")[0] === p)));

  const start = f.state.calls.length;
  const report = await buildContext(template, { profile: "fixture", profilesDir: dir, tasks: true }, deps);
  assert.equal(report.tasks.status, "verified");
  assert.equal(report.tasks.issues[0].category, "active_run");
  assert.ok(f.state.calls.slice(start).every(([method]) => method === "GET"));
  assert.match(formatContext(report), /## 任务诊断/);
  assert.match(formatContext(report), /DEV-10/);

  const failing = { ...deps, client: () => async (method, path, body) => {
    if (path === "/api/agent-task-snapshot") throw new Error("GET /api/agent-task-snapshot: HTTP 500 Bearer secret-token-value");
    return f.api(method, path, body);
  } };
  const broken = await buildContext(template, { profile: "fixture", profilesDir: dir, tasks: true }, failing);
  assert.equal(broken.tasks.status, "unavailable");
  assert.match(broken.tasks.reason, /HTTP 500/);
  assert.match(formatContext(broken), /未核验/);
  assert.match(formatContext(broken), /不能据此认为没有运行/);
  assert.doesNotMatch(JSON.stringify(broken), /hidden-token|secret-token-value/);

  assert.equal(parseArgs(["context", "--tasks"]).tasks, true);
  assert.throws(() => parseArgs(["init", "--tasks"]), /only to context/);
  assert.throws(() => parseArgs(["context", "--offline", "--tasks"]), /needs a live profile/);
  assert.equal(parseArgs(["context"]).tasks, undefined);
});

test("a condition-based event rule is a real continuation even without event_types", async () => {
  const f = fixture();
  await initializeTeam(f.api, template, options);
  const agent = f.state.agents[0];
  f.state.issues = [openIssue({ id: "condition", identifier: "DEV-30", assignee_id: agent.id })];
  f.state.wakeups = [wakeupRule({
    id: "w1", issue_id: "condition", event_types: [],
    condition: { type: "other_issue", state: "in_review", issue_id: "00000000-0000-0000-0000-000000000001", identifier: "DEV-5" },
  })];
  const live = await readLiveContext(f.api, template);
  const report = await readTaskDiagnosis(f.api, live, { now: Date.parse("2026-11-12T10:00:00Z") });
  const row = report.issues[0];
  assert.equal(row.category, "waiting_continuation");
  assert.match(row.facts.join("；"), /已配置条件 事项 DEV-5 达到 in_review（外部条件未验证）/);
  assert.doesNotMatch(row.facts.join("；"), /缺少事件类型/);
});

test("unknown rule state and system barriers stay facts, not usable continuations", async () => {
  const f = fixture();
  await initializeTeam(f.api, template, options);
  const agent = f.state.agents[0], squad = f.state.squads[0];
  f.state.issues = [
    openIssue({ id: "unknown", identifier: "DEV-31", assignee_id: agent.id }),
    openIssue({ id: "barrier", identifier: "DEV-32", assignee_id: agent.id }),
    openIssue({ id: "parent", identifier: "DEV-33", assignee_type: "squad", assignee_id: squad.id }),
    openIssue({ id: "child", identifier: "DEV-34", parent_issue_id: "parent", assignee_id: agent.id }),
  ];
  f.state.wakeups = [
    wakeupRule({ id: "w1", issue_id: "unknown", condition: { type: "future_condition" } }),
    wakeupRule({ id: "w2", issue_id: "barrier", event_types: [], rule: "child_done", system_remaining: 1 }),
    wakeupRule({ id: "w3", issue_id: "child", condition: { type: "future_condition" } }),
  ];
  const live = await readLiveContext(f.api, template);
  const report = await readTaskDiagnosis(f.api, live, { now: Date.parse("2026-11-12T10:00:00Z") });
  const by = Object.fromEntries(report.issues.map((i) => [i.identifier, i]));
  const facts = (id) => by[id].facts.join("；");

  assert.equal(by["DEV-31"].category, "continuation_unverified");
  assert.match(facts("DEV-31"), /部分规则无法验证，接续未核验/);
  assert.ok(!facts("DEV-31").includes("无可用接续"));
  assert.equal(by["DEV-32"].category, "no_continuation");
  assert.match(facts("DEV-32"), /阶段屏障 child_done\(剩余 1\)（仅随子项完成，不保证唤醒执行）/);
  assert.doesNotMatch(facts("DEV-32"), /可用接续 child_done/);
  const child = by["DEV-33"].children.find((c) => c.identifier === "DEV-34");
  assert.equal(child.category, "unverified");
  assert.equal(child.unknown, true);
  assert.equal(child.progressing, false);
  assert.match(facts("DEV-33"), /1 项接续未核验，不能判定是否会推进/);
  assert.doesNotMatch(facts("DEV-33"), /不会自行推进/);
  assert.match(facts("DEV-33"), /父项自身无有效接续，需核对是否真在等待/);
});

test("a truncated wakeup read leaves continuation unknown instead of calling the handoff broken", async () => {
  const f = fixture();
  await initializeTeam(f.api, template, options);
  const agent = f.state.agents[0];
  f.state.issues = [
    openIssue({ id: "parent", identifier: "DEV-40", assignee_type: "squad", assignee_id: f.state.squads[0].id }),
    openIssue({ id: "idle", identifier: "DEV-41", assignee_id: agent.id }),
    openIssue({ id: "child", identifier: "DEV-42", parent_issue_id: "parent", assignee_id: agent.id }),
  ];
  // 501 rules the page cap cannot fully read; none of them belong to these issues.
  f.state.wakeups = Array.from({ length: 501 }, (_, i) => wakeupRule({ id: `w${i}`, issue_id: "elsewhere", issue_identifier: "DEV-9" }));
  const live = await readLiveContext(f.api, template);
  const start = f.state.calls.length;
  const report = await readTaskDiagnosis(f.api, live, { now: Date.parse("2026-11-12T10:00:00Z") });
  const by = Object.fromEntries(report.issues.map((i) => [i.identifier, i]));

  assert.equal(report.counts.wakeup_truncated, true);
  assert.equal(report.counts.wakeup_rules, 500);
  assert.equal(f.state.calls.slice(start).filter(([, path]) => path.startsWith("/api/issue-wakeups")).length, 5);
  assert.equal(by["DEV-41"].category, "continuation_unverified");
  assert.match(by["DEV-41"].facts.join("；"), /规则未完整读取，接续未核验/);
  assert.ok(!by["DEV-41"].facts.join("；").includes("无可用接续"));
  assert.equal(by["DEV-42"].category, "continuation_unverified");
  const child = by["DEV-40"].children.find((c) => c.identifier === "DEV-42");
  assert.equal(child.category, "unverified");
  assert.equal(child.unknown, true);
  assert.equal(child.progressing, false);
  assert.match(by["DEV-40"].facts.join("；"), /1 项接续未核验，不能判定是否会推进/);
  assert.doesNotMatch(by["DEV-40"].facts.join("；"), /不会自行推进/);
  assert.match(by["DEV-40"].facts.join("；"), /父项自身接续未核验/);
  assert.equal(by["DEV-40"].category, "waiting_children");
  const text = formatContext({
    version: 1, observed_at: "t", target: { profile: "fixture" }, tasks: report,
    template: { agents: [], squads: [], common: "" },
    live: { status: "verified", agents: [], squads: [], differences: [], runtimes: [] },
  });
  assert.match(text, /接续未核验/);
  assert.match(text, /含接续未核验的子项/);
  assert.doesNotMatch(text, /含不会自行推进的子项/);
});

test("portable template contains the current roster without machine identities or credentials", () => {
  assert.equal(template.agents.length, 20);
  assert.equal(template.skills.length, 15);
  assert.equal(template.squads.length, 5);
  const serialized = JSON.stringify(template);
  assert.doesNotMatch(serialized, /\/home\/ops396|dev-xshadow-623|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i);
  for (const a of template.agents) {
    assert.equal(a.permission_mode, "private");
    assert.ok(!("custom_env" in a) && !("mcp_config" in a));
  }
});

test("new destination gets fresh references, prompts, skills and squad roles; rerun is a no-op", async () => {
  const f = fixture();
  const ids = await initializeTeam(f.api, template, options);
  assert.equal(f.state.agents.length, 20);
  assert.equal(f.state.skills.length, 15);
  assert.equal(f.state.squads.length, 5);
  for (const s of f.state.squads) {
    assert.doesNotMatch(s.instructions, /\{\{squad:/);
    for (const id of Object.values(ids.squads)) assert.ok(s.instructions.includes(id));
  }
  const pm = f.state.agents.find((a) => a.id === ids.agents.pm);
  assert.equal(pm.conversation_starters.length, 3);
  assert.deepEqual(new Set(f.state.bindings[pm.id]), new Set(template.agents.find((a) => a.key === "pm").skills.map((s) => ids.skills[s])));
  f.state.agents.push({ id: "other", name: "Unrelated", owner_id: "someone-else" });
  f.state.bindings[pm.id].push("extra-skill");
  f.state.members[ids.squads.delivery].push({ member_id: "human", member_type: "member", role: "Sponsor" });
  const before = f.mutations().length;
  await initializeTeam(f.api, template, options);
  assert.equal(f.mutations().length, before);
  assert.ok(f.state.bindings[pm.id].includes("extra-skill"));
  assert.ok(f.state.members[ids.squads.delivery].some((m) => m.member_id === "human"));
  assert.ok(f.state.agents.some((a) => a.id === "other"));
});

test("unsupported model fails before configuration writes and check mode performs no writes", async () => {
  const f = fixture();
  f.modelLists["target-claude"] = [];
  await assert.rejects(initializeTeam(f.api, template, options), /does not advertise.*deepseek/);
  assert.equal(f.mutations().length, 0);
  const good = fixture();
  await initializeTeam(good.api, template, { ...options, check: true });
  assert.equal(good.mutations().length, 0);
});

test("runtime selection never silently chooses another machine or an ambiguous provider", () => {
  const f = fixture();
  assert.throws(() => selectRuntimes(f.state.runtimes, ["claude"], {}, "other-machine"), /found 0/);
  const duplicate = { ...f.state.runtimes[0], id: "another" };
  assert.throws(() => selectRuntimes([...f.state.runtimes, duplicate], ["claude"]), /found 2/);
  assert.equal(selectRuntimes([...f.state.runtimes, duplicate], ["claude"], { claude: "another" }, "other-machine").claude.id, "another");
});

test("another owner's same-named agent stops initialization before writes", async () => {
  const f = fixture();
  f.state.agents.push({ name: template.agents[0].name, owner_id: "someone-else", id: "existing" });
  await assert.rejects(initializeTeam(f.api, template, options), /another account/);
  assert.equal(f.mutations().length, 0);
});

test("retry after a partial creation reuses completed objects", async () => {
  const f = fixture();
  let fail = true;
  const api = async (method, path, body) => {
    if (fail && method === "POST" && path === "/api/agents" && f.state.agents.length === 2) { fail = false; throw new Error("interrupted"); }
    return f.api(method, path, body);
  };
  await assert.rejects(initializeTeam(api, template, options), /interrupted/);
  assert.equal(f.state.agents.length, 2);
  await initializeTeam(api, template, options);
  assert.equal(f.state.agents.length, 20);
  assert.equal(f.state.skills.length, 15);
});

test("readback detects a server that ignores the requested settings", async () => {
  const f = fixture();
  await initializeTeam(f.api, template, options);
  f.state.agents[0].model = "wrong-model";
  const api = (method, path, body) => method === "PUT" && path.startsWith("/api/agents/") ? null : f.api(method, path, body);
  await assert.rejects(initializeTeam(api, template, options), /Readback mismatch/);
});

test("skill support files are synchronized with bodies and validated paths", async (t) => {
  const dir = mkdtempSync(join(tmpdir(), "agent-team-template-")); t.after(() => rmSync(dir, { recursive: true, force: true }));
  const raw = JSON.parse(readFileSync(join(ROOT, "agent-team/team.json"), "utf8"));
  writeFileSync(join(dir, "common.md"), "Shared instructions");
  for (const s of raw.skills) {
    mkdirSync(join(dir, s.directory, "references"), { recursive: true });
    writeFileSync(join(dir, s.directory, "SKILL.md"), "Skill content");
    writeFileSync(join(dir, s.directory, "references/evidence.md"), "Verified evidence");
  }
  const file = join(dir, "team.json"); writeFileSync(file, JSON.stringify(raw));
  const loaded = loadTemplate(file), f = fixture();
  await initializeTeam(f.api, loaded, options);
  assert.equal(f.state.skills[0].files[0].content, "Verified evidence");
  raw.common_instructions = "../outside.md"; writeFileSync(file, JSON.stringify(raw));
  assert.throws(() => loadTemplate(file), /escapes template/);
});

test("HTTP profile execution uses the selected account/workspace without an agent CLI", async (t) => {
  const f = fixture();
  const server = createServer(async (req, res) => {
    try {
      assert.equal(req.headers.authorization, "Bearer fixture-token");
      assert.equal(req.headers["x-workspace-id"], "fixture-workspace");
      let text = ""; for await (const chunk of req) text += chunk;
      const result = await f.api(req.method, req.url, text ? JSON.parse(text) : undefined);
      res.setHeader("Content-Type", "application/json"); res.end(JSON.stringify(result));
    } catch (error) { res.statusCode = 500; res.end(error.message); }
  });
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  t.after(() => server.close());
  const dir = mkdtempSync(join(tmpdir(), "agent-team-profile-")); t.after(() => rmSync(dir, { recursive: true, force: true }));
  mkdirSync(join(dir, "fixture"));
  const config = { server_url: `http://127.0.0.1:${server.address().port}`, workspace_id: "fixture-workspace", token: "fixture-token" };
  writeFileSync(join(dir, "fixture/config.json"), JSON.stringify(config));
  const args = ["init", "--profile", "fixture", "--profiles-dir", dir, ...f.state.runtimes.flatMap((r) => ["--runtime", `${r.provider}=${r.id}`])];
  await main(args, { MULTICA_TOKEN: "wrong-token", MULTICA_SERVER_URL: "https://wrong.invalid" });
  assert.equal(f.state.agents.length, 20);
  assert.throws(() => apiClient({ ...config, token: "" }), /Profile needs/);
});

test("Make dispatches init exactly once including parallel make and never combines lifecycle goals", () => {
  const out = execFileSync("make", ["-n", "-j", "agent", "init", "ARGS=--help"], { cwd: ROOT, encoding: "utf8" });
  assert.equal(out.split("\n").filter((line) => line.includes("node scripts/agent-team.mjs")).length, 1);
  assert.throws(() => execFileSync("make", ["-n", "agent", "init", "down"], { cwd: ROOT, stdio: "pipe" }));
  assert.throws(() => parseArgs(["init", "--profile", "../wrong"]), /Invalid profile/);
  assert.throws(() => parseArgs(["init", "--runtime", "codex=one", "--runtime", "codex=two"]), /repeated runtime/);
  const context = execFileSync("make", ["-n", "-j", "agent", "context", "ARGS=--offline"], { cwd: ROOT, encoding: "utf8" });
  assert.equal(context.split("\n").filter((line) => line.includes("node scripts/agent-team.mjs context")).length, 1);
  const sync = execFileSync("make", ["-n", "-j", "agent", "sync", "ARGS=--check"], { cwd: ROOT, encoding: "utf8" });
  assert.equal(sync.split("\n").filter((line) => line.includes("node scripts/agent-team.mjs sync")).length, 1);
  assert.throws(() => execFileSync("make", ["-n", "agent", "init", "context"], { cwd: ROOT, stdio: "pipe" }));
  assert.throws(() => execFileSync("make", ["-n", "agent", "sync", "context"], { cwd: ROOT, stdio: "pipe" }));
  assert.throws(() => parseArgs(["init", "--offline"]), /only to context/);
  assert.throws(() => parseArgs(["sync", "--tasks"]), /only to context/);
  assert.throws(() => parseArgs(["context", "--runtime", "grok=one"]), /reads existing bindings/);
});

test("live context is read-only, includes the whole workspace and reports actionable configuration drift", async () => {
  const f = fixture();
  const ids = await initializeTeam(f.api, template, options);
  const start = f.state.calls.length;
  const initial = await readLiveContext(f.api, template);
  assert.equal(initial.status, "verified");
  assert.deepEqual(initial.differences, []);
  assert.ok(f.state.calls.slice(start).every(([method]) => method === "GET"));

  const pm = f.state.agents.find((a) => a.id === ids.agents.pm);
  pm.model = "new-model";
  pm.instructions = "changed private prompt";
  pm.custom_env = { API_KEY: "secret-never-output" };
  pm.mcp_config = { secret: "secret-never-output" };
  f.state.skills[0].content = "changed skill body";
  f.state.members[ids.squads.delivery][1].role = "Changed role";
  f.state.agents.push({ id: "added-in-ui", name: "New specialist", model: "another-model", custom_env: { KEY: "secret-never-output" } });
  f.state.bindings["added-in-ui"] = [];
  f.state.runtimes[0].status = "offline";
  f.state.runtimes[0].custom_env = { KEY: "secret-never-output" };
  const current = await readLiveContext(f.api, template);
  assert.ok(current.differences.some((d) => d.name === pm.name && d.fields.includes("model") && d.fields.includes("instructions")));
  assert.ok(current.differences.some((d) => d.kind === "skill" && d.fields.includes("content")));
  assert.ok(current.differences.some((d) => d.kind === "squad" && d.fields.includes("members")));
  assert.ok(current.differences.some((d) => d.name === "New specialist" && d.type === "extra"));
  assert.ok(current.runtimes.some((r) => r.status === "offline"));
  assert.doesNotMatch(JSON.stringify(current), /secret-never-output|changed private prompt|changed skill body/);
});

test("context distinguishes missing and renamed members from an unverified workspace", async () => {
  const f = fixture();
  await initializeTeam(f.api, template, options);
  f.state.agents[0].name = "Renamed chief";
  const current = await readLiveContext(f.api, template);
  assert.ok(current.differences.some((d) => d.kind === "agent" && d.name === template.agents[0].name && d.type === "missing"));
  assert.ok(current.differences.some((d) => d.name === "Renamed chief" && d.type === "extra"));
});

test("offline context never discovers a profile, connects, starts services or pretends to be live", async () => {
  const report = await buildContext(template, { offline: true }, {
    root: "/missing", client() { throw new Error("must not connect"); }, discoverProfile() { throw new Error("must not discover"); },
  });
  assert.equal(report.live.status, "offline");
  assert.equal(report.template.agents.length, 20);
  assert.match(formatContext(report), /仅仓库模板；实时状态未核验/);
  assert.doesNotMatch(formatContext(report), /已读取目标工作区实时配置/);
});

test("local profile selection is explicit, overrideable and unavailable state stays honest", async (t) => {
  const dir = mkdtempSync(join(tmpdir(), "agent-team-context-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  mkdirSync(join(dir, "agent-team"));
  for (const profile of ["local-target", "explicit-target"]) {
    mkdirSync(join(dir, profile));
    writeFileSync(join(dir, profile, "config.json"), JSON.stringify({ server_url: "http://example.test", workspace_id: profile, token: "hidden-token" }));
  }
  writeFileSync(join(dir, "agent-team/local.json"), JSON.stringify({ profile: "local-target" }));
  const f = fixture(); await initializeTeam(f.api, template, options);
  const seen = [];
  const deps = { root: dir, env: { MULTICA_DEV_PROFILE: "wrong-target" }, client(config) { seen.push(config.workspace_id); return f.api; } };
  const local = await buildContext(template, { profilesDir: dir }, deps);
  assert.equal(local.target.profile, "local-target");
  assert.equal(local.live.status, "verified");
  const explicit = await buildContext(template, { profile: "explicit-target", profilesDir: dir }, deps);
  assert.equal(explicit.target.profile, "explicit-target");
  assert.deepEqual(seen, ["local-target", "explicit-target"]);
  assert.doesNotMatch(JSON.stringify(local), /hidden-token/);
  const down = await buildContext(template, { profilesDir: dir }, { ...deps, client() { return async () => { throw new Error("service unavailable"); }; } });
  assert.equal(down.live.status, "unavailable");
  assert.equal(down.template.agents.length, 20);
  assert.match(formatContext(down), /无法比较/);
  const missing = await buildContext(template, { profile: "missing", profilesDir: dir }, deps);
  assert.equal(missing.live.status, "unavailable");
});

function scratchTeam(t) {
  const dir = mkdtempSync(join(tmpdir(), "agent-team-sync-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  copyFileSync(join(ROOT, "agent-team/common.md"), join(dir, "common.md"));
  return dir;
}

async function syncInto(f, dir, runtimeIds = {}) {
  const current = loadTemplate(join(ROOT, "agent-team/team.json"));
  const plan = await prepareSync(f.api, { template: current, templatePath: join(dir, "team.json"), runtimeIds, displayRoot: dir });
  const report = applySyncPlan(plan, { check: false });
  return { plan, report, template: loadTemplate(join(dir, "team.json")) };
}

test("sync round-trips live agents, skills and squads into a template that init recreates", async (t) => {
  const source = fixture("source");
  const ids = await initializeTeam(source.api, template, options);
  const dir = scratchTeam(t);

  // Changes a user can make in the UI after initialization: a bound new skill
  // whose name is not a legal key, an unbound own skill, a shared skill, a
  // custom-instruction agent, concurrency drift and a squad role edit.
  const reactName = template.agents.find((a) => a.provider === "claude").name;
  const react = source.state.agents.find((a) => a.name === reactName);
  react.max_concurrent_tasks = 2;
  const uiAgent = {
    id: "ui-agent", owner_id: "owner", name: "UI 专项工程师", description: "现场新建", status: "active",
    runtime_id: react.runtime_id, model: react.model, thinking_level: "high", service_tier: "",
    max_concurrent_tasks: 2, conversation_starters: [{ label: "开始", prompt: "先读代码" }],
    permission_mode: "private", instructions: "只改 UI，不碰后端。",
  };
  source.state.agents.push(uiAgent);
  source.state.bindings[uiAgent.id] = [];
  const newSkill = { id: "skill-release", name: "Release Notes", description: "发布说明", config: {}, created_by: "owner", content: "# Release Notes\n用于发布说明。", files: [{ path: "notes/template.txt", content: "template" }] };
  const unboundSkill = { id: "skill-unbound", name: "Unbound Helper", description: "", config: {}, created_by: "owner", content: "未被绑定也要保留。", files: [] };
  const sharedSkill = { id: "skill-shared", name: "Shared Handbook", description: "other", config: {}, created_by: "someone-else", content: "来自其他账号。", files: [] };
  source.state.skills.push(newSkill, unboundSkill, sharedSkill);
  source.state.bindings[uiAgent.id].push(newSkill.id);
  const squadKey = template.squads[0].key;
  const member = source.state.members[ids.squads[squadKey]].find((m) => m.role !== "leader");
  member.role = "reviewer";
  const memberName = source.state.agents.find((a) => a.id === member.member_id).name;

  const start = source.state.calls.length;
  const { report, template: exported } = await syncInto(source, dir);
  assert.ok(source.state.calls.slice(start).every(([method]) => method === "GET"), "sync must only read");

  const exportedUi = exported.agents.find((a) => a.name === uiAgent.name);
  assert.match(exportedUi.key, /^ui/);
  assert.equal(exportedUi.include_common, false);
  assert.equal(exportedUi.instructions, uiAgent.instructions);
  assert.equal(exportedUi.conversation_starters[0].prompt, "先读代码");
  assert.equal(exported.agents.find((a) => a.name === react.name).include_common, undefined);
  assert.equal(exported.agents.find((a) => a.name === react.name).max_concurrent_tasks, 2);
  assert.equal(exported.agents.find((a) => a.name === react.name).key, template.agents.find((a) => a.name === react.name).key);

  const release = exported.skills.find((s) => s.name === newSkill.name);
  assert.equal(release.key, "release-notes");
  assert.notEqual(release.key, release.name);
  assert.ok(exported.skills.some((s) => s.name === unboundSkill.name));
  assert.ok(!exported.skills.some((s) => s.name === sharedSkill.name));
  assert.ok(report.skipped.some((s) => s.kind === "skill" && s.name === sharedSkill.name));
  assert.ok(exportedUi.skills.every((key) => exported.skills.some((s) => s.key === key)));
  assert.ok(exportedUi.skills.includes(release.key));
  assert.deepEqual(exported.skills.find((s) => s.key === release.key).files, newSkill.files);

  const exportedSquad = exported.squads.find((s) => s.key === squadKey);
  assert.match(exportedSquad.instructions, /\{\{squad:/);
  assert.doesNotMatch(exportedSquad.instructions, /[0-9a-f]{8}-[0-9a-f]{4}/);
  const memberKey = exported.agents.find((a) => a.name === memberName).key;
  assert.equal(exportedSquad.members.find((m) => m.agent === memberKey).role, "reviewer");
  assert.ok(exported.squads.some((s) => s.key === template.squads[4].key));

  // A second sync plans nothing and writes identical bytes.
  const before = readFileSync(join(dir, "team.json"), "utf8");
  const replan = await prepareSync(source.api, { template: loadTemplate(join(dir, "team.json")), templatePath: join(dir, "team.json"), displayRoot: dir });
  assert.deepEqual(replan.writes, []);
  assert.deepEqual(replan.deletions, []);
  const rereport = applySyncPlan(replan, { check: false });
  assert.equal(readFileSync(join(dir, "team.json"), "utf8"), before);
  assert.match(formatSyncReport(rereport, null), new RegExp(`${exported.agents.length} in template; 0 added, 0 changed, 0 removed`));

  // A fresh instance initialized from the exported template recreates the same
  // team, and a rerun writes nothing.
  const dest = fixture("recreated");
  const rebased = loadTemplate(join(dir, "team.json"));
  await initializeTeam(dest.api, rebased, options);
  const writes = dest.mutations().length;
  await initializeTeam(dest.api, rebased, options);
  assert.equal(dest.mutations().length, writes, "rerun init must not write");
  assert.equal(dest.state.agents.find((a) => a.name === uiAgent.name).instructions, uiAgent.instructions);
  assert.equal(dest.state.agents.find((a) => a.name === react.name).instructions, `${rebased.common}\n${rebased.agents.find((a) => a.name === react.name).instructions}`);
  assert.deepEqual(dest.state.skills.find((s) => s.name === newSkill.name).files, newSkill.files);
  const destSquad = dest.state.squads.find((s) => s.name === exportedSquad.name);
  assert.doesNotMatch(destSquad.instructions, /\{\{squad:/);
  assert.notEqual(destSquad.id, ids.squads[squadKey]);
  assert.ok(destSquad.instructions.includes(destSquad.id), "references must point at the new instance");
  assert.ok(!destSquad.instructions.includes(ids.squads[squadKey]), "source instance references must not survive");
});

test("sync fails before writing on unexpressible configuration and never leaks values", async (t) => {
  const source = fixture();
  await initializeTeam(source.api, template, options);
  const dir = scratchTeam(t);
  copyFileSync(join(ROOT, "agent-team/team.json"), join(dir, "team.json"));
  const before = readFileSync(join(dir, "team.json"), "utf8");
  const load = () => loadTemplate(join(ROOT, "agent-team/team.json"));

  const agent = source.state.agents[0];
  agent.has_custom_env = true;
  agent.custom_env_key_count = 1;
  agent.mcp_config = { token: "secret-value-never-output" };
  await assert.rejects(
    prepareSync(source.api, { template: load(), templatePath: join(dir, "team.json"), displayRoot: dir }),
    (error) => /custom_env/.test(error.message) && /mcp_config/.test(error.message) && !/secret-value-never-output/.test(error.message),
  );
  assert.equal(readFileSync(join(dir, "team.json"), "utf8"), before);
  assert.deepEqual(readdirSync(dir).sort(), ["common.md", "team.json"]);

  agent.mcp_config = {};
  agent.has_custom_env = false;
  agent.custom_env_key_count = 0;
  agent.mcp_config_redacted = false;
  source.state.skills.push({ id: "bad-skill", name: "Bad Files", description: "", config: {}, created_by: "owner", content: "x", files: [{ path: "../../escape.txt", content: "x" }] });
  await assert.rejects(
    prepareSync(source.api, { template: load(), templatePath: join(dir, "team.json"), displayRoot: dir }),
    /path outside the skill directory/,
  );
  assert.equal(readFileSync(join(dir, "team.json"), "utf8"), before);

  // Portable-text scanning is not limited to prompt bodies.
  agent.conversation_starters = [{ label: "开始", prompt: "交给 123e4567-e89b-12d3-a456-426614174000 处理" }];
  source.state.skills.push({
    id: "leaky-skill", name: "Leaky Skill", description: "ref 123e4567-e89b-12d3-a456-426614174000",
    config: {}, created_by: "owner", content: "读取 /home/alice/.ssh/id_rsa 后继续",
    files: [{ path: "notes/a.txt", content: "sk-abcdefghijklmnopqrstuvwx" }],
  });
  await assert.rejects(
    prepareSync(source.api, { template: load(), templatePath: join(dir, "team.json"), displayRoot: dir }),
    (error) => /skill Leaky Skill: description contains an instance UUID/.test(error.message)
      && /skill Leaky Skill: content contains a machine path/.test(error.message)
      && /files\[notes\/a\.txt\] contains a credential/.test(error.message)
      && /conversation_starters\[0\]\.prompt contains an instance UUID/.test(error.message)
      && !/alice|123e4567|sk-abcdef/.test(error.message),
  );
  assert.equal(readFileSync(join(dir, "team.json"), "utf8"), before);
});

test("sync leaves template files untouched when a service read fails", async (t) => {
  const source = fixture();
  await initializeTeam(source.api, template, options);
  const dir = scratchTeam(t);
  copyFileSync(join(ROOT, "agent-team/team.json"), join(dir, "team.json"));
  const before = readFileSync(join(dir, "team.json"), "utf8");
  const failing = async (method, path, body) => {
    if (path.endsWith("/members")) throw new Error("GET members: HTTP 503");
    return source.api(method, path, body);
  };
  await assert.rejects(
    prepareSync(failing, { template: loadTemplate(join(ROOT, "agent-team/team.json")), templatePath: join(dir, "team.json"), displayRoot: dir }),
    /HTTP 503/,
  );
  assert.equal(readFileSync(join(dir, "team.json"), "utf8"), before);
  assert.deepEqual(readdirSync(dir).sort(), ["common.md", "team.json"]);
});

test("sync --check validates every planned path, writes nothing and reports counts", async (t) => {
  const source = fixture();
  await initializeTeam(source.api, template, options);
  const dir = scratchTeam(t);
  source.state.agents[0].max_concurrent_tasks = 3;
  const plan = await prepareSync(source.api, { template: loadTemplate(join(ROOT, "agent-team/team.json")), templatePath: join(dir, "team.json"), displayRoot: dir });
  assert.ok(plan.writes.some((w) => w.kind === "template"));
  assert.ok(plan.writes.every((w) => typeof w.content === "string"));
  const report = applySyncPlan(plan, { check: true });
  assert.equal(report.written, 0);
  assert.ok(report.files.some((f) => f.path === "team.json" && f.status === "A"));
  assert.deepEqual(readdirSync(dir).sort(), ["common.md"]);
  const text = formatSyncReport(report, { server_url: "http://example.test", workspace_id: "w", profile: "p" });
  assert.match(text, /No files were written \(--check\)/);
  assert.match(text, new RegExp(`${template.agents.length} in template`));

  const outside = mkdtempSync(join(tmpdir(), "agent-team-outside-"));
  t.after(() => rmSync(outside, { recursive: true, force: true }));
  writeFileSync(join(outside, "secret.txt"), "outside");
  symlinkSync(join(outside, "secret.txt"), join(dir, "team.json"));
  assert.throws(() => applySyncPlan(plan, { check: true }), /symlink/);
  assert.equal(readFileSync(join(outside, "secret.txt"), "utf8"), "outside");
});

test("sync never guesses which of several bound runtimes owns the concurrency limit", async (t) => {
  const source = fixture();
  await initializeTeam(source.api, template, options);
  const dir = scratchTeam(t);
  const second = { id: "target-claude-2", provider: "claude", name: "claude second", status: "online", daemon_id: "test-machine", max_concurrent_tasks: 10 };
  source.state.runtimes.push(second);
  const agent = source.state.agents.find((a) => a.runtime_id === "target-claude");
  agent.runtime_id = second.id;
  const load = () => loadTemplate(join(ROOT, "agent-team/team.json"));
  await assert.rejects(
    prepareSync(source.api, { template: load(), templatePath: join(dir, "team.json"), displayRoot: dir }),
    /pass --runtime claude=/,
  );
  second.max_concurrent_tasks = 7;
  await assert.rejects(
    prepareSync(source.api, { template: load(), templatePath: join(dir, "team.json"), runtimeIds: { claude: second.id }, displayRoot: dir }),
    /different concurrency limits/,
  );
  second.max_concurrent_tasks = 10;
  const plan = await prepareSync(source.api, { template: load(), templatePath: join(dir, "team.json"), runtimeIds: { claude: second.id }, displayRoot: dir });
  assert.equal(plan.runtimeLimits.claude, 10);
  assert.equal(plan.next.runtime_limits.codex, 5);
});
