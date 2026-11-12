import { readFileSync, readdirSync } from "node:fs";
import { isAbsolute, join, relative, resolve } from "node:path";

export function localPath(root, path) {
  if (typeof path !== "string" || isAbsolute(path)) throw new Error(`Expected relative template path: ${path}`);
  const full = resolve(root, path);
  if (relative(root, full).split(/[\\/]/).includes("..")) throw new Error(`Path escapes template: ${path}`);
  return full;
}

export function skillFiles(dir, prefix = "") {
  return readdirSync(join(dir, prefix), { withFileTypes: true }).flatMap((entry) => {
    const path = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) return skillFiles(dir, path);
    if (!entry.isFile()) throw new Error(`Skill bundles require regular files: ${path}`);
    if (path === "SKILL.md") return [];
    return [{ path, content: readFileSync(join(dir, path), "utf8") }];
  }).sort((a, b) => a.path.localeCompare(b.path));
}

export function renderReferences(text, squadIds) {
  return text.replace(/\{\{squad:([^}]+)\}\}/g, (_, key) => {
    if (!squadIds[key]) throw new Error(`Unknown squad reference: ${key}`);
    return squadIds[key];
  });
}

export function validateTemplateObject(template) {
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
    if (agent.include_common !== undefined && typeof agent.include_common !== "boolean") throw new Error(`Invalid agent configuration: ${agent.name}`);
  }
  for (const squad of template.squads) {
    if (!agentKeys.has(squad.leader) || squad.members.some((m) => !agentKeys.has(m.agent))) throw new Error(`Unknown agent in squad ${squad.name}`);
  }
  const squadIds = Object.fromEntries(template.squads.map((s) => [s.key, s.key]));
  for (const squad of template.squads) renderReferences(squad.instructions, squadIds);
}

// include_common is opt-out: templates written before the field existed keep
// prefixing every agent body with common.md.
export function agentInstructions(common, agent) {
  return agent.include_common === false ? agent.instructions : `${common}\n${agent.instructions}`;
}