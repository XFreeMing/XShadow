"use client";

import { useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import {
  Check,
  FileQuestion,
  Pencil,
  RotateCcw,
  Trash2,
} from "@multica/ui/icons";
import { toast } from "sonner";
import {
  goalDetailOptions,
  goalIssuesPagesOptions,
  useDeleteGoal,
  useUnlinkGoalIssue,
  useUpdateGoal,
} from "@multica/core/goals";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { projectListOptions } from "@multica/core/projects/queries";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { AppLink, useNavigation } from "../../navigation";
import { CollectionPageState } from "../../layout/collection-page";
import { OBJECT_PAGE_BODY, ObjectPage } from "../../layout/object-page";
import { useT } from "../../i18n";
import { GoalStatusBadge } from "./goal-status-badge";
import { GoalFormDialog } from "./goal-form-dialog";
import { GoalLinkTaskDialog } from "./goal-link-task-dialog";
import { GoalOwnerPicker, type GoalOwnerValue } from "./goal-owner-picker";
import { GoalPlanSection } from "./goal-plan-section";
import { GoalProblemSolvingSection } from "./goal-problem-solving";
import { GoalOutcomeGallery } from "./goal-outcome-gallery";
import { GoalTaskPrototypes } from "./goal-task-prototypes";
import { isGoalCompleted, isGoalUnavailableError } from "../status";

const LAYOUT_ID = "multica_goal_detail_layout";
const GOAL_PAGE_BODY = "mx-auto w-full max-w-[1280px] px-6 py-5";

function PropRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="-mx-2 flex min-h-8 items-center gap-2 rounded-md px-2 transition-colors hover:bg-accent/50">
      <span className="w-16 shrink-0 text-caption text-muted-foreground">{label}</span>
      <div className="flex min-w-0 flex-1 items-center gap-1.5 truncate text-caption">
        {children}
      </div>
    </div>
  );
}

function BriefCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-1 rounded-lg border bg-card px-3.5 py-2.5">
      <h2 className="text-caption font-medium text-muted-foreground">{title}</h2>
      {children}
    </section>
  );
}

