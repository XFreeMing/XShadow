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
  if (options.offline) {
    if (options.tasks) report.tasks = { status: "unavailable", reason: "离线模式不查询任务；不能据此认为没有运行或没有阻塞。" };
    return report;
  }
  let api;
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
    api = client(config);
    report.live = await readLiveContext(api, template);
  } catch (error) {
    report.live = { status: "unavailable", reason: error.message };
  }
  if (options.tasks) {
    if (!api || report.live.status !== "verified") {
      report.tasks = { status: "unavailable", reason: `实时上下文未核验，未查询任务：${report.live.reason}` };
    } else {
      try { report.tasks = await readTaskDiagnosis(api, report.live); }
      catch (error) {
        // Never surface a raw error message: it can carry the server's response
        // or profile details. Keep only our own summary and an HTTP status.
        const message = String(error?.message ?? "");
        const status = /HTTP (\d{3})/.exec(message)?.[1];
        report.tasks = { status: "unavailable", reason: message.startsWith("任务诊断读取失败") ? message : `任务诊断请求失败${status ? ` (HTTP ${status})` : ""}；原始错误与响应内容未输出，不能据此认为没有运行。` };
      }
    }
  }
  return report;
}

const ACTIVE_RUN_STATUSES = ["running", "dispatched", "queued", "waiting_local_directory", "deferred"];
const WAKEUP_PAGE_SIZE = 100;
// Bound the workspace-wide rule read: 500 rules is far more than one team
// needs, and the page cap keeps a pathological workspace from turning a
// read-only diagnosis into an unbounded crawl. Truncation is reported.
const WAKEUP_PAGE_CAP = 5;
const TERMINAL_CATEGORIES = new Set(["done", "closed", "cancelled"]);

function statusCategory(issue) {
  return issue.status_category || issue.status;
}

async function readWakeupRules(api) {
  const items = [];
  let total = 0;
  for (let page = 0; page < WAKEUP_PAGE_CAP; page++) {
    const result = await api("GET", `/api/issue-wakeups?scope=all&kind=all&limit=${WAKEUP_PAGE_SIZE}&offset=${page * WAKEUP_PAGE_SIZE}`);
    if (!result || !Array.isArray(result.items)) throw new Error("Unexpected wakeup list response");
    items.push(...result.items);
    total = Number.isFinite(result.total) ? result.total : items.length;
    if (result.items.length < WAKEUP_PAGE_SIZE || items.length >= total) return { items, total, truncated: false };
  }
  return { items, total, truncated: items.length < total };
}

// A registered rule is not a continuation by itself: disabled, consumed,
// expired, timed-out, paused and errored rules cannot wake anyone, and an
// enabled event rule only means "registered" — never that its external
// condition already holds. System child-done rules are reported separately
// because they are stage barriers, not a fresh dispatch to the agent.
// Condition-based event wakeups are legal even with an empty event_types
// (validateCondition fills the hints), so the condition itself decides whether
// the registration is usable. Unknown condition shapes stay unverified.
function conditionLabel(condition) {
  if (!condition || typeof condition !== "object" || Array.isArray(condition) || !Object.keys(condition).length) return null;
  switch (condition.type) {
    case "issue_field": return `字段 ${condition.field}=${condition.value}`;
    case "other_issue": return `事项 ${condition.identifier ?? condition.issue_id ?? "未知"} 达到 ${condition.state}`;
    case "children_done": return "子项完成";
    default: return null;
  }
}

