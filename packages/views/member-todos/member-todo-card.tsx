"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  buildMemberAnswerContent,
  buildMemberAnswerNotice,
  findChiefHandlingComment,
  findMemberAnswerNotices,
  findMemberTodoAnswer,
  isAnswerableMemberTodo,
  issueColumnCategory,
  latestMemberComment,
  MEMBER_TODO_HANDLER,
  memberAnswerCommentId,
  parseMemberAnswer,
  type MemberAnswer,
  type MemberTodo,
  type MemberTodoOption,
} from "@multica/core/issues";
import { issueCommentsOptions, issueKeys, issueTasksOptions } from "@multica/core/issues/queries";
import { useCreateComment, useUpdateIssue } from "@multica/core/issues/mutations";
import { resolveChiefOfStaff } from "@multica/core/agents";
import { agentListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Issue } from "@multica/core/types";
import { AppLink } from "../navigation";
import { stripMentionMarkdown } from "../issues/utils/strip-mention-markdown";
import { useT } from "../i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import {
  ArrowUpRight,
  CircleAlert,
  CircleCheck,
  CircleHelp,
  Clock,
  Send,
} from "@multica/ui/icons";

type AnswerDraft =
  | { kind: "option"; option: MemberTodoOption }
  | { kind: "custom"; text: string };

type LocalAnswer = {
  commentId: string;
  answer: MemberAnswer;
  /** True only when a chief receipt or a matching real run proves delivery. */
  confirmedDelivery: boolean;
  /** Notice comments this window posted, so their runs are found immediately. */
  noticeIds: string[];
};

type DeliveryState = "handled" | "handling" | "failed" | "no_record" | "unconfirmed" | "waiting";

type MemberTodoCardProps = {
  issue: Issue;
  todo: MemberTodo;
  userId: string;
  variant?: "home" | "detail";
  className?: string;
};

function activeTask(status: string | undefined): boolean {
  return status === "queued" || status === "dispatched" || status === "waiting_local_directory" ||
    status === "deferred" || status === "running";
}

export function excerpt(value: string, limit = 220): string {
  const text = stripMentionMarkdown(value)
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/(\*\*|__|`|~~)/g, "")
    .replace(/^\s{0,3}(#{1,6}|[-*+]|>)\s+/gm, "")
    .replace(/\s+/g, " ")
    .trim();
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
}

/** `Word` next to an example `Word` or `例如：Word` adds nothing. */
export function isRedundantExample(example: string | undefined, label: string): boolean {
  if (!example) return true;
  const normalize = (value: string) =>
    value
      .toLowerCase()
      .replace(/^(例如|例子|示例|如|e\.g\.|for example|ex\.)\s*[:：]?\s*/i, "")
      .replace(/[\s“”"'`。.!！?？:：]/g, "")
      .trim();
  const normalized = normalize(example);
  return normalized.length === 0 || normalized === normalize(label);
}

/**
 * One member interaction surface, shared by the My Issues band and the task
 * detail/peek. It reads only the machine marker for choices, resolves the
 * configured chief of staff in this workspace, and never claims the chief
 * handled an answer without persisted evidence.
 */
