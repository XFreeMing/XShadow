import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual, parseArgs } from "node:util";
import { apiClient } from "./agent-team.mjs";
import { resolveProfile } from "./agent-team-context.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const catalog = JSON.parse(readFileSync(resolve(root, "agent-team/collaboration-templates.json"), "utf8"));
const { fields, formulas, templates, views } = catalog;
const paths = {
  properties: "/api/properties",
  types: "/api/issue-types",
  views: "/api/issue-views?scope_type=workspace",
};

function propertyDefinitions() {
  const reference = (key) => {
    const def = fields[key] ?? formulas[key];
    if (!def) throw new Error(`Unknown field: ${key}`);
    return `{${def.name}}`;
  };
  const definitions = Object.entries(fields).map(([key, field]) => ({
    key, ...field, config: key === "recipe" ? {
      options: Object.values(templates).map(({ name }) => ({ name, color: "#64748b" })),
    } : {},
  }));
  for (const [key, { name, description, all, interaction }] of Object.entries(formulas)) {
    const conditions = all.map(reference);
    if (interaction) {
      const flag = reference(interaction.flag);
      const prototype = reference(interaction.requiredWhenTrue);
      conditions.push(`IF((${flag} = BLANK), FALSE, IF(${flag}, IF(${prototype}, TRUE, FALSE), TRUE))`);
    }
    definitions.push({
      key, name, description, type: "formula",
      config: { expression: conditions.reduceRight((yes, condition) => `IF(${condition}, ${yes}, FALSE)`, "TRUE") },
    });
  }
  return definitions;
}

function named(rows, name) {
  const matches = rows.filter((row) => row.name === name);
  if (matches.length > 1) throw new Error(`名称重复，需核对现有配置：${name}`);
  return matches[0];
}

function compatible(actual, desired) {
  return Object.entries(desired).every(([key, value]) => isDeepStrictEqual(actual[key], value));
}

function assertCompatible(actual, desired, kind) {
  if (!compatible(actual, desired)) throw new Error(`${kind}「${desired.name}」与模板不一致。请核对已有设置后重试。`);
}

function propertyMatches(actual, desired) {
  if (desired.type === "select") {
    const { options, ...config } = actual.config;
    const active = options?.filter(({ name }) =>
      desired.name !== fields.recipe.name || name !== "七步问题解决 · 调研决策");
    return { ...actual, config: { ...config, options: active?.map(({ name, color }) => ({ name, color })) } };
  }
  return actual;
}

async function snapshot(api) {
  const [properties, types, savedViews, methods] = await Promise.all([
    api("GET", `${paths.properties}?include_archived=true`),
    api("GET", `${paths.types}?include_archived=true`),
    api("GET", paths.views),
    api("GET", "/api/work-methods"),
  ]);
  return { properties: properties.properties, types: types.issue_types, views: savedViews, methods: methods.methods };
}

function getBindings(properties) {
  const byKey = Object.fromEntries(propertyDefinitions().map(({ key, name }) => [key, named(properties, name)]));
  const allIDs = properties.filter((property) => !property.archived).map(({ id }) => id);
  const templateBody = ({ name, required, fields: selected }) => {
    const recipe = byKey.recipe;
    const choice = recipe.config.options.find((option) => option.name === name);
    return {
      name,
      fields: [
        { property_id: recipe.id, required: true, default_value: choice.id },
        ...selected.map((key) => ({ property_id: byKey[key].id, required: required.includes(key) })),
      ],
    };
  };
  const viewBody = ({ name, template, fields: selected }) => {
    const recipe = byKey.recipe;
    const choice = recipe.config.options.find((option) => option.name === templates[template].name);
    const visible = new Set(selected.map((key) => byKey[key].id));
    return {
      name,
      scope_type: "workspace",
      visibility: "workspace",
      definition_version: catalog.version,
      query: { propertyFilters: { [recipe.id]: [choice.id] } },
      display: {
        viewMode: "grid",
        gridColumnOrder: ["title", "type", "status", "assignee", ...selected.map((key) => `property:${byKey[key].id}`)],
        gridHiddenColumnIds: allIDs.filter((id) => !visible.has(id)).map((id) => `property:${id}`),
      },
    };
  };
  return { byKey, templateBody, viewBody };
}

