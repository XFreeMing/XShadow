// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  buildMemberAnswerContent,
  buildMemberAnswerNotice,
  escapeMarkerJson,
  findChiefHandlingComment,
  findMemberAnswerNotices,
  findMemberTodoAnswer,
  isAnswerableMemberTodo,
  latestMemberComment,
  MEMBER_ANSWER_MARKER,
  memberAnswerCommentId,
  MEMBER_TODO_HANDLER,
  MEMBER_TODO_MARKER,
  parseMemberAnswer,
  parseMemberNotice,
  parseMemberTodo,
  type MemberAnswerComment,
} from "./member-todo";

function todoBody(marker: Record<string, unknown>, visible = "问题\n---\n## 技术附录") {
  return `${visible}\n<!-- ${MEMBER_TODO_MARKER} ${JSON.stringify(marker)} -->`;
}

const baseMarker = {
  todo_key: "todo-abc",
  kind: "choice",
  source_issue: "DEV-65",
  source_id: "11111111-1111-7111-8111-111111111111",
  request: "order-first",
  completion_signal: "done",
  handler: "幕僚长",
  problem: "订单一屏放不下所有信息。",
  why_you: "只有你每天看这个列表。",
  action: "选出第一列。",
  options: [
    { key: "amount", label: "先显示金额", consequence: "状态要点开详情。", example: "回复“金额”" },
    { key: "status", label: "先显示状态", consequence: "金额要点开详情。" },
  ],
};

function memberComment(overrides: Partial<MemberAnswerComment> = {}): MemberAnswerComment {
  return {
    id: "c1",
    author_type: "member",
    author_id: "user-1",
    content: "随便说一句",
    created_at: "2026-11-19T10:00:00Z",
    ...overrides,
  };
}

describe("member todo markers", () => {
  it("reads an atomic option block only from the marker", () => {
    const todo = parseMemberTodo(todoBody(baseMarker));
    expect(todo).toMatchObject({
      todoKey: "todo-abc",
      kind: "choice",
      completionSignal: "done",
      handler: "幕僚长",
      problem: "订单一屏放不下所有信息。",
    });
    expect(todo?.options.map((option) => option.key)).toEqual(["amount", "status"]);
    expect(isAnswerableMemberTodo(todo)).toBe(true);
  });

  it("never infers options from the readable Markdown", () => {
    const prose = "可选的做法：\n- **先显示金额**：状态要点开详情。\n- **先显示状态**：金额要点开详情。";
    const todo = parseMemberTodo(todoBody({ ...baseMarker, options: undefined }, prose));
    expect(todo?.options).toEqual([]);
    expect(isAnswerableMemberTodo(todo)).toBe(false);
  });

  it("rejects a partial or duplicated option block as a whole", () => {
    for (const options of [
      [{ key: "amount", label: "先显示金额" }],
      [
        { key: "amount", label: "金额", consequence: "a" },
        { key: "amount", label: "状态", consequence: "b" },
      ],
      [
        { key: "amount", label: "金额", consequence: "a" },
        { key: "bad key", label: "状态", consequence: "b" },
      ],
    ]) {
      const todo = parseMemberTodo(todoBody({ ...baseMarker, options }));
      expect(todo?.options).toEqual([]);
      expect(isAnswerableMemberTodo(todo)).toBe(false);
    }
  });

  it("keeps historical cards readable but not answerable", () => {
    const old = {
      todo_key: "todo-old",
      kind: "decision",
      source_issue: "DEV-58",
      request: "pdf",
    };
    const todo = parseMemberTodo(todoBody(old));
    expect(todo?.todoKey).toBe("todo-old");
    expect(todo?.handler).toBe(MEMBER_TODO_HANDLER);
    expect(isAnswerableMemberTodo(todo)).toBe(false);
  });

  it("routes to the configured chief even when a marker names another handler", () => {
    const todo = parseMemberTodo(todoBody({ ...baseMarker, handler: "项目经理" }));
    expect(todo?.handler).toBe("项目经理");
    // The recorded value is display-only; the resolver and answer builder use
    // the fixed product convention.
    const answer = {
      todoKey: todo!.todoKey,
      answerId: "88888888-8888-5888-8888-888888888888",
      answer: "先显示金额",
      answeredBy: "user-1",
    };
    const content = buildMemberAnswerContent({
      handlerName: MEMBER_TODO_HANDLER,
      handlerId: "22222222-2222-7222-8222-222222222222",
      todo: todo!,
      answer,
      option: todo!.options[0],
    });
    expect(content).toContain("[@幕僚长](mention://agent/22222222-2222-7222-8222-222222222222)");
    expect(content).not.toContain("项目经理");
    expect(parseMemberAnswer(content)?.todoKey).toBe("todo-abc");
  });

  it("ignores unrelated bodies and unreadable markers", () => {
    expect(parseMemberTodo(null)).toBeNull();
    expect(parseMemberTodo("普通任务的正文")).toBeNull();
    expect(parseMemberTodo(`<!-- ${MEMBER_TODO_MARKER} {not json} -->`)).toBeNull();
    expect(parseMemberTodo(`<!-- ${MEMBER_TODO_MARKER} {"kind":"choice"} -->`)).toBeNull();
  });
});

