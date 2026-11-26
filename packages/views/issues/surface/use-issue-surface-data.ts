"use client";

import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Issue, Project } from "@multica/core/types";
import { projectListOptions } from "@multica/core/projects/queries";
import { childIssueProgressOptions } from "@multica/core/issues/queries";
import {
  filterDependencyState,
  type FilterDependencySource,
  type FilterDependencyState,
} from "@multica/core/issues/surface/filter-dependency";
import type { IssueStatus, ProjectStatus, PropertyFilterValue } from "@multica/core/types";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import {
  advancedFilterUsesField,
  statusColumnFilter,
  type AdvancedFilterGroup,
  type DueDateFilter,
} from "@multica/core/issues/stores/filter-tree";
import { useColumnWorkflow, workflowColumns } from "../utils/workflow-columns";
import { statusColumnKeys, visibleStatusKeys } from "@multica/core/issues";
import type {
  IssueRelationFilter,
  NegatableFilterDimension,
} from "@multica/core/issues/stores/view-store";
import {
  applyIssueFilters,
  type IssueFilterState,
  type IssueFilters,
  type IssueRelationContext,
} from "../utils/filter";
import type { ChildProgress } from "../components/list-row";
import type {
  IssueStatusBranches,
  IssueStatusPagination,
} from "./use-issue-status-branches";
import type { IssueGroupBranches } from "./use-issue-group-branches";

const EMPTY_ISSUES: Issue[] = [];
const EMPTY_CHILD_PROGRESS = new Map<string, ChildProgress>();
const EMPTY_PROJECTS: Project[] = [];

export interface IssueSurfaceData {
  surfaceIssues: Issue[];
  projectIssues: Issue[];
  issues: Issue[];
  swimlaneIssues: Issue[];
  visibleStatuses: IssueStatus[];
  columnStatuses: IssueStatus[];
  statusPagination: IssueStatusPagination;
  activeFilters: Omit<IssueFilters, "statusFilters">;
  childProgressMap: Map<string, ChildProgress>;
  projectMap: Map<string, Project>;
  resolveTableExportLookups: (needs: {
    projects: boolean;
    childProgress: boolean;
  }) => Promise<{
    projectMap: Map<string, Project>;
    childProgressMap: Map<string, ChildProgress>;
  }>;
  isLoading: boolean;
  /** The project list behind a project-status filter this surface applies
   *  itself. */
  projectCatalogDependency: FilterDependencyState;
  /** The viewer's subscriptions / agents behind a relation filter this
   *  surface applies itself. */
  relationDependency: FilterDependencyState;
  /** The window's data is being revalidated while the previous snapshot is
   *  shown as a placeholder (sort/date change, or any grouped-board filter
   *  change). Drives the header's deferred refresh indicator — content stays
   *  put, so this is NOT a loading state. */
  isRefreshing: boolean;
  isEmpty: boolean;
}