export async function installCatalog(api, apply) {
  let current = await snapshot(api);
  const desiredProperties = propertyDefinitions();
  const changes = { properties: [], types: [], views: [], methods: [] };
  for (const key of ["properties", "types"]) {
    const wanted = key === "properties" ? desiredProperties : Object.values(templates);
    for (const item of wanted) {
      const actual = named(current[key], item.name);
      if (!actual) changes[key].push(item.name);
      else if (actual.archived) throw new Error(`已归档的同名配置：${item.name}`);
    }
  }
  for (const { name } of views) if (!named(current.views, name)) changes.views.push(name);
  if (current.properties.filter((row) => !row.archived).length + changes.properties.length > 20) {
    throw new Error("安装后会超过工作区 20 个活动属性的限制。");
  }
  if (current.types.filter((row) => !row.archived).length + changes.types.length > 50) {
    throw new Error("安装后会超过工作区 50 个活动类型的限制。");
  }
  function methodEdits() {
    const { byKey } = getBindings(current.properties);
    return Object.values(templates).filter(({ method }) => method).map((template) => {
      const method = current.methods.find(({ id }) => id === template.method);
      if (!method) throw new Error(`工作区缺少工作方法：${template.method}`);
      const issueType = named(current.types, template.name);
      const steps = [...method.steps];
      let changed = false;
      const next = { ...method, steps };
      const taskOrigin = (key, target, seed) => {
        const type = named(current.types, target.name);
        const property = byKey[seed];
        const desired = type && property ? { issue_type_id: type.id, seed_property_id: property.id } : null;
        if (method[key] && !isDeepStrictEqual(method[key], desired)) throw new Error(`工作方法「${method.name}」已有不同的任务模板绑定`);
        if (!method[key]) {
          next[key] = desired;
          changed = true;
        }
      };
      taskOrigin("plan_task", template, "outcome");
      if (template.reworkTemplate) taskOrigin("rework_task", templates[template.reworkTemplate], "facts");
      for (const [name, signal] of Object.entries(template.gates)) {
        const step = named(steps, name);
        if (!step) throw new Error(`工作方法「${method.name}」缺少步骤「${name}」`);
        const property = byKey[signal];
        const desired = issueType && property ? { issue_type_id: issueType.id, property_id: property.id } : null;
        if (step.table_gate && !isDeepStrictEqual(step.table_gate, desired)) throw new Error(`步骤「${name}」已有不同公式门槛`);
        const formula = formulas[signal];
        const guidance = `使用「${template.name}」任务类型时，在原任务表格填写本步证据再提交。公式「${formula.name}」的条件是：${formula.description}CLI 用 multica issue property list 查看属性，使用 multica issue property set --name 与 --value 填写实际证据或链接。`;
        if (step.table_gate && step.instructions?.includes(guidance)) {
          continue;
        }
        changed = true;
        const instructions = step.instructions?.includes(guidance)
          ? step.instructions
          : [step.instructions, guidance].filter(Boolean).join("\n\n");
        if (Array.from(instructions).length > 4000) throw new Error(`步骤「${name}」的执行指南超过长度限制`);
        steps[steps.indexOf(step)] = { ...step, table_gate: desired, instructions };
      }
      return { ...next, changed };
    }).filter(({ changed }) => changed);
  }
  function verifyExisting() {
    for (const { key, ...desired } of desiredProperties) {
      const actual = named(current.properties, desired.name);
      if (actual) assertCompatible(propertyMatches(actual, desired), desired, "属性");
    }
    const { byKey, templateBody, viewBody } = getBindings(current.properties);
    for (const template of Object.values(templates)) {
      const actual = named(current.types, template.name);
      if (!actual) continue;
      if (!["recipe", ...template.fields].every((key) => byKey[key])) throw new Error(`缺少属性：${template.name}`);
      assertCompatible(actual, templateBody(template), "任务类型");
    }
    for (const view of views) {
      const actual = named(current.views, view.name);
      if (!actual) continue;
      if (!["recipe", ...view.fields].every((key) => byKey[key])) throw new Error(`缺少属性：${view.name}`);
      const desired = viewBody(view);
      // New unrelated workspace columns shouldn't change this template's identity.
      delete desired.display.gridHiddenColumnIds;
      assertCompatible({ ...actual, display: {
        viewMode: actual.display.viewMode, gridColumnOrder: actual.display.gridColumnOrder,
      } }, desired, "共享视图");
    }
  }
  verifyExisting();
  changes.methods = methodEdits().map(({ name }) => name);
  if (apply) {
    for (const { key, ...desired } of desiredProperties) {
      if (!changes.properties.includes(desired.name)) continue;
      await api("POST", paths.properties, desired);
    }
    current = await snapshot(api);
    const { templateBody, viewBody } = getBindings(current.properties);
    for (const template of Object.values(templates)) {
      if (changes.types.includes(template.name)) await api("POST", paths.types, templateBody(template));
    }
    for (const view of views) {
      if (changes.views.includes(view.name)) await api("POST", "/api/issue-views", viewBody(view));
    }
    current = await snapshot(api);
    for (const { changed, ...method } of methodEdits()) {
      await api("PUT", `/api/work-methods/${encodeURIComponent(method.id)}`, { ...method, base_revision: method.revision });
    }
    current = await snapshot(api);
    verifyExisting();
    if (methodEdits().length) throw new Error("未读回预期工作方法门槛。");
    for (const [key, names] of Object.entries(changes)) {
      for (const name of names) if (!named(current[key], name)) throw new Error(`未读回预期配置：${name}`);
    }
  }
  return {
    version: catalog.version,
    changes,
    types: Object.values(templates).map(({ name, method }) => ({
      name, id: named(current.types, name)?.id, method,
      methodRevision: current.methods.find(({ id }) => id === method)?.revision,
    })),
    views: views.map(({ name }) => ({ name, id: named(current.views, name)?.id })),
    formulas: Object.values(formulas).map(({ name }) => ({ name, id: named(current.properties, name)?.id })),
  };
}

