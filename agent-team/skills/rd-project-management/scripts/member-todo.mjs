// Shared generator and precheck for member todos.
//
// A member todo asks one real person for something only that person can give:
// material, an authorization, a preference, or a business trade-off. The first
// screen must be readable without engineering context, while the machine
// identity and the evidence stay in a technical appendix and an invisible
// marker so deduplication survives title changes.
//
// The module ships byte-identically inside both the rd-acceptance and
// rd-project-management skill bundles, so every creator has the same precheck
// without a project checkout; scripts/member-todo.mjs exposes it as a
// repository CLI. scripts/member-todo.test.mjs enforces the copies match.

import { createHash } from "node:crypto";
import { fileURLToPath, pathToFileURL } from "node:url";
import { mkdirSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

export const MEMBER_TODO_MARKER = "multica-member-todo";
export const MEMBER_ANSWER_MARKER = "multica-member-answer";
// The active chief-of-staff role in agent-team/team.json. Answers are routed to
// this recorded role name, never to a machine id: the client resolves the
// workspace agent when it submits, and a missing or ambiguous match is an
// honest failure instead of a fake delivery.
export const CHIEF_OF_STAFF_NAME = "幕僚长";
export const TECHNICAL_HEADING = "## 技术附录（团队内部，无需阅读）";
export const TODO_KINDS = Object.freeze({
  material: "人工材料",
  authorization: "授权",
  choice: "偏好选择",
  decision: "业务取舍",
});
export const MAX_TITLE = 60;
export const MAX_TITLE_ATTEMPTS = 9;

// Active duplicate protection normalizes the title and ignores the body key, so
// two different requests may legitimately need the same readable title. These
// are deterministic: every creator walks the same sequence, so a concurrent
// retry of one key lands on the same candidate as the first attempt.
export function titleCandidates(title, limit = MAX_TITLE_ATTEMPTS) {
  const base = oneLine(title);
  const candidates = [];
  for (let n = 1; n <= limit; n++) {
    const suffix = n === 1 ? "" : `（第 ${n} 项）`;
    const room = Math.max(1, MAX_TITLE - [...suffix].length);
    const prefix = [...base].slice(0, room).join("").trimEnd();
    candidates.push(`${prefix}${suffix}`);
  }
  return candidates;
}

// A duplicate-title hit is the same request only when its body really carries
// the key we are creating. The title no longer identifies a decision.
export function hasDecisionKey(description, key) {
  const body = String(description ?? "");
  const wanted = String(key ?? "").trim();
  if (!wanted) return false;
  const marker = body.match(/<!--\s*multica-acceptance-decision\s+([\s\S]*?)-->/);
  if (marker) {
    try {
      if (JSON.parse(marker[1]).decision_key === wanted) return true;
    } catch { /* fall through to the appendix line */ }
  }
  const escaped = wanted.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`^decision_key:\\s*${escaped}$`, "m").test(body);
}

export function duplicateIdentifier(message) {
  const match = String(message ?? "").match(/Active duplicate issue exists:\s*([A-Za-z][A-Za-z0-9_-]*-\d+)\b/);
  return match ? match[1] : null;
}

// Only the title and the first screen are checked. The technical appendix may
// carry standards, keys and revisions.
const VISIBLE_FORBIDDEN = [
  [/acc-[0-9a-f]{16}/i, "决策键"],
  [/\b[0-9a-f]{7,40}\b/i, "提交 SHA"],
  [/缺口键|标准版本|候选版本|attempt_id|environment_error|exit_code|in_review|in_progress/i, "工程标记"],
  [/\bC\d{1,3}\b/, "标准号"],
  [/\b(?:TODO|TBD|FIXME|xxx)\b|待填|待补充|待定/i, "占位内容"],
  [/<[^>]*>/, "占位模板"],
];

// A user can supply real material, but not the team's own verification output.
const INTERNAL_EVIDENCE = /日志|脚本|复跑|测试报告|回执|命令行|运行测试|执行命令|构建产物/;

function isText(value) {
  return typeof value === "string" && value.trim() !== "";
}

function oneLine(value) {
  return String(value).trim().replace(/\s*\n+\s*/g, " ");
}

function visiblePart(description) {
  const index = String(description).indexOf("\n---\n");
  return index === -1 ? String(description) : String(description).slice(0, index);
}