function classifyRule(rule, now) {
  const fires = Number(rule.fire_count ?? 0);
  if (rule.last_error) return { state: "errored", detail: "规则错误" };
  if (rule.issue_closed === true) return { state: "ended", detail: "事项已关闭" };
  if (rule.enabled !== true || rule.disabled_at) return { state: "ended", detail: "已停用或已消费" };
  if (rule.expires_at && Date.parse(rule.expires_at) <= now) return { state: "ended", detail: "已过期" };
  if (rule.timed_out_at) return { state: "ended", detail: "已超时" };
  if (rule.max_fires != null && fires >= rule.max_fires) return { state: "ended", detail: "触发次数已用完" };
  if (rule.mode === "once" && fires > 0) return { state: "ended", detail: "一次性规则已触发" };
  if (rule.paused_reason) return { state: "paused", detail: "已暂停" };
  // A system child-done rule is a stage barrier: it only fires after the
  // children finish, so it does not guarantee a fresh wake or dispatch of its
  // own. Report it as a fact, never as a usable continuation.
  if (rule.rule) return { state: "barrier", detail: `${rule.rule}${rule.system_remaining != null ? `(剩余 ${rule.system_remaining})` : ""}` };
  if (rule.kind === "event") {
    const condition = conditionLabel(rule.condition);
    if (condition) return { state: "usable", detail: `已配置条件 ${condition}（外部条件未验证）` };
    if (rule.condition && typeof rule.condition === "object" && Object.keys(rule.condition).length) return { state: "unknown", detail: "条件结构无法验证" };
    const events = Array.isArray(rule.event_types) ? rule.event_types.filter(Boolean) : [];
    if (!events.length) return { state: "unknown", detail: "事件规则缺少事件类型" };
    return { state: "usable", detail: `已配置事件 ${events.join("/")}（外部条件未验证）` };
  }
  if (["at", "every", "cron"].includes(rule.kind)) {
    const next = rule.next_fire_at ? Date.parse(rule.next_fire_at) : NaN;
    if (!Number.isFinite(next)) return { state: "unknown", detail: "定时规则缺少可解析的下次触发时间" };
    if (next <= now) return { state: "overdue", detail: "已到期但未触发" };
    return { state: "usable", detail: `已配置${rule.kind === "at" ? "定时" : "周期"}唤醒 ${rule.next_fire_at}` };
  }
  return { state: "unknown", detail: `未知规则类型 ${String(rule.kind ?? "")}` };
}

const RUN_LABELS = { running: "running", dispatched: "dispatched", queued: "queued", waiting_local_directory: "waiting_local_directory", deferred: "deferred" };
const runSummary = (runs) => ACTIVE_RUN_STATUSES.filter((s) => runs.some((r) => r.status === s)).map((s) => `${RUN_LABELS[s]} ${runs.filter((r) => r.status === s).length}`).join(" / ") || "无";

function ownerName(issue, live) {
  if (!issue.assignee_type || !issue.assignee_id) return "未分派";
  if (issue.assignee_type === "agent") return live.agents.find((a) => a.id === issue.assignee_id)?.name ?? `智能体 ${issue.assignee_id.slice(0, 8)}`;
  if (issue.assignee_type === "squad") return `小队 ${live.squads.find((s) => s.id === issue.assignee_id)?.name ?? issue.assignee_id.slice(0, 8)}`;
  return `成员 ${issue.assignee_id.slice(0, 8)}（名称未核验）`;
}

const TASK_CATEGORIES = {
  no_continuation: "需检查交接",
  continuation_unverified: "接续未核验",
  waiting_children: "等待子项",
  blocked: "显式阻塞",
  backlog_unassigned: "backlog 未分派",
  backlog_assigned: "backlog 已指派",
  awaiting_review: "等待验收",
  waiting_continuation: "已配置接续",
  active_run: "执行中",
};
const TASK_CATEGORY_RANK = Object.keys(TASK_CATEGORIES);

const CHILD_LABELS = {
  run: (runs) => `运行 ${runSummary(runs)}`,
  continuation: () => "已配置接续",
  blocked: () => "显式阻塞，不自行推进",
  backlog: () => "backlog，不自行推进",
  review: () => "等待验收，不自行推进",
  barrier: () => "仅有阶段屏障，不自行推进",
  unverified: () => "接续未核验，不能判定是否会推进",
  none: () => "无活动运行且无有效接续",
};

