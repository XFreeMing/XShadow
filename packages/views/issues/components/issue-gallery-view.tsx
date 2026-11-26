"use client";

import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useWorkspaceId } from "@multica/core/hooks";
import { childIssueProgressOptions } from "@multica/core/issues/queries";
import { propertyListOptions } from "@multica/core/properties";
import { projectListOptions } from "@multica/core/projects/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import type { Issue, IssueProperty, IssueTableQuerySpec, Project } from "@multica/core/types";
import { formatDateOnly } from "@multica/core/issues/date";
import { GalleryCardGrid } from "../../layout/gallery-card-grid";
import { useT, useLocale } from "../../i18n";
import { resolveClickIntent, useIntentNavigate } from "../../navigation";
import { useIssuePeekClick } from "../surface/peek-context";
import { IssueActionsContextMenu } from "../actions";
import { useStatusLabel } from "../utils/status-label";
import { CustomPropertyValueDisplay } from "./pickers/custom-property-picker";
import { resolveCardCustomProperties } from "../utils/card-custom-properties";
import { ComputedPropertyViewBoundary } from "./computed-property-view-boundary";
import { PriorityIcon } from "./priority-icon";
import { ProgressRing } from "./progress-ring";
import { StatusIcon } from "./status-icon";
import { LabelChip } from "../../labels/label-chip";
import { ProjectIcon } from "../../projects/components/project-icon";
import { ActorAvatar } from "../../common/actor-avatar";
import { PropertyIcon } from "../../common/property-icon";
import type { ChildProgress } from "./list-row";
import { useIssueTableFlatRowPages } from "./use-issue-table-flat-row-pages";

const GALLERY_PAGE_LIMIT = 50;

export interface IssueGalleryViewProps {
  serverQuery: IssueTableQuerySpec;
}

function formatShortDate(date: string, locale: string): string {
  return formatDateOnly(date, { month: "short", day: "numeric" }, locale);
}

function IssueGalleryCard({
  issue,
  childProgress,
  project,
  workspaceProperties,
}: {
  issue: Issue;
  childProgress?: ChildProgress;
  project?: Project;
  workspaceProperties: IssueProperty[];
}) {
  const locale = useLocale();
  const { t } = useT("issues");
  const statusLabel = useStatusLabel(useWorkspaceId());
  const paths = useWorkspacePaths();
  const handlePeekClick = useIssuePeekClick();
  const intentNavigate = useIntentNavigate();
  const storeProperties = useViewStore((s) => s.cardProperties);
  const cardPropertyIds = useViewStore((s) => s.cardPropertyIds);
  const cardCustomProperties = resolveCardCustomProperties(
    cardPropertyIds,
    workspaceProperties,
    issue,
  );
  const labels = issue.labels ?? [];

  const openIssue = useCallback(
    (event: React.MouseEvent) => {
      if (handlePeekClick(issue.id, event)) {
        if (event.shiftKey) window.getSelection()?.removeAllRanges();
        return;
      }
      intentNavigate(
        paths.issueDetail(issue.id),
        resolveClickIntent(event),
        issue.identifier,
      );
    },
    [handlePeekClick, intentNavigate, issue.id, issue.identifier, paths],
  );

  const rows: { key: string; label: string; value: React.ReactNode }[] = [
    {
      key: "status",
      label: t(($) => $.table.columns.status),
      value: (
        <span className="inline-flex items-center gap-1">
          <StatusIcon status={issue.status} category={issue.status_category} />
          {statusLabel(issue.status)}
        </span>
      ),
    },
  ];

  if (storeProperties.priority && issue.priority !== "none") {
    rows.push({
      key: "priority",
      label: t(($) => $.display.card_priority),
      value: (
        <span className="inline-flex items-center gap-1">
          <PriorityIcon priority={issue.priority} />
          {t(($) => $.priority[issue.priority])}
        </span>
      ),
    });
  }
  if (storeProperties.assignee && issue.assignee_type && issue.assignee_id) {
    rows.push({
      key: "assignee",
      label: t(($) => $.display.card_assignee),
      value: (
        <ActorAvatar
          actorType={issue.assignee_type}
          actorId={issue.assignee_id}
          size="sm"
          enableHoverCard
        />
      ),
    });
  }
  if (storeProperties.startDate && issue.start_date) {
    rows.push({
      key: "start_date",
      label: t(($) => $.display.card_start_date),
      value: formatShortDate(issue.start_date, locale),
    });
  }
  if (storeProperties.dueDate && issue.due_date) {
    rows.push({
      key: "due_date",
      label: t(($) => $.display.card_due_date),
      value: formatShortDate(issue.due_date, locale),
    });
  }
  if (storeProperties.project && project) {
    rows.push({
      key: "project",
      label: t(($) => $.display.card_project),
      value: (
        <span className="inline-flex items-center gap-1 truncate">
          <ProjectIcon project={project} size="sm" />
          <span className="truncate">{project.title}</span>
        </span>
      ),
    });
  }
  if (storeProperties.childProgress && childProgress) {
    rows.push({
      key: "child_progress",
      label: t(($) => $.display.card_child_progress),
      value: (
        <span className="inline-flex items-center gap-1">
          <ProgressRing done={childProgress.done} total={childProgress.total} size={14} />
          <span className="tabular-nums text-caption text-muted-foreground">
            {childProgress.done}/{childProgress.total}
          </span>
        </span>
      ),
    });
  }
  if (storeProperties.labels && labels.length > 0) {
    rows.push({
      key: "labels",
      label: t(($) => $.display.card_labels),
      value: (
        <span className="flex flex-wrap gap-1">
          {labels.map((label) => (
            <LabelChip key={label.id} label={label} />
          ))}
        </span>
      ),
    });
  }
  for (const property of cardCustomProperties) {
    rows.push({
      key: property.id,
      label: property.name,
      value: (
        <span className="inline-flex items-center gap-1">
          <PropertyIcon property={property} className="size-3" />
          <CustomPropertyValueDisplay
            property={property}
            value={issue.properties?.[property.id]}
            issueId={issue.id}
          />
        </span>
      ),
    });
  }

  return (
    <IssueActionsContextMenu issue={issue}>
      <article
        data-issue-id={issue.id}
        className="flex flex-col rounded-md border border-border bg-background p-3 shadow-2xs transition-colors hover:border-foreground/30"
      >
        <button
          type="button"
          className="mb-2 block w-full truncate text-left text-body font-semibold hover:underline"
          onClick={openIssue}
        >
          {issue.title}
        </button>
        <dl className="space-y-1.5">
          {rows.map((row) => (
            <div key={row.key} className="flex flex-col gap-0.5 text-caption">
              <dt className="text-muted-foreground">{row.label}</dt>
              <dd className="truncate text-foreground">{row.value}</dd>
            </div>
          ))}
        </dl>
      </article>
    </IssueActionsContextMenu>
  );
}