export function visibleViolations(text) {
  const body = String(text ?? "");
  return VISIBLE_FORBIDDEN.filter(([pattern]) => pattern.test(body)).map(([, label]) => label);
}

export function memberTodoKey({ source, request, decision_key: decisionKey } = {}) {
  if (isText(decisionKey)) return decisionKey.trim();
  const sourceLabel = isText(source?.identifier) ? source.identifier.trim() : "";
  const sourceId = isText(source?.id) ? source.id.trim() : "";
  const digest = createHash("sha256")
    .update(`${sourceLabel}|${sourceId}|${isText(request) ? request.trim() : ""}`)
    .digest("hex")
    .slice(0, 16);
  return `todo-${digest}`;
}

// The completion text must match the resume rule the creator actually
// registers. A decision or a request that only says "reply here" needs a
// listener on this todo itself, waking the manager who resumes the original
// issue; a reply on this todo never wakes a wait registered on the original.
export const COMPLETION_SIGNALS = Object.freeze({
  done: { done: "在本事项回复你的选择，并把本事项标记为完成（只回复不会自动接续）", next: "本事项完成后，原事项会自动继续处理" },
  comment: { done: "在本事项回复你的选择", next: "项目经理会在本待办收到你的回复，并接回原事项继续处理" },
  manager: { done: "在本事项回复你的选择", next: "项目经理会在本待办收到你的回复后继续原事项" },
});

// Options travel with the machine marker so a client can render real choices
// without guessing the readable Markdown. An explicit key keeps its identity
// across edits; otherwise it is derived deterministically from the label, so
// creators that already ship label/consequence keep working unchanged.
export function optionKey(option) {
  const explicit = isText(option?.key) ? option.key.trim() : "";
  if (explicit) return explicit;
  const digest = createHash("sha256").update(oneLine(option?.label ?? "")).digest("hex").slice(0, 10);
  return `opt-${digest}`;
}

export function encodeOptions(options) {
  if (!Array.isArray(options)) return [];
  return options
    .filter((option) => option && typeof option === "object" && isText(option.label) && isText(option.consequence ?? option.effect))
    .map((option) => ({
      key: optionKey(option),
      label: oneLine(option.label),
      consequence: oneLine(option.consequence ?? option.effect),
      ...(isText(option.example) ? { example: oneLine(option.example) } : {}),
    }));
}

function optionErrors(options, kind) {
  const errors = [];
  const rows = Array.isArray(options) ? options : [];
  const needsChoice = kind === "authorization" || kind === "choice" || kind === "decision";
  if (needsChoice && rows.length < 2) {
    errors.push("需要至少两个真实可选项，让用户能直接选择");
    return errors;
  }
  if (kind === "material" && rows.length > 0) {
    errors.push("人工材料待办不需要选项；把需要提供的材料写进 submit");
  }
  const seenKeys = new Set();
  rows.forEach((option, index) => {
    if (!option || typeof option !== "object") {
      errors.push(`第 ${index + 1} 个选项必须是对象`);
      return;
    }
    if (!isText(option.label)) errors.push(`第 ${index + 1} 个选项缺少名称 label`);
    if (!isText(option.consequence ?? option.effect)) errors.push(`选项「${option.label ?? index + 1}」缺少选择后的后果 consequence`);
    if (option.default !== undefined || option.recommended !== undefined) {
      errors.push(`选项「${option.label ?? index + 1}」预设了默认选择；请把差异写清楚，让用户自己选`);
    }
    if (isText(option.key) && !/^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$/.test(option.key.trim())) {
      errors.push(`选项「${option.label ?? index + 1}」的 key 只能使用字母、数字、下划线或连字符，最长 48 位`);
    }
    if (isText(option.label)) {
      const key = optionKey(option);
      if (seenKeys.has(key)) errors.push(`选项「${option.label}」的唯一键 ${key} 重复；不同选项必须能被区分`);
      seenKeys.add(key);
    }
    for (const [field, label] of [["label", "名称"], ["consequence", "后果"], ["effect", "后果"], ["example", "示例"]]) {
      if (!isText(option[field])) continue;
      for (const violation of visibleViolations(option[field])) {
        errors.push(`选项${label}里有${violation}，请移到正文末尾技术附录`);
      }
    }
  });
  return errors;
}