function childFact(child, context) {
  const runs = context.runsByIssue.get(child.id) ?? [];
  const classified = (context.rulesByIssue.get(child.id) ?? []).map((r) => classifyRule(r, context.now));
  const usable = classified.filter((r) => r.state === "usable");
  const unknown = classified.filter((r) => r.state === "unknown");
  const category = statusCategory(child);
  let kind;
  if (runs.length) kind = "run";
  else if (usable.length) kind = "continuation";
  else if (category === "blocked") kind = "blocked";
  else if (category === "backlog") kind = "backlog";
  else if (category === "in_review") kind = "review";
  else if (context.truncated || unknown.length) kind = "unverified";
  else if (classified.some((r) => r.state === "barrier")) kind = "barrier";
  else kind = "none";
  return {
    identifier: child.identifier, status: child.status, category: kind,
    // A child in backlog, in_review or blocked is an explicit state, not proof
    // that it will advance; only a run or a usable rule counts as progressing.
    // "unverified" is unknown, not a confirmed stall.
    progressing: kind === "run" || kind === "continuation",
    unknown: kind === "unverified",
    fact: `${child.identifier}(${CHILD_LABELS[kind](runs)})`,
  };
}

// Unknown or unread rules must not turn into a "no continuation" verdict:
// say what the read covered and leave the rest unverified. System barriers are
// listed separately because they cannot wake the issue on their own.
function ruleFacts(rules, truncated) {
  const usable = rules.filter((r) => r.state === "usable");
  const barriers = rules.filter((r) => r.state === "barrier");
  const unknown = rules.filter((r) => r.state === "unknown");
  const groups = new Map();
  for (const rule of rules) {
    if (rule.state === "usable" || rule.state === "barrier") continue;
    groups.set(rule.detail, (groups.get(rule.detail) ?? 0) + 1);
  }
  const parts = [];
  if (usable.length) parts.push(`可用接续 ${usable.map((r) => r.detail).join("、")}`);
  else if (unknown.length) parts.push("已读范围未发现可用接续，部分规则无法验证，接续未核验");
  else if (truncated) parts.push("已读范围未发现可用接续，规则未完整读取，接续未核验");
  else parts.push("无可用接续");
  for (const rule of barriers) parts.push(`阶段屏障 ${rule.detail}（仅随子项完成，不保证唤醒执行）`);
  for (const [label, count] of groups) parts.push(`${count} 条${label}`);
  if (truncated && rules.length) parts.push("未读到规则的事项不代表没有接续");
  return { text: parts.join("；"), usable: usable.length, unknown: unknown.length };
}

// Errors never carry the raw response or error message into the report: a
// server body or a CLI profile could contain more than this diagnostic is
// allowed to print. Only the source and an HTTP status survive.
async function safeRead(label, read) {
  try { return { label, value: await read() }; }
  catch (error) {
    const status = /HTTP (\d{3})/.exec(String(error?.message ?? ""))?.[1];
    return { label, reason: status ? `HTTP ${status}` : "请求失败" };
  }
}