export function useIssueSurfaceData({
  wsId,
  projectId,
  usesForm,
  usesTable,
  serverStatusBranches,
  serverGroupBranches,
  statusFilters,
  hiddenStatusKeys,
  statusDependency,
  priorityFilters,
  assigneeFilters,
  includeNoAssignee,
  agentRunningFilter,
  unreadFilter,
  creatorFilters,
  projectFilters,
  includeNoProject,
  projectStatusFilters,
  labelFilters,
  relationFilters,
  relationContext,
  relationSources,
  dueDateFilters,
  advancedFilter,
  today,
  negatedFilters,
  propertyFilters,
  workingIssueIDs,
  unreadIssueIDs,
  showSubIssues,
  loadProjects,
}: {
  wsId: string;
  projectId?: string;
  usesForm: boolean;
  usesTable: boolean;
  serverStatusBranches: IssueStatusBranches;
  serverGroupBranches: IssueGroupBranches;
  statusFilters: IssueStatus[];
  hiddenStatusKeys: IssueStatus[];
  /** The status catalog the columns and a custom status filter route through. */
  statusDependency: FilterDependencyState;
  priorityFilters: IssueFilterState["priorityFilters"];
  assigneeFilters: IssueFilterState["assigneeFilters"];
  includeNoAssignee: boolean;
  agentRunningFilter: boolean;
  unreadFilter: boolean;
  creatorFilters: IssueFilterState["creatorFilters"];
  projectFilters: string[];
  includeNoProject: boolean;
  projectStatusFilters: ProjectStatus[];
  labelFilters: string[];
  relationFilters: IssueRelationFilter[];
  relationContext: IssueRelationContext | undefined;
  /** What the active relation values read: the viewer's agents and squads
   *  for "my_agents", their subscriptions for "subscribed". */
  relationSources: readonly (FilterDependencySource | false)[];
  dueDateFilters: DueDateFilter[];
  /** The scoped, pruned rule tree. */
  advancedFilter: AdvancedFilterGroup | null;
  /** Set when due-date buckets are in play. */
  today: string | undefined;
  /** Negations that change the result (`effectiveNegatedFilters`). */
  negatedFilters: NegatableFilterDimension[];
  propertyFilters: Record<string, PropertyFilterValue[]>;
  /** Distinct running-task issue ids projected by `/api/working-agents`. */
  workingIssueIDs: ReadonlySet<string>;
  /** The viewer's unread issue ids, plus those kept on screen after reading. */
  unreadIssueIDs: ReadonlySet<string>;
  showSubIssues: boolean;
  loadProjects: boolean;
}): IssueSurfaceData {
  const projectsQuery = useQuery({
    ...projectListOptions(wsId),
    enabled: loadProjects,
  });
  const { data: projectData, refetch: refetchProjects } = projectsQuery;
  const projects = projectData ?? EMPTY_PROJECTS;
  const projectMap = useMemo(
    () => new Map(projects.map((project) => [project.id, project])),
    [projects],
  );
  // Keyed off `projectData`, NOT `projects`: the latter falls back to
  // EMPTY_PROJECTS while the query is loading or failed, which would build a
  // defined-but-empty map. `applyIssueFilters` treats a defined map as
  // authoritative, so that map would drop every issue and blank the board.
  // `undefined` is the honest answer until the catalog actually arrives, and
  // it makes the predicate a no-op.
  const projectStatusById = useMemo(
    () =>
      projectData
        ? new Map(projectData.map((project) => [project.id, project.status]))
        : undefined,
    [projectData],
  );
  // Only where this surface applies a predicate itself does it wait on that
  // predicate's data. Table and the server-status branches filter server-side.
  const usesClientPredicates = !usesTable && !serverStatusBranches.enabled;
  const projectCatalogDependency = filterDependencyState(
    usesClientPredicates &&
      (projectStatusFilters.length > 0 || advancedFilterUsesField(advancedFilter, "projectStatus")),
    [projectsQuery],
  );
  const relationDependency = filterDependencyState(usesClientPredicates, relationSources);

  const workingFilterContext = useMemo(
    () => ({
      runningIssueIds: workingIssueIDs,
      unreadIssueIds: unreadIssueIDs,
      projectStatusById,
      relation: relationContext,
      today,
    }),
    [projectStatusById, relationContext, today, unreadIssueIDs, workingIssueIDs],
  );
  const bucketedIssues = serverStatusBranches.enabled
    ? serverStatusBranches.issues
    : serverGroupBranches.enabled
      ? serverGroupBranches.issues
      : EMPTY_ISSUES;

  // Status branches already reflect the visible category set chosen by the
  // controller. Cancelled is hidden for a new view, but remains a first-class
  // branch once the user restores it or selects it explicitly in a status
  // filter; no client-only exclusion happens here.
  const surfaceIssues = usesTable ? EMPTY_ISSUES : bucketedIssues;

  const baseFilterState = useMemo<IssueFilterState>(
    () => ({
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      projectFilters,
      includeNoProject,
      projectStatusFilters,
      labelFilters,
      relationFilters,
      dueDateFilters,
      advancedFilter,
      negatedFilters,
      propertyFilters,
      workingOnly: agentRunningFilter,
      unreadOnly: unreadFilter,
      showSubIssues,
    }),
    [
      advancedFilter,
      assigneeFilters,
      agentRunningFilter,
      unreadFilter,
      creatorFilters,
      dueDateFilters,
      includeNoAssignee,
      includeNoProject,
      labelFilters,
      negatedFilters,
      priorityFilters,
      projectFilters,
      projectStatusFilters,
      propertyFilters,
      relationFilters,
      showSubIssues,
      statusFilters,
    ],
  );

  const issues = useMemo(
    () =>
      serverStatusBranches.enabled
        ? surfaceIssues
        : applyIssueFilters(
            surfaceIssues,
            baseFilterState,
            workingFilterContext,
          ),
    [
      baseFilterState,
      serverStatusBranches.enabled,
      surfaceIssues,
      workingFilterContext,
    ],
  );

  const statuslessFilterState = useMemo<IssueFilterState>(
    () => ({
      ...baseFilterState,
      statusFilters: [],
    }),
    [baseFilterState],
  );

  const swimlaneIssues = useMemo(
    () =>
      applyIssueFilters(
        surfaceIssues,
        statuslessFilterState,
        workingFilterContext,
      ),
    [statuslessFilterState, surfaceIssues, workingFilterContext],
  );

  const {
    data: childProgressData,
    refetch: refetchChildProgress,
  } = useQuery(childIssueProgressOptions(wsId));
  const childProgressMap = childProgressData ?? EMPTY_CHILD_PROGRESS;
  const resolveTableExportLookups = useCallback(
    async (needs: { projects: boolean; childProgress: boolean }) => {
      const [projectResult, progressResult] = await Promise.all([
        needs.projects ? refetchProjects() : Promise.resolve(null),
        needs.childProgress
          ? refetchChildProgress()
          : Promise.resolve(null),
      ]);
      if (projectResult?.error) throw projectResult.error;
      if (progressResult?.error) throw progressResult.error;
      if (needs.projects && !projectResult?.data) {
        throw new Error("Failed to load project data for export");
      }
      if (needs.childProgress && !progressResult?.data) {
        throw new Error("Failed to load child progress for export");
      }
      const resolvedProjects = projectResult?.data ?? projects;
      return {
        projectMap: new Map(
          resolvedProjects.map((project) => [project.id, project]),
        ),
        childProgressMap: progressResult?.data ?? childProgressMap,
      };
    },
    [
      childProgressMap,
      projects,
      refetchChildProgress,
      refetchProjects,
    ],
  );

  const catalog = useIssueStatuses(wsId);
  // Columns follow the project's workflow, or Default outside a project.
  // (MUL-7420)
  const statusNegated = negatedFilters.includes("status");
  const columnStatus = useMemo(
    () => statusColumnFilter(statusFilters, statusNegated, advancedFilter, hiddenStatusKeys),
    [advancedFilter, hiddenStatusKeys, statusFilters, statusNegated],
  );
  const { workflow: projectWorkflow } = useColumnWorkflow(
    wsId,
    projectId,
    columnStatus.statuses.length > 0 && !columnStatus.negated,
  );
  const { workflow: scopeWorkflow } = useColumnWorkflow(wsId, projectId, false);

  const visibleStatuses = useMemo<IssueStatus[]>(() => {
    // An explicit exact-key filter wins over hidden column preferences.
    return workflowColumns(
      visibleStatusKeys(
        columnStatus.statuses,
        columnStatus.hiddenStatuses,
        catalog,
        columnStatus.negated,
      ),
      projectWorkflow,
    );
  }, [columnStatus, catalog, projectWorkflow]);

  // The columns Display can show or hide: this surface's workflow steps, or
  // the whole catalog without a workflow. A status filter does not change
  // them, since it overrides hidden columns rather than replacing them.
  const columnStatuses = useMemo<IssueStatus[]>(
    () => workflowColumns(statusColumnKeys(catalog), scopeWorkflow),
    [catalog, scopeWorkflow],
  );

  const activeFilters = useMemo(
    () => ({
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      agentRunningFilter,
      runningIssueIds: workingIssueIDs,
      unreadFilter,
      unreadIssueIds: unreadIssueIDs,
      creatorFilters,
      projectFilters,
      includeNoProject,
      projectStatusFilters,
      projectStatusById,
      labelFilters,
      relationFilters,
      relationContext,
      dueDateFilters,
      advancedFilter,
      today,
      negatedFilters,
      propertyFilters,
      showSubIssues,
    }),
    [
      advancedFilter,
      assigneeFilters,
      agentRunningFilter,
      creatorFilters,
      dueDateFilters,
      includeNoAssignee,
      includeNoProject,
      labelFilters,
      negatedFilters,
      propertyFilters,
      priorityFilters,
      projectFilters,
      projectStatusById,
      projectStatusFilters,
      relationContext,
      relationFilters,
      showSubIssues,
      today,
      unreadFilter,
      unreadIssueIDs,
      workingIssueIDs,
    ],
  );

  const isLoading =
    statusDependency.pending ||
    projectCatalogDependency.pending ||
    relationDependency.pending ||
    (serverGroupBranches.enabled
      ? serverGroupBranches.isLoading
      : serverStatusBranches.enabled
        ? serverStatusBranches.isLoading
        : false);

  // Placeholder-backed revalidation of the ACTIVE query only. First loads are
  // isLoading (no previous data to place-hold).
  const isRefreshing = serverGroupBranches.enabled
    ? serverGroupBranches.isRefreshing
    : serverStatusBranches.enabled
      ? serverStatusBranches.isRefreshing
      : false;

  return {
    surfaceIssues,
    projectIssues: surfaceIssues,
    issues,
    swimlaneIssues,
    visibleStatuses,
    columnStatuses,
    statusPagination: serverStatusBranches.pagination,
    activeFilters,
    childProgressMap,
    projectMap,
    resolveTableExportLookups,
    isLoading,
    isRefreshing,
    // isEmpty asserts "this window has no issues". The board/list/swimlane
    // data IS the full window, so an empty result proves it. Table-row views
    // (including Gantt date schedule) own their own loading, empty and retry
    // states, so this shared legacy surface projection never asserts empty.
    isEmpty:
      !isLoading &&
      !statusDependency.error &&
      !projectCatalogDependency.error &&
      !relationDependency.error &&
      !usesForm &&
      !usesTable &&
      (serverStatusBranches.enabled
        ? serverStatusBranches.isTotalKnown &&
          serverStatusBranches.total === 0
        : serverGroupBranches.enabled &&
          !serverGroupBranches.isError &&
          serverGroupBranches.total === 0),
    projectCatalogDependency,
    relationDependency,
  };
}
