// @vitest-environment jsdom

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import {
  buildMemberAnswerContent,
  buildMemberAnswerNotice,
  memberAnswerCommentId,
  type MemberTodo,
} from "@multica/core/issues";
import en from "../locales/en/issues.json";
import { excerpt, isRedundantExample, MemberTodoCard } from "./member-todo-card";

const state = vi.hoisted(() => ({
  agents: [] as Array<Record<string, unknown>>,
  comments: [] as Array<Record<string, unknown>>,
  tasks: [] as Array<Record<string, unknown>>,
  createComment: vi.fn(),
  updateIssue: vi.fn(),
}));

const CHIEF_ID = "22222222-2222-7222-8222-222222222222";
const USER_ID = "user-1";
const ISSUE_ID = "issue-1";

function translate(selector: (resources: unknown) => unknown, vars?: Record<string, unknown>) {
  const path: string[] = [];
  const recorder: unknown = new Proxy({}, {
    get: (_target, key) => {
      path.push(String(key));
      return recorder;
    },
  });
  selector(recorder);
  const leaf = path.pop()!;
  let node = en as unknown as Record<string, unknown>;
  for (const key of path) node = node[key] as Record<string, unknown>;
  const template = String(node[leaf] ?? "");
  return template.replace(/\{\{(\w+)\}\}/g, (_match, key) => String(vars?.[key] ?? ""));
}

vi.mock("../i18n", () => ({
  useT: () => ({ t: translate }),
}));

vi.mock("../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/issues/${id}` }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({
    queryKey: ["agents", "ws-1"],
    queryFn: async () => state.agents,
  }),
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueCommentsOptions: (issueId: string) => ({
    queryKey: ["comments", issueId],
    queryFn: async () => state.comments,
  }),
  issueTasksOptions: (issueId: string) => ({
    queryKey: ["tasks", issueId],
    queryFn: async () => state.tasks,
    enabled: true,
    staleTime: 30_000,
  }),
  issueKeys: {
    all: (wsId: string) => ["issues", wsId],
    timeline: (issueId: string) => ["timeline", issueId],
    tasks: (issueId: string) => ["tasks", issueId],
  },
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useCreateComment: () => ({ mutateAsync: state.createComment, isPending: false }),
  useUpdateIssue: () => ({ mutateAsync: state.updateIssue, isPending: false }),
}));

const TODO: MemberTodo = {
  todoKey: "todo-abc",
  kind: "choice",
  sourceIssue: "DEV-65",
  sourceId: "11111111-1111-7111-8111-111111111111",
  completionSignal: "done",
  handler: "幕僚长",
  problem: "The order list cannot fit every field.",
  action: "Pick the first column.",
  options: [
    { key: "amount", label: "Show amount first", consequence: "Status needs a detail view." },
    { key: "status", label: "Show status first", consequence: "Amount needs a detail view." },
  ],
};

const ISSUE = {
  id: ISSUE_ID,
  title: "Order list layout",
  description: "",
  status: "todo",
  status_category: "started",
  assignee_type: "member",
  assignee_id: USER_ID,
  updated_at: "2026-11-19T10:00:00Z",
} as unknown as Issue;

function chiefAgent() {
  return {
    id: CHIEF_ID,
    name: "幕僚长",
    archived_at: null,
    owner_id: USER_ID,
  };
}

function answerContent(answer: string, optionKey?: string, answerId = "answer-id") {
  return buildMemberAnswerContent({
    handlerName: "幕僚长",
    handlerId: CHIEF_ID,
    todo: TODO,
    answer: {
      todoKey: TODO.todoKey,
      answerId,
      answer,
      ...(optionKey ? { optionKey } : {}),
      answeredBy: USER_ID,
    },
    option: optionKey ? TODO.options.find((option) => option.key === optionKey) : undefined,
  });
}

function answerComment(
  id: string,
  answer: string,
  optionKey?: string,
) {
  return {
    id,
    author_type: "member",
    author_id: USER_ID,
    content: answerContent(answer, optionKey),
    created_at: "2026-11-19T10:00:00Z",
    parent_id: null,
    type: "comment",
  };
}

function chiefReply(id: string, parentId: string | null, content = "Received; sending it back to the owner.") {
  return {
    id,
    author_type: "agent",
    author_id: CHIEF_ID,
    content,
    created_at: "2026-11-19T11:00:00Z",
    parent_id: parentId,
    type: "comment",
  };
}

