// @vitest-environment node
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import {
  StateStore, acquireLock, cycleKeyFor, defaultLockDir, defaultStateDir, findProjectManager,
  formatStatus, isGap, lockPath, matchesIssueFilter, parseWatchdogArgs, renderInstruction,
  ruleCycleKey, runWatchdog, scanOnce,
} from "./agent-team-watchdog.mjs";

const SCRIPT = fileURLToPath(new URL("./agent-team-watchdog.mjs", import.meta.url));
const ROOT = fileURLToPath(new URL("..", import.meta.url));
const WS = "11111111-2222-3333-4444-555555555555";
const PM = { id: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", name: "项目经理", status: "active" };
const BASE = Date.parse("2026-11-19T00:10:00Z");

function gapIssue(id = "10000000-0000-0000-0000-000000000001", identifier = "DEV-59", overrides = {}) {
  return {
    id, identifier, title: "试点事项",
    status: "in_progress", status_category: "in_progress",
    assignee_type: "agent", assignee_id: PM.id,
    last_activity_at: "2026-11-19T00:05:00Z",
    parent_issue_id: null,
    ...overrides,
  };
}

function createWorld({ issues = [gapIssue()], rules = [] } = {}) {
  return {
    issues, snapshot: [], rules: [...rules],
    issueWakeups: new Map(), wakeupRuns: new Map(), taskRuns: new Map(), comments: new Map(),
    posts: [], calls: [], fail: {}, postHook: null, nextId: 1,
  };
}

function fakeApi(world) {
  return async function api(method, path, body) {
    world.calls.push(`${method} ${path}`);
    const failure = world.fail[`${method} ${path.split("?")[0]}`];
    if (failure) {
      if (method === "POST" && path.split("?")[0].endsWith("/wakeups")) world.posts.push(body);
      throw new Error(`${method} ${path.split("?")[0]}: HTTP ${failure}. Check account access and server compatibility.`);
    }
    const url = new URL(path, "http://fake.local");
    if (method === "GET" && url.pathname === "/api/issues" && url.searchParams.get("open_only") === "true") {
      return { issues: world.issues };
    }
    if (method === "GET" && url.pathname === "/api/agent-task-snapshot") return world.snapshot;
    if (method === "GET" && url.pathname === "/api/issue-wakeups") {
      const limit = Number(url.searchParams.get("limit") ?? 100);
      const offset = Number(url.searchParams.get("offset") ?? 0);
      return { items: world.rules.slice(offset, offset + limit), total: world.rules.length };
    }
    const runs = /^\/api\/issues\/([^/]+)\/wakeups\/([^/]+)\/runs$/.exec(url.pathname);
    if (method === "GET" && runs) return world.wakeupRuns.get(runs[2]) ?? [];
    const perIssue = /^\/api\/issues\/([^/]+)\/(wakeups|task-runs|comments)$/.exec(url.pathname);
    if (perIssue) {
      const [, issueId, kind] = perIssue;
      if (kind === "wakeups" && method === "GET") return world.issueWakeups.get(issueId) ?? [];
      if (kind === "wakeups" && method === "POST") {
        world.posts.push(body);
        const rule = {
          id: `w-${world.nextId++}`, issue_id: issueId, agent_id: body.agent_id,
          instruction: body.instruction, kind: body.kind, mode: body.mode, condition: body.condition,
          enabled: true, fire_count: 0, disabled_at: null, expires_at: null, paused_reason: null,
          last_error: null, max_fires: null, created_at: new Date(BASE + world.nextId * 1000).toISOString(),
        };
        world.issueWakeups.set(issueId, [...(world.issueWakeups.get(issueId) ?? []), rule]);
        world.rules.push(rule);
        world.postHook?.(rule);
        return rule;
      }
      if (kind === "task-runs") return world.taskRuns.get(issueId) ?? [];
      if (kind === "comments") {
        const since = url.searchParams.get("since");
        const rows = world.comments.get(issueId) ?? [];
        return since ? rows.filter((c) => Date.parse(c.created_at) > Date.parse(since)) : rows;
      }
    }
    throw new Error(`Unexpected request ${method} ${url.pathname}`);
  };
}

function fire(world, rule, { status = "running", completedAt = null, commented = false } = {}) {
  rule.enabled = status === "running";
  rule.fire_count = 1;
  const created = rule.created_at;
  const run = {
    id: `t-${world.nextId++}`, status, created_at: created,
    started_at: created, completed_at: completedAt, commented, triggers: [],
  };
  world.wakeupRuns.set(rule.id, [run]);
  world.snapshot = status === "running" ? [{ id: run.id, issue_id: rule.issue_id, status, created_at: created }] : [];
  world.taskRuns.set(rule.issue_id, [...(world.taskRuns.get(rule.issue_id) ?? []), { id: run.id, issue_id: rule.issue_id, status, created_at: created, agent_id: PM.id }]);
  return run;
}

function harness(world) {
  const dir = mkdtempSync(join(tmpdir(), "watchdog-state-"));
  const store = new StateStore(join(dir, "state"));
  const state = store.load(WS);
  let clock = BASE;
  return {
    dir, store, state,
    cleanup() { rmSync(dir, { recursive: true, force: true }); },
    async scan(options = {}) {
      clock += 60_000;
      return scanOnce({
        api: fakeApi(world), live: { agents: [PM], squads: [] }, store, state, pmAgent: PM,
        now: clock, ...options,
      });
    },
    bumpClock(ms) { clock += ms; return clock; },
  };
}

test("a confirmed gap dispatches once and the guard's own rule is not a new gap", async (t) => {
  const world = createWorld();
  const h = harness(world);
  t.after(h.cleanup);

  const first = await h.scan();
  assert.equal(first.status, "scanned");
  assert.equal(world.posts.length, 1);
  const action = first.decisions.find((d) => d.issue === "DEV-59");
  assert.equal(action.action, "dispatch");
  const record = h.state.issues[world.issues[0].id].records.at(-1);
  assert.equal(record.phase, "dispatched");
  assert.equal(record.wakeup_id, world.issueWakeups.get(world.issues[0].id)[0].id);

  const instruction = world.posts[0].instruction;
  assert.match(instruction, /来源：agent-team-watchdog/);
  assert.match(instruction, /缺口键：10000000-0000-0000-0000-000000000001#1/);
  assert.equal(world.posts[0].agent_id, PM.id);
  assert.deepEqual(world.posts[0].condition, { type: "issue_field", field: "status", value: "in_progress" });

  // The manager comments, which bumps last_activity_at; the rule is still live.
  world.issues[0].last_activity_at = "2026-11-19T00:20:00Z";
  await h.scan();
  assert.equal(world.posts.length, 1);
  assert.equal(h.state.issues[world.issues[0].id].records.at(-1).phase, "dispatched");

  // Replay under the same rules changes nothing.
  await h.scan();
  assert.equal(world.posts.length, 1);
});

test("own run, own comment and a silent terminal end produce one anomaly, not a loop", async (t) => {
  const world = createWorld();
  const h = harness(world);
  t.after(h.cleanup);
  await h.scan();
  const rule = world.issueWakeups.get(world.issues[0].id)[0];

  // The condition rule fires; the manager run is active.
  const run = fire(world, rule, { status: "running" });
  await h.scan();
  assert.equal(world.posts.length, 1);
  assert.equal(h.state.issues[world.issues[0].id].records.at(-1).phase, "dispatched");

  // The manager comments (activity changes) and then ends without a handoff.
  world.issues[0].last_activity_at = "2026-11-19T00:11:30Z";
  const endedRun = fire(world, rule, { status: "completed", completedAt: "2026-11-19T00:12:30Z", commented: true });
  // A historical ordinary project-manager run must not count as guard evidence.
  world.taskRuns.get(world.issues[0].id).unshift({
    id: "t-historical", issue_id: world.issues[0].id, status: "completed",
    created_at: "2026-11-19T00:01:00Z", agent_id: PM.id,
  });
  const ended = await h.scan();
  assert.equal(world.posts.length, 1);
  assert.equal(ended.decisions[0].action, "anomaly");
  const record = h.state.issues[world.issues[0].id].records.at(-1);
  assert.equal(record.phase, "anomaly");
  assert.match(record.anomaly.reason, /运行已结束/);
  assert.match(formatStatus(h.state), /需要检查的异常（1）/);
  assert.match(formatStatus(h.state), /DEV-59/);

  // Replay does not dispatch or duplicate the anomaly.
  await h.scan();
  assert.equal(world.posts.length, 1);

  // Restart with no local state adopts the consumed rule and its run from the
  // platform: still exactly one dispatch, still an observable anomaly.
  const restarted = harness(world);
  t.after(restarted.cleanup);
  const adopted = await restarted.scan();
  assert.equal(world.posts.length, 1);
  assert.equal(adopted.decisions[0].action, "adopted");
  const adoptedRecord = restarted.state.issues[world.issues[0].id].records.at(-1);
  assert.equal(adoptedRecord.phase, "anomaly");
  assert.equal(endedRun.status, "completed");
});

test("outside progress reopens exactly one new cycle", async (t) => {
  const world = createWorld();
  const h = harness(world);
  t.after(h.cleanup);
  await h.scan();
  const rule = world.issueWakeups.get(world.issues[0].id)[0];
  fire(world, rule, { status: "completed", completedAt: "2026-11-19T00:20:00Z", commented: true });
  await h.scan(); // anomaly
  assert.equal(world.posts.length, 1);

  // A human replies after the anomaly was recorded: real outside progress.
  const closedAt = h.state.issues[world.issues[0].id].records.at(-1).closed_at;
  const replyAt = new Date(Date.parse(closedAt) + 60_000).toISOString();
  world.comments.set(world.issues[0].id, [{ id: "c-1", author_type: "member", created_at: replyAt, deleted_at: null }]);
  world.issues[0].last_activity_at = replyAt;
  const reopened = await h.scan();
  assert.equal(world.posts.length, 2);
  assert.ok(reopened.decisions.some((d) => d.action === "resolved" && /member_comment/.test(d.reason)));
  const cycle = h.state.issues[world.issues[0].id].records.at(-1).cycle_key;
  assert.equal(cycle, cycleKeyFor(world.issues[0].id, 2));

  await h.scan();
  assert.equal(world.posts.length, 2);
});

test("active runs, waits, review, parked, human-owned and unverified issues never dispatch", async (t) => {
  const cases = [
    ["active run", () => { const i = gapIssue(); const w = createWorld({ issues: [i] }); w.snapshot = [{ id: "t-x", issue_id: i.id, status: "running" }]; return w; }],
    ["child wait", () => { const i = gapIssue(); const child = gapIssue("10000000-0000-0000-0000-0000000000c1", "DEV-60", { parent_issue_id: i.id, status: "todo", status_category: "todo" }); return createWorld({ issues: [i, child] }); }],
    ["external continuation", () => createWorld({ rules: [{ id: "w-ext", issue_id: "10000000-0000-0000-0000-000000000001", agent_id: PM.id, enabled: true, fire_count: 0, kind: "event", mode: "once", condition: { type: "issue_field", field: "status", value: "in_progress" }, created_at: "2026-11-19T00:06:00Z" }] })],
    ["blocked", () => createWorld({ issues: [gapIssue(undefined, "DEV-59", { status: "blocked", status_category: "blocked" })] })],
    ["backlog", () => createWorld({ issues: [gapIssue(undefined, "DEV-59", { status: "backlog", status_category: "backlog" })] })],
    ["in review", () => createWorld({ issues: [gapIssue(undefined, "DEV-59", { status: "in_review", status_category: "in_review" })] })],
    ["human owned", () => createWorld({ issues: [gapIssue(undefined, "DEV-59", { assignee_type: "member", assignee_id: "99999999-0000-0000-0000-000000000001" })] })],
  ];
  for (const [name, build] of cases) {
    const world = build();
    const h = harness(world);
    t.after(h.cleanup);
    const result = await h.scan();
    assert.equal(world.posts.length, 0, `${name} must not dispatch`);
    assert.ok(!result.decisions.some((d) => d.action === "dispatch"), `${name} must not report dispatch`);
  }

  // A truncated workspace rule read is "unverified", never a gap.
  const filler = Array.from({ length: 520 }, (_, index) => ({
    id: `f-${index}`, issue_id: `20000000-0000-0000-0000-${String(index).padStart(12, "0")}`,
    agent_id: PM.id, enabled: true, fire_count: 0, kind: "event", mode: "continuous",
    event_types: ["comment.created"], created_at: "2026-11-19T00:00:00Z",
  }));
  const world = createWorld({ rules: filler });
  const h = harness(world);
  t.after(h.cleanup);
  const result = await h.scan();
  assert.equal(world.posts.length, 0);
  assert.equal(result.status, "scanned");
  assert.equal(h.state.scans.truncated, true);
});

test("--issue limits the watchdog to one issue", async (t) => {
  const first = gapIssue();
  const second = gapIssue("10000000-0000-0000-0000-0000000000b2", "DEV-61");
  const world = createWorld({ issues: [first, second] });
  const h = harness(world);
  t.after(h.cleanup);
  await h.scan({ issueFilter: first.id });
  assert.equal(world.posts.length, 1);
  assert.match(world.posts[0].instruction, /DEV-59/);
  await h.scan({ issueFilter: second.identifier });
  assert.equal(world.posts.length, 2);
  assert.match(world.posts[1].instruction, /DEV-61/);
});

test("a failed diagnosis skips the pass and recovers on the next scan", async (t) => {
  const world = createWorld();
  const h = harness(world);
  t.after(h.cleanup);
  world.fail["GET /api/issues"] = 503;
  const skipped = await h.scan();
  assert.equal(skipped.status, "skipped");
  assert.equal(world.posts.length, 0);
  assert.equal(Object.keys(h.state.issues).length, 0);
  delete world.fail["GET /api/issues"];
  const recovered = await h.scan();
  assert.equal(recovered.status, "scanned");
  assert.equal(world.posts.length, 1);
});

test("registration failures retry only up to the cap, then become an anomaly", async (t) => {
  const world = createWorld();
  const h = harness(world);
  t.after(h.cleanup);
  world.fail["POST /api/issues/10000000-0000-0000-0000-000000000001/wakeups"] = 500;
  for (let attempt = 0; attempt < 4; attempt++) await h.scan();
  assert.equal(world.posts.length, 3);
  const record = h.state.issues[world.issues[0].id].records.at(-1);
  assert.equal(record.phase, "anomaly");
  assert.match(record.anomaly.reason, /已尝试派发 3 次/);
  await h.scan();
  assert.equal(world.posts.length, 3);
});

test("a rule consumed before readback is still a successful dispatch", async (t) => {
  const world = createWorld();
  const h = harness(world);
  t.after(h.cleanup);
  world.postHook = (rule) => {
    rule.enabled = false;
    rule.fire_count = 1;
    world.wakeupRuns.set(rule.id, [{ id: "t-fast", status: "running", created_at: rule.created_at, started_at: rule.created_at, completed_at: null, commented: false, triggers: [] }]);
  };
  const result = await h.scan();
  assert.equal(world.posts.length, 1);
  assert.equal(result.decisions[0].action, "dispatch");
  assert.equal(h.state.issues[world.issues[0].id].records.at(-1).phase, "dispatched");
});

test("the writer lock is workspace-keyed, independent of state-dir, and never steals a corrupt lock", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "watchdog-lock-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const lockDir = join(dir, "locks");
  const first = acquireLock(lockDir, { workspaceId: WS, profile: "alpha", pid: 4242, isAlive: () => true });
  assert.equal(first.path, lockPath(lockDir, WS));
  assert.equal(JSON.parse(readFileSync(first.path, "utf8")).token, first.token);
  assert.throws(() => acquireLock(lockDir, { workspaceId: WS, profile: "beta", pid: 4343, isAlive: () => true }), /already writing workspace/);
  // A different state directory must not change the lock path.
  assert.equal(lockPath(lockDir, WS), first.path);
  assert.ok(!defaultLockDir({ XDG_STATE_HOME: "/var/state" }).includes("state-dir"));
  first.release();
  assert.equal(existsSync(first.path), false);

  // Two contenders may race after release, but a valid dead holder is stale.
  const staleDir = join(dir, "stale");
  const stalePath = lockPath(staleDir, WS);
  acquireLock(staleDir, { workspaceId: WS, pid: 5252, isAlive: () => true });
  const replaced = acquireLock(staleDir, { workspaceId: WS, pid: 5353, isAlive: () => false });
  assert.equal(JSON.parse(readFileSync(stalePath, "utf8")).pid, 5353);
  replaced.release();

  // An empty or half-written lock is held, never treated as ownerless.
  for (const content of ["", "{\"pid\": "]) {
    const brokenDir = join(dir, `broken-${content.length}`);
    acquireLock(brokenDir, { workspaceId: WS, pid: 6262, isAlive: () => true });
    writeFileSync(lockPath(brokenDir, WS), content);
    assert.throws(() => acquireLock(brokenDir, { workspaceId: WS, pid: 6363, isAlive: () => false }), /corrupt or incomplete/);
    assert.equal(readFileSync(lockPath(brokenDir, WS), "utf8"), content);
  }

  // A release from another token does not remove the current holder.
  const owned = acquireLock(join(dir, "token"), { workspaceId: WS, pid: 7272, isAlive: () => true });
  const foreign = { release: () => { const held = JSON.parse(readFileSync(owned.path, "utf8")); if (held.token === "other") rmSync(owned.path); } };
  foreign.release();
  assert.equal(existsSync(owned.path), true);
  owned.release();
});

test("two processes with different state dirs cannot both write the workspace", async (t) => {
  const dir = mkdtempSync(join(tmpdir(), "watchdog-e2e-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const profiles = join(dir, "profiles");
  mkdirSync(join(profiles, "p"), { recursive: true });
  const issue = gapIssue();
  let posts = 0;
  let rule = null;
  const server = createServer((request, response) => {
    const url = new URL(request.url, "http://127.0.0.1");
    const send = (status, body, delay = 0) => setTimeout(() => {
      response.writeHead(status, { "Content-Type": "application/json" });
      response.end(JSON.stringify(body));
    }, delay);
    if (url.pathname === "/api/agents") return send(200, [PM], 400); // hold the first writer inside the lock
    if (url.pathname === "/api/squads") return send(200, []);
    if (url.pathname === "/api/issues" && url.searchParams.get("open_only") === "true") return send(200, { issues: [issue] });
    if (url.pathname === "/api/agent-task-snapshot") return send(200, []);
    if (url.pathname === "/api/issue-wakeups") return send(200, { items: rule ? [rule] : [], total: rule ? 1 : 0 });
    if (url.pathname === `/api/issues/${issue.id}/wakeups` && request.method === "POST") {
      posts += 1;
      rule = {
        id: "w-1", issue_id: issue.id, agent_id: PM.id, instruction: "", kind: "event", mode: "once",
        condition: { type: "issue_field", field: "status", value: "in_progress" },
        enabled: true, fire_count: 0, created_at: new Date().toISOString(),
      };
      let body = "";
      request.on("data", (chunk) => { body += chunk; });
      request.on("end", () => { rule = { ...rule, ...JSON.parse(body), id: "w-1" }; send(201, rule); });
      return;
    }
    if (url.pathname === `/api/issues/${issue.id}/wakeups`) return send(200, rule ? [rule] : []);
    if (url.pathname === `/api/issues/${issue.id}/wakeups/w-1/runs`) return send(200, []);
    if (url.pathname === `/api/issues/${issue.id}/task-runs`) return send(200, []);
    if (url.pathname === `/api/issues/${issue.id}/comments`) return send(200, []);
    send(404, { error: "not found" });
  });
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  const { port } = server.address();
  writeFileSync(join(profiles, "p/config.json"), JSON.stringify({ server_url: `http://127.0.0.1:${port}`, token: "t", workspace_id: WS }));
  const run = (stateDir) => new Promise((done) => {
    const child = spawn(process.execPath, [SCRIPT, "scan", "--profile", "p", "--profiles-dir", profiles, "--state-dir", stateDir], {
      env: { ...process.env, XDG_STATE_HOME: join(dir, "xdg") },
    });
    let stderr = "";
    let stdout = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("close", (code) => done({ code, stdout, stderr }));
  });
  try {
    const [a, b] = await Promise.all([run(join(dir, "state-a")), run(join(dir, "state-b"))]);
    const codes = [a.code, b.code].sort();
    assert.deepEqual(codes, [0, 1]);
    const loser = [a, b].find((r) => r.code === 1);
    assert.match(loser.stderr, /already writing workspace/);
    assert.equal(posts, 1);
  } finally {
    server.close();
  }
});

test("debounced scans survive a burst and a fatal socket ends the run", async (t) => {
  const world = createWorld({ issues: [] });
  const h = harness(world);
  t.after(h.cleanup);
  const sockets = [];
  const connect = (_url, handlers) => {
    sockets.push(handlers);
    return { close() { handlers.closed = true; } };
  };
  const run = await runWatchdog({
    api: fakeApi(world), config: { server_url: "http://127.0.0.1:1", token: "t", workspace_id: WS },
    store: h.store, state: h.state, live: { agents: [PM], squads: [] }, pmAgent: PM,
    connect, debounceMs: 15, reconnectMs: 5, rescanMs: 0, log: () => {},
  });
  sockets[0].onReady();
  for (let index = 0; index < 60; index++) sockets[0].onMessage({ type: "issue:updated" });
  await new Promise((done) => setTimeout(done, 80));
  const scansAfterBurst = h.state.scans.count;
  assert.ok(scansAfterBurst >= 2, `burst must still scan (${scansAfterBurst})`);
  assert.ok(scansAfterBurst <= 5, `burst must coalesce (${scansAfterBurst})`);

  sockets[0].onFatal(new Error("auth rejected"));
  const why = await run.done;
  assert.match(String(why), /^fatal:/);
  await new Promise((done) => setTimeout(done, 20));
  assert.equal(sockets.length, 1, "a fatal socket must not reconnect");
});

test("a reconnect rescans and a corrupt lock refuses before any API call", async (t) => {
  const world = createWorld({ issues: [] });
  const h = harness(world);
  t.after(h.cleanup);
  const handlers = [];
  const connect = (_url, captured) => { handlers.push(captured); return { close() {} }; };
  const run = await runWatchdog({
    api: fakeApi(world), config: { server_url: "http://127.0.0.1:1", token: "t", workspace_id: WS },
    store: h.store, state: h.state, live: { agents: [PM], squads: [] }, pmAgent: PM,
    connect, debounceMs: 5, reconnectMs: 5, rescanMs: 0, log: () => {},
  });
  const before = h.state.scans.count;
  assert.equal(handlers.length, 1);
  handlers[0].onClose();
  await new Promise((done) => setTimeout(done, 30));
  assert.equal(handlers.length, 2, "a dropped socket schedules a reconnect");
  handlers[1].onReady();
  await new Promise((done) => setTimeout(done, 20));
  assert.ok(h.state.scans.count > before, "the reconnect scans before trusting new events");
  run.stop("test");
  await run.done;

  // A corrupt lock stops a writer before it reads or fetches anything.
  const dir = mkdtempSync(join(tmpdir(), "watchdog-corrupt-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const lockDir = defaultLockDir({ XDG_STATE_HOME: dir });
  mkdirSync(lockDir, { recursive: true });
  writeFileSync(lockPath(lockDir, WS), "");
  assert.throws(() => acquireLock(lockDir, { workspaceId: WS, isAlive: () => false }), /corrupt or incomplete/);
  assert.ok(existsSync(lockPath(lockDir, WS)));
});
test("the dispatch instruction carries the real handoff and human-input callback", () => {
  const instruction = renderInstruction(gapIssue(), "10000000-0000-0000-0000-000000000001#3");
  assert.match(instruction, /来源：agent-team-watchdog/);
  assert.match(instruction, /缺口键：10000000-0000-0000-0000-000000000001#3/);
  assert.match(instruction, /真实交接/);
  assert.match(instruction, /--until-issue/);
  assert.match(instruction, /assignee/);
  assert.match(instruction, /no_action/);
  assert.match(instruction, /同一缺口键/);
  assert.match(instruction, /--task-id/);
  // Every command names the original issue, not a placeholder, so the manager
  // cannot register the wait on the wrong issue.
  assert.match(instruction, /issue wakeup create DEV-59 /);
  assert.match(instruction, /issue wakeup list DEV-59 /);
  assert.doesNotMatch(instruction, /<本事项>/);
  // No write-in deadline: a late completion or reply must still resume the issue.
  assert.doesNotMatch(instruction, /72h/);
  assert.match(instruction, /默认不设到期/);
  assert.match(instruction, /晚几天/);
  // An expiry is allowed only as a conditional, and must wake the manager.
  assert.match(instruction, /--expires-in <期限> --on-timeout wake/);
  // Reuse an unresolved human request even when the guard cycle key changed.
  assert.match(instruction, /issue list --assignee <接收人>/);
  assert.match(instruction, /复用/);
  assert.match(instruction, /不再新建第二条/);
  // The marker round-trips through the rule instruction parser.
  assert.equal(ruleCycleKey(instruction, "10000000-0000-0000-0000-000000000001"), "10000000-0000-0000-0000-000000000001#3");

  const other = renderInstruction(gapIssue("10000000-0000-0000-0000-000000000009", "DEV-77"), "10000000-0000-0000-0000-000000000009#1");
  assert.match(other, /issue wakeup create DEV-77 /);
  assert.doesNotMatch(other, /DEV-59/);
});

test("the planner skill documents unwritten deadlines and todo reuse", () => {
  const skill = readFileSync(join(ROOT, "agent-team/skills/rd-project-management/SKILL.md"), "utf8");
  assert.doesNotMatch(skill, /72h/);
  assert.match(skill, /without an expiry by default/);
  assert.match(skill, /--expires-in <deadline> --on-timeout wake/);
  assert.match(skill, /late completion or a late member reply still wakes/);
  assert.match(skill, /issue list --assignee <person>/);
  assert.match(skill, /even when the guard's gap key changed/);
  assert.match(skill, /never create a second equivalent todo/);
});

test("arguments, agent resolution and gap classification are strict", () => {
  const options = parseWatchdogArgs(["scan", "--issue", "DEV-83", "--dry-run", "--max-dispatches", "2"]);
  assert.equal(options.command, "scan");
  assert.equal(options.issue, "DEV-83");
  assert.equal(options.dryRun, true);
  assert.equal(options.maxDispatches, 2);
  assert.throws(() => parseWatchdogArgs(["scan", "--issue", "not an id"]), /--issue/);
  assert.throws(() => parseWatchdogArgs(["run", "--dry-run"]), /--dry-run/);
  assert.throws(() => parseWatchdogArgs(["status", "--issue", "DEV-83"]), /local state/);

  assert.equal(findProjectManager([PM], "项目经理").id, PM.id);
  assert.equal(findProjectManager([PM], PM.id).id, PM.id);
  assert.throws(() => findProjectManager([PM], "nobody"), /No active agent/);
  assert.throws(() => findProjectManager([PM, { ...PM, id: "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee" }], "项目经理"), /More than one/);

  assert.equal(isGap({ category: "no_continuation", status: "in_progress", assignee_type: "agent" }), true);
  assert.equal(isGap({ category: "no_continuation", status: "in_progress", assignee_type: "member" }), false);
  assert.equal(isGap({ category: "active_run", status: "in_progress", assignee_type: "agent" }), false);
  assert.equal(matchesIssueFilter({ id: "x", identifier: "DEV-1" }, "DEV-1"), true);
  assert.equal(matchesIssueFilter({ id: "x", identifier: "DEV-1" }, "DEV-2"), false);
  assert.equal(defaultStateDir(WS, { XDG_STATE_HOME: "/var/state" }), join("/var/state", "multica-agent-team-watchdog", WS));
});

test("the entry point runs when invoked through a symlinked workbench path", async (t) => {
  const dir = mkdtempSync(join(tmpdir(), "watchdog-link-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const link = join(dir, "agent-team-watchdog.mjs");
  symlinkSync(SCRIPT, link);
  const result = await new Promise((done) => {
    const child = spawn(process.execPath, [link, "scan", "--help"], { env: process.env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("close", (code) => done({ code, stdout, stderr }));
  });
  assert.equal(result.code, 0);
  assert.match(result.stdout, /Usage: node scripts\/agent-team-watchdog\.mjs/);
});
