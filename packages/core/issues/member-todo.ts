import type { Comment } from "../types/comment";

/**
 * Member todos are ordinary issues whose body ends with a machine marker. The
 * marker is the only source of a choice form: a client either understands the
 * whole option block or shows no form at all, so unrelated Markdown can never
 * be mistaken for a question with answers.
 */
export const MEMBER_TODO_MARKER = "multica-member-todo";
export const MEMBER_ANSWER_MARKER = "multica-member-answer";
/** Re-notification of a saved answer; distinct from the answer marker. */
export const MEMBER_NOTICE_MARKER = "multica-member-notice";

/**
 * The one configured receiver. The product convention routes every member
 * answer to the active chief of staff in the current workspace; an arbitrary
 * `handler` recorded in an issue body never reassigns it to another role.
 */
export const MEMBER_TODO_HANDLER = "幕僚长";

export type MemberTodoKind = "material" | "authorization" | "choice" | "decision";
export type MemberTodoCompletionSignal = "done" | "comment" | "manager";

export interface MemberTodoOption {
  key: string;
  label: string;
  consequence: string;
  example?: string;
}

export interface MemberTodo {
  todoKey: string;
  kind: MemberTodoKind;
  sourceIssue: string;
  sourceId?: string;
  request?: string;
  completionSignal?: MemberTodoCompletionSignal;
  /** Recorded name, kept for display and old markers. Routing always uses MEMBER_TODO_HANDLER. */
  handler: string;
  problem?: string;
  whyYou?: string;
  action?: string;
  /** Empty when the marker has no complete, unambiguous option block. */
  options: MemberTodoOption[];
}

export interface MemberAnswer {
  todoKey: string;
  answerId: string;
  answer: string;
  optionKey?: string;
  sourceIssue?: string;
  handler?: string;
  answeredBy?: string;
}

export interface MemberAnswerComment {
  id: string;
  author_type: string;
  author_id: string;
  content: string;
  created_at: string;
  parent_id?: string | null;
  /** Timeline entries call this `comment_type`; the comments endpoint omits it. */
  comment_type?: string;
  type?: string;
}

export interface MemberAnswerNotice {
  todoKey: string;
  answerId: string;
  answerCommentId?: string;
}

const TODO_KINDS = new Set<MemberTodoKind>([
  "material",
  "authorization",
  "choice",
  "decision",
]);
const COMPLETION_SIGNALS = new Set<MemberTodoCompletionSignal>([
  "done",
  "comment",
  "manager",
]);
const OPTION_KEY = /^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$/;