function chiefTask(triggerCommentId: string, status: string, agentId = CHIEF_ID) {
  return {
    id: `task-${status}-${triggerCommentId}`,
    agent_id: agentId,
    trigger_comment_id: triggerCommentId,
    status,
    created_at: "2026-11-19T11:00:00Z",
  };
}

let qc: QueryClient;

function renderCard(userId = USER_ID, issue = ISSUE) {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemberTodoCard issue={issue} todo={TODO} userId={userId} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  state.agents = [chiefAgent()];
  state.comments = [];
  state.tasks = [];
  state.createComment.mockReset();
  state.updateIssue.mockReset();
  state.updateIssue.mockResolvedValue({});
});

afterEach(() => {
  cleanup();
  qc?.clear();
});

describe("member todo card", () => {
  it("shows the question, options and one submit that names the receiver", async () => {
    renderCard();

    expect(await screen.findByText("Choose one")).toBeInTheDocument();
    expect(screen.getByText("The order list cannot fit every field.")).toBeInTheDocument();
    expect(screen.getByText("Status needs a detail view.")).toBeInTheDocument();

    const submit = screen.getByRole("button", { name: "Submit to chief of staff" });
    expect(submit).toBeDisabled();

    fireEvent.click(screen.getByLabelText(/Show amount first/));
    expect(submit).toBeEnabled();
  });

  it("submits the submitted option once with a stable comment id and completes the todo", async () => {
    let posted: { content: string; commentId?: string } | null = null;
    state.createComment.mockImplementation(async (input: { content: string; commentId?: string }) => {
      posted = input;
      return {
        id: input.commentId,
        content: input.content,
        trigger_outcomes: [
          { target_type: "agent", target_id: CHIEF_ID, status: "queued", reason_code: "ok" },
        ],
      };
    });

    renderCard();
    fireEvent.click(await screen.findByLabelText(/Show amount first/));
    fireEvent.click(screen.getByRole("button", { name: "Submit to chief of staff" }));

    await waitFor(() => expect(state.createComment).toHaveBeenCalledTimes(1));
    expect(posted!.content).toContain("Show amount first");
    expect(posted!.commentId).toBe(await memberAnswerCommentId(TODO.todoKey, USER_ID));
    await waitFor(() => expect(state.updateIssue).toHaveBeenCalledWith({ id: ISSUE_ID, status: "done" }));
    expect(await screen.findByText("Submitted — waiting for the chief of staff")).toBeInTheDocument();
  });

  it("displays a replayed answer from the server instead of this window's draft", async () => {
    state.createComment.mockImplementation(async (input: { content: string; commentId?: string }) => ({
      id: input.commentId,
      content: answerContent("saved in another window", "status"),
      trigger_outcomes: [
        { target_type: "agent", target_id: CHIEF_ID, status: "queued", reason_code: "ok" },
      ],
    }));

    renderCard();
    fireEvent.change(await screen.findByLabelText("Or write your own answer"), {
      target: { value: "draft typed here" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit to chief of staff" }));

    // The stored option key decides the rendered answer, even though this
    // window sent a custom draft.
    expect(await screen.findByText("You chose Show status first")).toBeInTheDocument();
    expect(screen.queryByText("draft typed here")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Submit to chief of staff" })).not.toBeInTheDocument();
  });

  it("does not complete the todo when the server returns no delivery receipt", async () => {
    state.createComment.mockImplementation(async (input: { content: string; commentId?: string }) => ({
      id: input.commentId,
      content: input.content,
      trigger_outcomes: [],
    }));

    renderCard();
    fireEvent.click(await screen.findByLabelText(/Show amount first/));
    fireEvent.click(screen.getByRole("button", { name: "Submit to chief of staff" }));

    expect(
      await screen.findByText("Submitted — no confirmation that the chief of staff received it"),
    ).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Notify again" })).toBeInTheDocument());
    expect(state.updateIssue).not.toHaveBeenCalled();
    expect(screen.getByText("Your answer was saved, but the chief of staff was not notified.")).toBeInTheDocument();
  });

  it("does not treat another agent's run as the chief receiving the answer", async () => {
    const answerId = await memberAnswerCommentId(TODO.todoKey, USER_ID);
    state.tasks = [chiefTask(answerId, "running", "another-agent")];
    state.createComment.mockImplementation(async (input: { content: string; commentId?: string }) => ({
      id: input.commentId,
      content: input.content,
      trigger_outcomes: [],
    }));

    renderCard();
    fireEvent.click(await screen.findByLabelText(/Show amount first/));
    fireEvent.click(screen.getByRole("button", { name: "Submit to chief of staff" }));

    expect(
      await screen.findByText("Submitted — no confirmation that the chief of staff received it"),
    ).toBeInTheDocument();
    expect(screen.queryByText("The chief of staff is handling this")).not.toBeInTheDocument();
    expect(state.updateIssue).not.toHaveBeenCalled();
  });

  it("shows a chief reply in the answer thread as the handling record", async () => {
    const answer = answerComment("answer-1", "Show amount first", "amount");
    state.comments = [answer, chiefReply("chief-reply", "answer-1")];

    renderCard();

    expect(await screen.findByText("Handled by the chief of staff")).toBeInTheDocument();
    expect(screen.getByText(/Show amount first/)).toBeInTheDocument();
    expect(screen.getByText(/Read and continued by 幕僚长:/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Submit to chief of staff" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Notify again" })).not.toBeInTheDocument();
  });

  it("recognizes the run started by a re-notification after a refresh", async () => {
    const answer = answerComment("answer-1", "Show amount first", "amount");
    const notice = {
      id: "notice-1",
      author_type: "member",
      author_id: USER_ID,
      content: buildMemberAnswerNotice({
        handlerName: "幕僚长",
        handlerId: CHIEF_ID,
        todo: TODO,
        answer: {
          todoKey: TODO.todoKey,
          answerId: "answer-id",
          answer: "Show amount first",
          answeredBy: USER_ID,
        },
        answerCommentId: "answer-1",
      }),
      created_at: "2026-11-19T11:00:00Z",
      parent_id: null,
      type: "comment",
    };
    state.comments = [answer, notice];
    state.tasks = [chiefTask("notice-1", "running")];

    renderCard();

    expect(await screen.findByText("The chief of staff is handling this")).toBeInTheDocument();
    expect(
      screen.queryByText("Submitted — no confirmation that the chief of staff received it"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Notify again" })).not.toBeInTheDocument();
  });

  it("does not count an unrelated later chief comment as handling", async () => {
    const answer = answerComment("answer-1", "Show amount first", "amount");
    state.comments = [answer, chiefReply("later-progress", null, "Unrelated progress update.")];

    renderCard();

    expect(
      await screen.findByText("Submitted — no confirmation that the chief of staff received it"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Handled by the chief of staff")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Notify again" })).toBeInTheDocument();
  });

  it("shows a failed chief run as retryable", async () => {
    const answer = answerComment("answer-1", "Show amount first", "amount");
    state.comments = [answer];
    state.tasks = [chiefTask("answer-1", "failed")];

    renderCard();

    expect(await screen.findByText("The chief of staff could not process this answer")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Notify again" })).toBeInTheDocument();
    expect(state.updateIssue).not.toHaveBeenCalled();
  });

  it("shows a completed run without a reply as missing a handling record", async () => {
    const answer = answerComment("answer-1", "Show amount first", "amount");
    state.comments = [answer];
    state.tasks = [chiefTask("answer-1", "completed")];

    renderCard();

    expect(
      await screen.findByText("The chief of staff run ended without a handling record"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Notify again" })).toBeInTheDocument();
  });

  it("does not offer the form to someone other than the assignee", async () => {
    renderCard("user-2");

    await waitFor(() => expect(state.comments).toEqual([]));
    expect(screen.queryByText("Choose one")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Submit to chief of staff" })).not.toBeInTheDocument();
  });

  it("renders a handling summary as plain text, not raw markdown or mention links", () => {
    expect(excerpt(
      "**收到** [DEV-64](mention://issue/11111111-1111-7111-8111-111111111111) 见 [说明](https://example.com) `done`",
    )).toBe("收到 DEV-64 见 说明 done");
    expect(excerpt("<!-- multica-member-todo {} -->正文")).toBe("正文");
  });

  it("skips examples that only repeat the option label", () => {
    expect(isRedundantExample("Word", "Word")).toBe(true);
    expect(isRedundantExample("例如：Word", "Word")).toBe(true);
    expect(isRedundantExample(" Example: Book title ", "Book")).toBe(false);
    expect(isRedundantExample(undefined, "Word")).toBe(true);
  });
});