"use client";

import { useQuery } from "@tanstack/react-query";
import {
  isAnswerableMemberTodo,
  issueColumnCategory,
  parseMemberTodo,
} from "@multica/core/issues";
import { memberTodoIssuesOptions } from "@multica/core/issues/queries";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import type { Issue } from "@multica/core/types";
import type { MemberTodo } from "@multica/core/issues";
import { useT } from "../i18n";
import { MemberTodoCard } from "./member-todo-card";

const HOME_LIMIT = 4;

function closed(issue: Issue): boolean {
  const category = issueColumnCategory(issue);
  return category === "done" || category === "closed";
}

/**
 * The first-screen surface for member interaction requests. It is a distinct
 * band above the ordinary task views, not a restyle of the regular issue card;
 * the normal list stays available below it.
 */
export function MemberTodosBand() {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const userId = useAuthStore((state) => state.user?.id ?? "");
  const { data: issues = [], isError } = useQuery(memberTodoIssuesOptions(wsId, userId));

  if (!userId || isError) return null;

  const rows = issues
    .filter((issue) =>
      issue.assignee_type === "member" && issue.assignee_id === userId)
    .map((issue) => ({ issue, todo: parseMemberTodo(issue.description) }))
    .filter((row): row is { issue: Issue; todo: MemberTodo } =>
      isAnswerableMemberTodo(row.todo) && !closed(row.issue))
    .sort((a, b) => b.issue.updated_at.localeCompare(a.issue.updated_at));

  if (rows.length === 0) return null;

  const visible = rows.slice(0, HOME_LIMIT);
  const extra = rows.length - visible.length;

  return (
    <section
      className="border-b border-border/60 bg-muted/10 px-4 py-4 md:px-6"
      aria-labelledby="member-todos-title"
    >
      <div className="mx-auto max-w-5xl">
        <div className="flex items-baseline gap-2">
          <h2 id="member-todos-title" className="text-title-sm font-medium">
            {t(($) => $.member_answer.band_title)}
          </h2>
          <span className="text-micro text-muted-foreground">{rows.length}</span>
        </div>
        <div className="mt-3 grid gap-3 md:grid-cols-2">
          {visible.map(({ issue, todo }) => (
            <MemberTodoCard
              key={issue.id}
              issue={issue}
              todo={todo}
              userId={userId}
              variant="home"
            />
          ))}
        </div>
        {extra > 0 ? (
          <p className="mt-2 text-micro text-muted-foreground">
            {t(($) => $.member_answer.band_more, { count: extra })}
          </p>
        ) : null}
      </div>
    </section>
  );
}