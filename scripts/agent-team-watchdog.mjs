#!/usr/bin/env node
// Event-driven handoff watchdog for the personal agent team.
//
// An issue can sit at in_progress with no run that will advance it and no
// registered continuation. The platform starts nothing for that state, so the
// project manager never learns it is needed. This daemon listens to the
// workspace websocket, re-reads the existing read-only diagnosis on connect and
// on change, and dispatches exactly one ordinary project-manager run per real
// unattended cycle. It never writes the issue's status or assignee.
//
// The dispatch is a native one-shot condition wakeup, so the platform owns the
// run lifecycle and the rule plus its runs are evidence the manager ran. A local
// state file deduplicates across events and restarts; when that file is lost the
// guard's own instruction marker on the issue's wakeup rules is the fallback.
import { randomUUID } from "node:crypto";
import { linkSync, mkdirSync, readFileSync, realpathSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { apiClient } from "./agent-team.mjs";
import { readTaskDiagnosis, resolveProfile } from "./agent-team-context.mjs";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const SOURCE = "agent-team-watchdog";
const STATE_VERSION = 2;
const ACTIVE_RUN_STATUSES = new Set(["queued", "dispatched", "running", "waiting_local_directory", "deferred"]);
const TERMINAL_RUN_STATUSES = new Set(["completed", "failed", "cancelled"]);
const OPEN_PHASES = new Set(["dispatching", "dispatched", "failed"]);
const WATCHED_EVENTS = new Set([
  "issue:created", "issue:updated", "issue:deleted", "issue_status:changed",
  "issue_labels:changed", "issue_properties:changed", "issue_workflow:changed",
  "comment:created", "comment:updated", "comment:deleted",
  "task:queued", "task:coalesced", "task:dispatch", "task:running",
  "task:waiting_local_directory", "task:completed", "task:failed", "task:cancelled",
  "agent:status", "daemon:register", "pull_request:linked", "pull_request:updated", "pull_request:unlinked",
]);
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const IDENTIFIER_RE = /^[A-Za-z][A-Za-z0-9]*-\d+$/;
const DEFAULT_RESCAN_MS = 60000;
const RETRY_MS = 15000;

const HELP = `Usage: node scripts/agent-team-watchdog.mjs <run|scan|status|unit> [options]

run      Follow the workspace websocket and dispatch the project manager for a
         confirmed in_progress gap. Startup and every reconnect run a full
         read-only scan; a slow recovery tick also rescans when no event
         arrives. One writer service per host per workspace: the mutex lives in
         a fixed machine-local place keyed by workspace, independent of
         --state-dir, so two state directories cannot make two writers. Do not
         install this unit on two machines for the same workspace.
scan     One scan with the same rules, then exit. --dry-run plans without writing.
status   Print the local dispatch records and unresolved anomalies. Read-only.
unit     Print (or write with --output) a systemd user unit for run.

Options:
  --profile NAME        Authenticated user profile; never inherits MULTICA_TOKEN.
                        The profile must be able to create wakeups (a member session).
  --profiles-dir PATH   Profile root (default ~/.multica/profiles)
  --issue ID            Limit the watchdog to one issue (UUID or identifier).
                        Without it the whole workspace is watched.
  --pm-agent NAME|UUID  Project-manager agent to dispatch (default 项目经理).
  --state-dir PATH      Durable state directory; the writer lock does not move
                        with it (default $XDG_STATE_HOME/multica-agent-team-watchdog/<workspace>)
  --max-dispatches N    Dispatch attempts per issue per window (default 3)
  --window-hours N      Attempt window (default 24)
  --debounce-ms N       Event coalescing delay (default 750; never postponed)
  --rescan-ms N         Recovery rescan interval (default 60000; 0 disables)
  --reconnect-ms N      Base reconnect delay (default 1000, capped at 30000)
  --json                Machine-readable scan/status output
  --dry-run             scan: read and decide, write nothing
  --node PATH           unit: node executable to pin (default this process)
  --root PATH           unit: verified checkout to pin (default this script's)
  --output FILE         unit: write the unit file instead of stdout
  --help                Show this help`;

export function parseWatchdogArgs(argv) {
  const options = {
    command: argv.shift(), maxDispatches: 3, windowHours: 24,
    debounceMs: 750, rescanMs: DEFAULT_RESCAN_MS, reconnectMs: 1000,
  };
  if (!["run", "scan", "status", "unit"].includes(options.command)) throw new Error(HELP);
  const valued = {
    "--profile": "profile", "--profiles-dir": "profilesDir", "--issue": "issue",
    "--pm-agent": "pmAgent", "--state-dir": "stateDir", "--max-dispatches": "maxDispatches",
    "--window-hours": "windowHours", "--debounce-ms": "debounceMs", "--rescan-ms": "rescanMs",
    "--reconnect-ms": "reconnectMs", "--node": "node", "--root": "root", "--output": "output",
  };
  while (argv.length) {
    const flag = argv.shift();
    if (flag === "--help") { options.help = true; continue; }
    if (flag === "--json") { options.json = true; continue; }
    if (flag === "--dry-run") { options.dryRun = true; continue; }
    const key = valued[flag];
    if (!key || !argv[0] || argv[0].startsWith("--")) throw new Error(`Invalid option: ${flag}\n${HELP}`);
    options[key] = argv.shift();
  }
  if (options.profile && !/^[\w.-]+$/.test(options.profile)) throw new Error("Invalid profile name");
  if (options.issue && !UUID_RE.test(options.issue) && !IDENTIFIER_RE.test(options.issue)) throw new Error("--issue takes an issue UUID or identifier such as DEV-83");
  for (const key of ["maxDispatches", "windowHours", "debounceMs", "rescanMs", "reconnectMs"]) {
    options[key] = Number(options[key]);
    if (!Number.isFinite(options[key]) || options[key] < 0) throw new Error(`Invalid --${key.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)}`);
  }
  if (options.maxDispatches < 1) throw new Error("--max-dispatches must be at least 1");
  if (options.command !== "scan" && options.dryRun) throw new Error("--dry-run applies only to scan");
  if (options.command === "status" && (options.issue || options.pmAgent)) throw new Error("status reads only the local state; run a scan to query the workspace");
  return options;
}

const iso = (at = Date.now()) => new Date(at).toISOString();
const isUuid = (value) => typeof value === "string" && UUID_RE.test(value);
const text = (value) => (typeof value === "string" ? value : "");

// A cycle is one real unattended period on one issue, not one observation.
// It ends only on evidence outside the guard: the status or assignee moves, an
// external run or wait appears, a human replies, or the issue closes. The
// manager's own comment, its no_action, and the guard's own rule and run all
// belong to the same cycle, so they must never reopen it.
export function cycleKeyFor(issueId, cycle) {
  return `${issueId}#${cycle}`;
}

export function parseCycleKey(value, issueId) {
  const match = /#(\d+)\s*$/.exec(text(value));
  if (!match) return null;
  return { issueId, cycle: Number(match[1]), key: cycleKeyFor(issueId, Number(match[1])) };
}

// Only an in_progress issue that no run can advance, that has no usable
// continuation, and that is not assigned to a human is a watchdog gap. The
// diagnosis already treats queued/waited runs, child waits, review, parked and
// unknown/unreadable continuation as non-gaps; this is the narrower contract.
export function isGap(issue) {
  if (issue.category !== "no_continuation") return false;
  if (!(issue.status === "in_progress" || issue.status_category === "in_progress")) return false;
  if (issue.assignee_type === "member") return false;
  return true;
}

export function matchesIssueFilter(issue, filter) {
  if (!filter) return true;
  return issue.id === filter || issue.identifier === filter;
}

function sanitize(error) {
  return String(error?.message ?? error ?? "").replace(/[\r\n]+/g, " ").slice(0, 300);
}

export function defaultStateDir(workspaceId, env = process.env) {
  const base = env.XDG_STATE_HOME || join(homedir(), ".local/state");
  return join(base, "multica-agent-team-watchdog", workspaceId);
}

// The writer mutex must not move with --state-dir: two processes started with
// different state directories still write the same workspace and must collide.
// One machine-local directory per user, keyed by workspace only.
export function defaultLockDir(env = process.env) {
  const base = env.XDG_STATE_HOME || join(homedir(), ".local/state");
  return join(base, "multica-agent-team-watchdog", "locks");
}

export function lockPath(lockDir, workspaceId) {
  return join(lockDir, `${workspaceId}.lock`);
}

export class StateStore {
  constructor(dir) { this.dir = dir; this.path = join(dir, "state.json"); }
  load(workspaceId) {
    const empty = { version: STATE_VERSION, workspace_id: workspaceId, updated_at: null, scans: {}, issues: {} };
    let raw;
    try { raw = readFileSync(this.path, "utf8"); }
    catch (error) {
      if (error.code === "ENOENT") return empty;
      throw error;
    }
    let state;
    try { state = JSON.parse(raw); }
    catch { throw new Error(`State file ${this.path} is not valid JSON; move it aside and rescan.`); }
    if (!state || state.version !== STATE_VERSION || !state.issues) throw new Error(`State file ${this.path} has an unsupported shape.`);
    if (state.workspace_id !== workspaceId) throw new Error(`State file ${this.path} belongs to workspace ${state.workspace_id}, not ${workspaceId}; use a separate --state-dir.`);
    state.scans ??= {};
    return state;
  }
  save(state) {
    mkdirSync(this.dir, { recursive: true });
    state.updated_at = iso();
    const tmp = `${this.path}.${process.pid}.tmp`;
    writeFileSync(tmp, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
    renameSync(tmp, this.path);
  }
}

// The lock path is derived from the workspace only, so two processes started
// with different --state-dir values still meet on the same file. The content is
// written to a temporary file and published with link(), which is an atomic
// exclusive create: the lock never exists half-written, and a corrupt file is
// never treated as ownerless — deleting it could hand the workspace to a second
// writer while the first is still alive.
export function acquireLock(lockDir, { workspaceId, profile = "", service = SOURCE, pid = process.pid, isAlive = alive, now = Date.now() } = {}) {
  mkdirSync(lockDir, { recursive: true });
  const path = lockPath(lockDir, workspaceId);
  const token = randomUUID();
  const body = JSON.stringify({ service, profile, pid, token, workspace_id: workspaceId, started_at: iso(now) });
  const tmp = join(lockDir, `.${workspaceId}.${pid}.${token}.tmp`);
  for (let attempt = 0; attempt < 3; attempt++) {
    writeFileSync(tmp, body, { mode: 0o600 });
    try {
      linkSync(tmp, path);
      rmSync(tmp, { force: true });
      return {
        path, token,
        release() {
          try {
            const held = JSON.parse(readFileSync(path, "utf8"));
            if (held.token === token) rmSync(path, { force: true });
          } catch { /* already gone or replaced */ }
        },
      };
    } catch (error) {
      rmSync(tmp, { force: true });
      if (error.code !== "EEXIST") throw error;
    }
    let raw;
    try { raw = readFileSync(path, "utf8"); }
    catch (error) {
      // Released between our link attempt and this read; try the link again.
      if (error.code === "ENOENT") continue;
      throw error;
    }
    let held;
    try { held = JSON.parse(raw); } catch { held = null; }
    if (!held || !Number.isInteger(held.pid) || held.workspace_id !== workspaceId) {
      throw new Error(`Lock file ${path} is corrupt or incomplete; refusing to assume it is free. Inspect the running watchdog and remove the file manually only if none is running.`);
    }
    if (isAlive(held.pid)) {
      throw new Error(`Another watchdog (pid ${held.pid}${held.profile ? `, profile ${held.profile}` : ""}) is already writing workspace ${workspaceId}; one writer per host is allowed.`);
    }
    // Only a structurally valid lock whose recorded process is gone is stale.
    try { rmSync(path); } catch (error) { if (error.code !== "ENOENT") throw error; }
  }
  throw new Error(`Could not acquire the writer lock ${path}; another process kept winning it.`);
}

function alive(pid) {
  try { process.kill(pid, 0); return true; }
  catch (error) { return error.code === "EPERM"; }
}

export function renderInstruction(issue, cycleKey, { source = SOURCE } = {}) {
  const id = issue.identifier;
  return [
    "这是军团接续守护自动触发的项目经理接单，不是新的用户需求。",
    `触发事实：${id} 处于 ${issue.status}，没有任何可推进的运行，也没有已登记的接续规则，且当前负责人不是正在办理的人类成员。`,
    `缺口键：${cycleKey}`,
    `来源：${source}（同一无人推进周期只派发一次；本次运行自身的评论与 no_action 不算交接，不要为它再次派发）。`,
    "",
    "请先核实现场，再完成一次真实交接：",
    `- multica issue get ${id}、multica issue comment list ${id} --roots-only --summary`,
    `- multica issue runs ${id} --active、multica issue wakeup list ${id} --output json`,
    "",
    "按 rd-project-management 判断并执行：",
    "1. 团队能处理：把下一步实际派给有权限的执行方（指派或有效 mention），保留原负责人和状态；随后为这次运行登记事件唤醒，运行结束的真实终态会唤醒你恢复本事项：",
    `   multica issue wakeup create ${id} --event task.completed,task.failed,task.cancelled --task-id <本次 run> --mode once --agent-id <你的 id> --instruction-file <文件>`,
    "   默认不设到期，运行多久都在终态唤回；只有这次派发确有约定的实际期限时，才加 --expires-in <期限> --on-timeout wake，并在指令里要求到期核实现状，不能把到期当成已解决。",
    `2. 只有指定人能提供输入：先在 multica issue list --assignee <接收人> --output json 中查重，若已有未关闭、正文指向 ${id} 且是同一输入请求的待办，直接复用该待办及其仍然有效的恢复条件；缺口键变化不再新建第二条。没有才创建一条可见待办并直接指派给有证据的真实接收人；正文写清来源 ${id} 与本缺口键；创建后核对 assignee 确实是该人，再在 ${id} 登记恢复条件（二选一）：`,
    `   multica issue wakeup create ${id} --until-issue <待办> --until-issue-state done --mode once --agent-id <你的 id> --instruction-file <文件>`,
    `   multica issue wakeup create ${id} --event comment.created --filter-actor-type member --filter-actor-id <接收人 user id> --mode once --agent-id <你的 id> --instruction-file <文件>`,
    "   这两种等待默认都不设到期：用户晚几天才完成待办或才回复，仍会唤回本事项。只有与该接收人约定了实际期限时，才加 --expires-in <期限> --on-timeout wake，并要求到期核实现状。",
    "3. 已经解决或确认无需动作：直接恢复推进，或留下 no_action 的具体依据；不要伪造完成，也不要为同一输入请求重复创建待办。",
    `登记后用 multica issue wakeup list ${id} --output json 核对规则的 agent_id、条件与状态；已触发的规则以规则 id 及其运行为证据，不要重复创建同等规则。`,
    "",
    `同一缺口键 ${cycleKey} 只处理一次。没有有效依据时，留下可检查的异常说明，不要反复自唤醒。`,
  ].join("\n");
}

export function isWatchdogInstruction(instruction) {
  const body = text(instruction);
  return body.includes(`来源：${SOURCE}`) && body.includes("缺口键：");
}

export function ruleCycleKey(instruction, issueId) {
  const match = /缺口键：([^\s（(]+)/.exec(text(instruction));
  if (!match) return null;
  return match[1].startsWith(`${issueId}#`) ? match[1] : null;
}

export function findProjectManager(agents, spec) {
  const active = agents.filter((a) => !a.archived_at && a.status !== "archived");
  const matches = isUuid(spec) ? active.filter((a) => a.id === spec) : active.filter((a) => a.name === spec);
  if (matches.length === 1) return matches[0];
  if (matches.length === 0) throw new Error(`No active agent named ${spec}; resolve the project manager's name or UUID first.`);
  throw new Error(`More than one active agent matches ${spec}; pass the project manager's UUID.`);
}

async function readIssueWakeups(api, issueId) {
  const rows = await api("GET", `/api/issues/${issueId}/wakeups`);
  if (!Array.isArray(rows)) throw new Error("Unexpected issue wakeup response");
  return rows;
}

async function readWakeupRuns(api, issueId, wakeupId) {
  const rows = await api("GET", `/api/issues/${issueId}/wakeups/${wakeupId}/runs`);
  if (!Array.isArray(rows)) throw new Error("Unexpected wakeup run response");
  return rows;
}

function conditionTargetsStatus(condition, status) {
  if (!condition || typeof condition !== "object") return false;
  const value = typeof condition.value === "string" ? condition.value : condition.value?.value;
  return condition.type === "issue_field" && condition.field === "status" && value === status;
}

// Registering is not dispatching: the readback proves the rule exists, targets
// the manager and still carries this cycle's marker. The platform may have
// consumed the one-shot rule before the readback (a condition that already
// holds fires on the next tick), so a disabled rule with our agent and
// instruction — or evidence of its run — is a successful dispatch, not a
// failure to retry.
async function registerDispatch(api, issue, cycleKey, pmAgent) {
  const instruction = renderInstruction(issue, cycleKey);
  const created = await api("POST", `/api/issues/${issue.id}/wakeups`, {
    agent_id: pmAgent.id,
    instruction,
    kind: "event",
    mode: "once",
    condition: { type: "issue_field", field: "status", value: issue.status },
    // A condition that already holds fires on the next scheduler tick. The
    // deadline bounds the case where it never fires, so the cycle can be
    // reconciled instead of waiting behind a permanently pending rule.
    expires_in_seconds: 900,
  });
  if (!created?.id) throw new Error("Wakeup registration returned no rule id");
  if (created.agent_id !== pmAgent.id) throw new Error("Wakeup registration did not target the project manager");
  if (!conditionTargetsStatus(created.condition, issue.status)) throw new Error("Wakeup registration did not target the issue status");
  if (text(created.instruction) !== instruction) throw new Error("Wakeup registration did not keep the guard instruction");

  const saved = (await readIssueWakeups(api, issue.id)).find((r) => r.id === created.id);
  if (saved) {
    if (saved.agent_id !== pmAgent.id) throw new Error("Wakeup readback does not target the project manager");
    if (ruleCycleKey(saved.instruction, issue.id) !== cycleKey) throw new Error("Wakeup readback does not carry this cycle's marker");
    return { id: saved.id, fired: saved.enabled !== true || Number(saved.fire_count ?? 0) > 0 };
  }
  // Not in the list: before calling it lost, accept run evidence for the id we
  // were handed. A rule consumed and deleted between the two reads still ran.
  const runs = await readWakeupRuns(api, issue.id, created.id);
  if (runs.length) return { id: created.id, fired: true };
  throw new Error("Wakeup registration could not be confirmed by rule or run readback");
}

// Reconstruct a record from the platform when the local state is missing.
// Only instructions written by this guard count; a historical ordinary manager
// run is not a guard dispatch and must not suppress the first real one.
async function adoptWatchdogRule(api, issue) {
  let rows;
  try { rows = await readIssueWakeups(api, issue.id); }
  catch (error) { return { error: sanitize(error) }; }
  const candidates = rows
    .filter((r) => isWatchdogInstruction(r.instruction) && ruleCycleKey(r.instruction, issue.id))
    .sort((a, b) => Date.parse(b.created_at ?? 0) - Date.parse(a.created_at ?? 0));
  const rule = candidates[0];
  if (!rule) return { record: null };
  const key = ruleCycleKey(rule.instruction, issue.id);
  let runs = [];
  try { runs = await readWakeupRuns(api, issue.id, rule.id); }
  catch { return { error: "接续规则存在，但运行记录暂时无法核验" }; }
  return {
    record: {
      cycle_key: key,
      phase: runs.length || rule.enabled === true ? "dispatched" : "failed",
      wakeup_id: rule.id,
      dispatched_at: rule.created_at ?? iso(),
      attempts: [rule.created_at ?? iso()],
      adopted: true,
    },
  };
}

async function memberCommentSince(api, issueId, since) {
  const rows = await api("GET", `/api/issues/${issueId}/comments?since=${encodeURIComponent(since)}`);
  if (!Array.isArray(rows)) throw new Error("Unexpected comment response");
  return rows.find((c) => c.author_type === "member" && !c.deleted_at && Date.parse(c.created_at) > Date.parse(since)) ?? null;
}

async function externalRunSince(api, issueId, since, ours) {
  const rows = await api("GET", `/api/issues/${issueId}/task-runs`);
  if (!Array.isArray(rows)) throw new Error("Unexpected task run response");
  return rows.find((r) => !ours.has(r.id) && Date.parse(r.created_at) > Date.parse(since)) ?? null;
}

// Evidence that something outside this guard moved the issue after `since`.
// A member reply is authoritative; an outside run means real work happened.
// Reading either is best-effort: a failed read is "unverified", never progress.
async function externalProgress(api, issue, since, ours, { allowActivity = false, closedAt = null } = {}) {
  if (since) {
    try {
      const comment = await memberCommentSince(api, issue.id, since);
      if (comment) return { kind: "member_comment", at: comment.created_at, id: comment.id };
    } catch { /* fall through to the other signals */ }
    try {
      const run = await externalRunSince(api, issue.id, since, ours);
      if (run) return { kind: "external_run", at: run.created_at, id: run.id };
    } catch { /* fall through */ }
  }
  // `closed_at` is written by a later scan, so activity after it cannot be the
  // manager's own comment; it is a fallback when comments are unreadable.
  if (allowActivity && closedAt && issue.last_activity_at && Date.parse(issue.last_activity_at) > Date.parse(closedAt)) {
    return { kind: "activity" };
  }
  return null;
}

function lastRecord(state, issueId) {
  const records = state.issues?.[issueId]?.records ?? [];
  return records.length ? records[records.length - 1] : null;
}

function openRecord(state, issueId) {
  const record = lastRecord(state, issueId);
  return record && OPEN_PHASES.has(record.phase) ? record : null;
}

function attemptsInWindow(entry, now, windowMs) {
  return (entry?.attempts ?? []).filter((at) => now - Date.parse(at) <= windowMs);
}

function rememberRecord(state, issue, record) {
  state.issues ??= {};
  const entry = (state.issues[issue.id] ??= { identifier: issue.identifier, cycles: 0, attempts: [], records: [] });
  entry.identifier = issue.identifier;
  const index = entry.records.findIndex((r) => r.cycle_key === record.cycle_key);
  if (index === -1) entry.records.push(record);
  else entry.records[index] = record;
  // Keep the last rounds visible without letting the state file grow forever.
  if (entry.records.length > 20) entry.records.splice(0, entry.records.length - 20);
  return record;
}

function closeRecord(record, phase, reason, now) {
  record.phase = phase;
  record.closed_at = iso(now);
  record.closed_reason = reason;
  if (phase === "anomaly") record.anomaly = { reason, at: iso(now) };
  return record;
}

function noteAttempt(entry, now) {
  entry.attempts = [...(entry.attempts ?? []), iso(now)].slice(-50);
}

export async function scanOnce({
  api, live, store, state, pmAgent, now = Date.now(), issueFilter = null,
  dryRun = false, maxDispatches = 3, windowMs = 24 * 3600 * 1000, log = () => {},
}) {
  state.scans ??= {};
  state.scans.started_at = iso(now);
  state.scans.count = (state.scans.count ?? 0) + 1;
  let diagnosis;
  try {
    diagnosis = await readTaskDiagnosis(api, live, { now });
  } catch (error) {
    // An unconfirmed interface is never "no gap": skip the whole pass and
    // recover on the next event, reconnect or recovery tick.
    state.scans.last_error = sanitize(error);
    state.scans.last_completed_at = iso(now);
    if (!dryRun) store.save(state);
    return { status: "skipped", reason: state.scans.last_error, decisions: [] };
  }
  state.scans.last_error = null;
  state.scans.truncated = Boolean(diagnosis.counts?.wakeup_truncated);
  const decisions = [];
  const seen = new Set();
  const dispatch = [];
  const decide = (issue, action, reason, extra = {}) => {
    decisions.push({ issue: issue.identifier, action, reason, ...extra });
    if (action === "dispatch") dispatch.push({ issue, reason, extra });
  };

  for (const issue of diagnosis.issues) {
    seen.add(issue.id);
    if (!matchesIssueFilter(issue, issueFilter)) continue;
    const entry = state.issues?.[issue.id] ?? null;
    const last = lastRecord(state, issue.id);
    let record = openRecord(state, issue.id);

    // A recorded anomaly is final for its cycle; only evidence outside the
    // guard reopens it. Without that evidence the guard stays silent.
    if (!record && last?.phase === "anomaly") {
      if (!isGap(issue)) {
        if (!dryRun) { closeRecord(last, "resolved", `状态已离开该缺口：${issue.category}`, now); store.save(state); }
        decide(issue, "resolved", `状态已离开该缺口：${issue.category}`);
        continue;
      }
      const evidence = await externalProgress(api, issue, last.closed_at ?? last.dispatched_at, new Set(), { allowActivity: true, closedAt: last.closed_at });
      if (!evidence) { decide(issue, "hold", "上一周期已记录异常，等待真实外部进展"); continue; }
      if (!dryRun) { closeRecord(last, "resolved", `外部进展：${evidence.kind}`, now); store.save(state); }
      decide(issue, "resolved", `外部进展：${evidence.kind}`);
    } else if (!record && !last) {
      if (!isGap(issue)) continue;
      // No local record at all: make sure the platform does not already hold
      // one of ours (state lost, or a crash between registration and readback).
      const adopted = await adoptWatchdogRule(api, issue);
      if (adopted.error) { decide(issue, "hold", `无法核验既有派发：${adopted.error}`); continue; }
      if (adopted.record) {
        record = adopted.record;
        state.issues ??= {};
        state.issues[issue.id] = { identifier: issue.identifier, cycles: Number(parseCycleKey(record.cycle_key, issue.id)?.cycle ?? 0), attempts: record.attempts, records: [] };
        rememberRecord(state, issue, record);
        if (!dryRun) store.save(state);
        decide(issue, "adopted", `从平台恢复已登记的派发（${record.wakeup_id}）`);
      }
    }

    if (record) {
      const outcome = await inspectRecord({ api, issue, record, now, maxDispatches, windowMs, entry: state.issues?.[issue.id] ?? entry });
      if (outcome.action === "hold") { decide(issue, "hold", outcome.reason); if (!dryRun && outcome.save) store.save(state); continue; }
      if (outcome.action === "resolve") {
        if (!dryRun) { closeRecord(record, "resolved", outcome.reason, now); store.save(state); }
        decide(issue, "resolved", outcome.reason);
        if (!isGap(issue)) continue;
      } else if (outcome.action === "anomaly") {
        if (!dryRun) { closeRecord(record, "anomaly", outcome.reason, now); store.save(state); }
        decide(issue, "anomaly", outcome.reason);
        continue;
      } else if (outcome.action === "retry") {
        // Registration produced no run: allowed again under the attempt cap.
        record = null;
      }
    }

    if (!isGap(issue)) continue;
    const currentEntry = state.issues?.[issue.id];
    const spent = attemptsInWindow(currentEntry, now, windowMs).length;
    if (spent >= maxDispatches) {
      const reason = `同一事项在 ${Math.round(windowMs / 3600000)} 小时内已尝试派发 ${spent} 次，仍未恢复；停止自唤醒`;
      if (!dryRun) {
        const blocked = lastRecord(state, issue.id);
        if (blocked && OPEN_PHASES.has(blocked.phase)) closeRecord(blocked, "anomaly", reason, now);
        else {
          state.issues ??= {};
          state.issues[issue.id] ??= { identifier: issue.identifier, cycles: 0, attempts: [], records: [] };
          rememberRecord(state, issue, {
            cycle_key: cycleKeyFor(issue.id, (state.issues[issue.id].cycles ?? 0) + 1),
            phase: "anomaly", wakeup_id: null, dispatched_at: null, attempts: [],
            closed_at: iso(now), closed_reason: reason, anomaly: { reason, at: iso(now) },
          });
        }
        store.save(state);
      }
      decide(issue, "anomaly", reason);
      continue;
    }
    decide(issue, "dispatch", "confirmed unattended cycle");
  }

  // Records for issues no longer in the open set are closed; with --issue the
  // rest of the workspace is deliberately unread, so absence proves nothing.
  if (!issueFilter && !dryRun) {
    for (const [issueId, entry] of Object.entries(state.issues ?? {})) {
      if (seen.has(issueId)) continue;
      const record = entry.records?.at(-1);
      if (!record || record.phase === "resolved") continue;
      closeRecord(record, "resolved", "事项已关闭或已离开未完成集合", now);
      decisions.push({ issue: entry.identifier ?? issueId, action: "resolved", reason: "事项已关闭或已离开未完成集合" });
    }
  }

  for (const item of dispatch) {
    const { issue } = item;
    state.issues ??= {};
    const entry = (state.issues[issue.id] ??= { identifier: issue.identifier, cycles: 0, attempts: [], records: [] });
    const cycle = (entry.cycles ?? 0) + 1;
    const cycleKey = cycleKeyFor(issue.id, cycle);
    if (dryRun) {
      decisions.push({ issue: issue.identifier, action: "dispatch", reason: item.reason, cycle_key: cycleKey, dry_run: true });
      continue;
    }
    entry.cycles = cycle;
    noteAttempt(entry, now);
    const record = { cycle_key: cycleKey, phase: "dispatching", wakeup_id: null, dispatched_at: iso(now), attempts: [...entry.attempts] };
    // Persist the intent before the write. If the process dies between here and
    // readback, the platform's own rule list is consulted before any retry, so
    // the cycle is never dispatched twice.
    rememberRecord(state, issue, record);
    store.save(state);
    try {
      const saved = await registerDispatch(api, issue, cycleKey, pmAgent);
      record.phase = "dispatched";
      record.wakeup_id = saved.id;
      record.fired = saved.fired;
      rememberRecord(state, issue, record);
      store.save(state);
      decisions.push({ issue: issue.identifier, action: "dispatch", reason: "registered one project-manager wakeup", wakeup_id: saved.id, cycle_key: cycleKey });
      log(`${issue.identifier}: dispatched one project-manager wakeup (${saved.id}) for ${cycleKey}`);
    } catch (error) {
      record.phase = "failed";
      record.error = sanitize(error);
      rememberRecord(state, issue, record);
      store.save(state);
      decisions.push({ issue: issue.identifier, action: "failed", reason: record.error });
      log(`${issue.identifier}: dispatch failed — ${record.error}`);
    }
  }

  state.scans.last_completed_at = iso(now);
  if (!dryRun) store.save(state);
  return { status: "scanned", decisions };
}

// Evaluate an open cycle against the issue's current diagnosis, the guard's
// own rule and its runs. The guard's activity never counts as progress; only an
// outside run, a member reply, or a state that is no longer this gap does.
async function inspectRecord({ api, issue, record, now, maxDispatches, windowMs, entry }) {
  let runs = [];
  if (record.wakeup_id) {
    try { runs = await readWakeupRuns(api, issue.id, record.wakeup_id); }
    catch { return { action: "hold", reason: "派发运行记录暂时无法核验，跳过并可恢复" }; }
  }
  const ours = new Set(runs.map((r) => r.id));
  const active = runs.find((r) => ACTIVE_RUN_STATUSES.has(r.status));
  if (active) return { action: "hold", reason: `派发的项目经理运行仍在 ${active.status}` };

  // The own rule may be missing from the workspace-wide rule page (pagination
  // truncation); the issue's own list is authoritative for the rule we wrote.
  let rule = (issue.continuation_rules ?? []).find((r) => r.id === record.wakeup_id) ?? null;
  if (record.wakeup_id && !rule) {
    try {
      const rows = await readIssueWakeups(api, issue.id);
      const row = rows.find((r) => r.id === record.wakeup_id);
      if (row) rule = { id: row.id, state: row.enabled === true && !row.disabled_at ? "usable" : "ended", enabled: row.enabled === true };
    } catch {
      return { action: "hold", reason: "接续规则状态暂时无法核验，跳过并可恢复" };
    }
  }
  const ruleUsable = rule?.state === "usable" || rule?.state === "overdue";
  const latest = runs[0] ?? null;

  if (issue.category === "continuation_unverified") {
    return { action: "hold", reason: "接续未核验（规则读取被截断），不据此派发" };
  }

  if (!isGap(issue)) {
    // An enabled one-shot rule of ours can itself make the diagnosis read
    // "waiting_continuation". That is the guard's own pending dispatch, not
    // outside progress, so the cycle stays open.
    if (ruleUsable && runs.length === 0) return { action: "hold", reason: "已登记接续规则，等待平台触发" };
    if (issue.category === "active_run") return { action: "resolve", reason: "外部运行正在推进" };
    return { action: "resolve", reason: `状态已离开该缺口：${issue.category}` };
  }

  // Still the same gap: in_progress with no usable continuation of its own.
  if (latest && TERMINAL_RUN_STATUSES.has(latest.status)) {
    const since = latest.completed_at ?? latest.created_at;
    const evidence = await externalProgress(api, issue, since, ours);
    if (evidence) return { action: "resolve", reason: `外部进展：${evidence.kind}` };
    const when = latest.completed_at ? new Date(latest.completed_at).toISOString() : iso(now);
    const note = latest.commented ? "，已评论" : "，未评论";
    return {
      action: "anomaly",
      reason: `派发的项目经理运行已结束（${latest.status}${note}，${when}），仍未登记接续；同一周期不再派发`,
    };
  }
  if (latest && ACTIVE_RUN_STATUSES.has(latest.status)) return { action: "hold", reason: `派发的运行仍在 ${latest.status}` };
  if (runs.length === 0 && ruleUsable) return { action: "hold", reason: "接续规则已登记，等待平台触发" };

  if (!latest) {
    const spent = attemptsInWindow(entry, now, windowMs).length;
    if (record.phase === "dispatching" && spent === 0) return { action: "retry", reason: "派发中断，未发现平台上的规则或运行" };
    if (record.phase === "failed" || spent < maxDispatches) return { action: "retry", reason: "接续规则没有产生运行" };
    return { action: "anomaly", reason: "接续规则多次没有产生运行，已达到派发上限" };
  }

  // A run exists but the platform reports no state we can act on yet; wait for
  // the next event instead of stacking another dispatch on it.
  return { action: "hold", reason: `派发的运行状态未知（${latest.status}）` };
}

export function formatStatus(state) {
  const rows = ["# 军团接续守护状态", `工作区：${state.workspace_id}`, `最近扫描：${state.scans?.last_completed_at ?? "从未"}`];
  if (state.scans?.last_error) rows.push(`最近扫描错误：${state.scans.last_error}`);
  if (state.scans?.truncated) rows.push("注意：工作区唤醒规则读取被截断，未核验的事项不会派发。");
  const records = [];
  const anomalies = [];
  for (const [issueId, entry] of Object.entries(state.issues ?? {})) {
    for (const record of entry.records ?? []) {
      const row = { issue: entry.identifier ?? issueId, ...record };
      records.push(row);
      if (record.phase === "anomaly") anomalies.push(row);
    }
  }
  rows.push("", `## 需要检查的异常（${anomalies.length}）`);
  if (!anomalies.length) rows.push("无。");
  else for (const a of anomalies) rows.push(`- ${a.issue}（${a.cycle_key ?? "周期未知"}）：${a.anomaly?.reason ?? a.closed_reason ?? "未说明"}（${a.anomaly?.at ?? a.closed_at ?? "时间未知"}）`);
  rows.push("", `## 派发周期（${records.length}）`, "| 事项 | 周期 | 阶段 | 派发时间 | 接续规则 | 说明 |", "| --- | --- | --- | --- | --- | --- |");
  if (!records.length) rows.push("| — | — | — | — | — | 无 |");
  for (const r of records.slice(-40)) rows.push(`| ${r.issue} | ${r.cycle_key ?? "—"} | ${r.phase} | ${r.dispatched_at ?? "—"} | ${r.wakeup_id ?? "—"} | ${(r.anomaly?.reason ?? r.closed_reason ?? r.error ?? "").replace(/\|/g, "\\|")} |`);
  return rows.join("\n");
}

function wsUrl(config) {
  const url = new URL(config.server_url);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  const base = url.pathname.replace(/\/$/, "");
  url.pathname = `${base}/ws`;
  url.searchParams.set("workspace_id", config.workspace_id);
  url.searchParams.set("client_platform", "watchdog");
  return url.toString();
}

// The browser/desktop client's protocol: upgrade with the workspace in the
// query, send the token in the first frame, then read workspace events.
export function defaultConnect(url, { token, onReady, onMessage, onClose, onFatal }) {
  const socket = new WebSocket(url);
  socket.addEventListener("open", () => {
    socket.send(JSON.stringify({ type: "auth", payload: { token } }));
  });
  socket.addEventListener("message", (event) => {
    let message;
    try { message = JSON.parse(event.data); } catch { return; }
    if (!message || typeof message.type !== "string") return;
    if (message.type === "auth_ack") { onReady(); return; }
    if (message.type === "auth_error") { onFatal(new Error("Workspace websocket authentication failed; check the profile token.")); return; }
    onMessage(message);
  });
  socket.addEventListener("close", () => onClose());
  socket.addEventListener("error", () => { /* close follows and schedules the reconnect */ });
  return { close() { try { socket.close(); } catch { /* already closed */ } } };
}

export async function runWatchdog({
  api, config, store, state, live, pmAgent, issueFilter = null,
  log = () => {}, signal = null, connect = defaultConnect, debounceMs = 750,
  reconnectMs = 1000, reconnectMaxMs = 30000, rescanMs = DEFAULT_RESCAN_MS,
  maxDispatches = 3, windowMs = 24 * 3600 * 1000,
}) {
  const local = new AbortController();
  const stopSignal = signal ? AbortSignal.any([signal, local.signal]) : local.signal;
  let stopped = false;
  let socket = null;
  let reconnectTimer = null;
  let scanTimer = null;
  let heartbeatTimer = null;
  let scanning = false;
  let pendingReason = null;
  let attempt = 0;
  let failures = 0;
  let reason = "stopped";

  const stop = (why = "stopped") => {
    if (stopped) return;
    stopped = true;
    reason = why;
    clearTimeout(reconnectTimer);
    clearTimeout(scanTimer);
    clearTimeout(heartbeatTimer);
    socket?.close();
    local.abort(why);
  };
  stopSignal.addEventListener("abort", () => stop(stopSignal.reason ?? "stopped"), { once: true });

  async function scan(why) {
    if (stopped) return;
    if (scanning) { pendingReason = why; return; }
    scanning = true;
    try {
      const result = await scanOnce({ api, live, store, state, pmAgent, issueFilter, maxDispatches, windowMs, log });
      if (result.status === "skipped") {
        failures += 1;
        // No later event may arrive (a disconnected websocket, a quiet
        // workspace), so failures schedule their own retry with a cap.
        scheduleRecovery(Math.min(RETRY_MS * 2 ** (failures - 1), rescanMs || RETRY_MS * 4));
      } else {
        failures = 0;
        scheduleRecovery(rescanMs);
      }
      log(`scan (${why}): ${result.status}${result.status === "skipped" ? ` — ${result.reason}` : `, ${result.decisions.length} decision(s)`}`);
    } catch (error) {
      failures += 1;
      log(`scan (${why}) failed: ${sanitize(error)}`);
      scheduleRecovery(Math.min(RETRY_MS * 2 ** (failures - 1), rescanMs || RETRY_MS * 4));
    } finally {
      scanning = false;
      if (pendingReason && !stopped) { const next = pendingReason; pendingReason = null; scheduleScan(next); }
    }
  }

  // Coalesce but never postpone: the timer is not restarted by later events,
  // so a stream of unrelated events cannot keep a scan from running.
  function scheduleScan(why) {
    if (stopped || scanTimer) return;
    scanTimer = setTimeout(() => { scanTimer = null; scan(why); }, debounceMs);
  }

  function scheduleRecovery(delay) {
    if (stopped) return;
    clearTimeout(heartbeatTimer);
    if (!delay) return;
    heartbeatTimer = setTimeout(() => { heartbeatTimer = null; scan("recovery"); }, delay);
  }

  function scheduleReconnect() {
    if (stopped) return;
    const delay = Math.min(reconnectMs * 2 ** attempt, reconnectMaxMs);
    attempt += 1;
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(() => { if (!stopped) openSocket(); }, delay);
  }

  function openSocket() {
    if (stopped) return;
    try {
      socket = connect(wsUrl(config), {
        token: config.token,
        onReady: () => {
          attempt = 0;
          // Startup and every reconnect rescan: events between the last
          // confirmed connection and this one were never received.
          scan("connected");
        },
        onMessage: (message) => {
          if (!WATCHED_EVENTS.has(message.type)) return;
          scheduleScan(message.type);
        },
        onClose: () => { socket = null; scheduleReconnect(); },
        onFatal: (error) => {
          log(`websocket fatal: ${sanitize(error)}`);
          // Never keep a dead socket while the process waits forever: end the
          // run so the service manager can surface the failure.
          stop(`fatal: ${sanitize(error)}`);
        },
      });
    } catch (error) {
      log(`connect failed: ${sanitize(error)}`);
      scheduleReconnect();
    }
  }

  // Scan before the socket is confirmed so a server that is down still
  // produces a diagnosis; the onReady scan is idempotent and coalesces.
  await scan("startup");
  openSocket();

  const done = new Promise((resolveDone) => {
    if (stopped) { resolveDone(reason); return; }
    stopSignal.addEventListener("abort", () => resolveDone(reason), { once: true });
  });
  return { stop, done, get socket() { return socket; } };
}

function renderUnit({ profile, root, node, stateDir, issue, pmAgent }) {
  const args = ["run", "--profile", profile];
  if (issue) args.push("--issue", issue);
  if (pmAgent) args.push("--pm-agent", pmAgent);
  if (stateDir) args.push("--state-dir", stateDir);
  return [
    "[Unit]",
    `Description=Multica agent-team handoff watchdog (profile ${profile})`,
    "After=network-online.target",
    "Wants=network-online.target",
    "",
    "[Service]",
    "Type=simple",
    `WorkingDirectory=${root}`,
    `ExecStart=${node} ${join(root, "scripts/agent-team-watchdog.mjs")} ${args.join(" ")}`,
    "Restart=on-failure",
    "RestartSec=10",
    "# One writer per host per workspace is enforced by the daemon's lock file.",
    "# Do not install this unit on a second machine for the same workspace: the",
    "# lock is machine-local and cannot see another host's writer.",
    "",
    "[Install]",
    "WantedBy=default.target",
    "",
  ].join("\n");
}

export async function main(argv, env = process.env) {
  const options = parseWatchdogArgs([...argv]);
  if (options.help) { console.log(HELP); return; }
  if (options.command === "unit") {
    if (!options.profile) throw new Error("unit needs --profile so the service targets one account");
    const unit = renderUnit({
      profile: options.profile, root: resolve(options.root ?? ROOT),
      node: options.node ?? process.execPath, stateDir: options.stateDir,
      issue: options.issue, pmAgent: options.pmAgent,
    });
    if (options.output) {
      const target = resolve(options.output);
      mkdirSync(dirname(target), { recursive: true });
      writeFileSync(target, unit);
      console.log(`Wrote ${target}. Install with: systemctl --user daemon-reload && systemctl --user enable --now ${/([^/]+)\.service$/.exec(target)?.[1] ?? "the unit"}.`);
    } else console.log(unit);
    return { unit };
  }
  const resolved = resolveProfile(options, { root: ROOT, env });
  const store = new StateStore(options.stateDir ?? defaultStateDir(resolved.config.workspace_id, env));
  if (options.command === "status") {
    const state = store.load(resolved.config.workspace_id);
    if (options.json) console.log(JSON.stringify(state, null, 2));
    else console.log(formatStatus(state));
    return state;
  }
  const dryRun = options.command === "scan" && options.dryRun;
  // The writer lock is machine-local and keyed by workspace, never by
  // --state-dir. State is read only after the lock is held, so a second process
  // never observes and acts on a partially updated state.
  const lock = dryRun ? null : acquireLock(defaultLockDir(env), { workspaceId: resolved.config.workspace_id, profile: resolved.profile });
  try {
    const state = store.load(resolved.config.workspace_id);
    const api = apiClient(resolved.config);
    const agents = await api("GET", "/api/agents");
    const squads = await api("GET", "/api/squads");
    if (!Array.isArray(agents) || !Array.isArray(squads)) throw new Error("Unexpected workspace read response");
    const live = { agents, squads };
    const pmAgent = findProjectManager(agents, options.pmAgent ?? "项目经理");
    if (options.command === "scan") {
      const result = await scanOnce({
        api, live, store, state, pmAgent, issueFilter: options.issue,
        dryRun, maxDispatches: options.maxDispatches, windowMs: options.windowHours * 3600 * 1000,
        log: options.json ? () => {} : (line) => console.error(`[watchdog] ${line}`),
      });
      if (options.json) console.log(JSON.stringify(result, null, 2));
      else {
        console.log(`扫描：${result.status}${result.reason ? `（${result.reason}）` : ""}`);
        for (const d of result.decisions) console.log(`- ${d.issue}: ${d.action} — ${d.reason}`);
      }
      return result;
    }
    const controller = new AbortController();
    process.once("SIGINT", () => controller.abort("SIGINT"));
    process.once("SIGTERM", () => controller.abort("SIGTERM"));
    const run = await runWatchdog({
      api, config: resolved.config, store, state, live, pmAgent,
      issueFilter: options.issue, signal: controller.signal,
      debounceMs: options.debounceMs, rescanMs: options.rescanMs, reconnectMs: options.reconnectMs,
      maxDispatches: options.maxDispatches, windowMs: options.windowHours * 3600 * 1000,
      log: options.json ? () => {} : (line) => console.error(`[watchdog ${new Date().toISOString()}] ${line}`),
    });
    const why = await run.done;
    if (String(why).startsWith("fatal:")) {
      console.error(`[agent-team-watchdog] ${why}`);
      process.exitCode = 2;
    }
    return run;
  } finally {
    lock?.release();
  }
}

function invokedDirectly() {
  if (!process.argv[1]) return false;
  try { return realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url)); }
  catch { return false; }
}

if (invokedDirectly()) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(`[agent-team-watchdog] ${error.message}`);
    process.exitCode = 1;
  });
}