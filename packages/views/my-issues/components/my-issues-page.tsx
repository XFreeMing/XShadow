"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { activeMemberMentions, inboxListOptions } from "@multica/core/inbox/queries";
import { useIssueStatuses } from "@multica/core/issue-statuses";
import { useWorkspacePaths } from "@multica/core/paths";
import { CircleUser, ListTodo, Plus } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { useAuthStore } from "@multica/core/auth";
import { myIssuesRelationFromScope } from "@multica/core/issues/stores/my-issues-view-store";
import { CollectionPageState } from "../../layout/collection-page";
import { PageHeader } from "../../layout/page-header";
import { IssueSurface } from "../../issues/surface/issue-surface";
import { IssueViewHeader } from "../../issue-views/view-header";
import { MemberTodosBand } from "../../member-todos/member-todos-band";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { useMyIssuesScope } from "../use-my-issues-scope";

const MY_BASE = { kind: "my" } as const;

function MentionedIssues({ userId }: { userId: string }) {
  const workspaceId = useWorkspaceId();
  const catalog = useIssueStatuses(workspaceId);
  const paths = useWorkspacePaths();
  const { t } = useT("inbox");
  const { data = [] } = useQuery(inboxListOptions(workspaceId));
  const mentions = activeMemberMentions(data, userId, workspaceId, catalog);
  if (mentions.length === 0) return null;

  return (
    <section className="border-b border-border/60 px-4 py-2 md:px-6" aria-labelledby="my-issue-mentions">
      <h2 id="my-issue-mentions" className="text-title-sm font-medium">{t(($) => $.filters.type_mention)}</h2>
      <ul className="max-h-32 overflow-y-auto">
        {mentions.map(({ id, title, commentId }) => (
          <li key={id}>
            <AppLink
              className="block truncate py-1 text-body hover:underline"
              href={paths.issueDetail(id) + (commentId ? `#comment-${commentId}` : "")}
            >
              {title}
            </AppLink>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function MyIssuesPage() {
  const { t } = useT("my-issues");
  const { t: tIssues } = useT("issues");
  const user = useAuthStore((s) => s.user);
  const scope = useMyIssuesScope();

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      {user ? (
        <>
          <MemberTodosBand />
          <MentionedIssues userId={user.id} />
          <IssueSurface
            scope={{
              type: "my",
              userId: user.id,
              relation: myIssuesRelationFromScope(scope),
            }}
            modes={["board", "list", "table", "grid", "swimlane"]}
            batchToolbar="list"
            renderHeader={({ controller }) => <IssueViewHeader controller={controller} base={MY_BASE} />}
            renderEmpty={({ controller }) => (
              <CollectionPageState
                icon={CircleUser}
                title={t(($) => $.page.empty_title)}
                description={t(($) => $.page.empty_description)}
                actions={
                  <Button size="sm" onClick={() => controller.openCreateIssue()}>
                    <Plus aria-hidden="true" />
                    {tIssues(($) => $.views.new_issue)}
                  </Button>
                }
              />
            )}
          />
        </>
      ) : (
        <PageHeader>
          <ListTodo className="size-4 text-muted-foreground" />
          <h1 className="text-body font-medium">{t(($) => $.page.breadcrumb)}</h1>
        </PageHeader>
      )}
    </div>
  );
}