export function validateTodoInput(input) {
  if (!input || typeof input !== "object" || Array.isArray(input)) return ["输入必须是一个对象"];
  const errors = [];
  const kind = input.kind;
  if (!Object.hasOwn(TODO_KINDS, kind)) {
    errors.push(`kind 必须是 ${Object.keys(TODO_KINDS).join("、")} 之一`);
  }
  if (!Object.hasOwn(COMPLETION_SIGNALS, input.completion_signal)) {
    errors.push(`缺少真实完成信号 completion_signal（${Object.keys(COMPLETION_SIGNALS).join("、")}）`);
  }

  for (const [field, label] of [
    ["title", "标题"], ["problem", "实际问题"], ["why_you", "为什么需要你"],
    ["action", "需要做的那一件事"], ["where", "入口或回复位置"], ["submit", "最小提交物"],
  ]) {
    if (!isText(input[field])) errors.push(`缺少${label}（${field}）`);
  }
  if (Object.hasOwn(COMPLETION_SIGNALS, input.completion_signal)) {
    const needed = {
      done: input.completion_signal === "done" ? "完成" : "回复",
      next: input.completion_signal === "done" ? "自动继续" : "项目经理",
    };
    for (const field of ["done", "next"]) {
      if (isText(input[field]) && !input[field].includes(needed[field])) {
        errors.push(`${field} 与 completion_signal=${input.completion_signal} 不一致，实际接续方式会落空`);
      }
    }
    if (input.completion_signal !== "done" && isText(input.next) && !/收到你的回复|唤醒/.test(input.next)) {
      errors.push("next 必须说明项目经理如何收到回复并被唤醒；只写“经理会查看”会失联");
    }
  }
  if (isText(input.title)) {
    if (input.title.includes("\n")) errors.push("标题只能有一行");
    if ([...input.title.trim()].length > MAX_TITLE) errors.push(`标题超过 ${MAX_TITLE} 个字符`);
  }
  if (!isText(input.recipient?.name) && !isText(input.recipient?.id)) {
    errors.push("缺少真实接收人（recipient.name 或 recipient.id）");
  }
  // Receivers must not re-route a member answer by an arbitrary name in the
  // issue body. The product convention is one receiver, the configured chief
  // of staff, resolved in the current workspace; a different handler is
  // rejected rather than silently ignored.
  if (input.handler !== undefined) {
    const handler = isText(input.handler?.name) ? input.handler.name.trim()
      : isText(input.handler) ? input.handler.trim()
        : "";
    if (handler !== CHIEF_OF_STAFF_NAME) {
      errors.push(`接件人统一为${CHIEF_OF_STAFF_NAME}；不能通过 handler 改派其他角色`);
    }
  }
  if (!isText(input.source?.identifier)) errors.push("缺少来源事项 source.identifier，去重和回链会失效");
  if (!isText(input.request) && !isText(input.decision_key)) {
    errors.push("缺少稳定的输入请求标识 request（或 decision_key），无法复用同一输入请求");
  }
  if (isText(input.decision_key) && !/^acc-[0-9a-f]{16}$/.test(input.decision_key.trim())) {
    errors.push("decision_key 必须是 acc- 加 16 位十六进制");
  }
  errors.push(...optionErrors(input.options, kind));

  const userFacing = [input.problem, input.why_you, input.action, input.where, input.submit].filter(isText).join("\n");
  if (INTERNAL_EVIDENCE.test(userFacing)) {
    errors.push("不要要求用户生成日志、脚本或内部证据；团队能做的验证留在团队内部");
  }
  if (isText(input.title)) {
    for (const label of visibleViolations(input.title)) errors.push(`标题里有${label}，请移到正文末尾技术附录`);
  }
  for (const label of visibleViolations(userFacing)) {
    errors.push(`首屏内容里有${label}，请移到正文末尾技术附录`);
  }
  return [...new Set(errors)];
}

function sourceLabel(source) {
  if (isText(source?.id)) return `[${source.identifier}](mention://issue/${source.id})`;
  return source.identifier;
}

function renderTechnical(input) {
  const lines = [`- 来源事项：${sourceLabel(input.source)}`];
  if (isText(input.request)) lines.push(`- 输入请求：${input.request.trim()}`);
  for (const [label, value] of Object.entries(input.technical ?? {})) {
    if (value === undefined || value === null || !isText(String(value))) continue;
    lines.push(`- ${label}：${String(value).replace(/\n/g, "\n  ")}`);
  }
  return lines.join("\n");
}