export async function readTaskDiagnosis(api, live, { now = Date.now() } = {}) {
  // open_only=true returns every non-terminal issue without pagination; the
  // snapshot is one workspace-wide read of active runs; rules are paginated.
  // That keeps this diagnosis at a bounded number of requests instead of one
  // per issue, and every request is a GET.
  const [open, snapshot, wakeups] = await Promise.all([
    safeRead("未完成事项", () => api("GET", "/api/issues?open_only=true")),
    safeRead("活动运行", () => api("GET", "/api/agent-task-snapshot")),
    safeRead("唤醒规则", () => readWakeupRules(api)),
  ]);
  const failures = [open, snapshot, wakeups].filter((s) => s.reason);
  if (failures.length) throw new Error(`任务诊断读取失败：${failures.map((f) => `${f.label} ${f.reason}`).join("、")}；原始错误与响应内容未输出，不能据此认为没有运行或没有接续。`);
  if (!open.value || !Array.isArray(open.value.issues)) throw new Error("任务诊断读取失败：未完成事项响应结构不符；原始响应未输出。");
  if (!Array.isArray(snapshot.value)) throw new Error("任务诊断读取失败：活动运行响应结构不符；原始响应未输出。");

  const issues = open.value.issues.filter((i) => !TERMINAL_CATEGORIES.has(statusCategory(i)));
  const runsByIssue = new Map();
  for (const task of snapshot.value) {
    if (!ACTIVE_RUN_STATUSES.includes(task.status) || !task.issue_id) continue;
    if (!runsByIssue.has(task.issue_id)) runsByIssue.set(task.issue_id, []);
    runsByIssue.get(task.issue_id).push(task);
  }
  const { items: rules, total: ruleTotal, truncated } = wakeups.value;
  const rulesByIssue = new Map();
  for (const rule of rules) {
    if (!rule.issue_id) continue;
    if (!rulesByIssue.has(rule.issue_id)) rulesByIssue.set(rule.issue_id, []);
    rulesByIssue.get(rule.issue_id).push(rule);
  }
  const childrenByParent = new Map();
  for (const issue of issues) {
    if (!issue.parent_issue_id) continue;
    if (!childrenByParent.has(issue.parent_issue_id)) childrenByParent.set(issue.parent_issue_id, []);
    childrenByParent.get(issue.parent_issue_id).push(issue);
  }
  const context = { runsByIssue, rulesByIssue, childrenByParent, now, truncated };
  const activeRuns = [];

  const rows = issues.map((issue) => {
    const runs = runsByIssue.get(issue.id) ?? [];
    activeRuns.push(...runs);
    const classifiedRules = (rulesByIssue.get(issue.id) ?? []).map((r) => classifyRule(r, now));
    const usableRules = classifiedRules.filter((r) => r.state === "usable");
    const unknownRules = classifiedRules.filter((r) => r.state === "unknown");
    const ruleFactsText = ruleFacts(classifiedRules, truncated).text;
    const children = (childrenByParent.get(issue.id) ?? []).map((child) => childFact(child, context));
    const category = statusCategory(issue);
    let kind;
    if (runs.length) kind = "active_run";
    else if (category === "blocked") kind = "blocked";
    else if (category === "backlog") kind = issue.assignee_id ? "backlog_assigned" : "backlog_unassigned";
    else if (children.length) kind = "waiting_children";
    else if (category === "in_review") kind = "awaiting_review";
    else if (usableRules.length) kind = "waiting_continuation";
    // An unknown rule or a truncated rule read does not prove there is no
    // continuation, so the issue cannot be called a broken handoff.
    else if (unknownRules.length || truncated) kind = "continuation_unverified";
    else kind = "no_continuation";

    const facts = [];
    if (runs.length) facts.push(`活动运行 ${runSummary(runs)}`);
    if (children.length) {
      const stalled = children.filter((c) => !c.progressing && !c.unknown);
      const unverified = children.filter((c) => c.unknown);
      const childParts = [`等待 ${children.length} 个子项：${children.slice(0, 5).map((c) => c.fact).join("、")}${children.length > 5 ? ` 等 ${children.length} 项` : ""}`];
      if (stalled.length) childParts.push(`其中 ${stalled.length} 项不会自行推进`);
      if (unverified.length) childParts.push(`${unverified.length} 项接续未核验，不能判定是否会推进`);
      facts.push(childParts.join("；"));
      // A child list is a fact, not proof that the parent is genuinely waiting
      // on them: say so when the parent has no continuation of its own.
      if (!usableRules.length) facts.push(truncated || unknownRules.length ? "父项自身接续未核验，需核对是否真在等待" : "父项自身无有效接续，需核对是否真在等待");
    }
    facts.push(ruleFactsText);
    if (!runs.length) facts.push("无活动运行");
    return {
      identifier: issue.identifier, title: issue.title, id: issue.id,
      status: issue.status, status_category: issue.status_category ?? null,
      owner: ownerName(issue, live),
      category: kind, category_label: TASK_CATEGORIES[kind],
      run_counts: Object.fromEntries(ACTIVE_RUN_STATUSES.map((s) => [s, runs.filter((r) => r.status === s).length])),
      children,
      facts,
    };
  }).sort((a, b) => TASK_CATEGORY_RANK.indexOf(a.category) - TASK_CATEGORY_RANK.indexOf(b.category) || a.identifier.localeCompare(b.identifier));

  const summary = Object.fromEntries(Object.keys(TASK_CATEGORIES).map((key) => [key, rows.filter((r) => r.category === key).length]));
  return {
    status: "verified",
    observed_at: new Date().toISOString(),
    counts: {
      issues: rows.length, active_runs: activeRuns.length,
      wakeup_rules: rules.length, wakeup_total: ruleTotal, wakeup_truncated: truncated,
    },
    summary,
    issues: rows,
  };
}