async function bindRecipe(api, options) {
  const { issue: issueID, agent: agentID, signal } = options;
  if (!issueID || !agentID || !signal) throw new Error("bind 需要 --issue、--agent 和 --signal。");
  const issuePath = `/api/issues/${encodeURIComponent(issueID)}`;
  const [issue, current, method, wakeups] = await Promise.all([
    api("GET", issuePath), snapshot(api), api("GET", `${issuePath}/work-method`), api("GET", `${issuePath}/wakeups`),
  ]);
  if (!["in_progress", "in_review"].includes(issue.status)) throw new Error("接续只绑定已知正在处理或验收中的任务。");
  const type = current.types.find(({ id, archived }) => id === issue.issue_type_id && !archived);
  const template = Object.values(templates).find(({ name }) => name === type?.name);
  const instruction = template?.handoffs[signal];
  if (!instruction) throw new Error("所选任务类型没有这个公式接续。");
  if (template.method && (method.method?.id !== template.method || method.state === "ready" || method.state === "closed")) {
    throw new Error("请先由有权负责人在原任务启动该模板对应的工作方法。");
  }
  const { byKey, templateBody } = getBindings(current.properties);
  assertCompatible(type, templateBody(template), "任务类型");
  const property = byKey[signal];
  const { key, ...definition } = propertyDefinitions().find(({ key }) => key === signal);
  if (!property || property.archived) throw new Error("接续公式未安装或已归档。");
  assertCompatible(property, definition, "公式");
  const condition = { type: "issue_field", field: "formula", property_id: property.id, value: true };
  const found = wakeups.find((rule) => !rule.disabled_at && rule.mode === "once" && rule.agent_id === agentID &&
    rule.instruction === instruction && isDeepStrictEqual(rule.condition, condition));
  const rule = found ?? await api("POST", `${issuePath}/wakeups`, { agent_id: agentID, kind: "event", mode: "once", instruction, condition });
  return { issue: issue.id, agent: agentID, signal, wakeup: rule.id, reused: Boolean(found) };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const { values, positionals: [command, ...extra] } = parseArgs({
      allowPositionals: true,
      options: Object.fromEntries(["profile", "profiles-dir", "issue", "agent", "signal"].map((key) => [key, { type: "string" }])),
    });
    if (extra.length || !["check", "install", "bind"].includes(command)) {
      throw new Error("用法：node scripts/collaboration-templates.mjs <check|install|bind> --profile NAME。bind 另需 --issue ID --agent ID --signal SIGNAL。");
    }
    const { config, target } = resolveProfile({ profile: values.profile, profilesDir: values["profiles-dir"] }, { root });
    const api = apiClient(config);
    const report = command === "bind" ? await bindRecipe(api, values) : await installCatalog(api, command === "install");
    console.log(JSON.stringify({ target, command, ...report }, null, 2));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
