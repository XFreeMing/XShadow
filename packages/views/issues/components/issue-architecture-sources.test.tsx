/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { Goal, GoalPlan, Issue, IssueTableQuerySpec } from "@multica/core/types";
import {
  buildGoalDependencyGraphData,
  IssueArchitectureSources,
} from "./issue-architecture-sources";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("../../i18n", () => ({
  useT: (ns: string) => ({
    t: (selector: (root: Record<string, Record<string, string>>) => string) => {
      const table: Record<string, Record<string, Record<string, string>>> = {
        issues: {
          table: {
            hierarchy: "Hierarchy",
            load_failed: "Failed to load issues.",
            load_failed_retry: "Retry",
          },
          gantt: {
            goal_plan: "Goal dependencies",
            empty_plan: "This goal has no saved plan items.",
          },
          org_chart: {
            empty: "No issues match this view.",
            loading_more: "Loading more…",
            loaded_progress: "Loaded",
          },
        },
        goals: {
          list: {
            title: "Goals",
            load_more: "Load more",
            retry: "Retry",
          },
          plan: {
            project_required: "Link this goal to a project to draft and generate a plan.",
            load_failed: "Couldn't load the plan.",
            retry: "Retry",
            dependency_hard: "Required",
            dependency_conditional: "Conditional",
            validation_cycle: "Dependencies can't form a cycle.",
            no_dependencies: "No dependencies.",
            dependency_reason: "Why",
          },
          map: {
            upstream: "Depends on:",
            downstream: "Unlocks:",
            close_inspector: "Close details",
            click_hint: "Click a task to inspect details and highlight its dependency path.",
          },
        },
      };
      return selector(table[ns] ?? {});
    },
  }),
}));

const SERVER_QUERY: IssueTableQuerySpec = {
  scope: { kind: "project", project_id: "proj-1" },
  filters: {},
  sort: { field: "updated_at", direction: "desc" },
};