export function GoalDetailPage({ goalId }: { goalId: string }) {
  const { t } = useT("goals");
  const { t: tLayout } = useT("layout");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const router = useNavigation();

  const goalQuery = useQuery(goalDetailOptions(wsId, goalId));
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const issuesQuery = useInfiniteQuery(goalIssuesPagesOptions(wsId, goalId));

  const updateGoal = useUpdateGoal();
  const deleteGoal = useDeleteGoal();
  const unlinkIssue = useUnlinkGoalIssue();

  const [editOpen, setEditOpen] = useState(false);
  const [linkOpen, setLinkOpen] = useState(false);
  const [completeOpen, setCompleteOpen] = useState(false);
  const [reopenOpen, setReopenOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const goal = goalQuery.data && goalQuery.data.id ? goalQuery.data : null;
  // React Query keeps the last successful payload after a refetch fails. A
  // terminal 4xx means this viewer can no longer see the goal, so the cached
  // title must stop appearing in the breadcrumb; offline/5xx keep it.
  const unavailable = goalQuery.isError && isGoalUnavailableError(goalQuery.error);
  const issuesUnavailable = issuesQuery.isError && isGoalUnavailableError(issuesQuery.error);
  const issuePages = issuesUnavailable ? undefined : issuesQuery.data?.pages;
  const linkedIssues = issuePages?.flatMap((page) => page.issues) ?? [];
  const linkedIssueCount = issuePages?.[0]?.total ?? 0;
  const project = goal?.project_id
    ? (projects.find((candidate) => candidate.id === goal.project_id) ?? null)
    : null;

  const segments = [{ href: wsPaths.goals(), label: tLayout(($) => $.nav.goals) }];
  const leaf = unavailable ? (
    <span className="min-w-0 truncate text-muted-foreground">
      {tLayout(($) => $.tab.goal)}
    </span>
  ) : goal ? (
    <span className="min-w-0 truncate">{goal.title}</span>
  ) : (
    <Skeleton className="h-4 w-40" />
  );
  const shell = (children: ReactNode, actions?: ReactNode, panel?: ReactNode) => (
    <ObjectPage layoutId={LAYOUT_ID} segments={segments} leaf={leaf} actions={actions} panel={panel}>
      {children}
    </ObjectPage>
  );

  if (goalQuery.isLoading) {
    return shell(
      <div className={cn(OBJECT_PAGE_BODY, "space-y-8")}>
        <Skeleton className="h-6 w-64" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-5/6" />
        <Skeleton className="h-40 w-full" />
      </div>,
    );
  }

  if (goalQuery.isError || !goal) {
    const gone = unavailable;
    return shell(
      <CollectionPageState
        icon={FileQuestion}
        title={gone ? t(($) => $.detail.not_found) : t(($) => $.detail.load_failed)}
        actions={
          gone ? (
            <AppLink
              href={wsPaths.goals()}
              className={buttonVariants({ variant: "outline", size: "sm" })}
            >
              {t(($) => $.detail.back_to_goals)}
            </AppLink>
          ) : (
            <Button variant="outline" size="sm" onClick={() => void goalQuery.refetch()}>
              {t(($) => $.list.retry)}
            </Button>
          )
        }
      />,
    );
  }

  const completed = isGoalCompleted(goal.status);

  const handleOwnerChange = (next: GoalOwnerValue) => {
    updateGoal.mutate(
      { id: goal.id, owner_type: next.type, owner_id: next.id },
      { onError: () => toast.error(t(($) => $.form.save_failed)) },
    );
  };

  const handleSetStatus = (status: "completed" | "planned") => {
    updateGoal.mutate(
      { id: goal.id, status },
      {
        onSuccess: () => {
          toast.success(
            status === "completed"
              ? t(($) => $.toast.completed)
              : t(($) => $.toast.reopened),
          );
          setCompleteOpen(false);
          setReopenOpen(false);
        },
        onError: () => toast.error(t(($) => $.form.save_failed)),
      },
    );
  };

  const handleDelete = () => {
    deleteGoal.mutate(goal.id, {
      onSuccess: () => {
        toast.success(t(($) => $.toast.deleted));
        setDeleteOpen(false);
        router.push(wsPaths.goals());
      },
      onError: () => toast.error(t(($) => $.confirm.delete_failed)),
    });
  };

  const handleUnlink = (issueId: string) => {
    unlinkIssue.mutate(
      { goalId: goal.id, issueId },
      {
        onSuccess: () => toast.success(t(($) => $.link.unlinked)),
        onError: () => toast.error(t(($) => $.detail.unlink_failed)),
      },
    );
  };

  const actions = (
    <>
      <Button size="sm" variant="outline" onClick={() => setEditOpen(true)}>
        <Pencil className="size-3.5" />
        {t(($) => $.detail.edit)}
      </Button>
      {completed ? (
        <Button size="sm" variant="outline" onClick={() => setReopenOpen(true)}>
          <RotateCcw className="size-3.5" />
          {t(($) => $.detail.reopen)}
        </Button>
      ) : (
        <Button size="sm" onClick={() => setCompleteOpen(true)}>
          <Check className="size-3.5" />
          {t(($) => $.detail.mark_completed)}
        </Button>
      )}
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label={t(($) => $.detail.delete)}
        className="text-muted-foreground"
        onClick={() => setDeleteOpen(true)}
      >
        <Trash2 />
      </Button>
    </>
  );

  const panel = (
    <div className="space-y-1">
      <PropRow label={t(($) => $.form.status)}>
        <GoalStatusBadge status={goal.status} />
      </PropRow>
      <PropRow label={t(($) => $.form.project)}>
        <span className={cn("truncate", !project && "text-muted-foreground")}>
          {project?.title ?? t(($) => $.form.no_project)}
        </span>
      </PropRow>
      <PropRow label={t(($) => $.form.owner)}>
        <GoalOwnerPicker
          value={{ type: goal.owner_type, id: goal.owner_id }}
          onChange={handleOwnerChange}
        />
      </PropRow>
    </div>
  );

  return shell(
    <div className={cn(GOAL_PAGE_BODY, "space-y-5")}>
      {/* Compact 3-column Goal Brief */}
      <div className="grid gap-3 md:grid-cols-3">
        <BriefCard title={t(($) => $.detail.intent)}>
          <p className="whitespace-pre-wrap text-label text-foreground">
            {goal.intent || t(($) => $.detail.no_intent)}
          </p>
        </BriefCard>

        <BriefCard title={t(($) => $.detail.scope)}>
          <p className="whitespace-pre-wrap text-label text-muted-foreground">
            {goal.scope || t(($) => $.detail.no_scope)}
          </p>
        </BriefCard>

        <BriefCard title={t(($) => $.detail.acceptance_criteria)}>
          <p className="whitespace-pre-wrap text-label text-muted-foreground">
            {goal.acceptance_criteria || t(($) => $.detail.no_criteria)}
          </p>
        </BriefCard>
      </div>

      <GoalProblemSolvingSection key={`problem-solving-${goal.id}`}
        wsId={wsId} goalId={goal.id} linkedIssues={linkedIssues}
        issueHref={(issueID) => wsPaths.issueDetail(issueID)}
        onLinkTask={() => setLinkOpen(true)} />

      <GoalPlanSection
        key={`goal-plan-${goal.id}`}
        goal={goal}
        issues={{
          linkedIssues,
          issuesLoading: issuesQuery.isLoading,
          issuesError: issuesQuery.isError,
          hasNextPage: !issuesUnavailable && Boolean(issuesQuery.hasNextPage),
          isFetchingNextPage: issuesQuery.isFetchingNextPage,
          isFetchNextPageError: issuesQuery.isFetchNextPageError,
          unlinkingIssueId: unlinkIssue.isPending ? (unlinkIssue.variables?.issueId ?? null) : null,
          issueHref: (issue) => wsPaths.issueDetail(issue.identifier || issue.id),
          onRetryIssues: () => void issuesQuery.refetch(),
          onFetchNextPage: () => void issuesQuery.fetchNextPage(),
          onOpenLinkDialog: () => setLinkOpen(true),
          onUnlinkIssue: handleUnlink,
        }}
      />

      <GoalOutcomeGallery key={goal.id} wsId={wsId} goalId={goal.id} />
      {!issuesQuery.isError && <GoalTaskPrototypes key={`prototypes-${goal.id}`} issues={linkedIssues} />}

      {!goal.project_id ? (
        <p className="rounded-md bg-muted px-3 py-2 text-caption text-muted-foreground">
          {t(($) => $.detail.workspace_goal_visible)}
        </p>
      ) : null}

      <GoalFormDialog
        open={editOpen}
        onOpenChange={setEditOpen}
        goal={goal}
        linkedIssueCount={linkedIssueCount}
      />
      <GoalLinkTaskDialog
        open={linkOpen}
        onOpenChange={setLinkOpen}
        goal={goal}
        linkedIssueIds={linkedIssues.map((issue) => issue.id)}
      />

      <AlertDialog
        open={completeOpen}
        onOpenChange={(open) => {
          if (!updateGoal.isPending) setCompleteOpen(open);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.confirm.complete_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.confirm.complete_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={updateGoal.isPending}>
              {t(($) => $.form.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={updateGoal.isPending}
              aria-busy={updateGoal.isPending}
              onClick={() => handleSetStatus("completed")}
            >
              {updateGoal.isPending && <Spinner className="size-3.5" aria-hidden="true" />}
              {t(($) => $.confirm.complete_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={reopenOpen}
        onOpenChange={(open) => {
          if (!updateGoal.isPending) setReopenOpen(open);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.confirm.reopen_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.confirm.reopen_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={updateGoal.isPending}>
              {t(($) => $.form.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={updateGoal.isPending}
              aria-busy={updateGoal.isPending}
              onClick={() => handleSetStatus("planned")}
            >
              {updateGoal.isPending && <Spinner className="size-3.5" aria-hidden="true" />}
              {t(($) => $.confirm.reopen_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={deleteOpen}
        onOpenChange={(open) => {
          if (!deleteGoal.isPending) setDeleteOpen(open);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.confirm.delete_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.confirm.delete_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteGoal.isPending}>
              {t(($) => $.form.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={deleteGoal.isPending}
              aria-busy={deleteGoal.isPending}
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={handleDelete}
            >
              {deleteGoal.isPending ? (
                <Spinner className="size-3.5" aria-hidden="true" />
              ) : null}
              {deleteGoal.isPending
                ? t(($) => $.confirm.deleting)
                : t(($) => $.confirm.delete_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>,
    actions,
    panel,
  );
}
