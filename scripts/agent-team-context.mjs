import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { isDeepStrictEqual } from "node:util";

function changedFields(actual, expected) {
  return Object.keys(expected).filter((key) => !isDeepStrictEqual(actual[key] ?? (typeof expected[key] === "string" ? "" : null), expected[key]));
}

const sorted = (values) => [...values].sort();
const select = (object, fields) => Object.fromEntries(fields.map((key) => [key, object[key] ?? null]));
const AGENT_FIELDS = ["name", "description", "model", "thinking_level", "service_tier", "max_concurrent_tasks", "conversation_starters", "permission_mode"];

export async function readLiveContext(api, template) {
  const [agents, skills, squads, runtimes] = await Promise.all([
    api("GET", "/api/agents"), api("GET", "/api/skills"), api("GET", "/api/squads"), api("GET", "/api/runtimes"),
  ]);
  const [bindings, members, skillBodies] = await Promise.all([
    Promise.all(agents.map(async (a) => [a.id, await api("GET", `/api/agents/${a.id}/skills`)])),
    Promise.all(squads.map(async (s) => [s.id, await api("GET", `/api/squads/${s.id}/members`)])),
    Promise.all(skills.filter((s) => template.skills.some((t) => t.name === s.name)).map(async (s) => [s.id, await api("GET", `/api/skills/${s.id}?include=content`)])),
  ]);
  const bindingMap = Object.fromEntries(bindings), memberMap = Object.fromEntries(members), skillMap = Object.fromEntries(skillBodies);
  const differences = [];
  function match(kind, expected, actual) {
    const rows = actual.filter((a) => a.name === expected.name);
    if (rows.length !== 1) {
      differences.push({ kind, name: expected.name, type: rows.length ? "ambiguous" : "missing" });
      return null;
    }
    return rows[0];
  }
  function record(kind, name, fields) {
    if (fields.length) differences.push({ kind, name, type: "changed", fields });
  }
  const agentIds = {}, squadIds = {};
  for (const a of template.agents) {
    const actual = match("agent", a, agents);
    if (!actual) continue;
    agentIds[a.key] = actual.id;
    const runtime = runtimes.find((r) => r.id === actual.runtime_id);
    const expected = { ...select(a, AGENT_FIELDS), instructions: `${template.common}\n${a.instructions}`, provider: a.provider };
    const fields = changedFields({ ...actual, provider: runtime?.provider }, expected);
    const assigned = bindingMap[actual.id].map((s) => skills.find((x) => x.id === s.id)?.name ?? s.name ?? s.id);
    const expectedSkills = a.skills.map((key) => template.skills.find((s) => s.key === key).name);
    if (!isDeepStrictEqual(sorted(assigned), sorted(expectedSkills))) fields.push("skills");
    if (actual.archived_at || actual.status === "archived") fields.push("archived");
    record("agent", a.name, fields);
  }
  for (const s of template.skills) {
    const actual = match("skill", s, skills);
    if (!actual) continue;
    const full = skillMap[actual.id];
    const files = (full.files ?? []).map(({ path, content }) => ({ path, content })).sort((a, b) => a.path.localeCompare(b.path));
    record("skill", s.name, changedFields({ ...full, files }, { description: s.description, content: s.content, files: s.files }));
  }
  for (const s of template.squads) {
    const actual = match("squad", s, squads);
    if (actual) squadIds[s.key] = actual.id;
  }
  for (const s of template.squads) {
    const actual = squads.find((a) => a.id === squadIds[s.key]);
    if (!actual) continue;
    const instructions = s.instructions.replace(/\{\{squad:([^}]+)\}\}/g, (_, key) => squadIds[key] ?? `<missing:${key}>`);
    const expectedMembers = [{ agent: s.leader, role: "leader" }, ...s.members].map((m) => `agent:${agentIds[m.agent] ?? `<missing:${m.agent}>`}:${m.role}`);
    const actualMembers = memberMap[actual.id].map((m) => `${m.member_type}:${m.member_id}:${m.role}`);
    record("squad", s.name, changedFields({ ...actual, members: sorted(actualMembers) }, { description: s.description, leader_id: agentIds[s.leader] ?? null, instructions, members: sorted(expectedMembers) }));
  }
  for (const [kind, actual, expected] of [["agent", agents, template.agents], ["skill", skills, template.skills], ["squad", squads, template.squads]]) {
    for (const a of actual) if (!expected.some((e) => e.name === a.name)) differences.push({ kind, name: a.name, type: "extra" });
  }
  const usedRuntimeIds = new Set(agents.filter((a) => template.agents.some((t) => t.name === a.name)).map((a) => a.runtime_id));
  for (const r of runtimes) {
    if (usedRuntimeIds.has(r.id) && r.provider in template.runtime_limits) record("runtime", r.name, changedFields(r, { max_concurrent_tasks: template.runtime_limits[r.provider] }));
  }
  return {
    status: "verified", differences,
    agents: agents.map((a) => ({
      ...select(a, ["id", "name", "description", "model", "thinking_level", "max_concurrent_tasks", "permission_mode", "runtime_id", "archived_at"]),
      provider: runtimes.find((r) => r.id === a.runtime_id)?.provider ?? null,
      runtime_status: runtimes.find((r) => r.id === a.runtime_id)?.status ?? "unbound",
      skills: bindingMap[a.id].map((s) => skills.find((x) => x.id === s.id)?.name ?? s.name ?? s.id),
    })),
    skills: skills.map((s) => select(s, ["id", "name", "description"])),
    squads: squads.map((s) => ({ ...select(s, ["id", "name", "leader_id"]), members: memberMap[s.id].map((m) => ({ ...select(m, ["member_id", "member_type", "role"]), name: agents.find((a) => a.id === m.member_id)?.name ?? m.member_id })) })),
    runtimes: runtimes.map((r) => select(r, ["id", "name", "provider", "status", "max_concurrent_tasks", "last_seen_at", "daemon_id"])),
  };
}

