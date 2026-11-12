"use client";

import type { ReactNode } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Target } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import type { Goal } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { projectListOptions } from "@multica/core/projects/queries";
import { goalListPagesOptions } from "@multica/core/goals";
import { useActorName } from "@multica/core/workspace/hooks";
import { AppLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { CollectionPageState } from "../../layout/collection-page";
import { useT } from "../../i18n";
import { GoalStatusBadge } from "./goal-status-badge";

function GoalRow({
  goal,
  projectTitle,
}: {
  goal: Goal;
  projectTitle: string | null;
}) {
  const { t } = useT("goals");
  const wsPaths = useWorkspacePaths();
  const { getActorName } = useActorName();
  const ownerName =
    goal.owner_type && goal.owner_id ? getActorName(goal.owner_type, goal.owner_id) : null;

  return (
    <li className="border-b border-border/60 last:border-b-0">
      <AppLink
        href={wsPaths.goalDetail(goal.id)}
        className="flex items-start gap-3 px-4 py-3 transition-colors hover:bg-accent/40"
      >
        <Target className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-2">
            <span className="min-w-0 truncate text-body font-medium">{goal.title}</span>
            <GoalStatusBadge status={goal.status} />
          </div>
          <div className="mt-1 flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
            <span className="truncate">
              {projectTitle ?? t(($) => $.list.workspace_scope)}
            </span>
            {ownerName && (
              <>
                <span aria-hidden="true">·</span>
                <ActorAvatar
                  actorType={goal.owner_type ?? "member"}
                  actorId={goal.owner_id ?? ""}
                  size="xs"
                  profileLink={false}
                />
                <span className="truncate">{ownerName}</span>
              </>
            )}
          </div>
        </div>
      </AppLink>
    </li>
  );
}

/**
 * The goal collection used by both the workspace Goals page and a project's
 * Goals tab. Offset-paged: a "load more" row keeps long lists reachable
 * without a second page model, and a failed page can be retried in place.
 */
export function GoalList({
  wsId,
  projectId,
  status,
  emptyAction,
  compactEmpty = false,
  className,
}: {
  wsId: string;
  /** Restrict to one project. Omit for the workspace-wide list. */
  projectId?: string;
  status?: string | null;
  emptyAction?: ReactNode;
  compactEmpty?: boolean;
  className?: string;
}) {
  const { t } = useT("goals");
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const query = useInfiniteQuery(
    goalListPagesOptions(wsId, { projectId: projectId ?? null, status: status ?? null }),
  );
  const goals = query.data?.pages.flatMap((page) => page.goals) ?? [];

  if (query.isLoading) {
    return (
      <div className={className}>
        {Array.from({ length: 4 }, (_, index) => (
          <div key={index} className="flex items-center gap-3 border-b border-border/60 px-4 py-3.5">
            <Skeleton className="size-4 rounded-sm" />
            <div className="space-y-1.5">
              <Skeleton className="h-4 w-52" />
              <Skeleton className="h-3 w-32" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  if (goals.length === 0) {
    return (
      <CollectionPageState
        icon={Target}
        size={compactEmpty ? "compact" : "default"}
        title={t(($) => $.list.empty_title)}
        description={t(($) => $.list.empty_description)}
        actions={emptyAction}
        className={className}
      />
    );
  }

  return (
    <div className={className}>
      <ul aria-label={t(($) => $.list.title)}>
        {goals.map((goal) => (
          <GoalRow
            key={goal.id}
            goal={goal}
            projectTitle={
              goal.project_id
                ? (projects.find((project) => project.id === goal.project_id)?.title ?? null)
                : null
            }
          />
        ))}
      </ul>
      {query.isFetchNextPageError ? (
        <div className="flex items-center justify-center gap-2 py-3">
          <span className="text-caption text-destructive">{t(($) => $.list.load_more_failed)}</span>
          <Button variant="ghost" size="sm" onClick={() => void query.fetchNextPage()}>
            {t(($) => $.list.retry)}
          </Button>
        </div>
      ) : query.hasNextPage ? (
        <div className="flex justify-center py-3">
          <Button
            variant="outline"
            size="sm"
            disabled={query.isFetchingNextPage}
            aria-busy={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            {t(($) => $.list.load_more)}
          </Button>
        </div>
      ) : null}
    </div>
  );
}