function markerJson(content: unknown, marker: string): Record<string, unknown> | null {
  if (typeof content !== "string" || content.length === 0) return null;
  const match = content.match(new RegExp(`<!--\\s*${marker}\\s+([\\s\\S]*?)-->`));
  if (!match) return null;
  const body = match[1];
  if (body === undefined) return null;
  try {
    const parsed = JSON.parse(body);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

function text(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed.length > 0 ? trimmed : undefined;
}

/**
 * Read the option block atomically: all entries must be complete and carry
 * unique keys, otherwise the todo exposes no options and the client falls back
 * to the ordinary comment flow rather than rendering a partial choice set.
 */
export function parseMemberTodoOptions(value: unknown): MemberTodoOption[] {
  if (!Array.isArray(value) || value.length < 2) return [];
  const options: MemberTodoOption[] = [];
  const keys = new Set<string>();
  for (const raw of value) {
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return [];
    const row = raw as Record<string, unknown>;
    const key = text(row.key);
    const label = text(row.label);
    const consequence = text(row.consequence);
    if (!key || !OPTION_KEY.test(key) || !label || !consequence) return [];
    if (keys.has(key)) return [];
    keys.add(key);
    const example = text(row.example);
    options.push({ key, label, consequence, ...(example ? { example } : {}) });
  }
  return options;
}

/**
 * Parse the member-todo marker. Missing `completion_signal` or a missing /
 * partial option block marks a historical card: it is returned for readable
 * status, but `isAnswerableMemberTodo` is false and no form is rendered.
 */
export function parseMemberTodo(description: string | null | undefined): MemberTodo | null {
  const raw = markerJson(description, MEMBER_TODO_MARKER);
  if (!raw) return null;
  const todoKey = text(raw.todo_key);
  const kind = text(raw.kind) as MemberTodoKind | undefined;
  const sourceIssue = text(raw.source_issue);
  if (!todoKey || !kind || !TODO_KINDS.has(kind) || !sourceIssue) return null;
  const completionSignal = text(raw.completion_signal) as MemberTodoCompletionSignal | undefined;
  const parsed: MemberTodo = {
    todoKey,
    kind,
    sourceIssue,
    handler: text(raw.handler) ?? MEMBER_TODO_HANDLER,
    options: parseMemberTodoOptions(raw.options),
    ...(completionSignal && COMPLETION_SIGNALS.has(completionSignal)
      ? { completionSignal }
      : {}),
  };
  const sourceId = text(raw.source_id);
  const request = text(raw.request);
  const problem = text(raw.problem);
  const whyYou = text(raw.why_you);
  const action = text(raw.action);
  if (sourceId) parsed.sourceId = sourceId;
  if (request) parsed.request = request;
  if (problem) parsed.problem = problem;
  if (whyYou) parsed.whyYou = whyYou;
  if (action) parsed.action = action;
  // Historical cards have no completion signal. Keep them readable but never
  // turn them into a submit form.
  return parsed;
}

/**
 * A choice form is available only when the card was generated with a real
 * completion signal and a complete option block. Material todos need a file or
 * text, not a choice, so they stay on the ordinary comment flow.
 */
export function isAnswerableMemberTodo(todo: MemberTodo | null): todo is MemberTodo {
  return !!todo && todo.kind !== "material" && todo.completionSignal !== undefined &&
    todo.options.length >= 2;
}

export function parseMemberAnswer(content: string | null | undefined): MemberAnswer | null {
  const raw = markerJson(content, MEMBER_ANSWER_MARKER);
  if (!raw) return null;
  const todoKey = text(raw.todo_key);
  const answerId = text(raw.answer_id);
  const answer = typeof raw.answer === "string" ? raw.answer : undefined;
  if (!todoKey || !answerId || answer === undefined || answer.trim().length === 0) return null;
  return {
    todoKey,
    answerId,
    answer,
    optionKey: text(raw.option_key),
    sourceIssue: text(raw.source_issue),
    handler: text(raw.handler),
    answeredBy: text(raw.answered_by),
  };
}

/** True for a real member comment, ignoring system narration and tombstones. */
export function isMemberAnswerComment(comment: MemberAnswerComment): boolean {
  if (comment.author_type !== "member") return false;
  if (comment.content.trim().length === 0) return false;
  const type = comment.comment_type ?? comment.type;
  return type === undefined || type === "comment" || type === "progress_update";
}

/**
 * The answer is the persisted, machine-marked comment the assignee posted. A
 * member's unrelated comment is never treated as an answer by itself; callers
 * may use `latestMemberComment` for the legacy reply+done flow.
 */
export function findMemberTodoAnswer(
  comments: readonly MemberAnswerComment[],
  todoKey: string,
  userId: string,
): { comment: MemberAnswerComment; answer: MemberAnswer } | null {
  let found: { comment: MemberAnswerComment; answer: MemberAnswer } | null = null;
  for (const comment of comments) {
    if (comment.author_type !== "member" || comment.author_id !== userId) continue;
    const answer = parseMemberAnswer(comment.content);
    if (!answer || answer.todoKey !== todoKey) continue;
    // A marker can only vouch for its own author. A copied answer comment must
    // never let another member claim the answer.
    if (answer.answeredBy && answer.answeredBy !== comment.author_id) continue;
    // Keep the last one should a historical client have saved more than once.
    found = { comment, answer };
  }
  return found;
}

/** Latest real member comment, used only when an issue is already closed. */
export function latestMemberComment(
  comments: readonly MemberAnswerComment[],
  userId: string,
): MemberAnswerComment | null {
  let latest: MemberAnswerComment | null = null;
  for (const comment of comments) {
    if (comment.author_type !== "member" || comment.author_id !== userId) continue;
    if (!isMemberAnswerComment(comment)) continue;
    if (!latest || comment.created_at >= latest.created_at) latest = comment;
  }
  return latest;
}

/**
 * Evidence that the chief actually handled the answer: a chief-agent comment
 * posted as a reply in the answer's thread (or in a re-notification thread for
 * the same answer). An unrelated later progress comment is not handling
 * evidence, however recent it is.
 */
export function findChiefHandlingComment(
  comments: readonly MemberAnswerComment[],
  threadRootIds: readonly string[],
  chiefAgentIds: readonly string[],
): MemberAnswerComment | null {
  const chiefIds = new Set(chiefAgentIds);
  const roots = new Set(threadRootIds);
  if (roots.size === 0) return null;
  let latest: MemberAnswerComment | null = null;
  for (const comment of comments) {
    if (comment.author_type !== "agent" || !chiefIds.has(comment.author_id)) continue;
    if (!comment.parent_id || !roots.has(comment.parent_id)) continue;
    if (comment.content.trim().length === 0) continue;
    const type = comment.comment_type ?? comment.type;
    if (type !== undefined && type !== "comment" && type !== "progress_update") continue;
    if (!latest || comment.created_at >= latest.created_at) latest = comment;
  }
  return latest;
}

/**
 * Re-notifications that belong to one saved answer. They let a later refresh
 * recognize the run a retry actually started, instead of forgetting it.
 */
export function findMemberAnswerNotices(
  comments: readonly MemberAnswerComment[],
  todoKey: string,
  answerId: string,
): Array<{ comment: MemberAnswerComment; notice: MemberAnswerNotice }> {
  const out: Array<{ comment: MemberAnswerComment; notice: MemberAnswerNotice }> = [];
  for (const comment of comments) {
    const notice = parseMemberNotice(comment.content);
    if (!notice || notice.todoKey !== todoKey || notice.answerId !== answerId) continue;
    out.push({ comment, notice });
  }
  return out;
}

export function parseMemberNotice(content: string | null | undefined): MemberAnswerNotice | null {
  const raw = markerJson(content, MEMBER_NOTICE_MARKER);
  if (!raw) return null;
  const todoKey = text(raw.todo_key);
  const answerId = text(raw.answer_id);
  if (!todoKey || !answerId) return null;
  return { todoKey, answerId, answerCommentId: text(raw.answer_comment_id) };
}

/** Escape the only sequence an HTML comment cannot carry. */
export function escapeMarkerJson(json: string): string {
  return json.replace(/--/g, "\\u002d\\u002d");
}

export function buildMemberAnswerContent(input: {
  handlerName: string;
  handlerId: string;
  todo: MemberTodo;
  answer: MemberAnswer;
  option?: MemberTodoOption;
}): string {
  const { handlerName, handlerId, todo, answer, option } = input;
  const label = handlerName.replace(/[[\]]/g, "\\$&");
  const origin = todo.sourceId
    ? `[${todo.sourceIssue}](mention://issue/${todo.sourceId})`
    : todo.sourceIssue;
  const visible = option
    ? `答复：**${option.label}**\n\n选择后的结果：${option.consequence}`
    : `答复：${answer.answer}`;
  const marker = `<!-- ${MEMBER_ANSWER_MARKER} ${escapeMarkerJson(JSON.stringify({
    todo_key: todo.todoKey,
    answer_id: answer.answerId,
    answer: answer.answer,
    ...(answer.optionKey ? { option_key: answer.optionKey } : {}),
    source_issue: todo.sourceIssue,
    handler: MEMBER_TODO_HANDLER,
    answered_by: answer.answeredBy,
  }))} -->`;
  return [
    `[@${label}](mention://agent/${handlerId})`,
    "",
    visible,
    "",
    `来源：${origin}（成员待办 ${todo.todoKey}）`,
    "",
    marker,
  ].join("\n");
}

/**
 * Stable UUID for one member's answer to one todo. A double click, retry or a
 * second window therefore asks the server for the same row; the server replays
 * the existing comment instead of creating another answer or another run.
 */
export async function memberAnswerCommentId(todoKey: string, userId: string): Promise<string> {
  const bytes = new TextEncoder().encode(`multica-member-answer:${todoKey}:${userId}`);
  const digest = await crypto.subtle.digest("SHA-1", bytes);
  const uuid = new Uint8Array(digest).slice(0, 16);
  uuid[6] = ((uuid[6] ?? 0) & 0x0f) | 0x50;
  uuid[8] = ((uuid[8] ?? 0) & 0x3f) | 0x80;
  const hex = Array.from(uuid, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/**
 * Header the answer comment uses to notify the chief without duplicating the
 * answer body. A re-notification never carries the answer marker, so the
 * persisted answer stays unique.
 */
export function buildMemberAnswerNotice(input: {
  handlerName: string;
  handlerId: string;
  todo: MemberTodo;
  answer: MemberAnswer;
  answerCommentId: string;
}): string {
  const label = input.handlerName.replace(/[[\]]/g, "\\$&");
  const marker = `<!-- ${MEMBER_NOTICE_MARKER} ${escapeMarkerJson(JSON.stringify({
    todo_key: input.todo.todoKey,
    answer_id: input.answer.answerId,
    answer_comment_id: input.answerCommentId,
  }))} -->`;
  return [
    `[@${label}](mention://agent/${input.handlerId})`,
    "",
    `补发通知：成员答复已保存，请读取并接续。`,
    "",
    `答复：${input.answer.answer}`,
    `来源：${input.todo.sourceIssue}（成员待办 ${input.todo.todoKey}，答复 ${input.answer.answerId}）`,
    "",
    marker,
  ].join("\n");
}

// Kept for callers that hold a full Comment and want the narrower shape.
export function toMemberAnswerComment(comment: Comment): MemberAnswerComment {
  return {
    id: comment.id,
    author_type: comment.author_type,
    author_id: comment.author_id,
    content: comment.content,
    created_at: comment.created_at,
    parent_id: comment.parent_id,
    type: comment.type,
  };
}