export function buildMemberTodo(input) {
  const errors = validateTodoInput(input);
  if (errors.length > 0) {
    const error = new Error(`成员待办内容不完整：\n- ${errors.join("\n- ")}`);
    error.errors = errors;
    throw error;
  }

  const kind = input.kind;
  const todoKey = memberTodoKey(input);
  const completion = COMPLETION_SIGNALS[input.completion_signal];
  const defaultDone = input.completion_signal === "done" && kind === "material"
    ? "把材料回复在本事项，并把本事项标记为完成（只回复不会自动接续）"
    : completion.done;
  const doneText = isText(input.done) ? oneLine(input.done) : defaultDone;
  const nextText = isText(input.next) ? oneLine(input.next) : completion.next;
  const options = Array.isArray(input.options) ? input.options : [];
  const optionBlock = options.length > 0
    ? ["可选的做法：", ...options.map((option) => {
      const consequence = option.consequence ?? option.effect;
      const example = isText(option.example) ? `（例如：${oneLine(option.example)}）` : "";
      return `- **${oneLine(option.label)}**：${oneLine(consequence)}${example}`;
    })].join("\n")
    : "";
  const ask = kind === "material"
    ? `需要你提供：${oneLine(input.submit)}`
    : `请在本事项回复：${oneLine(input.submit)}`;

  const visible = [
    oneLine(input.problem),
    "",
    oneLine(input.why_you),
    "",
    `**你需要做一件事：** ${oneLine(input.action)}`,
    "",
    `入口：${oneLine(input.where)}`,
    ...(optionBlock ? ["", optionBlock] : []),
    "",
    ask,
    "",
    `怎样算完成：${doneText}`,
    "",
    `之后：${nextText}`,
  ].join("\n");

  const markerBody = {
    todo_key: todoKey,
    kind,
    source_issue: input.source.identifier,
    ...(isText(input.source.id) ? { source_id: input.source.id } : {}),
    ...(isText(input.request) ? { request: input.request.trim() } : {}),
    completion_signal: input.completion_signal,
    // Always the configured chief of staff name. An arbitrary handler in an
    // issue body never reassigns the answer to another role.
    handler: CHIEF_OF_STAFF_NAME,
    ...(isText(input.problem) ? { problem: oneLine(input.problem) } : {}),
    ...(isText(input.why_you) ? { why_you: oneLine(input.why_you) } : {}),
    ...(isText(input.action) ? { action: oneLine(input.action) } : {}),
    // One atomic block: a client either understands the whole option set or
    // shows no choice form at all. It never infers options from the prose.
    ...(encodeOptions(options).length > 0 ? { options: encodeOptions(options) } : {}),
  };
  const marker = `<!-- ${MEMBER_TODO_MARKER} ${JSON.stringify(markerBody)} -->`;
  const keyLine = isText(input.decision_key) ? `decision_key: ${input.decision_key.trim()}` : null;
  const extraMarkers = Array.isArray(input.markers) ? input.markers.filter(isText) : [];

  const description = [
    visible,
    "",
    "---",
    "",
    TECHNICAL_HEADING,
    "",
    renderTechnical(input),
    ...(keyLine ? ["", keyLine] : []),
    ...(extraMarkers.length > 0 ? ["", ...extraMarkers] : []),
    "",
    marker,
  ].join("\n");

  const todo = {
    kind,
    todo_key: todoKey,
    title: oneLine(input.title),
    title_candidates: titleCandidates(input.title),
    description,
    first_screen: visible,
    marker,
    metadata: {
      member_todo_key: todoKey,
      member_todo_kind: kind,
      member_todo_source: input.source.identifier,
      ...(isText(input.decision_key) ? { acceptance_decision_key: input.decision_key.trim() } : {}),
    },
  };

  const problems = validateMemberTodo(todo);
  if (problems.length > 0) {
    const error = new Error(`生成的成员待办不符合格式：\n- ${problems.join("\n- ")}`);
    error.errors = problems;
    throw error;
  }
  return todo;
}

export function readMemberTodoMarker(description) {
  const match = String(description ?? "").match(new RegExp(`<!--\\s*${MEMBER_TODO_MARKER}\\s+([\\s\\S]*?)-->`));
  if (!match) return null;
  try {
    return JSON.parse(match[1]);
  } catch {
    return null;
  }
}