export function MemberTodoCard({ issue, todo, userId, variant = "detail", className }: MemberTodoCardProps) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const qc = useQueryClient();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: comments = [], isSuccess: commentsLoaded } = useQuery(issueCommentsOptions(issue.id));
  const { data: tasks = [], isSuccess: tasksLoaded } = useQuery(issueTasksOptions(issue.id));
  const createComment = useCreateComment(issue.id);
  const updateIssue = useUpdateIssue();

  const [selected, setSelected] = useState<string | null>(null);
  const [customText, setCustomText] = useState("");
  const [local, setLocal] = useState<LocalAnswer | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [completionFailed, setCompletionFailed] = useState(false);
  const [notifyNonce] = useState(() => globalThis.crypto?.randomUUID?.() ?? String(Date.now()));
  const [notifying, setNotifying] = useState(false);

  const chief = useMemo(
    () => resolveChiefOfStaff(agents, userId, MEMBER_TODO_HANDLER),
    [agents, userId],
  );
  // Only the resolved receiver vouches for a run. Another agent that happens
  // to be named in the body or task list is not handling evidence.
  const chiefId = chief.status === "ok" ? chief.agent.id : null;

  const assigneeIsUser = issue.assignee_type === "member" && issue.assignee_id === userId;
  const closed = (() => {
    const category = issueColumnCategory(issue);
    return category === "done" || category === "closed";
  })();

  const persisted = findMemberTodoAnswer(comments, todo.todoKey, userId);
  const legacy = !persisted && closed ? latestMemberComment(comments, userId) : null;
  const answerCommentId = persisted?.comment.id ?? local?.commentId ?? null;
  const answer = persisted?.answer ?? local?.answer ?? null;
  const answerText = answer?.answer ?? legacy?.content.trim() ?? null;
  const answered = answerText !== null;

  // A retry's notice thread is its own trigger root, and the marker lets a
  // later refresh rebuild that set instead of forgetting the run it started.
  const noticeIds = useMemo(() => {
    const ids = new Set(local?.noticeIds ?? []);
    if (answer) {
      for (const row of findMemberAnswerNotices(comments, todo.todoKey, answer.answerId)) {
        ids.add(row.comment.id);
      }
    }
    return [...ids];
  }, [comments, local?.noticeIds, todo.todoKey, answer]);
  const threadRootIds = useMemo(
    () => (answerCommentId ? [answerCommentId, ...noticeIds] : noticeIds),
    [answerCommentId, noticeIds],
  );
  const threadRoots = useMemo(() => new Set(threadRootIds), [threadRootIds]);

  const chiefTasks = useMemo(() => {
    if (!chiefId) return [];
    return tasks.filter((task) =>
      task.agent_id === chiefId &&
      !!task.trigger_comment_id &&
      threadRoots.has(task.trigger_comment_id),
    );
  }, [chiefId, tasks, threadRoots]);
  const latestChiefTask = useMemo(() => {
    let latest: (typeof chiefTasks)[number] | undefined;
    for (const task of chiefTasks) {
      if (!latest || task.created_at >= latest.created_at) latest = task;
    }
    return latest;
  }, [chiefTasks]);

  const handledComment = answerCommentId
    ? findChiefHandlingComment(comments, threadRootIds, chiefId ? [chiefId] : [])
    : null;
  const handling = !!latestChiefTask && activeTask(latestChiefTask.status);
  const taskFailed = latestChiefTask?.status === "failed" || latestChiefTask?.status === "cancelled";
  // A completed run only proves "no handling record" once the comment list has
  // actually loaded; otherwise the chief's reply may simply still be in flight.
  const taskCompleted = commentsLoaded && latestChiefTask?.status === "completed";
  const confirmedDelivery = local?.confirmedDelivery === true || chiefTasks.length > 0;
  const deliveryUnconfirmed = answered && !handledComment && !confirmedDelivery;
  const evidenceLoaded = commentsLoaded && tasksLoaded;

  const deliveryState: DeliveryState = handledComment
    ? "handled"
    : handling
      ? "handling"
      : taskFailed
        ? "failed"
        : taskCompleted
          ? "no_record"
          : deliveryUnconfirmed
            ? "unconfirmed"
            : "waiting";

  // Retrying is safe only when the evidence is loaded and no run is active.
  // A completed run with a loaded thread and no chief reply has no record.
  const canNotify = answered && evidenceLoaded && !handledComment && !handling &&
    (deliveryUnconfirmed || taskFailed || taskCompleted);

  const errorText = (code: string): string => {
    switch (code) {
      case "custom_required": return t(($) => $.member_answer.custom_required);
      case "select_required": return t(($) => $.member_answer.select_required);
      case "chief_missing": return t(($) => $.member_answer.chief_missing);
      case "chief_ambiguous": return t(($) => $.member_answer.chief_ambiguous);
      case "delivery_failed": return t(($) => $.member_answer.delivery_failed);
      case "retry_failed": return t(($) => $.member_answer.retry_failed);
      default: return t(($) => $.member_answer.submit_failed);
    }
  };

  const invalidateTodos = () =>
    qc.invalidateQueries({ queryKey: [...issueKeys.all(wsId), "member-todos"] });

  const completeTodo = async () => {
    if (todo.completionSignal !== "done" || closed) return;
    try {
      await updateIssue.mutateAsync({ id: issue.id, status: "done" });
      setCompletionFailed(false);
      await invalidateTodos();
    } catch {
      setCompletionFailed(true);
    }
  };

  /** Read the run list again instead of trusting a stale cache after a post. */
  const freshTasks = async () => {
    await qc.invalidateQueries({ queryKey: issueKeys.tasks(issue.id) });
    return qc.fetchQuery({ ...issueTasksOptions(issue.id), staleTime: 0 });
  };

  const submitAnswer = useMutation({
    mutationFn: async (draft: AnswerDraft) => {
      const option = draft.kind === "option" ? draft.option : undefined;
      const text = draft.kind === "custom" ? draft.text : option!.label;
      const resolution = resolveChiefOfStaff(agents, userId, MEMBER_TODO_HANDLER);
      if (resolution.status !== "ok") {
        throw new Error(resolution.status === "missing" ? "chief_missing" : "chief_ambiguous");
      }
      const answerId = await memberAnswerCommentId(todo.todoKey, userId);
      const draftAnswer: MemberAnswer = {
        todoKey: todo.todoKey,
        answerId,
        answer: text,
        ...(option ? { optionKey: option.key } : {}),
        answeredBy: userId,
      };
      const comment = await createComment.mutateAsync({
        content: buildMemberAnswerContent({
          handlerName: MEMBER_TODO_HANDLER,
          handlerId: resolution.agent.id,
          todo,
          answer: draftAnswer,
          option,
        }),
        commentId: answerId,
      });
      // A replayed create can return another window's persisted answer. Show
      // what the server saved, never this window's unsaved draft text.
      const stored = parseMemberAnswer(comment.content);
      const answer: MemberAnswer = stored && stored.todoKey === todo.todoKey ? stored : draftAnswer;
      const outcome = comment.trigger_outcomes?.find(
        (row) => row.target_type === "agent" && row.target_id === resolution.agent.id,
      );
      // Missing outcomes (a replay, or an older server) is not a receipt.
      const receipt = !!outcome && outcome.status !== "blocked";
      const nextTasks = await freshTasks();
      const matched = nextTasks.some((task) =>
        task.agent_id === resolution.agent.id && task.trigger_comment_id === comment.id,
      );
      const delivered = receipt || matched;
      setLocal({
        commentId: comment.id,
        answer,
        confirmedDelivery: delivered,
        noticeIds: [],
      });
      await Promise.all([
        qc.invalidateQueries({ queryKey: issueKeys.timeline(issue.id) }),
        invalidateTodos(),
      ]);
      if (!delivered) {
        setError("delivery_failed");
        return comment;
      }
      await completeTodo();
      return comment;
    },
    onError: (cause) => {
      const code = cause instanceof Error ? cause.message : "submit_failed";
      setError(
        code === "chief_missing" || code === "chief_ambiguous" ? code : "submit_failed",
      );
    },
  });

  const retryCompletion = useMutation({
    mutationFn: async () => {
      await updateIssue.mutateAsync({ id: issue.id, status: "done" });
      setCompletionFailed(false);
      await invalidateTodos();
    },
    onError: () => setCompletionFailed(true),
  });

  const notifyAgain = async () => {
    if (notifying) return;
    const resolution = resolveChiefOfStaff(agents, userId, MEMBER_TODO_HANDLER);
    if (resolution.status !== "ok") {
      setError(resolution.status === "missing" ? "chief_missing" : "chief_ambiguous");
      return;
    }
    if (!answer || !answerCommentId) return;
    setNotifying(true);
    setError(null);
    try {
      const noticeId = await memberAnswerCommentId(
        todo.todoKey,
        `${userId}:notice:${notifyNonce}`,
      );
      const comment = await createComment.mutateAsync({
        content: buildMemberAnswerNotice({
          handlerName: MEMBER_TODO_HANDLER,
          handlerId: resolution.agent.id,
          todo,
          answer,
          answerCommentId,
        }),
        commentId: noticeId,
      });
      const outcome = comment.trigger_outcomes?.find(
        (row) => row.target_type === "agent" && row.target_id === resolution.agent.id,
      );
      const receipt = !!outcome && outcome.status !== "blocked";
      const nextTasks = await freshTasks();
      const matched = nextTasks.some((task) =>
        task.agent_id === resolution.agent.id && task.trigger_comment_id === comment.id,
      );
      await qc.invalidateQueries({ queryKey: issueKeys.timeline(issue.id) });
      if (!receipt && !matched) {
        setError("retry_failed");
        return;
      }
      setLocal((prev) => {
        const base: LocalAnswer = prev ?? {
          commentId: answerCommentId,
          answer: answer!,
          confirmedDelivery: false,
          noticeIds: [],
        };
        return {
          ...base,
          confirmedDelivery: true,
          noticeIds: [...new Set([...base.noticeIds, comment.id])],
        };
      });
      await completeTodo();
    } catch {
      setError("retry_failed");
    } finally {
      setNotifying(false);
    }
  };

  const answerable = isAnswerableMemberTodo(todo);
  if (!answered && (!answerable || !assigneeIsUser)) return null;
  if (!answered && closed) {
    return (
      <CardShell variant={variant} className={className} tone="muted">
        <div className="flex items-center gap-2 text-body text-muted-foreground">
          <CircleAlert className="size-4" aria-hidden="true" />
          {t(($) => $.member_answer.status_closed)}
        </div>
      </CardShell>
    );
  }

  const selectedOption = selected?.startsWith("option:")
    ? todo.options.find((option) => option.key === selected.slice("option:".length))
    : undefined;
  const custom = selected === "custom";
  const canSubmit = !submitAnswer.isPending &&
    (selectedOption !== undefined || (custom && customText.trim().length > 0));
  const pending = submitAnswer.isPending;

  const submit = () => {
    setError(null);
    if (selectedOption) {
      submitAnswer.mutate({ kind: "option", option: selectedOption });
      return;
    }
    if (custom && customText.trim().length > 0) {
      // The exact text the member typed is submitted, not a normalized copy.
      submitAnswer.mutate({ kind: "custom", text: customText });
      return;
    }
    setError(custom ? "custom_required" : "select_required");
  };

  const statusLabel = deliveryState === "handled"
    ? t(($) => $.member_answer.status_handled)
    : deliveryState === "handling"
      ? t(($) => $.member_answer.status_handling)
      : deliveryState === "failed"
        ? t(($) => $.member_answer.status_failed)
        : deliveryState === "no_record"
          ? t(($) => $.member_answer.status_no_record)
          : deliveryState === "unconfirmed"
            ? t(($) => $.member_answer.status_unconfirmed)
            : t(($) => $.member_answer.status_waiting);

  const StatusIcon = deliveryState === "handled"
    ? CircleCheck
    : deliveryState === "failed" || deliveryState === "unconfirmed"
      ? CircleAlert
      : Clock;
  const statusTone = deliveryState === "handled"
    ? "text-emerald-600 dark:text-emerald-400"
    : deliveryState === "failed" || deliveryState === "unconfirmed"
      ? "text-amber-600 dark:text-amber-400"
      : "text-muted-foreground";

  const answeredOptionKey = answer?.optionKey ?? local?.answer.optionKey;
  const answeredOption = todo.options.find((option) => option.key === answeredOptionKey);

  return (
    <CardShell variant={variant} className={className} tone={answered ? "answered" : "ask"}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="inline-flex items-center gap-1.5 rounded-full border border-primary/20 bg-primary/5 px-2 py-0.5 text-micro font-medium text-primary">
          <CircleHelp className="size-3.5" aria-hidden="true" />
          {t(($) => $.member_answer.band_title)}
        </span>
        <span className="rounded-full bg-muted px-2 py-0.5 text-micro text-muted-foreground">
          {todo.kind === "material"
            ? t(($) => $.member_answer.kind_material)
            : todo.kind === "authorization"
              ? t(($) => $.member_answer.kind_authorization)
              : todo.kind === "choice"
                ? t(($) => $.member_answer.kind_choice)
                : t(($) => $.member_answer.kind_decision)}
        </span>
        {variant === "home" ? (
          <AppLink
            href={paths.issueDetail(issue.id)}
            className="ml-auto inline-flex items-center gap-1 text-micro text-muted-foreground hover:text-foreground"
          >
            {t(($) => $.member_answer.open_issue)}
            <ArrowUpRight className="size-3.5" aria-hidden="true" />
          </AppLink>
        ) : null}
      </div>

      {variant === "home" ? (
        <h3 className="mt-2 text-title-sm font-medium leading-snug">{issue.title}</h3>
      ) : null}

      {todo.problem ? (
        <p className={cn("text-body leading-relaxed text-foreground", variant === "home" ? "mt-1.5" : "mt-3")}>
          {todo.problem}
        </p>
      ) : null}

      {answered ? (
        <AnsweredState
          answerText={answerText ?? ""}
          option={answeredOption}
          statusLabel={statusLabel}
          statusTone={statusTone}
          StatusIcon={StatusIcon}
          handledText={handledComment ? excerpt(handledComment.content) : null}
          chiefName={MEMBER_TODO_HANDLER}
          completionFailed={completionFailed}
          retryingCompletion={retryCompletion.isPending}
          onRetryCompletion={() => retryCompletion.mutate()}
          canNotify={canNotify}
          notifying={notifying}
          onNotifyAgain={notifyAgain}
          error={error ? errorText(error) : null}
        />
      ) : (
        <>
          {todo.action ? (
            <p className="mt-1.5 text-body text-muted-foreground">{todo.action}</p>
          ) : null}

          <fieldset className="mt-4 space-y-2" disabled={pending}>
            <legend className="text-label font-medium text-muted-foreground">
              {t(($) => $.member_answer.options_heading)}
            </legend>
            {todo.options.map((option) => {
              const value = `option:${option.key}`;
              const checked = selected === value;
              return (
                <label
                  key={option.key}
                  className={cn(
                    "flex cursor-pointer gap-3 rounded-lg border p-3 transition-colors",
                    checked
                      ? "border-primary/40 bg-primary/[0.06]"
                      : "border-border/70 hover:border-border hover:bg-muted/40",
                  )}
                >
                  <input
                    type="radio"
                    name={`member-answer-${todo.todoKey}`}
                    value={value}
                    checked={checked}
                    onChange={() => {
                      setSelected(value);
                      setError(null);
                    }}
                    className="mt-0.5 size-4 shrink-0 accent-primary"
                  />
                  <span className="min-w-0">
                    <span className="block text-body font-medium leading-snug">{option.label}</span>
                    <span className="mt-0.5 block text-body leading-snug text-muted-foreground">
                      {option.consequence}
                    </span>
                    {option.example && !isRedundantExample(option.example, option.label) ? (
                      <span className="mt-1 block text-micro text-muted-foreground">
                        {t(($) => $.member_answer.option_example, { example: option.example })}
                      </span>
                    ) : null}
                  </span>
                </label>
              );
            })}
          </fieldset>

          <div className="mt-4">
            <label
              htmlFor={`member-custom-${issue.id}`}
              className="text-label font-medium text-muted-foreground"
            >
              {t(($) => $.member_answer.custom_heading)}
            </label>
            <Textarea
              id={`member-custom-${issue.id}`}
              value={customText}
              disabled={pending}
              onChange={(event) => {
                setCustomText(event.target.value);
                setSelected("custom");
                setError(null);
              }}
              placeholder={t(($) => $.member_answer.custom_placeholder)}
              className="mt-1.5 min-h-20"
            />
          </div>

          {error ? (
            <p role="alert" className="mt-2 text-body text-destructive">
              {errorText(error)}
            </p>
          ) : !selectedOption && !custom ? (
            <p className="mt-2 text-body text-muted-foreground">
              {t(($) => $.member_answer.select_required)}
            </p>
          ) : custom && customText.trim().length === 0 ? (
            <p className="mt-2 text-body text-muted-foreground">
              {t(($) => $.member_answer.custom_required)}
            </p>
          ) : null}

          <div className="mt-3 flex items-center gap-2">
            <Button type="button" size="sm" onClick={submit} disabled={!canSubmit} aria-busy={pending}>
              {pending ? <Spinner className="size-3.5" /> : <Send className="size-3.5" aria-hidden="true" />}
              {pending ? t(($) => $.member_answer.submitting) : t(($) => $.member_answer.submit)}
            </Button>
            <span className="text-micro text-muted-foreground">
              {t(($) => $.member_answer.source, { source: todo.sourceIssue })}
            </span>
          </div>
        </>
      )}
    </CardShell>
  );
}

