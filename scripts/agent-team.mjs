#!/usr/bin/env node
import { readFileSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { spawnSync } from "node:child_process";
import { isDeepStrictEqual } from "node:util";
import { buildContext, formatContext } from "./agent-team-context.mjs";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const HELP = `Usage: make agent <init|context> [ARGS="options"]

init without --profile: start this checkout's managed API and daemon, then initialize its workspace.
context is read-only: show template, live roster and drift without starting services or models.
  --profile NAME           Use an already authenticated CLI profile; do not start services
  --profiles-dir PATH      Profile root (default ~/.multica/profiles)
  --runtime PROVIDER=ID    Explicit target runtime; repeat for claude, codex, grok
  --template PATH          Template JSON (default agent-team/team.json)
  --check                  Preflight only; requires an existing environment/profile
  --offline                context: template only, no profile or network access
  --tasks                  context: also read unfinished issues, active runs and wakeups (read-only)
  --json                   context: machine-readable output
  --help                   Show this help

init synchronizes template-owned fields of same-named skills, agents and squads.
Keeps unrelated objects, extra agent skill bindings and extra squad members.
Does not import credentials, create projects or start model tasks.`;

export function parseArgs(args) {
  const options = { command: args.shift(), runtimeIds: {}, check: false };
  if (!["init", "context"].includes(options.command)) throw new Error(HELP);
  while (args.length) {
    const flag = args.shift();
    if (flag === "--help") { options.help = true; continue; }
    if (flag === "--check") { options.check = true; continue; }
    if (flag === "--offline") { options.offline = true; continue; }
    if (flag === "--json") { options.json = true; continue; }
    if (flag === "--tasks") { options.tasks = true; continue; }
    const key = { "--profile": "profile", "--profiles-dir": "profilesDir", "--template": "template", "--runtime": "runtime" }[flag];
    if (!key || !args[0] || args[0].startsWith("--")) throw new Error(`Invalid option: ${flag}\n${HELP}`);
    const value = args.shift();
    if (key === "runtime") {
      const match = /^(claude|codex|grok)=([^/\s]+)$/.exec(value);
      if (!match || options.runtimeIds[match[1]]) throw new Error(`Invalid or repeated runtime mapping: ${value}`);
      options.runtimeIds[match[1]] = match[2];
    } else options[key] = value;
  }
  if (options.profile && !/^[\w.-]+$/.test(options.profile)) throw new Error("Invalid profile name");
  if (options.command === "init" && (options.offline || options.json || options.tasks)) throw new Error("--offline, --json and --tasks apply only to context");
  if (options.command === "context" && options.tasks && options.offline) throw new Error("--tasks needs a live profile; remove --offline");
  if (options.command === "context" && (options.check || Object.keys(options.runtimeIds).length)) throw new Error("context reads existing bindings; --check and --runtime apply only to init");
  return options;
}

function localPath(root, path) {
  if (typeof path !== "string" || isAbsolute(path)) throw new Error(`Expected relative template path: ${path}`);
  const full = resolve(root, path);
  if (relative(root, full).split(/[\\/]/).includes("..")) throw new Error(`Path escapes template: ${path}`);
  return full;
}

function skillFiles(dir, prefix = "") {
  return readdirSync(join(dir, prefix), { withFileTypes: true }).flatMap((entry) => {
    const path = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) return skillFiles(dir, path);
    if (!entry.isFile()) throw new Error(`Skill bundles require regular files: ${path}`);
    if (path === "SKILL.md") return [];
    return [{ path, content: readFileSync(join(dir, path), "utf8") }];
  }).sort((a, b) => a.path.localeCompare(b.path));
}

