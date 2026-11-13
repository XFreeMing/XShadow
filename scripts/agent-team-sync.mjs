import { createHash } from "node:crypto";
import { lstatSync, mkdirSync, readFileSync, readdirSync, realpathSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, posix, relative, resolve } from "node:path";
import { localPath, validateTemplateObject } from "./agent-team-template.mjs";

const UUID_PATTERN = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;
// Values the source machine can read but a portable template must not carry.
// Matches are reported by field only; the offending text is never echoed.
const UNPORTABLE = [
  [UUID_PATTERN, "an instance UUID"],
  [/(^|[\s"'`(=])\/(?:home|Users|root)\/[^\s"'`)]+/, "a machine path"],
  [/\b[A-Za-z]:\\[^\s"'`]+/, "a machine path"],
  [/\b(?:sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})\b/, "a credential"],
  [/\bBearer\s+[A-Za-z0-9._-]{20,}/i, "a credential"],
];

const emptyJson = (value) => value == null
  || (typeof value === "string" && !value.trim())
  || (typeof value === "object" && !Array.isArray(value) && Object.keys(value).length === 0);

function safeFilePath(value) {
  if (typeof value !== "string" || !value || value.includes("\x00") || value.includes("\\") || value.startsWith("/")) return false;
  if (/^[A-Za-z]:/.test(value)) return false;
  const clean = posix.normalize(value);
  return clean !== "." && clean !== ".." && !clean.startsWith("../") && clean === value && value !== "SKILL.md";
}

function portableIssues(text) {
  return UNPORTABLE.filter(([pattern]) => pattern.test(text)).map(([, label]) => label);
}

function scanText(issues, kind, name, field, text) {
  for (const reason of portableIssues(typeof text === "string" ? text : "")) issues.push({ kind, name, field, reason: `contains ${reason}` });
}

function slugKey(kind, name, used) {
  let base = name.normalize("NFKD").replace(/[̀-ͯ]/g, "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  if (!/^[a-z]/.test(base)) base = `${kind}-${createHash("sha256").update(name).digest("hex").slice(0, 8)}`;
  let key = base, suffix = 2;
  while (used.has(key)) key = `${base}-${suffix++}`;
  return key;
}

function itemDiff(oldItems, newItems, normalize) {
  const oldByKey = new Map(oldItems.map((item) => [item.key, item]));
  const newByKey = new Map(newItems.map((item) => [item.key, item]));
  return {
    added: newItems.filter((item) => !oldByKey.has(item.key)).map((item) => item.name),
    removed: oldItems.filter((item) => !newByKey.has(item.key)).map((item) => item.name),
    changed: newItems.filter((item) => oldByKey.has(item.key) && JSON.stringify(normalize(oldByKey.get(item.key))) !== JSON.stringify(normalize(item))).map((item) => item.name),
  };
}

const normalizeAgent = (a) => {
  const row = {
    key: a.key, provider: a.provider, name: a.name, description: a.description,
    model: a.model, thinking_level: a.thinking_level, service_tier: a.service_tier,
    max_concurrent_tasks: a.max_concurrent_tasks, conversation_starters: a.conversation_starters,
    instructions: a.instructions,
  };
  if (a.include_common === false) row.include_common = false;
  row.skills = a.skills; row.permission_mode = a.permission_mode;
  return row;
};
const normalizeSkill = (s) => ({
  key: s.key, name: s.name, description: s.description, directory: s.directory,
  content: s.content, files: s.files ?? [],
});
const normalizeSquad = (s) => ({
  key: s.key, name: s.name, description: s.description, instructions: s.instructions,
  leader: s.leader, members: s.members,
});

export async function readLiveTeam(api) {
  const me = await api("GET", "/api/me");
  const [agents, skills, squads, runtimes] = await Promise.all([
    api("GET", "/api/agents"), api("GET", "/api/skills"), api("GET", "/api/squads"), api("GET", "/api/runtimes"),
  ]);
  for (const [label, rows] of [["agents", agents], ["skills", skills], ["squads", squads], ["runtimes", runtimes]]) {
    if (!Array.isArray(rows)) throw new Error(`Unexpected ${label} response; the server does not match this CLI.`);
  }
  const owned = agents.filter((a) => a.owner_id === me.id);
  const live = {
    me,
    agents: owned.filter((a) => !a.archived_at && a.status !== "archived"),
    archived: owned.filter((a) => a.archived_at || a.status === "archived"),
    skills, squads: squads.filter((s) => !s.archived_at), runtimes,
    bindings: {}, skillBodies: {}, members: {},
    readSkill: async (id) => {
      if (!(id in live.skillBodies)) live.skillBodies[id] = await api("GET", `/api/skills/${id}?include=content`);
      return live.skillBodies[id];
    },
  };
  const bindingLists = await Promise.all(live.agents.map((a) => api("GET", `/api/agents/${a.id}/skills`)));
  live.agents.forEach((a, i) => { live.bindings[a.id] = bindingLists[i] ?? []; });
  const memberLists = await Promise.all(live.squads.map((s) => api("GET", `/api/squads/${s.id}/members`)));
  live.squads.forEach((s, i) => { live.members[s.id] = memberLists[i] ?? []; });
  return live;
}

function checkAgent(agent, runtime, issues) {
  const add = (field, reason) => issues.push({ kind: "agent", name: agent.name, field, reason });
  if (!runtime) add("runtime_id", "has no bound runtime, so its provider cannot be expressed");
  if (agent.permission_mode !== "private") add("permission_mode", "is not private");
  if ((agent.invocation_targets ?? []).length) add("invocation_targets", "grants invocation permissions the template cannot express");
  if (agent.has_custom_env || (agent.custom_env_key_count ?? 0) > 0) add("custom_env", "has environment secrets that must not enter the template");
  if (agent.mcp_config_redacted || !emptyJson(agent.mcp_config)) add("mcp_config", "has MCP configuration the template cannot express");
  if ((agent.custom_args ?? []).length) add("custom_args", "has custom arguments the template cannot express");
  if (!emptyJson(agent.runtime_config)) add("runtime_config", "has runtime configuration the template cannot express");
  if (agent.runtime_mode && agent.runtime_mode !== "local") add("runtime_mode", "is not local, which init does not configure");
  if ((agent.disabled_runtime_skills ?? []).length) add("disabled_runtime_skills", "has disabled runtime skills the template cannot express");
  if (!agent.model) add("model", "is empty");
  if (typeof agent.thinking_level !== "string") add("thinking_level", "must be a string; an explicit empty string selects the runtime default");
  if (!Number.isInteger(agent.max_concurrent_tasks) || agent.max_concurrent_tasks < 1) add("max_concurrent_tasks", "is not a positive integer");
  const starters = agent.conversation_starters ?? [];
  if (!Array.isArray(starters) || starters.some((s) => !s || typeof s.label !== "string" || typeof s.prompt !== "string")) {
    add("conversation_starters", "have an unexpected shape");
  } else {
    starters.forEach((s, i) => {
      scanText(issues, "agent", agent.name, `conversation_starters[${i}].label`, s.label);
      scanText(issues, "agent", agent.name, `conversation_starters[${i}].prompt`, s.prompt);
    });
  }
  scanText(issues, "agent", agent.name, "description", agent.description);
}

function checkSkill(body, issues) {
  const add = (field, reason) => issues.push({ kind: "skill", name: body.name ?? body.id, field, reason });
  if (!body.name) add("name", "is empty");
  if (typeof body.content !== "string") add("content", "is not text");
  if (!emptyJson(body.config)) add("config", "has configuration the template cannot express");
  scanText(issues, "skill", body.name ?? body.id, "name", body.name);
  scanText(issues, "skill", body.name ?? body.id, "description", body.description);
  scanText(issues, "skill", body.name ?? body.id, "content", body.content);
  const files = body.files ?? [];
  if (!Array.isArray(files)) { add("files", "have an unexpected shape"); return; }
  const seen = new Set();
  for (const file of files) {
    if (!file || !safeFilePath(file.path)) { add("files", "contain a path outside the skill directory"); continue; }
    if (seen.has(file.path)) add("files", "contain a duplicate path");
    if (typeof file.content !== "string") add("files", "contain a non-text body");
    scanText(issues, "skill", body.name ?? body.id, `files[${file.path}]`, file.path);
    scanText(issues, "skill", body.name ?? body.id, `files[${file.path}]`, file.content);
    seen.add(file.path);
  }
}

export async function prepareSync(api, { template, templatePath, runtimeIds = {}, displayRoot = dirname(templatePath) }) {
  const live = await readLiveTeam(api);
  const root = dirname(templatePath);
  const issues = [], skipped = [], warnings = [];
  for (const agent of live.archived) skipped.push({ kind: "agent", name: agent.name, reason: "archived" });

  const runtimesById = new Map(live.runtimes.map((r) => [r.id, r]));
  const ownIds = new Set(live.agents.map((a) => a.id));
  const seenAgentNames = new Set();
  for (const agent of live.agents) {
    if (seenAgentNames.has(agent.name)) issues.push({ kind: "agent", name: agent.name, field: "name", reason: "is not unique" });
    seenAgentNames.add(agent.name);
    checkAgent(agent, runtimesById.get(agent.runtime_id), issues);
  }

  // Skills are exported for the whole initializable workspace: the account's own
  // skills, template-matched names and anything an exported agent binds. Skills
  // visible only because another account shared them are reported, not dropped
  // silently, because their ownership cannot be expressed in the template.
  const skillById = new Map(live.skills.map((s) => [s.id, s]));
  const skillNameCount = new Map();
  for (const skill of live.skills) skillNameCount.set(skill.name, (skillNameCount.get(skill.name) ?? 0) + 1);
  for (const [name, count] of skillNameCount) if (count > 1) issues.push({ kind: "skill", name, field: "name", reason: "is not unique" });
  const boundIds = new Set(Object.values(live.bindings).flat().map((b) => b.id));
  const templateNames = new Set(template.skills.map((s) => s.name));
  const exportedSkillIds = new Set();
  for (const skill of live.skills) {
    if (skill.created_by === live.me.id || templateNames.has(skill.name) || boundIds.has(skill.id)) {
      exportedSkillIds.add(skill.id);
      if (skill.created_by !== live.me.id && !templateNames.has(skill.name)) warnings.push(`Skill ${skill.name} is bound to an exported agent but was created by another account; only its content is recorded.`);
    } else {
      skipped.push({ kind: "skill", name: skill.name, reason: "is shared from another account and cannot be expressed portably" });
    }
  }
  for (const id of boundIds) {
    if (!skillById.has(id)) issues.push({ kind: "skill", name: id, field: "id", reason: "is bound to an exported agent but missing from the skill list" });
  }
  const boundSkills = [];
  for (const id of exportedSkillIds) {
    const summary = skillById.get(id);
    const body = await live.readSkill(id);
    if (!body) { issues.push({ kind: "skill", name: summary.name, field: "id", reason: "could not be read" }); continue; }
    checkSkill(body, issues);
    boundSkills.push({ summary, body });
  }
  const bodyByName = new Map(boundSkills.map((s) => [s.summary.name, s]));
  const liveSkillNames = live.skills.map((s) => s.name);
  const skillNameOrder = [
    ...template.skills.map((s) => s.name).filter((name) => bodyByName.has(name)),
    ...liveSkillNames.filter((name) => bodyByName.has(name) && !templateNames.has(name)),
  ];

  const usedSkillKeys = new Set(template.skills.map((s) => s.key));
  const skillKeyByName = new Map(template.skills.map((s) => [s.name, s.key]));
  const skills = skillNameOrder.map((name) => {
    const { body } = bodyByName.get(name);
    let key = skillKeyByName.get(name);
    if (!key) { key = slugKey("skill", name, usedSkillKeys); usedSkillKeys.add(key); skillKeyByName.set(name, key); }
    const previous = template.skills.find((s) => s.name === name);
    return {
      key, name, description: body.description ?? "", directory: previous?.directory ?? `skills/${key}`,
      content: body.content, files: [...(body.files ?? [])].map(({ path, content }) => ({ path, content })).sort((a, b) => a.path.localeCompare(b.path)),
    };
  });
  const reserved = new Set([resolve(templatePath), localPath(root, template.common_instructions)]);
  for (let i = 0; i < skills.length; i++) {
    const skill = skills[i];
    const dir = localPath(root, skill.directory);
    if (dir === resolve(root)) issues.push({ kind: "skill", name: skill.name, field: "directory", reason: "is the template root" });
    const targets = [join(dir, "SKILL.md"), ...skill.files.map((f) => localPath(root, `${skill.directory}/${f.path}`))];
    if (targets.some((target) => reserved.has(resolve(target)))) issues.push({ kind: "skill", name: skill.name, field: "directory", reason: "would overwrite a template control file" });
    for (let j = i + 1; j < skills.length; j++) {
      const other = skills[j].directory;
      if (skill.directory === other || skill.directory.startsWith(`${other}/`) || other.startsWith(`${skill.directory}/`)) {
        issues.push({ kind: "skill", name: skill.name, field: "directory", reason: "overlaps another skill directory" });
      }
    }
  }

  // Agents: existing keys survive by name; new objects get deterministic keys.
  const usedAgentKeys = new Set(template.agents.map((a) => a.key));
  const agentKeyByName = new Map(template.agents.map((a) => [a.name, a.key]));
  const agentLiveByName = new Map(live.agents.map((a) => [a.name, a]));
  const agentOrder = [
    ...template.agents.map((a) => a.name).filter((name) => agentLiveByName.has(name)),
    ...live.agents.map((a) => a.name).filter((name) => !template.agents.some((a) => a.name === name)),
  ];
  const common = template.common;
  const agents = agentOrder.map((name) => {
    const agent = agentLiveByName.get(name);
    const runtime = runtimesById.get(agent.runtime_id);
    let key = agentKeyByName.get(name);
    if (!key) { key = slugKey("agent", name, usedAgentKeys); usedAgentKeys.add(key); agentKeyByName.set(name, key); }
    const prefixed = Boolean(common) && agent.instructions.startsWith(`${common}\n`);
    const instructions = prefixed ? agent.instructions.slice(common.length + 1) : agent.instructions;
    scanText(issues, "agent", name, "instructions", instructions);
    const boundKeys = [];
    for (const binding of live.bindings[agent.id] ?? []) {
      const skill = skillById.get(binding.id);
      if (!skill) continue;
      if (binding.enabled === false) { issues.push({ kind: "agent", name, field: "skills", reason: `has a disabled binding for ${skill.name}` }); continue; }
      const skillKey = skillKeyByName.get(skill.name);
      if (skillKey) boundKeys.push(skillKey);
    }
    const previous = template.agents.find((a) => a.name === name);
    const ordered = previous
      ? [...previous.skills.filter((skillKey) => boundKeys.includes(skillKey)), ...boundKeys.filter((skillKey) => !previous.skills.includes(skillKey))]
      : boundKeys;
    const entry = {
      key, provider: runtime?.provider, name, description: agent.description ?? "",
      model: agent.model, thinking_level: agent.thinking_level, service_tier: agent.service_tier ?? "",
      max_concurrent_tasks: agent.max_concurrent_tasks,
      conversation_starters: (agent.conversation_starters ?? []).map(({ label, prompt }) => ({ label, prompt })),
      instructions,
    };
    if (!prefixed) entry.include_common = false;
    entry.skills = ordered;
    entry.permission_mode = "private";
    return entry;
  });

  // Squads are exported when at least one exported agent belongs to them.
  const squadAgentKeys = new Map(agents.map((a) => [a.name, a.key]));
  const liveAgentById = new Map(live.agents.map((a) => [a.id, a]));
  const inScope = live.squads.filter((squad) => (live.members[squad.id] ?? []).some((m) => m.member_type === "agent" && ownIds.has(m.member_id)));
  for (const squad of live.squads) {
    if (!inScope.includes(squad)) skipped.push({ kind: "squad", name: squad.name, reason: "no exported agent is a member" });
  }
  const usedSquadKeys = new Set(template.squads.map((s) => s.key));
  const squadKeyByName = new Map(template.squads.map((s) => [s.name, s.key]));
  const squadKeyById = new Map();
  for (const squad of inScope) {
    let key = squadKeyByName.get(squad.name);
    if (!key) { key = slugKey("squad", squad.name, usedSquadKeys); usedSquadKeys.add(key); squadKeyByName.set(squad.name, key); }
    squadKeyById.set(squad.id, key);
  }
  const liveSquadByName = new Map(inScope.map((squad) => [squad.name, squad]));
  const squadOrder = [
    ...template.squads.map((s) => s.name).filter((name) => liveSquadByName.has(name)),
    ...inScope.map((s) => s.name).filter((name) => !template.squads.some((s) => s.name === name)),
  ];
  const templateAgentByKey = new Map(template.agents.map((a) => [a.key, a]));
  const squads = squadOrder.map((name) => {
    const squad = liveSquadByName.get(name);
    const key = squadKeyById.get(squad.id);
    const add = (field, reason) => issues.push({ kind: "squad", name: squad.name, field, reason });
    const members = live.members[squad.id] ?? [];
    if (!ownIds.has(squad.leader_id)) add("leader_id", "is not in the exported agent roster");
    if (members.some((m) => m.member_type !== "agent")) add("members", "include a human or non-agent member the template cannot express");
    if (members.some((m) => m.member_type === "agent" && !ownIds.has(m.member_id))) add("members", "include an agent outside the exported roster");
    if (members.some((m) => m.member_type === "agent" && !m.role)) add("members", "have an empty role");
    const agentMembers = members.filter((m) => m.member_type === "agent");
    if (new Set(agentMembers.map((m) => m.member_id)).size !== agentMembers.length) add("members", "contain a duplicate agent");
    const leaderEntry = agentMembers.find((m) => m.member_id === squad.leader_id);
    if (!leaderEntry) add("members", "do not contain the leader");
    else if (leaderEntry.role !== "leader") add("members", "give the leader a role other than leader");
    let instructions = squad.instructions ?? "";
    for (const [id, squadKey] of squadKeyById) instructions = instructions.split(id).join(`{{squad:${squadKey}}}`);
    scanText(issues, "squad", squad.name, "name", squad.name);
    scanText(issues, "squad", squad.name, "description", squad.description);
    scanText(issues, "squad", squad.name, "instructions", instructions);
    const memberRows = agentMembers.filter((m) => m.member_id !== squad.leader_id).map((m) => ({
      name: liveAgentById.get(m.member_id)?.name, role: m.role,
    }));
    const previous = template.squads.find((s) => s.name === squad.name);
    let ordered = memberRows;
    if (previous) {
      const templateAgentNames = previous.members.map((m) => templateAgentByKey.get(m.agent)?.name).filter(Boolean);
      ordered = [
        ...templateAgentNames.filter((agentName) => memberRows.some((m) => m.name === agentName)).map((agentName) => memberRows.find((m) => m.name === agentName)),
        ...memberRows.filter((m) => !templateAgentNames.includes(m.name)),
      ];
    }
    const entry = {
      key, name: squad.name, description: squad.description ?? "", instructions,
      leader: squadAgentKeys.get(liveAgentById.get(squad.leader_id)?.name),
      members: ordered.map(({ name: agentName, role }) => ({ agent: squadAgentKeys.get(agentName), role })),
    };
    if (entry.members.some((m) => !m.agent)) add("members", "reference an agent without an exported key");
    if (!entry.leader) add("leader_id", "has no exported key");
    return entry;
  });

  // Runtime limits come from the runtimes actually bound by exported agents.
  const providerOrder = [];
  for (const agent of live.agents) {
    const provider = runtimesById.get(agent.runtime_id)?.provider;
    if (provider && !providerOrder.includes(provider)) providerOrder.push(provider);
  }
  const runtimeLimits = {};
  for (const provider of providerOrder) {
    const ids = [...new Set(live.agents.filter((a) => runtimesById.get(a.runtime_id)?.provider === provider).map((a) => a.runtime_id))];
    let selected = ids[0];
    if (ids.length > 1) {
      const explicit = runtimeIds[provider];
      if (!explicit) { issues.push({ kind: "runtime", name: provider, field: "runtimes", reason: `has ${ids.length} bound runtimes; pass --runtime ${provider}=ID` }); continue; }
      if (!ids.includes(explicit)) { issues.push({ kind: "runtime", name: provider, field: "runtimes", reason: "does not include the explicitly mapped runtime" }); continue; }
      selected = explicit;
      const capacity = runtimesById.get(explicit)?.max_concurrent_tasks;
      if (ids.some((id) => runtimesById.get(id)?.max_concurrent_tasks !== capacity)) {
        issues.push({ kind: "runtime", name: provider, field: "runtimes", reason: "have different concurrency limits, which one template value cannot express" });
        continue;
      }
    }
    const capacity = runtimesById.get(selected)?.max_concurrent_tasks;
    if (!Number.isInteger(capacity) || capacity < 1) { issues.push({ kind: "runtime", name: provider, field: "max_concurrent_tasks", reason: "is not a positive integer" }); continue; }
    runtimeLimits[provider] = capacity;
  }
  for (const provider of Object.keys(runtimeIds)) {
    if (!providerOrder.includes(provider)) issues.push({ kind: "runtime", name: provider, field: "runtime", reason: "is not bound to any exported agent" });
  }

  if (issues.length) {
    const lines = issues.map((i) => `- ${i.kind} ${i.name}: ${i.field} ${i.reason}`);
    throw new Error(`Template not written; the live configuration cannot be expressed portably:\n${lines.join("\n")}`);
  }

  const limits = {};
  for (const provider of [...Object.keys(template.runtime_limits), ...providerOrder.filter((p) => !(p in template.runtime_limits))]) {
    if (provider in runtimeLimits) limits[provider] = runtimeLimits[provider];
  }
  const next = {
    version: 1,
    name: template.name,
    runtime_limits: limits,
    common_instructions: template.common_instructions,
    skills: skills.map(({ key, name, description, directory }) => ({ key, name, description, directory })),
    agents,
    squads,
  };
  validateTemplateObject(next);

  const changes = {
    agents: itemDiff(template.agents, agents, normalizeAgent),
    skills: itemDiff(template.skills, skills, normalizeSkill),
    squads: itemDiff(template.squads, squads, normalizeSquad),
    runtime_limits: {
      added: Object.keys(limits).filter((p) => !(p in template.runtime_limits)),
      removed: Object.keys(template.runtime_limits).filter((p) => !(p in limits)),
      changed: Object.keys(limits).filter((p) => p in template.runtime_limits && template.runtime_limits[p] !== limits[p]),
    },
  };

  const show = (full) => {
    const rel = relative(displayRoot, full);
    return rel.startsWith("..") ? full : rel;
  };
  const readIfExists = (path) => {
    try { return readFileSync(path, "utf8"); } catch (error) { if (error.code === "ENOENT") return null; throw error; }
  };
  const writes = [], deletions = [];
  const templateContent = `${JSON.stringify(next, null, 2)}\n`;
  const currentTemplate = readIfExists(templatePath);
  if (currentTemplate !== templateContent) writes.push({ path: templatePath, content: templateContent, kind: "template", status: currentTemplate === null ? "A" : "M" });

  let unchanged = 0;
  for (const skill of skills) {
    const dir = localPath(root, skill.directory);
    const desired = new Set(["SKILL.md", ...skill.files.map((f) => f.path)]);
    const targets = [{ path: join(dir, "SKILL.md"), content: skill.content }, ...skill.files.map((f) => ({ path: localPath(root, `${skill.directory}/${f.path}`), content: f.content }))];
    for (const target of targets) {
      const current = readIfExists(target.path);
      if (current === target.content) unchanged++;
      else writes.push({ ...target, kind: "skill", status: current === null ? "A" : "M" });
    }
    for (const stale of existingFiles(dir)) {
      const rel = relative(dir, stale).split(/[\\/]/).join("/");
      if (!desired.has(rel)) deletions.push({ path: stale, kind: "delete" });
    }
  }
  for (const skill of template.skills) {
    if (!skills.some((s) => s.name === skill.name)) warnings.push(`Skill directory ${skill.directory} is no longer referenced by the template; it was left in place.`);
  }
  for (const agent of changes.agents.removed) warnings.push(`Agent ${agent} is no longer in the roster and was removed from the template only.`);
  for (const squad of changes.squads.removed) warnings.push(`Squad ${squad} is no longer in the roster and was removed from the template only.`);

  return {
    root, templatePath, next, templateContent, writes, deletions, changes, runtimeLimits: limits,
    counts: { agents: agents.length, skills: skills.length, squads: squads.length },
    skipped, warnings, unchanged,
    display: show,
  };
}

function existingFiles(dir) {
  let stat;
  try { stat = lstatSync(dir); } catch (error) { if (error.code === "ENOENT") return []; throw error; }
  if (stat.isSymbolicLink()) throw new Error(`Refusing to follow a symlinked skill directory: ${relative(process.cwd(), dir)}`);
  if (!stat.isDirectory()) throw new Error(`Skill directory is not a directory: ${relative(process.cwd(), dir)}`);
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isSymbolicLink()) throw new Error(`Refusing to follow a symlink in a skill directory: ${entry.name}`);
    if (entry.isDirectory()) out.push(...existingFiles(full));
    else if (entry.isFile()) out.push(full);
    else throw new Error(`Skill directories may only contain regular files: ${entry.name}`);
  }
  return out;
}

// Every planned path is checked in both --check and real runs, so a dry run
// proves the same safety conditions as the write path.
function validatePlanPaths(plan, rootReal) {
  const seen = new Set();
  for (const item of [...plan.writes, ...plan.deletions]) {
    const full = resolve(item.path);
    const rel = relative(rootReal, full);
    if (!rel || rel.startsWith("..") || rel.split(/[\\/]/).includes("..")) throw new Error(`Refusing to write outside the template directory: ${rel}`);
    if (seen.has(full)) throw new Error(`Duplicate planned path: ${rel}`);
    seen.add(full);
    let current = rootReal;
    const parts = rel.split(/[\\/]/);
    for (let i = 0; i < parts.length; i++) {
      current = join(current, parts[i]);
      let stat;
      try { stat = lstatSync(current); } catch (error) { if (error.code === "ENOENT") break; throw error; }
      if (stat.isSymbolicLink()) throw new Error(`Refusing to write through a symlink: ${relative(rootReal, current)}`);
      if (i === parts.length - 1 && !stat.isFile()) throw new Error(`Refusing to overwrite a non-file: ${relative(rootReal, current)}`);
      if (i < parts.length - 1 && !stat.isDirectory()) throw new Error(`Template path component is not a directory: ${relative(rootReal, current)}`);
    }
  }
}

export function applySyncPlan(plan, { check = false } = {}) {
  const rootReal = realpathSync(plan.root);
  validatePlanPaths(plan, rootReal);
  const files = [
    ...plan.writes.map((w) => ({ path: plan.display(w.path), status: w.status ?? "M" })),
    ...plan.deletions.map((d) => ({ path: plan.display(d.path), status: "D" })),
  ];
  const report = {
    check, template: plan.display(plan.templatePath),
    changes: plan.changes, runtime_limits: plan.runtimeLimits, counts: plan.counts,
    skipped: plan.skipped, warnings: plan.warnings,
    files, unchanged: plan.unchanged,
    written: 0, deleted: 0,
  };
  if (check) return report;

  const before = new Map();
  for (const write of plan.writes) {
    const full = resolve(write.path);
    try { before.set(full, readFileSync(full, "utf8")); } catch (error) { if (error.code !== "ENOENT") throw error; before.set(full, null); }
  }
  for (const deletion of plan.deletions) before.set(resolve(deletion.path), readFileSync(resolve(deletion.path), "utf8"));

  // Write skill files first and the template last, so an interrupted run never
  // points the template at files that were not written yet. Renames are not a
  // transaction; failures restore the prior bytes on a best-effort basis.
  const ordered = [...plan.writes.filter((w) => w.kind !== "template"), ...plan.writes.filter((w) => w.kind === "template")];
  const staged = [];
  try {
    for (const write of ordered) {
      const full = resolve(write.path);
      mkdirSync(dirname(full), { recursive: true });
      const temp = `${full}.tmp-${process.pid}-${staged.length}`;
      writeFileSync(temp, write.content, { encoding: "utf8", flag: "wx" });
      staged.push([temp, full]);
    }
    for (const [temp, full] of staged) renameSync(temp, full);
    for (const deletion of plan.deletions) rmSync(resolve(deletion.path));
  } catch (error) {
    for (const [temp] of staged) { try { rmSync(temp, { force: true }); } catch { /* best effort */ } }
    for (const [full, prior] of before) {
      try {
        if (prior === null) rmSync(full, { force: true });
        else writeFileSync(full, prior);
      } catch { /* best effort */ }
    }
    throw new Error(`${error.message} The template was restored on a best-effort basis; review the working tree before retrying.`);
  }
  report.written = plan.writes.length;
  report.deleted = plan.deletions.length;
  return report;
}

export function formatSyncReport(report, target) {
  const title = report.check
    ? (report.files.length ? "Sync check (no files written)" : "Sync check (no changes planned)")
    : (report.written || report.deleted ? "Sync complete" : "Sync complete (no changes)");
  const rows = [title];
  if (target) rows.push(`Target: ${target.server_url} / workspace ${target.workspace_id} / profile ${target.profile}`);
  rows.push(`Template: ${report.template}`);
  for (const [kind, label] of [["agents", "Agents"], ["skills", "Skills"], ["squads", "Squads"]]) {
    const c = report.changes[kind];
    rows.push(`${label}: ${report.counts[kind]} in template; ${c.added.length} added, ${c.changed.length} changed, ${c.removed.length} removed.`);
  }
  rows.push(`Runtime limits: ${Object.entries(report.runtime_limits).map(([p, v]) => `${p} ${v}`).join(", ")}`);
  if (report.files.length) rows.push("", ...report.files.map((f) => `${f.status} ${f.path}`));
  else rows.push("", "No file changes.");
  if (report.check) rows.push("", "No files were written (--check).");
  else rows.push(`Files written: ${report.written}${report.deleted ? `; deleted: ${report.deleted}` : ""}.`);
  for (const warning of report.warnings) rows.push(`! ${warning}`);
  for (const skip of report.skipped) rows.push(`- Skipped ${skip.kind} ${skip.name}: ${skip.reason}.`);
  return rows.join("\n");
}