function formatTasks(tasks, profile) {
  const rows = ["", "## 任务诊断"];
  if (tasks.status !== "verified") {
    rows.push("数据状态：未核验", `原因：${cell(tasks.reason)}`, "活动运行与接续均未核验；不能据此认为没有运行，也不能认定存在阻塞或缺少接续。");
    if (profile) rows.push(`重试入口：make agent context ARGS="--profile ${cell(profile)} --tasks"`);
    return rows;
  }
  rows.push(`数据状态：已读取 ${tasks.counts.issues} 个未完成事项、${tasks.counts.active_runs} 个活动运行、${tasks.counts.wakeup_rules} 条唤醒规则${tasks.counts.wakeup_truncated ? `（共 ${tasks.counts.wakeup_total} 条，仅核验前 ${tasks.counts.wakeup_rules} 条）` : ""}。`);
  rows.push("业务状态不等于活动 run：in_progress 可能无人运行，blocked 与 backlog 也不会自行启动。事件规则只表示已配置，不能证明外部条件或授权已满足；本报告只读，不代表平台会自动推进。");
  rows.push("", "| 事项 | 状态 | 负责人 | 活动运行 | 分类 | 接续事实 |", "| --- | --- | --- | --- | --- | --- |");
  for (const row of tasks.issues.slice(0, 60)) {
    rows.push(`| ${cell(row.identifier)} | ${cell(row.status)} | ${cell(row.owner)} | ${cell(ACTIVE_RUN_STATUSES.filter((s) => row.run_counts[s]).map((s) => `${s} ${row.run_counts[s]}`).join(" / ") || "无")} | ${cell(row.category_label)} | ${cell(row.facts.join("；"))} |`);
  }
  if (tasks.issues.length > 60) rows.push(`| … | | | | | 另有 ${tasks.issues.length - 60} 个事项未展开，使用 --json 查看全部 |`);
  const summary = Object.entries(TASK_CATEGORIES).map(([key, label]) => `${label} ${tasks.summary[key]}`).join("、");
  const stalled = tasks.issues.filter((i) => i.category === "waiting_children" && i.children.some((c) => !c.progressing && !c.unknown)).length;
  const unverified = tasks.issues.filter((i) => i.category === "waiting_children" && i.children.some((c) => c.unknown)).length;
  const notes = [stalled ? `${stalled} 项含不会自行推进的子项` : "", unverified ? `${unverified} 项含接续未核验的子项` : ""].filter(Boolean);
  rows.push("", `汇总：${summary}${notes.length ? `（等待子项中 ${notes.join("，")}）` : ""}。`);
  rows.push(`检查入口：make agent context ARGS="--profile ${cell(profile ?? "NAME")} --tasks"；单事项可用于核对的识别号见上表，详情用 multica issue get <识别号> --output json。`);
  return rows;
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
  rows.push("", "变更前读取 agent-team/CONTEXT.md。修改具体内容时读取对应角色/技能/小队的完整配置；本摘要不含凭据或完整实时提示词。不要通过重新初始化覆盖未核对的差异。");
  if (report.tasks) rows.push(...formatTasks(report.tasks, report.target?.profile));
  rows.push("");
  return rows.join("\n");
}