export function loadTemplate(path) {
  const root = dirname(path);
  const template = JSON.parse(readFileSync(path, "utf8"));
  if (template.version !== 1) throw new Error("Unsupported team template version");
  for (const kind of ["agents", "skills", "squads"]) {
    const keys = new Set(), names = new Set();
    if (!Array.isArray(template[kind]) || !template[kind].length) throw new Error(`Missing ${kind}`);
    for (const item of template[kind]) {
      if (!/^[a-z][a-z0-9-]*$/.test(item.key) || !item.name || keys.has(item.key) || names.has(item.name)) throw new Error(`Duplicate/invalid ${kind} key or name`);
      keys.add(item.key); names.add(item.name);
    }
  }
  const agentKeys = new Set(template.agents.map((a) => a.key));
  const skillKeys = new Set(template.skills.map((s) => s.key));
  for (const agent of template.agents) {
    if (!(agent.provider in template.runtime_limits) || !agent.model || !agent.thinking_level || agent.permission_mode !== "private" || !Array.isArray(agent.skills) || agent.skills.some((s) => !skillKeys.has(s))) throw new Error(`Invalid agent configuration: ${agent.name}`);
  }
  for (const squad of template.squads) {
    if (!agentKeys.has(squad.leader) || squad.members.some((m) => !agentKeys.has(m.agent))) throw new Error(`Unknown agent in squad ${squad.name}`);
  }
  const squadIds = Object.fromEntries(template.squads.map((s) => [s.key, s.key]));
  for (const squad of template.squads) renderReferences(squad.instructions, squadIds);
  return {
    ...template,
    common: readFileSync(localPath(root, template.common_instructions), "utf8").trimEnd(),
    skills: template.skills.map((s) => {
      const dir = localPath(root, s.directory);
      return { ...s, content: readFileSync(join(dir, "SKILL.md"), "utf8"), files: skillFiles(dir) };
    }),
  };
}

export function renderReferences(text, squadIds) {
  return text.replace(/\{\{squad:([^}]+)\}\}/g, (_, key) => {
    if (!squadIds[key]) throw new Error(`Unknown squad reference: ${key}`);
    return squadIds[key];
  });
}