export function validateMemberTodo(todo) {
  if (!todo || typeof todo !== "object") return ["待办对象缺失"];
  const errors = [];
  if (!Object.hasOwn(TODO_KINDS, todo.kind)) errors.push("kind 无效");
  if (!isText(todo.title)) errors.push("缺少标题");
  if (isText(todo.title)) {
    if ([...todo.title].length > MAX_TITLE) errors.push(`标题超过 ${MAX_TITLE} 个字符`);
    for (const label of visibleViolations(todo.title)) errors.push(`标题里有${label}`);
  }
  if (!isText(todo.description)) errors.push("缺少正文");
  if (errors.length > 0) return errors;

  const visible = visiblePart(todo.description);
  for (const label of visibleViolations(visible)) errors.push(`首屏内容里有${label}`);
  for (const [needle, label] of [
    ["**你需要做一件事：**", "需要做的一件事"],
    ["入口：", "入口或回复位置"],
    ["怎样算完成：", "完成条件"],
    ["之后：", "后续动作"],
    [TECHNICAL_HEADING, "技术附录"],
  ]) {
    if (!todo.description.includes(needle)) errors.push(`正文缺少${label}`);
  }
  if (todo.kind === "authorization" || todo.kind === "choice" || todo.kind === "decision") {
    if (!visible.includes("可选的做法：")) errors.push("需要给出可选的做法及各自后果");
  }
  const marker = readMemberTodoMarker(todo.description);
  if (!marker) errors.push("缺少可检索的成员待办标记");
  else if (!isText(marker.todo_key)) errors.push("成员待办标记缺少 todo_key");
  else if (todo.todo_key && marker.todo_key !== todo.todo_key) errors.push("标记里的 todo_key 与待办不一致");
  if (marker && !isText(marker.handler)) errors.push("成员待办标记缺少接件角色 handler");
  if (marker && !Object.hasOwn(COMPLETION_SIGNALS, marker.completion_signal)) {
    errors.push("成员待办标记缺少真实完成信号 completion_signal");
  }
  if (marker) {
    const markerOptions = Array.isArray(marker.options) ? marker.options : [];
    if (todo.kind === "authorization" || todo.kind === "choice" || todo.kind === "decision") {
      if (markerOptions.length < 2) errors.push("标记里需要至少两个可选项，客户端才能直接选择");
    }
    if (todo.kind === "material" && markerOptions.length > 0) {
      errors.push("人工材料待办不应携带选择项");
    }
    const keys = new Set();
    markerOptions.forEach((option, index) => {
      if (!isText(option?.key) || !isText(option?.label) || !isText(option?.consequence)) {
        errors.push(`标记里的第 ${index + 1} 个选项缺少唯一键、名称或后果`);
        return;
      }
      if (keys.has(option.key)) errors.push(`标记里的选项唯一键 ${option.key} 重复`);
      keys.add(option.key);
      if (!visible.includes(oneLine(option.label))) {
        errors.push(`标记里的选项「${option.label}」没有出现在首屏`);
      }
    });
  }
  return [...new Set(errors)];
}

function materialSample() {
  return {
    kind: "material",
    title: "需要你提供最近的真实订单导出文件",
    recipient: { name: "dev" },
    source: { identifier: "DEV-58", id: "sample-issue-58" },
    request: "real-order-export",
    completion_signal: "done",
    problem: "导入功能目前只用样例数据验证过，真实订单文件可能有样例里没有的格式，导入后可能出现金额对不上。",
    why_you: "真实订单文件只有你的账号能导出，团队没有这个权限，也不想用样例数据冒充真实结果。",
    action: "从日常使用的订单系统导出最近一份订单文件。",
    where: "订单系统 → 订单列表 → 右上角「导出」。",
    submit: "把导出的文件作为附件回复在本事项。",
    done: "把导出的文件作为附件回复在本事项，并把本事项标记为完成（只回复不会自动接续）",
    next: "本事项完成后，原事项会自动继续处理，我会告诉你金额和订单号是否都对得上。",
  };
}