export async function buildContext(template, options, { root, env = process.env, client, discoverProfile } = {}) {
  const report = {
    version: 1, observed_at: new Date().toISOString(),
    template: {
      name: template.name, common: template.common,
      agents: template.agents.map((a) => ({ ...select(a, ["key", "name", "description", "provider", "model", "thinking_level", "skills"]), instructions: a.instructions })),
      skills: template.skills.map((s) => select(s, ["key", "name", "description", "directory"])),
      squads: template.squads.map((s) => select(s, ["key", "name", "leader", "members"])),
    },
    live: { status: "offline", reason: "Offline mode requested; template is not verified live state." },
  };
  if (options.offline) return report;
  try {
    let profile = options.profile;
    if (!profile) {
      let local;
      try { local = JSON.parse(readFileSync(join(root, "agent-team/local.json"), "utf8")); }
      catch (error) { if (error.code !== "ENOENT") throw new Error("Invalid agent-team/local.json; expected a JSON object with profile."); }
      if (local && (!local.profile || typeof local.profile !== "string")) throw new Error("agent-team/local.json needs a profile name.");
      profile = local?.profile ?? env.MULTICA_DEV_PROFILE;
    }
    if (!profile) {
      if (discoverProfile) profile = discoverProfile();
      else {
        const result = spawnSync("bash", [join(root, "scripts/dev-env.sh"), "status", "--json"], { cwd: root, env, encoding: "utf8", timeout: 15000 });
        if (result.status === 0) profile = JSON.parse(result.stdout).profile;
      }
    }
    if (!profile) throw new Error("No target profile. Pass --profile NAME, or set agent-team/local.json. No service was started.");
    if (!/^[\w.-]+$/.test(profile)) throw new Error("Invalid profile name");
    const dir = options.profilesDir ?? env.MULTICA_DEV_PROFILES_HOME ?? join(homedir(), ".multica/profiles");
    let config;
    try { config = JSON.parse(readFileSync(join(dir, profile, "config.json"), "utf8")); }
    catch { throw new Error(`Cannot read authenticated profile ${profile}; sign in and select the intended workspace.`); }
    const url = new URL(config.server_url);
    report.target = { profile, server_url: url.origin + url.pathname, workspace_id: config.workspace_id };
    report.live = await readLiveContext(client(config), template);
  } catch (error) {
    report.live = { status: "unavailable", reason: error.message };
  }
  return report;
}

const cell = (value) => String(value ?? "—").replaceAll("|", "\\|").replace(/[\r\n]+/g, " ");

export function formatContext(report) {
  const live = report.live.status === "verified";
  const rows = ["# 个人智能体军团上下文", `读取时间：${report.observed_at}`, `数据状态：${live ? "已读取目标工作区实时配置" : "仅仓库模板；实时状态未核验"}`];
  if (report.target) rows.push(`目标：${report.target.server_url} / 工作区 ${report.target.workspace_id} / profile ${report.target.profile}`);
  if (!live) rows.push(`原因：${report.live.reason}`);
  rows.push("", "## 长期规则", report.template.common, "", `## ${live ? "当前工作区角色" : "模板角色"}`, "| 角色 | 工具 | 模型 / 推理等级 | 技能 | 运行时 |", "| --- | --- | --- | --- | --- |");
  for (const a of live ? report.live.agents : report.template.agents) rows.push(`| ${cell(a.name)} | ${cell(a.provider)} | ${cell(a.model)} / ${cell(a.thinking_level)} | ${cell(a.skills.join(", "))} | ${live ? cell(a.runtime_status) : "未核验"} |`);
  rows.push("", "## 小队");
  for (const s of live ? report.live.squads : report.template.squads) {
    const leader = live ? report.live.agents.find((a) => a.id === s.leader_id)?.name : report.template.agents.find((a) => a.key === s.leader)?.name;
    const members = live ? s.members.map((m) => m.name) : s.members.map((m) => report.template.agents.find((a) => a.key === m.agent)?.name);
    rows.push(`- ${s.name}：负责人 ${leader ?? "未知"}；成员 ${members.join("、")}`);
  }
  rows.push("", "## 与模板的差异");
  if (!live) rows.push("无法比较。不得据此假设服务端缺失角色，也不得自动初始化或重绑运行时。");
  else if (!report.live.differences.length) rows.push("模板覆盖的配置一致；未执行模型任务或验证实际交付能力。");
  else for (const d of report.live.differences) rows.push(`- ${d.kind} / ${d.name}：${d.type}${d.fields ? ` (${d.fields.join(", ")})` : ""}`);
  rows.push("", "变更前读取 agent-team/CONTEXT.md。修改具体内容时读取对应角色/技能/小队的完整配置；本摘要不含凭据或完整实时提示词。不要通过重新初始化覆盖未核对的差异。", "");
  return rows.join("\n");
}