function CardShell({
  variant,
  tone,
  className,
  children,
}: {
  variant: "home" | "detail";
  tone: "ask" | "answered" | "muted";
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <section
      className={cn(
        "relative overflow-hidden rounded-xl border",
        variant === "detail" ? "mt-5 p-4 md:p-5" : "p-4",
        tone === "ask"
          ? "border-primary/25 bg-card shadow-sm ring-1 ring-primary/5"
          : tone === "answered"
            ? "border-border/70 bg-muted/20"
            : "border-dashed border-border/70 bg-muted/10",
        className,
      )}
    >
      {tone === "ask" ? (
        <span className="absolute inset-y-0 left-0 w-1 bg-primary/60" aria-hidden="true" />
      ) : null}
      <div className={cn(tone === "ask" && "pl-1.5")}>{children}</div>
    </section>
  );
}

function AnsweredState({
  answerText,
  option,
  statusLabel,
  statusTone,
  StatusIcon,
  handledText,
  chiefName,
  completionFailed,
  retryingCompletion,
  onRetryCompletion,
  canNotify,
  notifying,
  onNotifyAgain,
  error,
}: {
  answerText: string;
  option?: MemberTodoOption;
  statusLabel: string;
  statusTone: string;
  StatusIcon: typeof CircleCheck;
  handledText: string | null;
  chiefName: string;
  completionFailed: boolean;
  retryingCompletion: boolean;
  onRetryCompletion: () => void;
  canNotify: boolean;
  notifying: boolean;
  onNotifyAgain: () => void;
  error: string | null;
}) {
  const { t } = useT("issues");
  return (
    <div className="mt-3 space-y-3">
      <div>
        <p className="text-label font-medium text-muted-foreground">
          {t(($) => $.member_answer.answer_heading)}
        </p>
        {option ? (
          <p className="mt-1 text-body">
            {t(($) => $.member_answer.answer_option, { label: option.label })}
          </p>
        ) : (
          <p className="mt-1 whitespace-pre-wrap text-body leading-relaxed">{answerText}</p>
        )}
        {option ? (
          <p className="mt-0.5 text-body text-muted-foreground">{option.consequence}</p>
        ) : null}
      </div>

      <div className={cn("flex items-center gap-1.5 text-body", statusTone)} aria-live="polite">
        <StatusIcon className="size-4 shrink-0" aria-hidden="true" />
        <span>{statusLabel}</span>
      </div>

      {handledText ? (
        <p className="rounded-md bg-muted/50 px-2.5 py-2 text-body text-muted-foreground">
          {t(($) => $.member_answer.handled_note, { name: chiefName })}{handledText}
        </p>
      ) : null}

      {completionFailed ? (
        <div className="flex flex-wrap items-center gap-2 text-body text-destructive">
          <span>{t(($) => $.member_answer.status_completion_failed)}</span>
          <Button type="button" size="sm" variant="outline" onClick={onRetryCompletion} disabled={retryingCompletion}>
            {retryingCompletion ? <Spinner className="size-3.5" /> : null}
            {t(($) => $.member_answer.retry_completion)}
          </Button>
        </div>
      ) : null}

      {canNotify ? (
        <div>
          <Button type="button" size="sm" variant="outline" onClick={onNotifyAgain} disabled={notifying}>
            {notifying ? <Spinner className="size-3.5" /> : null}
            {notifying ? t(($) => $.member_answer.retrying) : t(($) => $.member_answer.retry_delivery)}
          </Button>
        </div>
      ) : null}

      {error ? (
        <p role="alert" className="text-body text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}