function authorizationSample() {
  return {
    kind: "authorization",
    title: "需要你确认能否读取最近 30 天的订单",
    recipient: { name: "dev" },
    source: { identifier: "DEV-61", id: "sample-issue-61" },
    request: "read-order-history",
    completion_signal: "comment",
    problem: "验证订单导入时需要用你账号里的真实订单，团队自己的账号看不到这些数据。",
    why_you: "这个账号只有你能授权，团队不能替你同意。",
    action: "确认是否允许团队只读最近 30 天的订单数据。",
    where: "直接在本事项回复。",
    options: [
      { key: "allow", label: "允许", consequence: "团队可以只读最近 30 天的订单验证导入，不会改动任何数据。", example: "回复“允许”" },
      { key: "deny", label: "不允许", consequence: "这项验证继续使用样例数据，真实账号下的问题暂时无法确认。", example: "回复“不允许”" },
    ],
    submit: "回复“允许”或“不允许”。",
    done: "在本事项回复“允许”或“不允许”",
    next: "项目经理会在本待办收到你的回复，并接回原事项继续处理。",
  };
}

function choiceSample() {
  return {
    kind: "choice",
    title: "订单列表先显示金额还是先显示状态",
    recipient: { name: "dev" },
    source: { identifier: "DEV-63", id: "sample-issue-63" },
    request: "order-list-first-column",
    completion_signal: "comment",
    problem: "订单一屏放不下所有信息，需要先定第一列显示什么，否则上线后再改会影响日常查看。",
    why_you: "这是每天使用习惯的取舍，只有你每天看这个列表。",
    action: "选一个你更希望打开列表就先看到的信息。",
    where: "直接在本事项回复。",
    options: [
      { key: "amount", label: "先显示金额", consequence: "打开列表先看到每单金额，订单状态要点开详情查看。", example: "回复“金额”" },
      { key: "status", label: "先显示状态", consequence: "打开列表先看到订单进度，金额要点开详情查看。", example: "回复“状态”" },
    ],
    submit: "回复“金额”或“状态”。",
    done: "在本事项回复“金额”或“状态”",
    next: "项目经理会在本待办收到你的回复，并接回原事项继续处理。",
  };
}

function decisionSample() {
  return {
    kind: "decision",
    title: "「PDF 引用跳转」需要你做一个取舍（DEV-58）",
    recipient: { name: "dev" },
    source: { identifier: "DEV-58", id: "sample-issue-58" },
    request: "acceptance-decision",
    completion_signal: "done",
    decision_key: "acc-0123456789abcdef",
    problem: "读者能看到 PDF 正文，但点里面的引用打不开原文，无法核对来源。",
    why_you: "本期先发还是在发布说明里标注限制后再发，只有你能定这个发布取舍。",
    action: "选择这次 PDF 怎么发。",
    where: "直接在本事项回复你的选择。",
    options: [
      { key: "ship", label: "本期先发，标注引用暂不可跳转", consequence: "读者先看到正文；引用暂时点不开，发布说明会写明这条限制。", example: "回复“先发”" },
      { key: "wait", label: "等引用能跳转后再发", consequence: "发布时间推迟，直到 PDF 引用可以打开原文。", example: "回复“等修好”" },
    ],
    submit: "回复“先发”或“等修好”。",
    done: "选好后请把本事项标记为完成（只回复不会自动接续）",
    next: "本事项完成后，原事项会自动继续处理。",
    technical: {
      原事项: "[DEV-58](mention://issue/sample-issue-58)",
      当前候选: "a1b2c3d4e5f6",
      验收标准版本: "standard-v1",
      失败标准: "- C1：PDF 引用无法打开原文",
      团队结论: "研发经理：现有方案已无法让引用在本期可跳转",
    },
  };
}

export function sampleTodoInputs() {
  return [materialSample(), authorizationSample(), choiceSample(), decisionSample()];
}

export function sampleTodos() {
  return sampleTodoInputs().map((input) => ({ input, todo: buildMemberTodo(input) }));
}

export const USAGE = `Usage:
  node member-todo.mjs --input <todo.json> [--out-dir <dir>] [--check]
  node member-todo.mjs --sample [--out-dir <dir>]

--input   Structured todo content. The tool validates it before writing.
--out-dir Write title.txt, body.md and metadata.json there (default .member-todo).
--check   Validate only; write nothing.
--sample  Generate one runnable example of each kind and print it as JSON.

The same file ships inside the rd-acceptance and rd-project-management skills,
so it runs directly from an installed skill directory with no project checkout.

Exit codes: 0 valid, 2 invalid input or arguments.
`;