describe("member answers", () => {
  it("keeps a custom answer verbatim, including characters a comment cannot carry", () => {
    const todo = parseMemberTodo(todoBody(baseMarker))!;
    const custom = "先显示金额 -- 但金额大于 1 万时用红色 --> 可以吗？";
    const content = buildMemberAnswerContent({
      handlerName: MEMBER_TODO_HANDLER,
      handlerId: "22222222-2222-7222-8222-222222222222",
      todo,
      answer: {
        todoKey: todo.todoKey,
        answerId: "88888888-8888-5888-8888-888888888888",
        answer: custom,
        answeredBy: "user-1",
      },
    });
    expect(content).toContain(custom);
    expect(content).not.toContain("--> 可以吗？ -->");
    expect(parseMemberAnswer(content)?.answer).toBe(custom);
    expect(escapeMarkerJson('{"a":"--"}')).toBe('{"a":"\\u002d\\u002d"}');
  });

  it("finds only the assignee's marked answer, not an unrelated comment", () => {
    const todo = parseMemberTodo(todoBody(baseMarker))!;
    const content = buildMemberAnswerContent({
      handlerName: MEMBER_TODO_HANDLER,
      handlerId: "22222222-2222-7222-8222-222222222222",
      todo,
      answer: {
        todoKey: todo.todoKey,
        answerId: "88888888-8888-5888-8888-888888888888",
        answer: "先显示金额",
        optionKey: "amount",
        answeredBy: "user-1",
      },
      option: todo.options[0],
    });
    const comments = [
      memberComment({ id: "c0", content: "我看看" }),
      memberComment({ id: "c1", content }),
      memberComment({ id: "c2", content, author_id: "user-2" }),
    ];
    expect(findMemberTodoAnswer(comments, todo.todoKey, "user-1")?.answer.answer).toBe("先显示金额");
    expect(findMemberTodoAnswer(comments, todo.todoKey, "user-2")).toBeNull();
    expect(findMemberTodoAnswer(comments.map((c) => ({ ...c, content: "我看看" })), todo.todoKey, "user-1")).toBeNull();
  });

  it("shows the legacy reply+done comment only through the explicit fallback", () => {
    const comments = [
      memberComment({ id: "c1", created_at: "2026-11-19T10:00:00Z", content: "选金额" }),
      memberComment({ id: "c2", created_at: "2026-11-19T11:00:00Z", content: "补充一句" }),
      memberComment({ id: "c3", author_id: "user-2", content: "别人" }),
    ];
    expect(latestMemberComment(comments, "user-1")?.id).toBe("c2");
    expect(latestMemberComment(comments, "user-3")).toBeNull();
  });

  it("counts only a chief reply in the answer thread as handling", () => {
    const comments: MemberAnswerComment[] = [
      memberComment({ id: "answer", content: "答复", created_at: "2026-11-19T10:00:00Z" }),
      memberComment({
        id: "chief-elsewhere", author_type: "agent", author_id: "chief-1",
        content: "之后的一条普通进度评论", created_at: "2026-11-19T11:00:00Z",
      }),
      memberComment({
        id: "other-agent", author_type: "agent", author_id: "other-1",
        content: "别的智能体回复", parent_id: "answer", created_at: "2026-11-19T11:00:00Z",
      }),
    ];
    expect(findChiefHandlingComment(comments, ["answer"], ["chief-1"])).toBeNull();
    comments.push(memberComment({
      id: "chief-reply", author_type: "agent", author_id: "chief-1",
      content: "已收到，派回研发继续", parent_id: "answer", created_at: "2026-11-19T11:00:00Z",
    }));
    expect(findChiefHandlingComment(comments, ["answer"], ["chief-1"])?.id).toBe("chief-reply");
  });

  it("recognizes a re-notification thread after refresh", () => {
    const todo = parseMemberTodo(todoBody(baseMarker))!;
    const answer = {
      todoKey: todo.todoKey,
      answerId: "88888888-8888-5888-8888-888888888888",
      answer: "先显示金额",
      answeredBy: "user-1",
    };
    const notice = buildMemberAnswerNotice({
      handlerName: MEMBER_TODO_HANDLER,
      handlerId: "22222222-2222-7222-8222-222222222222",
      todo,
      answer,
      answerCommentId: "answer-comment",
    });
    const parsed = parseMemberNotice(notice);
    expect(parsed).toEqual({
      todoKey: todo.todoKey,
      answerId: answer.answerId,
      answerCommentId: "answer-comment",
    });
    const comments = [
      memberComment({ id: "answer-comment", author_type: "member", author_id: "user-1", content: "答复" }),
      memberComment({ id: "notice-comment", author_type: "member", author_id: "user-1", content: notice }),
      memberComment({
        id: "chief-notice-reply", author_type: "agent", author_id: "chief-1",
        content: "收到补发，已接续", parent_id: "notice-comment", created_at: "2026-11-19T12:00:00Z",
      }),
    ];
    expect(findMemberAnswerNotices(comments, todo.todoKey, answer.answerId).map((row) => row.comment.id))
      .toEqual(["notice-comment"]);
    expect(findChiefHandlingComment(
      comments,
      ["answer-comment", "notice-comment"],
      ["chief-1"],
    )?.id).toBe("chief-notice-reply");
  });

  it("builds a stable per-todo, per-member answer id", async () => {
    const first = await memberAnswerCommentId("todo-abc", "user-1");
    const second = await memberAnswerCommentId("todo-abc", "user-1");
    const otherTodo = await memberAnswerCommentId("todo-def", "user-1");
    const otherUser = await memberAnswerCommentId("todo-abc", "user-2");
    expect(first).toBe(second);
    expect(first).not.toBe(otherTodo);
    expect(first).not.toBe(otherUser);
    expect(first).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });

  it("sends a re-notification without a second answer marker", () => {
    const todo = parseMemberTodo(todoBody(baseMarker))!;
    const notice = buildMemberAnswerNotice({
      handlerName: MEMBER_TODO_HANDLER,
      handlerId: "22222222-2222-7222-8222-222222222222",
      todo,
      answer: {
        todoKey: todo.todoKey,
        answerId: "88888888-8888-5888-8888-888888888888",
        answer: "先显示金额",
      },
      answerCommentId: "answer-comment",
    });
    expect(notice).toContain("mention://agent/22222222-2222-7222-8222-222222222222");
    expect(notice).toContain("先显示金额");
    expect(parseMemberAnswer(notice)).toBeNull();
    expect(notice).not.toContain(MEMBER_ANSWER_MARKER);
  });
});