export function IssueGalleryView({ serverQuery }: IssueGalleryViewProps) {
  const wsId = useWorkspaceId();
  const { t } = useT("issues");
  const cardProperties = useViewStore((s) => s.cardProperties);
  const cardPropertyIds = useViewStore((s) => s.cardPropertyIds);
  const {
    issues,
    total,
    isHeadLoading,
    isHeadError,
    isTailLoading,
    isTailError,
    canLoadMore,
    handleLoadMore,
    retryHead,
    retryTail,
  } = useIssueTableFlatRowPages(wsId, serverQuery, GALLERY_PAGE_LIMIT);
  const visibleIssueIds = useMemo(() => issues.map((issue) => issue.id), [issues]);

  const propertiesQuery = useQuery(propertyListOptions(wsId));
  const workspaceProperties = propertiesQuery.data ?? [];
  const projectsQuery = useQuery({
    ...projectListOptions(wsId),
    enabled: cardProperties.project,
  });
  const childProgressQuery = useQuery({
    ...childIssueProgressOptions(wsId),
    enabled: cardProperties.childProgress,
  });
  const projectMap = useMemo(
    () => new Map((projectsQuery.data ?? []).map((project) => [project.id, project])),
    [projectsQuery.data],
  );
  const childProgressMap = childProgressQuery.data ?? new Map<string, ChildProgress>();

  if (isHeadLoading) {
    return (
      <div className="flex-1 p-3" aria-busy="true">
        <GalleryCardGrid>
          {Array.from({ length: 8 }).map((_, index) => (
            <Skeleton key={index} className="h-28 w-full rounded-md" />
          ))}
        </GalleryCardGrid>
      </div>
    );
  }

  if (isHeadError) {
    return (
      <div role="alert" className="flex items-center gap-2 p-4 text-body text-destructive">
        <span>{t(($) => $.table.load_failed)}</span>
        <Button size="sm" variant="outline" onClick={retryHead}>
          {t(($) => $.table.load_failed_retry)}
        </Button>
      </div>
    );
  }

  if (issues.length === 0) {
    return <GalleryCardGrid empty={t(($) => $.table.empty)} />;
  }

  const body = (
    <>
      <div className="flex-1 min-h-0 overflow-auto">
        <GalleryCardGrid>
          {issues.map((issue) => (
            <IssueGalleryCard
              key={issue.id}
              issue={issue}
              workspaceProperties={workspaceProperties}
              project={
                issue.project_id ? projectMap.get(issue.project_id) : undefined
              }
              childProgress={childProgressMap.get(issue.id)}
            />
          ))}
        </GalleryCardGrid>
        <div className="flex items-center justify-center px-4 py-3">
          {isTailError ? (
            <Button size="sm" variant="ghost" className="text-destructive" onClick={retryTail}>
              {t(($) => $.table.load_more_failed_retry)}
            </Button>
          ) : isTailLoading ? (
            <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              {t(($) => $.table.loading_branch)}
            </span>
          ) : canLoadMore ? (
            <Button size="sm" variant="outline" onClick={handleLoadMore}>
              {t(($) => $.table.load_more)}
            </Button>
          ) : issues.length > 0 && total > GALLERY_PAGE_LIMIT ? (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.table.no_more)}
            </span>
          ) : null}
        </div>
      </div>
    </>
  );

  return (
    <ComputedPropertyViewBoundary
      wsId={wsId}
      issueIds={visibleIssueIds}
      cardPropertyIds={cardPropertyIds}
      workspaceProperties={workspaceProperties}
    >
      <div className="flex flex-1 min-h-0 flex-col overflow-hidden">{body}</div>
    </ComputedPropertyViewBoundary>
  );
}
