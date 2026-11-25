// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { issueKeys } from "@multica/core/issues/queries";
import {
  getIssueSurfaceViewStore,
  pruneIssueSurfaceViewStates,
} from "@multica/core/issues/stores/surface-view-store";
import { issueScopeKey } from "@multica/core/issues/surface/scope";
import { useModalStore } from "@multica/core/modals";
import type {
  Agent,
  Issue,
  IssueTableRowsRequest,
  IssueTableRowsResponse,
  MemberWithUser,
} from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssueSurface } from "../surface/issue-surface";

const mockWsId = vi.hoisted(() => ({ current: "ws-1" }));
const mockIntentNavigate = vi.hoisted(() => vi.fn());
const mockToastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => mockWsId.current,
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: (state: unknown) => unknown) => {
      const state = {
        user: { id: "user-1", email: "alice@example.com", name: "Alice" },
        isAuthenticated: true,
      };
      return selector ? selector(state) : state;
    },
    {
      getState: () => ({
        user: { id: "user-1", email: "alice@example.com", name: "Alice" },
        isAuthenticated: true,
      }),
    },
  ),
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", slug: "demo" }),
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/w/demo/issues/${id}`,
    memberDetail: (id: string) => `/w/demo/members/${id}`,
    agentDetail: (id: string) => `/w/demo/agents/${id}`,
    squadDetail: (id: string) => `/w/demo/squads/${id}`,
  }),
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ children, href, ...props }: React.ComponentProps<"a">) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/" }),
  resolveClickIntent: () => "push",
  useIntentNavigate: () => mockIntentNavigate,
}));

vi.mock("sonner", () => ({
  toast: {
    error: mockToastError,
    success: vi.fn(),
    info: vi.fn(),
  },
}));

function makeIssue(overrides: Partial<Issue> & { id: string; title: string }): Issue {
  return {
    workspace_id: "ws-1",
    number: 1,
    identifier: "XSH-1",
    description: null,
    status: "todo",
    status_category: "unstarted",
    priority: "medium",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position: 1000,
    stage: null,
    metadata: {},
    properties: {},
    start_date: null,
    due_date: null,
    created_at: "2026-11-25T00:00:00Z",
    updated_at: "2026-11-25T00:00:00Z",
    revision: 1,
    ...overrides,
  };
}

const MEMBERS: MemberWithUser[] = [
  {
    id: "m-1",
    workspace_id: "ws-1",
    user_id: "user-1",
    role: "owner",
    created_at: "2026-11-01T00:00:00Z",
    name: "Alice",
    email: "alice@example.com",
    avatar_url: null,
  },
  {
    id: "m-2",
    workspace_id: "ws-1",
    user_id: "user-2",
    role: "member",
    created_at: "2026-11-01T00:00:00Z",
    name: "Bob",
    email: "bob@example.com",
    avatar_url: null,
  },
];

const AGENTS: Agent[] = [
  {
    id: "agent-1",
    workspace_id: "ws-1",
    name: "Coder Bot",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "cloud",
    runtime_id: "rt-1",
    runtime_config: {},
    archived_at: null,
    visibility: "workspace",
    status: "idle",
    max_concurrent_tasks: 2,
    owner_id: "user-1",
    skills: [],
    created_at: "2026-11-01T00:00:00Z",
    updated_at: "2026-11-01T00:00:00Z",
  } as unknown as Agent,
];

function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity },
      mutations: { retry: false },
    },
  });
}

function setupApi(overrides: Partial<ApiClient> = {}) {
  const base: Record<string, unknown> = {
    getBaseUrl: vi.fn(() => "http://localhost"),
    listIssueStatuses: vi.fn().mockResolvedValue([
      { key: "backlog", label: "Backlog", category: "backlog", color: "#888", icon: "circle-dashed", position: 0 },
      { key: "todo", label: "Todo", category: "unstarted", color: "#999", icon: "circle", position: 1 },
      { key: "in_progress", label: "In Progress", category: "started", color: "#09f", icon: "circle-dot", position: 2 },
      { key: "done", label: "Done", category: "done", color: "#0a0", icon: "check-circle", position: 3 },
    ]),
    listIssueWorkflows: vi.fn().mockResolvedValue([]),
    listProperties: vi.fn().mockResolvedValue([]),
    listProjects: vi.fn().mockResolvedValue([]),
    listMembers: vi.fn().mockResolvedValue(MEMBERS),
    listAgents: vi.fn().mockResolvedValue(AGENTS),
    listSquads: vi.fn().mockResolvedValue([]),
    getAssigneeFrequency: vi.fn().mockResolvedValue([]),
    getChildIssueProgress: vi.fn().mockResolvedValue({ progress: [] }),
    listWorkspaceWakeupSummaries: vi.fn().mockResolvedValue([]),
    listIssueTableFacets: vi.fn().mockResolvedValue({ facets: [], total: 0 }),
    listIssueTableRows: vi.fn().mockResolvedValue({
      rows: [],
      total: 0,
      branch_total: 0,
      next_cursor: null,
      group_key: null,
      parent_id: null,
      query_fingerprint: "fp-1",
    } satisfies IssueTableRowsResponse),
    updateIssue: vi.fn(),
    ...overrides,
  };
  setApiInstance(base as unknown as ApiClient);
  return base;
}

describe("MultidimensionalGridView", () => {
  beforeEach(() => {
    mockWsId.current = "ws-1";
    mockIntentNavigate.mockReset();
    mockToastError.mockReset();
    useModalStore.getState().close();
    pruneIssueSurfaceViewStates(new Set());
  });

  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  it("loads native UUID rows with cursor pagination, deduplicates IDs, opens detail, and opens create dialog with project context", async () => {
    const issue1 = makeIssue({ id: "uuid-1", identifier: "XSH-1", title: "First task", revision: 2 });
    const issue2 = makeIssue({ id: "uuid-2", identifier: "XSH-2", title: "Second task", revision: 4 });
    const listIssueTableRows = vi.fn(async (req: IssueTableRowsRequest): Promise<IssueTableRowsResponse> => {
      if (!req.page?.cursor) {
        return {
          rows: [{ issue: issue1, direct_child_count: 0 }],
          total: 2,
          branch_total: 2,
          next_cursor: "cursor-page-2",
          group_key: null,
          parent_id: null,
          query_fingerprint: "fp-1",
        };
      }
      return {
        // Deliberately repeat uuid-1 alongside uuid-2 to prove deduplication.
        rows: [
          { issue: issue1, direct_child_count: 0 },
          { issue: issue2, direct_child_count: 0 },
        ],
        total: 2,
        branch_total: 2,
        next_cursor: null,
        group_key: null,
        parent_id: null,
        query_fingerprint: "fp-1",
      };
    });
    setupApi({ listIssueTableRows });

    const surfaceKey = "project:proj-1:all";
    getIssueSurfaceViewStore(surfaceKey).getState().setViewMode("grid");

    const qc = createQueryClient();
    renderWithI18n(
      <QueryClientProvider client={qc}>
        <IssueSurface
          surfaceKey={surfaceKey}
          scope={{ type: "project", projectId: "proj-1", actorKind: "all" }}
          modes={["board", "list", "table", "grid", "swimlane", "gantt"]}
        />
      </QueryClientProvider>,
    );

    expect(await screen.findByDisplayValue("First task")).toBeInTheDocument();
    expect(listIssueTableRows).toHaveBeenCalledTimes(1);
    expect(document.querySelector('tr[data-record-id="uuid-1"]')).toBeTruthy();

    // Open native detail navigation from row trigger.
    fireEvent.click(screen.getByRole("button", { name: /Open task 1/i }));
    expect(mockIntentNavigate).toHaveBeenCalledWith("/w/demo/issues/uuid-1", "push", "XSH-1");

    // Load more appends distinct native UUID without duplicating uuid-1.
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByDisplayValue("Second task")).toBeInTheDocument();
    expect(listIssueTableRows).toHaveBeenCalledTimes(2);
    expect(document.querySelectorAll('tr[data-record-id="uuid-1"]')).toHaveLength(1);
    expect(document.querySelectorAll('tr[data-record-id="uuid-2"]')).toHaveLength(1);

    // Create button opens native create-issue modal with project context.
    fireEvent.click(screen.getByRole("button", { name: "New issue" }));
    expect(useModalStore.getState().modal).toBe("create-issue");
    expect(useModalStore.getState().data).toMatchObject({ project_id: "proj-1" });
  });

  it("never shows stale or unauthorized rows when scope changes while the next query is pending", async () => {
    const projectAIssue = makeIssue({ id: "uuid-a", identifier: "XSH-10", title: "Project A secret", project_id: "proj-a" });
    let resolveProjectB!: (res: IssueTableRowsResponse) => void;
    const listIssueTableRows = vi.fn((req: IssueTableRowsRequest) => {
      if (req.query.scope.kind === "project" && req.query.scope.project_id === "proj-a") {
        return Promise.resolve({
          rows: [{ issue: projectAIssue, direct_child_count: 0 }],
          total: 1,
          branch_total: 1,
          next_cursor: null,
          group_key: null,
          parent_id: null,
          query_fingerprint: "fp-a",
        } satisfies IssueTableRowsResponse);
      }
      return new Promise<IssueTableRowsResponse>((resolve) => {
        resolveProjectB = resolve;
      });
    });
    setupApi({ listIssueTableRows });

    const scopeA = { type: "project" as const, projectId: "proj-a", actorKind: "all" as const };
    const scopeB = { type: "project" as const, projectId: "proj-b", actorKind: "all" as const };
    getIssueSurfaceViewStore(issueScopeKey(scopeA)).getState().setViewMode("grid");
    getIssueSurfaceViewStore(issueScopeKey(scopeB)).getState().setViewMode("grid");

    const qc = createQueryClient();
    const { rerender } = renderWithI18n(
      <QueryClientProvider client={qc}>
        <IssueSurface
          scope={scopeA}
          modes={["board", "list", "table", "grid"]}
        />
      </QueryClientProvider>,
    );

    expect(await screen.findByDisplayValue("Project A secret")).toBeInTheDocument();

    rerender(
      <QueryClientProvider client={qc}>
        <IssueSurface
          scope={scopeB}
          modes={["board", "list", "table", "grid"]}
        />
      </QueryClientProvider>,
    );

    // While Project B is loading, Project A's row must never appear.
    expect(screen.queryByDisplayValue("Project A secret")).not.toBeInTheDocument();

    const projectBIssue = makeIssue({ id: "uuid-b", identifier: "XSH-20", title: "Project B task", project_id: "proj-b" });
    await act(async () => {
      resolveProjectB({
        rows: [{ issue: projectBIssue, direct_child_count: 0 }],
        total: 1,
        branch_total: 1,
        next_cursor: null,
        group_key: null,
        parent_id: null,
        query_fingerprint: "fp-b",
      });
    });

    expect(await screen.findByDisplayValue("Project B task")).toBeInTheDocument();
  });

  it("mutates title, status, and assignee through native updateIssue with revision checks, run-confirm gate, and conflict toast", async () => {
    let currentIssue = makeIssue({
      id: "uuid-edit",
      identifier: "XSH-99",
      title: "Original title",
      status: "todo",
      assignee_type: "member",
      assignee_id: "user-1",
      revision: 7,
    });
    const listIssueTableRows = vi.fn(async (): Promise<IssueTableRowsResponse> => ({
      rows: [{ issue: currentIssue, direct_child_count: 0 }],
      total: 1,
      branch_total: 1,
      next_cursor: null,
      group_key: null,
      parent_id: null,
      query_fingerprint: "fp-edit",
    }));
    const updateIssue = vi.fn(async (_id: string, body: Record<string, unknown>) => {
      if (body.title === "Conflict title") {
        throw new ApiError("Revision conflict", 409, "revision_conflict");
      }
      currentIssue = {
        ...currentIssue,
        ...(typeof body.title === "string" ? { title: body.title } : {}),
        ...(typeof body.status === "string" ? { status: body.status } : {}),
        ...(Object.prototype.hasOwnProperty.call(body, "assignee_type")
          ? { assignee_type: body.assignee_type as Issue["assignee_type"] }
          : {}),
        ...(Object.prototype.hasOwnProperty.call(body, "assignee_id")
          ? { assignee_id: body.assignee_id as Issue["assignee_id"] }
          : {}),
        revision: (currentIssue.revision ?? 0) + 1,
      };
      return currentIssue;
    });
    setupApi({ listIssueTableRows, updateIssue });

    const surfaceKey = "workspace:all";
    getIssueSurfaceViewStore(surfaceKey).getState().setViewMode("grid");

    const qc = createQueryClient();
    renderWithI18n(
      <QueryClientProvider client={qc}>
        <IssueSurface
          surfaceKey={surfaceKey}
          scope={{ type: "workspace", actorKind: "all" }}
          modes={["board", "list", "table", "grid"]}
        />
      </QueryClientProvider>,
    );

    const titleInput = await screen.findByDisplayValue("Original title");

    // 1. Title edit fires one native mutation with title_base and expected_revision.
    fireEvent.focus(titleInput);
    fireEvent.change(titleInput, { target: { value: "Updated title" } });
    fireEvent.blur(titleInput);

    await waitFor(() => {
      expect(updateIssue).toHaveBeenCalledTimes(1);
    });
    expect(updateIssue).toHaveBeenLastCalledWith("uuid-edit", {
      title: "Updated title",
      title_base: "Original title",
      expected_revision: 7,
    });

    // 2. Status edit through StatusPicker fires one native mutation with latest revision.
    fireEvent.click(screen.getByRole("button", { name: "Todo" }));
    fireEvent.click(await screen.findByText("In Progress"));
    await waitFor(() => {
      expect(updateIssue).toHaveBeenCalledTimes(2);
    });
    expect(updateIssue).toHaveBeenLastCalledWith("uuid-edit", {
      status: "in_progress",
      expected_revision: 8,
    });

    // 3. Assignee change to another member, then clear to Unassigned.
    fireEvent.click(screen.getByRole("button", { name: /Alice/ }));
    fireEvent.click(await screen.findByText("Bob"));
    await waitFor(() => {
      expect(updateIssue).toHaveBeenCalledTimes(3);
    });
    expect(updateIssue).toHaveBeenLastCalledWith("uuid-edit", {
      assignee_type: "member",
      assignee_id: "user-2",
      expected_revision: 9,
    });

    fireEvent.click(await screen.findByRole("button", { name: /Bob/ }));
    fireEvent.click(await screen.findByText("Unassigned"));
    await waitFor(() => {
      expect(updateIssue).toHaveBeenCalledTimes(4);
    });
    expect(updateIssue).toHaveBeenLastCalledWith("uuid-edit", {
      assignee_type: null,
      assignee_id: null,
      expected_revision: 10,
    });

    // 4. Assigning an agent opens the native run-confirm gate without sending an unconfirmed mutation.
    fireEvent.click(await screen.findByRole("button", { name: "Unassigned" }));
    fireEvent.click(await screen.findByText("Coder Bot"));
    expect(useModalStore.getState().modal).toBe("issue-run-confirm");
    expect(updateIssue).toHaveBeenCalledTimes(4);

    // 5. Revision conflict surfaces a readable error toast.
    const latestTitleInput = await screen.findByDisplayValue("Updated title");
    fireEvent.focus(latestTitleInput);
    fireEvent.change(latestTitleInput, { target: { value: "Conflict title" } });
    fireEvent.blur(latestTitleInput);

    await waitFor(() => {
      expect(mockToastError).toHaveBeenCalled();
    });
  });

  it("preserves matching tail rows on ordinary head refresh and detaches stale tail when head next_cursor changes", async () => {
    const issue0 = makeIssue({ id: "uuid-0", identifier: "XSH-0", title: "New top task", revision: 1 });
    const issue1 = makeIssue({ id: "uuid-1", identifier: "XSH-1", title: "Task one", revision: 1 });
    const issue2 = makeIssue({ id: "uuid-2", identifier: "XSH-2", title: "Boundary task", revision: 1 });
    const issue3 = makeIssue({ id: "uuid-3", identifier: "XSH-3", title: "Tail task", revision: 1 });

    let headPhase: "initial" | "same-cursor" | "shifted-cursor" = "initial";
    const listIssueTableRows = vi.fn(async (req: IssueTableRowsRequest): Promise<IssueTableRowsResponse> => {
      const cursor = req.page?.cursor ?? null;
      if (cursor === null) {
        if (headPhase === "shifted-cursor") {
          return {
            rows: [
              { issue: issue0, direct_child_count: 0 },
              { issue: issue1, direct_child_count: 0 },
            ],
            total: 4,
            branch_total: 4,
            next_cursor: "cursor-new",
            group_key: null,
            parent_id: null,
            query_fingerprint: "fp-head",
          };
        }
        return {
          rows: [
            {
              issue: headPhase === "same-cursor" ? { ...issue1, title: "Task one refreshed", revision: 2 } : issue1,
              direct_child_count: 0,
            },
            { issue: issue2, direct_child_count: 0 },
          ],
          total: 3,
          branch_total: 3,
          next_cursor: "cursor-old",
          group_key: null,
          parent_id: null,
          query_fingerprint: "fp-head",
        };
      }
      if (cursor === "cursor-old") {
        return {
          rows: [{ issue: issue3, direct_child_count: 0 }],
          total: 3,
          branch_total: 3,
          next_cursor: null,
          group_key: null,
          parent_id: null,
          query_fingerprint: "fp-head",
        };
      }
      if (cursor === "cursor-new") {
        return {
          rows: [
            { issue: issue2, direct_child_count: 0 },
            { issue: issue3, direct_child_count: 0 },
          ],
          total: 4,
          branch_total: 4,
          next_cursor: null,
          group_key: null,
          parent_id: null,
          query_fingerprint: "fp-head",
        };
      }
      throw new Error(`Unexpected cursor: ${cursor}`);
    });
    setupApi({ listIssueTableRows });

    const surfaceKey = "workspace:all";
    getIssueSurfaceViewStore(surfaceKey).getState().setViewMode("grid");

    const qc = createQueryClient();
    renderWithI18n(
      <QueryClientProvider client={qc}>
        <IssueSurface
          surfaceKey={surfaceKey}
          scope={{ type: "workspace", actorKind: "all" }}
          modes={["board", "list", "table", "grid"]}
        />
      </QueryClientProvider>,
    );

    expect(await screen.findByDisplayValue("Task one")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    const tailInput = await screen.findByDisplayValue("Tail task");

    // Start an in-progress draft on page 2 and trigger an ordinary head refresh
    // whose next_cursor still matches "cursor-old". Page 2 and its draft stay intact.
    fireEvent.focus(tailInput);
    fireEvent.change(tailInput, { target: { value: "Tail task draft" } });
    headPhase = "same-cursor";
    await act(async () => {
      await qc.invalidateQueries({ queryKey: issueKeys.tableAll("ws-1") });
    });
    expect(await screen.findByDisplayValue("Task one refreshed")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Tail task draft")).toBeInTheDocument();
    fireEvent.blur(tailInput);

    // Now force a head refetch whose next_cursor shifts ("cursor-old" -> "cursor-new")
    // after a new top item pushes Boundary task off page 1.
    headPhase = "shifted-cursor";
    await act(async () => {
      await qc.invalidateQueries({ queryKey: issueKeys.tableAll("ws-1") });
    });

    expect(await screen.findByDisplayValue("New top task")).toBeInTheDocument();
    // Stale tail from "cursor-old" must not mix with the new head.
    expect(screen.queryByDisplayValue("Tail task")).not.toBeInTheDocument();
    expect(document.querySelector('tr[data-record-id="uuid-3"]')).toBeNull();

    // Load more follows the new valid cursor and restores the shifted boundary row.
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByDisplayValue("Boundary task")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("Tail task")).toBeInTheDocument();
    expect(document.querySelectorAll('tr[data-record-id]')).toHaveLength(4);
    expect(listIssueTableRows).toHaveBeenCalledWith(
      expect.objectContaining({
        page: expect.objectContaining({ cursor: "cursor-new" }),
      }),
    );
  });

  it("hides and restores Status and Assignee independently while the issue title stays visible", async () => {
    const issue = makeIssue({
      id: "uuid-cols",
      identifier: "XSH-7",
      title: "Column task",
    });
    setupApi({
      listIssueTableRows: vi.fn(async () => ({
        rows: [{ issue, direct_child_count: 0 }],
        total: 1,
        branch_total: 1,
        next_cursor: null,
        group_key: null,
        parent_id: null,
        query_fingerprint: "fp-cols",
      })),
    });

    const surfaceKey = "workspace:columns";
    getIssueSurfaceViewStore(surfaceKey).getState().setViewMode("grid");

    const qc = createQueryClient();
    renderWithI18n(
      <QueryClientProvider client={qc}>
        <IssueSurface
          surfaceKey={surfaceKey}
          scope={{ type: "workspace", actorKind: "all" }}
          modes={["board", "list", "table", "grid"]}
        />
      </QueryClientProvider>,
    );

    expect(await screen.findByDisplayValue("Column task")).toBeInTheDocument();
    const grid = within(document.querySelector("[data-apitable-grid]") as HTMLElement);
    expect(grid.getByRole("columnheader", { name: "Issue" })).toBeInTheDocument();
    expect(grid.getByRole("columnheader", { name: "Status" })).toBeInTheDocument();
    expect(grid.getByRole("columnheader", { name: "Assignee" })).toBeInTheDocument();

    const columnsButton = screen.getByRole("button", { name: "Columns" });
    fireEvent.click(columnsButton);

    const issueOption = await screen.findByRole("menuitemcheckbox", { name: "Issue" });
    const statusOption = screen.getByRole("menuitemcheckbox", { name: "Status" });
    const assigneeOption = screen.getByRole("menuitemcheckbox", { name: "Assignee" });
    expect(issueOption).toHaveAttribute("aria-disabled", "true");
    expect(issueOption).toHaveAttribute("aria-checked", "true");
    expect(statusOption).toHaveAttribute("aria-checked", "true");
    expect(assigneeOption).toHaveAttribute("aria-checked", "true");

    fireEvent.click(statusOption);
    await waitFor(() => {
      expect(grid.queryByRole("columnheader", { name: "Status" })).not.toBeInTheDocument();
    });
    expect(grid.getByRole("columnheader", { name: "Issue" })).toBeInTheDocument();
    expect(grid.getByRole("columnheader", { name: "Assignee" })).toBeInTheDocument();
    expect(screen.getByRole("menuitemcheckbox", { name: "Status" })).toHaveAttribute(
      "aria-checked",
      "false",
    );

    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "Assignee" }));
    await waitFor(() => {
      expect(grid.queryByRole("columnheader", { name: "Assignee" })).not.toBeInTheDocument();
    });
    expect(grid.getByRole("columnheader", { name: "Issue" })).toBeInTheDocument();
    expect(grid.queryByRole("columnheader", { name: "Status" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "Status" }));
    await waitFor(() => {
      expect(grid.getByRole("columnheader", { name: "Status" })).toBeInTheDocument();
    });
    expect(grid.queryByRole("columnheader", { name: "Assignee" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "Assignee" }));
    await waitFor(() => {
      expect(grid.getByRole("columnheader", { name: "Assignee" })).toBeInTheDocument();
    });
    expect(grid.getByRole("columnheader", { name: "Issue" })).toBeInTheDocument();
    expect(grid.getByRole("columnheader", { name: "Status" })).toBeInTheDocument();
  });
});