export function apiClient(config) {
  const base = new URL(config.server_url);
  if (!["http:", "https:"].includes(base.protocol) || !config.token || !config.workspace_id) throw new Error("Profile needs server_url, token and workspace_id; sign in and select a workspace first.");
  return async (method, path, body) => {
    const response = await fetch(`${base.href.replace(/\/$/, "")}${path}`, {
      method, redirect: "error", signal: AbortSignal.timeout(30000),
      headers: { Authorization: `Bearer ${config.token}`, "X-Workspace-ID": config.workspace_id, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) throw new Error(`${method} ${path}: HTTP ${response.status}. Check account access and server compatibility.`);
    const text = await response.text();
    return text ? JSON.parse(text) : null;
  };
}

export function selectRuntimes(runtimes, providers, explicit = {}, daemonId) {
  return Object.fromEntries(providers.map((provider) => {
    const candidates = runtimes.filter((r) => r.provider === provider && r.status === "online" && (
      explicit[provider] ? r.id === explicit[provider] : !daemonId || r.daemon_id === daemonId
    ));
    if (candidates.length !== 1) throw new Error(`${provider}: expected one online target runtime, found ${candidates.length}. Install/login the CLI and start its daemon, or pass --runtime ${provider}=ID.`);
    return [provider, candidates[0]];
  }));
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function catalog(api, runtime, wait, timeoutMs) {
  let result = await api("POST", `/api/runtimes/${runtime.id}/models`, {});
  const deadline = Date.now() + timeoutMs;
  while (["pending", "running"].includes(result.status) && Date.now() < deadline) {
    await wait(1000);
    result = await api("GET", `/api/runtimes/${runtime.id}/models/${result.id}`);
  }
  if (result.status !== "completed" || !result.supported || !Array.isArray(result.models)) throw new Error(`${runtime.provider}: model discovery failed or timed out. Check CLI authentication and retry.`);
  return result.models;
}

function byName(rows, name) {
  const matches = rows.filter((row) => row.name === name);
  if (matches.length > 1) throw new Error(`Ambiguous existing name: ${name}`);
  return matches[0];
}

function differs(current, desired) {
  return Object.entries(desired).some(([key, value]) => !isDeepStrictEqual(current[key] ?? (typeof value === "string" ? "" : null), value));
}

function verifyFields(actual, expected, label) {
  if (differs(actual, expected)) throw new Error(`Readback mismatch: ${label}. Initialization is incomplete; inspect and rerun.`);
}

export async function initializeTeam(api, template, { runtimeIds = {}, daemonId, check = false, wait = sleep, timeoutMs = 90000, log = console.log } = {}) {
  const [runtimeList, agentList, skillList, squadList, me] = await Promise.all([
    api("GET", "/api/runtimes"), api("GET", "/api/agents"), api("GET", "/api/skills"), api("GET", "/api/squads"), api("GET", "/api/me"),
  ]);
  const runtimes = selectRuntimes(runtimeList, Object.keys(template.runtime_limits), runtimeIds, daemonId);
  for (const a of template.agents) {
    const existing = byName(agentList, a.name);
    if (existing && (existing.owner_id !== me.id || existing.status === "archived")) throw new Error(`Agent ${a.name} is archived or belongs to another account; resolve the name before initialization.`);
  }
  for (const s of template.skills) byName(skillList, s.name);
  for (const s of template.squads) byName(squadList, s.name);
  const catalogs = Object.fromEntries(await Promise.all(Object.entries(runtimes).map(async ([p, r]) => [p, await catalog(api, r, wait, timeoutMs)])));
  for (const a of template.agents) {
    const model = catalogs[a.provider].find((m) => m.id === a.model);
    if (!model?.thinking?.supported_levels?.some((level) => level.value === a.thinking_level)) throw new Error(`${a.name}: ${a.provider} does not advertise ${a.model} / ${a.thinking_level}. Configure that model or deliberately edit the template; no automatic substitution was made.`);
    if (a.service_tier === "default" && !model.supports_explicit_standard_service_tier) throw new Error(`${a.name}: this runtime cannot select Standard service tier; update its CLI or edit the template.`);
  }
  log(`Preflight passed: ${template.agents.length} agents, ${template.skills.length} skills, ${template.squads.length} squads.`);
  for (const [provider, runtime] of Object.entries(runtimes)) log(`  ${provider}: ${runtime.name} (${runtime.id})`);
  if (check) return { checked: true };

  const skills = {}, agents = {}, squads = {};
  for (const [provider, runtime] of Object.entries(runtimes)) {
    const desired = { max_concurrent_tasks: template.runtime_limits[provider] };
    if (differs(runtime, desired)) await api("PATCH", `/api/runtimes/${runtime.id}`, desired);
  }
  for (const skill of template.skills) {
    const body = { name: skill.name, description: skill.description, content: skill.content, files: skill.files };
    let current = byName(skillList, skill.name);
    if (current) {
      current = await api("GET", `/api/skills/${current.id}?include=content`);
      const files = (current.files ?? []).map(({ path, content }) => ({ path, content })).sort((a, b) => a.path.localeCompare(b.path));
      if (differs({ ...current, files }, body)) await api("PUT", `/api/skills/${current.id}`, body);
    } else current = await api("POST", "/api/skills", body);
    skills[skill.key] = current.id;
  }
  for (const agent of template.agents) {
    const body = Object.fromEntries(["name", "description", "model", "thinking_level", "service_tier", "max_concurrent_tasks", "conversation_starters", "permission_mode"].map((key) => [key, agent[key]]));
    body.instructions = `${template.common}\n${agent.instructions}`;
    body.runtime_id = runtimes[agent.provider].id;
    let current = byName(agentList, agent.name);
    if (current) {
      current = await api("GET", `/api/agents/${current.id}`);
      if (differs(current, body)) await api("PUT", `/api/agents/${current.id}`, body);
    } else current = await api("POST", "/api/agents", { ...body, skill_ids: agent.skills.map((s) => skills[s]) });
    agents[agent.key] = current.id;
    const assigned = await api("GET", `/api/agents/${current.id}/skills`);
    const missing = agent.skills.map((s) => skills[s]).filter((id) => !assigned.some((s) => s.id === id));
    if (missing.length) await api("POST", `/api/agents/${current.id}/skills/add`, { skill_ids: missing });
  }
  for (const squad of template.squads) {
    const body = { name: squad.name, description: squad.description, leader_id: agents[squad.leader] };
    let current = byName(squadList, squad.name);
    if (current) {
      if (differs(current, body)) await api("PUT", `/api/squads/${current.id}`, body);
    } else current = await api("POST", "/api/squads", body);
    squads[squad.key] = current.id;
  }
  for (const squad of template.squads) {
    const path = `/api/squads/${squads[squad.key]}`;
    const desired = { instructions: renderReferences(squad.instructions, squads) };
    if (differs(await api("GET", path), desired)) await api("PUT", path, desired);
    const members = await api("GET", `${path}/members`);
    const expected = [{ agent: squad.leader, role: "leader" }, ...squad.members];
    for (const member of expected) {
      const body = { member_type: "agent", member_id: agents[member.agent], role: member.role };
      const current = members.find((m) => m.member_type === "agent" && m.member_id === body.member_id);
      if (!current) await api("POST", `${path}/members`, body);
      else if (current.role !== body.role) await api("PATCH", `${path}/members/role`, body);
    }
  }

  for (const skill of template.skills) {
    const current = await api("GET", `/api/skills/${skills[skill.key]}?include=content`);
    verifyFields(current, { name: skill.name, description: skill.description, content: skill.content }, skill.name);
    verifyFields({ files: (current.files ?? []).map(({ path, content }) => ({ path, content })).sort((a, b) => a.path.localeCompare(b.path)) }, { files: skill.files }, `${skill.name} files`);
  }
  for (const a of template.agents) {
    const current = await api("GET", `/api/agents/${agents[a.key]}`);
    const expected = Object.fromEntries(["name", "description", "model", "thinking_level", "service_tier", "max_concurrent_tasks", "conversation_starters", "permission_mode"].map((key) => [key, a[key]]));
    verifyFields(current, { ...expected, runtime_id: runtimes[a.provider].id, instructions: `${template.common}\n${a.instructions}` }, a.name);
    const assigned = await api("GET", `/api/agents/${agents[a.key]}/skills`);
    if (a.skills.some((s) => !assigned.some((x) => x.id === skills[s]))) throw new Error(`Missing skill binding: ${a.name}`);
  }
  for (const s of template.squads) {
    const path = `/api/squads/${squads[s.key]}`;
    verifyFields(await api("GET", path), { name: s.name, description: s.description, leader_id: agents[s.leader], instructions: renderReferences(s.instructions, squads) }, s.name);
    const members = await api("GET", `${path}/members`);
    for (const m of [{ agent: s.leader, role: "leader" }, ...s.members]) {
      if (!members.some((x) => x.member_type === "agent" && x.member_id === agents[m.agent] && x.role === m.role)) throw new Error(`Missing squad membership: ${s.name} / ${m.agent}`);
    }
  }
  const updatedRuntimes = await api("GET", "/api/runtimes");
  for (const [p, r] of Object.entries(runtimes)) verifyFields(updatedRuntimes.find((x) => x.id === r.id) ?? {}, { max_concurrent_tasks: template.runtime_limits[p] }, `${p} concurrency`);
  log("Initialization verified. No projects, schedules or model tasks were created.");
  return { agents, skills, squads };
}

export async function main(args, env = process.env) {
  const options = parseArgs([...args]);
  if (options.help) { console.log(HELP); return; }
  const template = loadTemplate(resolve(options.template ?? join(ROOT, "agent-team/team.json")));
  if (options.command === "context") {
    const report = await buildContext(template, options, { root: ROOT, env, client: apiClient });
    console.log(options.json ? JSON.stringify(report, null, 2) : formatContext(report));
    return report;
  }
  if (!options.profile && !env.MULTICA_DEV_PROFILE) {
    const run = (argv) => {
      const result = spawnSync("bash", [join(ROOT, "scripts/dev-env.sh"), ...argv], { cwd: ROOT, env, stdio: "inherit" });
      if (result.error) throw result.error;
      if (result.status !== 0) throw new Error("Managed environment setup failed; resolve the reported prerequisite and retry.");
    };
    if (!options.check) run(["up", "--components", "api,daemon"]);
    run(["exec", "--", process.execPath, fileURLToPath(import.meta.url), ...args]);
    return;
  }
  const profile = options.profile ?? env.MULTICA_DEV_PROFILE;
  if (!/^[\w.-]+$/.test(profile)) throw new Error("Invalid profile name");
  const profilesDir = options.profilesDir ?? env.MULTICA_DEV_PROFILES_HOME ?? join(homedir(), ".multica/profiles");
  const configPath = join(profilesDir, profile, "config.json");
  let config;
  try { config = JSON.parse(readFileSync(configPath, "utf8")); }
  catch { throw new Error(`Cannot read authenticated profile ${profile}. Use multica setup/login first, or omit --profile for the managed local environment.`); }
  let daemonId;
  try { daemonId = readFileSync(join(homedir(), ".multica/daemon.id"), "utf8").trim(); } catch { /* Explicit runtime mappings remain available. */ }
  console.log(`Target: ${config.server_url} / workspace ${config.workspace_id} / profile ${profile}`);
  return initializeTeam(apiClient(config), template, { ...options, daemonId });
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  main(process.argv.slice(2)).catch((error) => { console.error(`[agent-team] ${error.message}`); process.exitCode = 1; });
}