function parseTodoArgs(argv) {
  const args = { outDir: null };
  for (let i = 0; i < argv.length; i++) {
    const token = argv[i];
    if (token === "-h" || token === "--help") { args.help = true; continue; }
    if (token === "--check") { args.check = true; continue; }
    if (token === "--sample") { args.sample = true; continue; }
    if (!token.startsWith("--")) throw new Error(`unexpected argument: ${token}`);
    const eq = token.indexOf("=");
    const name = eq === -1 ? token.slice(2) : token.slice(2, eq);
    const value = eq === -1 ? argv[++i] : token.slice(eq + 1);
    if (value === undefined || value.startsWith("--")) throw new Error(`missing value for --${name}`);
    if (name === "input") args.input = value;
    else if (name === "out-dir") args.outDir = value;
    else throw new Error(`unknown option: --${name}`);
  }
  return args;
}

function writeTodo(dir, todo, prefix = "") {
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, `${prefix}title.txt`), `${todo.title}\n`);
  writeFileSync(join(dir, `${prefix}titles.txt`), `${todo.title_candidates.join("\n")}\n`);
  writeFileSync(join(dir, `${prefix}body.md`), `${todo.description}\n`);
  writeFileSync(join(dir, `${prefix}metadata.json`), `${JSON.stringify(todo.metadata, null, 2)}\n`);
}

function createCommand(dir, todo, prefix = "") {
  return `"$MULTICA_CLI_PATH" issue create --title "$(cat ${join(dir, `${prefix}title.txt`)})" --description-file ${join(dir, `${prefix}body.md`)} --assignee-id <接收人用户 ID>`;
}

export function runMemberTodoCli(argv = process.argv.slice(2), stdout = process.stdout) {
  const args = parseTodoArgs(argv);
  if (args.help) { stdout.write(USAGE); return 0; }

  if (args.sample) {
    const samples = sampleTodos().map(({ input, todo }, index) => {
      const prefix = `sample-${index + 1}-`;
      let command = null;
      if (args.outDir) {
        writeTodo(args.outDir, todo, prefix);
        command = createCommand(args.outDir, todo, prefix);
      }
      return {
        kind: todo.kind,
        kind_label: TODO_KINDS[todo.kind],
        title: todo.title,
        title_candidates: todo.title_candidates,
        first_screen: todo.first_screen,
        description: todo.description,
        metadata: todo.metadata,
        ...(command ? { create_command: command } : {}),
        input,
      };
    });
    stdout.write(`${JSON.stringify({ samples }, null, 2)}\n`);
    return 0;
  }

  if (!args.input) throw new Error("需要 --input <todo.json>，或使用 --sample");
  const raw = JSON.parse(readFileSync(args.input, "utf8"));
  if (args.check) {
    const errors = validateTodoInput(raw);
    if (errors.length === 0) {
      try { buildMemberTodo(raw); }
      catch (error) { errors.push(...(error.errors ?? [error.message])); }
    }
    if (errors.length > 0) {
      stdout.write(`${JSON.stringify({ ok: false, errors }, null, 2)}\n`);
      return 2;
    }
    stdout.write(`${JSON.stringify({ ok: true }, null, 2)}\n`);
    return 0;
  }

  const todo = buildMemberTodo(raw);
  const outDir = args.outDir ?? ".member-todo";
  writeTodo(outDir, todo);
  stdout.write(`${JSON.stringify({
    ok: true,
    kind: todo.kind,
    todo_key: todo.todo_key,
    title: todo.title,
    title_candidates: todo.title_candidates,
    first_screen: todo.first_screen,
    metadata: todo.metadata,
    files: {
      title: join(resolve(outDir), "title.txt"),
      titles: join(resolve(outDir), "titles.txt"),
      body: join(resolve(outDir), "body.md"),
      metadata: join(resolve(outDir), "metadata.json"),
    },
    create_command: createCommand(outDir, todo),
  }, null, 2)}\n`);
  return 0;
}

function isEntryPoint() {
  if (!process.argv[1]) return false;
  try {
    return realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return import.meta.url === pathToFileURL(process.argv[1]).href;
  }
}

if (isEntryPoint()) {
  try {
    process.exitCode = runMemberTodoCli();
  } catch (error) {
    process.stderr.write(`member-todo: ${error.message}\n`);
    process.exitCode = 2;
  }
}