function makeIssue(id: string, identifier: string, title: string, projectId = "proj-1"): Issue {
  return {
    id,
    workspace_id: "ws-1",
    project_id: projectId,
    number: 1,
    identifier,
    title,
    description: null,
    status: "todo",
    status_category: "unstarted",
    priority: "medium",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    position: 0,
    due_date: null,
    start_date: null,
    stage: null,
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function makeGoal(id: string, title: string, projectId: string | null): Goal {
  return {
    id,
    workspace_id: "ws-1",
    project_id: projectId,
    title,
    intent: "",
    scope: "",
    acceptance_criteria: "",
    owner_type: null,
    owner_id: null,
    status: "in_progress",
    creator_type: "member",
    creator_id: "user-1",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function makePlan(goalId: string, projectId: string): GoalPlan {
  return {
    id: "plan-1",
    goal_id: goalId,
    project_id: projectId,
    revision: 1,
    state: "draft",
    items: [
      {
        id: "item-1",
        title: "Plan A",
        outcome: "",
        acceptance_criteria: "",
        evidence_refs: [],
        binding: { type: "existing", issue_id: "iss-1" },
        proposed_owner: null,
        stage: 1,
        coordination: null,
      },
      {
        id: "item-2",
        title: "Plan B",
        outcome: "",
        acceptance_criteria: "",
        evidence_refs: [],
        binding: { type: "existing", issue_id: "iss-2" },
        proposed_owner: null,
        stage: 2,
        coordination: null,
      },
      {
        id: "item-3",
        title: "Filtered out task",
        outcome: "",
        acceptance_criteria: "",
        evidence_refs: [],
        binding: { type: "existing", issue_id: "iss-hidden" },
        proposed_owner: null,
        stage: 3,
        coordination: null,
      },
    ],
    edges: [
      {
        prerequisite_item_id: "item-1",
        dependent_item_id: "item-2",
        kind: "required",
        reason: "Schema first",
      },
      {
        prerequisite_item_id: "item-2",
        dependent_item_id: "item-3",
        kind: "conditional",
        reason: "Depends on hidden",
      },
    ],
    context_snapshot: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function createQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  cleanup();
});

describe("buildGoalDependencyGraphData", () => {
  it("maps only visible project issues and drops edges touching invisible tasks", () => {
    const plan = makePlan("goal-1", "proj-1");
    const visible = [
      makeIssue("iss-1", "MUL-1", "First"),
      makeIssue("iss-2", "MUL-2", "Second"),
      makeIssue("iss-other", "MUL-9", "Other project", "proj-2"),
    ];

    const data = buildGoalDependencyGraphData(plan, visible, "proj-1");
    expect(data.rows.map((r) => r.id)).toEqual(["iss-1", "iss-2"]);
    expect(data.edges).toEqual([
      {
        id: "item-1->item-2",
        fromId: "iss-1",
        toId: "iss-2",
        kind: "required",
        reason: "Schema first",
      },
    ]);
  });
});

describe("IssueArchitectureSources", () => {
  it("defaults to hierarchy children and switches to goal dependencies using filtered rows", async () => {
    // When multiple goals exist or plan edges are empty, hierarchy remains default
    const listGoals = vi.fn().mockResolvedValue({
      goals: [
        makeGoal("goal-no-project", "Workspace Goal", null),
        makeGoal("goal-1", "Launch Goal", "proj-1"),
        makeGoal("goal-2", "Second Goal", "proj-1"),
      ],
      total: 3,
    });
    const getGoalPlan = vi.fn().mockResolvedValue(makePlan("goal-1", "proj-1"));
    const listIssueTableRows = vi.fn().mockResolvedValue({
      rows: [
        { issue: makeIssue("iss-1", "MUL-1", "First") },
        { issue: makeIssue("iss-2", "MUL-2", "Second") },
      ],
      total: 2,
      next_cursor: null,
    });
    const getIssue = vi.fn();

    setApiInstance({
      listGoals,
      getGoalPlan,
      listIssueTableRows,
      getIssue,
    } as unknown as ApiClient);

    const { container } = render(
      <QueryClientProvider client={createQueryClient()}>
        <IssueArchitectureSources
          serverQuery={SERVER_QUERY}
          projectId="proj-1"
          renderHierarchy={() => <div data-testid="org-chart-child">Original Org Chart</div>}
        />
      </QueryClientProvider>,
    );

    // Default renders hierarchy children
    expect(screen.getByTestId("org-chart-child")).toBeTruthy();

    // Switch to Goal dependencies
    fireEvent.click(screen.getByRole("button", { name: "Goal dependencies" }));

    await waitFor(() => {
      expect(screen.getByText("MUL-1")).toBeTruthy();
      expect(screen.getByText("MUL-2")).toBeTruthy();
    });

    // Invisible issue detail is never fetched
    expect(getIssue).not.toHaveBeenCalled();
    expect(screen.queryByText("Filtered out task")).toBeNull();

    // Only the visible prerequisite -> dependent edge is drawn
    const edges = container.querySelectorAll("[data-dependency-edge]");
    expect(edges).toHaveLength(1);
    expect(edges[0]?.getAttribute("data-from-id")).toBe("iss-1");
    expect(edges[0]?.getAttribute("data-to-id")).toBe("iss-2");

    // Switching back restores original children
    fireEvent.click(screen.getByRole("button", { name: "Hierarchy" }));
    expect(screen.getByTestId("org-chart-child")).toBeTruthy();
  });

  it("automatically defaults to goal dependencies when visible tasks have no visible parent-child and unique goal has real edges", async () => {
    const listGoals = vi.fn().mockResolvedValue({
      goals: [makeGoal("goal-1", "Launch Goal", "proj-1")],
      total: 1,
    });
    const getGoalPlan = vi.fn().mockResolvedValue(makePlan("goal-1", "proj-1"));
    const listIssueTableRows = vi.fn().mockResolvedValue({
      rows: [
        { issue: makeIssue("iss-1", "MUL-1", "First") },
        { issue: makeIssue("iss-2", "MUL-2", "Second") },
        { issue: makeIssue("iss-3", "MUL-3", "Task from another project", "proj-2") },
      ],
      total: 3,
      next_cursor: null,
    });

    setApiInstance({
      listGoals,
      getGoalPlan,
      listIssueTableRows,
    } as unknown as ApiClient);

    const { container } = render(
      <QueryClientProvider client={createQueryClient()}>
        <IssueArchitectureSources
          serverQuery={{ ...SERVER_QUERY, scope: { kind: "workspace" } }}
          renderHierarchy={() => <div data-testid="org-chart-child">Original Org Chart</div>}
        />
      </QueryClientProvider>,
    );

    // Automatically selects goal dependencies
    await waitFor(() => {
      expect(screen.getByText("MUL-1")).toBeTruthy();
      expect(screen.getByText("MUL-2")).toBeTruthy();
    });

    const edges = container.querySelectorAll("[data-dependency-edge]");
    expect(edges).toHaveLength(1);
    expect(edges[0]?.getAttribute("data-from-id")).toBe("iss-1");
    expect(edges[0]?.getAttribute("data-to-id")).toBe("iss-2");

    // Manual selection has unconditional priority: switching to hierarchy works
    fireEvent.click(screen.getByRole("button", { name: "Hierarchy" }));
    expect(screen.getByTestId("org-chart-child")).toBeTruthy();
  });

  it("allows retrying when loading goal plan fails", async () => {
    const listGoals = vi.fn().mockResolvedValue({
      goals: [makeGoal("goal-1", "Launch Goal", "proj-1")],
      total: 1,
    });
    const getGoalPlan = vi
      .fn()
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce(makePlan("goal-1", "proj-1"));
    const listIssueTableRows = vi.fn().mockResolvedValue({
      rows: [
        { issue: makeIssue("iss-1", "MUL-1", "First") },
        { issue: makeIssue("iss-2", "MUL-2", "Second") },
      ],
      total: 2,
      next_cursor: null,
    });

    setApiInstance({
      listGoals,
      getGoalPlan,
      listIssueTableRows,
    } as unknown as ApiClient);

    render(
      <QueryClientProvider client={createQueryClient()}>
        <IssueArchitectureSources
          serverQuery={SERVER_QUERY}
          projectId="proj-1"
          renderHierarchy={() => <div data-testid="org-chart-child">Original Org Chart</div>}
        />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Goal dependencies" }));

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("Couldn't load the plan.");
    });

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(screen.getByText("MUL-1")).toBeTruthy();
    });
  });
});
