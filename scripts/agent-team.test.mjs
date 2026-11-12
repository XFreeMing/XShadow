import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { apiClient, initializeTeam, loadTemplate, main, parseArgs, selectRuntimes } from "./agent-team.mjs";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const template = loadTemplate(join(ROOT, "agent-team/team.json"));

function fixture() {
  let next = 1;
  const state = {
    agents: [], skills: [], squads: [], members: {}, bindings: {}, calls: [],
    runtimes: Object.keys(template.runtime_limits).map((provider) => ({ id: `target-${provider}`, provider, name: provider, status: "online", daemon_id: "test-machine", max_concurrent_tasks: null })),
  };
  const modelLists = Object.fromEntries(state.runtimes.map((r) => [r.id, [...new Set(template.agents.filter((a) => a.provider === r.provider).map((a) => a.model))].map((id) => ({ id, thinking: { supported_levels: [{ value: "high" }] }, supports_explicit_standard_service_tier: true }))]));
  const mutations = () => state.calls.filter(([method, path]) => method !== "GET" && !path.endsWith("/models"));
  const api = async (method, input, body) => {
    state.calls.push([method, input, structuredClone(body)]);
    const path = input.split("?")[0];
    const parts = path.split("/").filter(Boolean);
    if (path === "/api/me") return { id: "owner" };
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
        const item = { id: `destination-${next++}`, owner_id: "owner", ...structuredClone(body) };
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

test("portable template contains the current roster without machine identities or credentials", () => {
  assert.equal(template.agents.length, 18);
  assert.equal(template.skills.length, 13);
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
  assert.equal(f.state.agents.length, 18);
  assert.equal(f.state.skills.length, 13);
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
  assert.equal(f.state.agents.length, 18);
  assert.equal(f.state.skills.length, 13);
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
  assert.equal(f.state.agents.length, 18);
  assert.throws(() => apiClient({ ...config, token: "" }), /Profile needs/);
});

test("Make dispatches init exactly once including parallel make and never combines lifecycle goals", () => {
  const out = execFileSync("make", ["-n", "-j", "agent", "init", "ARGS=--help"], { cwd: ROOT, encoding: "utf8" });
  assert.equal(out.split("\n").filter((line) => line.includes("node scripts/agent-team.mjs")).length, 1);
  assert.throws(() => execFileSync("make", ["-n", "agent", "init", "down"], { cwd: ROOT, stdio: "pipe" }));
  assert.throws(() => parseArgs(["init", "--profile", "../wrong"]), /Invalid profile/);
  assert.throws(() => parseArgs(["init", "--runtime", "codex=one", "--runtime", "codex=two"]), /repeated runtime/);
});
