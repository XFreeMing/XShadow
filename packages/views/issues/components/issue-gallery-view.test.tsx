// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import {
  getIssueSurfaceViewStore,
  pruneIssueSurfaceViewStates,
} from "@multica/core/issues/stores/surface-view-store";
import { issueScopeKey } from "@multica/core/issues/surface/scope";
import {
  issueViewDefinitionFromState,
  issueViewSeedDefinition,
} from "@multica/core/issue-views/definition";
import { createStore } from "zustand/vanilla";
import { viewStoreSlice } from "@multica/core/issues/stores/view-store";
import type {
  Issue,
  IssueTableRowsRequest,
  IssueTableRowsResponse,
} from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssueSurface } from "../surface/issue-surface";

const mockWsId = vi.hoisted(() => ({ current: "ws-1" }));
const mockIntentNavigate = vi.hoisted(() => vi.fn());

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

function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity },
      mutations: { retry: false },
    },
  });
}

function setupApi(overrides: Partial<ApiClient> = {}) {
  const listGenericRecords = vi.fn();
  const listGenericTables = vi.fn();
  const base: Record<string, unknown> = {
    getBaseUrl: vi.fn(() => "http://localhost"),
    listIssueStatuses: vi.fn().mockResolvedValue([
      { key: "todo", label: "Todo", category: "unstarted", color: "#999", icon: "circle", position: 1 },
      { key: "done", label: "Done", category: "done", color: "#0a0", icon: "check-circle", position: 3 },
    ]),
    listIssueWorkflows: vi.fn().mockResolvedValue([]),
    listIssueTypes: vi.fn().mockResolvedValue({ issue_types: [], total: 0 }),
    listProperties: vi.fn().mockResolvedValue({ properties: [], total: 0 }),
    listProjects: vi.fn().mockResolvedValue([]),
    listMembers: vi.fn().mockResolvedValue([]),
    listAgents: vi.fn().mockResolvedValue([]),
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
    listGenericRecords,
    listGenericTables,
    updateIssue: vi.fn(),
    ...overrides,
  };
  setApiInstance(base as unknown as ApiClient);
  return { api: base, listGenericRecords, listGenericTables };
}

function prepareGalleryViewStore(surfaceKey: string) {
  const store = getIssueSurfaceViewStore(surfaceKey);
  store.getState().setViewMode("gallery");
  return store;
}

describe("IssueGalleryView", () => {
  beforeEach(() => {
    mockWsId.current = "ws-1";
    mockIntentNavigate.mockReset();
    pruneIssueSurfaceViewStates(new Set());
  });

  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  it("parses and round-trips gallery in saved view display serialization", () => {
    const store = createStore(viewStoreSlice);
    store.getState().setViewMode("gallery");
    const definition = issueViewDefinitionFromState(store.getState());
    expect(definition.display.viewMode).toBe("gallery");

    const seeded = issueViewSeedDefinition({
      query: {},
      display: { viewMode: "gallery" },
    });
    expect(seeded.display.viewMode).toBe("gallery");
  });

  it("requests native table rows, paginates, opens canonical detail, and never calls generic record APIs", async () => {
    const issue1 = makeIssue({ id: "uuid-1", identifier: "XSH-1", title: "Gallery task one", priority: "urgent" });
    const issue2 = makeIssue({ id: "uuid-2", identifier: "XSH-2", title: "Gallery task two" });
    const listIssueTableRows = vi.fn(async (req: IssueTableRowsRequest): Promise<IssueTableRowsResponse> => {
      if (!req.page?.cursor) {
        return {
          rows: [{ issue: issue1, direct_child_count: 0 }],
          total: 2,
          branch_total: 2,
          next_cursor: "cursor-2",
          group_key: null,
          parent_id: null,
          query_fingerprint: "fp-1",
        };
      }
      return {
        rows: [{ issue: issue2, direct_child_count: 0 }],
        total: 2,
        branch_total: 2,
        next_cursor: null,
        group_key: null,
        parent_id: null,
        query_fingerprint: "fp-1",
      };
    });
    const { listGenericRecords, listGenericTables } = setupApi({ listIssueTableRows });

    const surfaceKey = "project:proj-1:all";
    const store = prepareGalleryViewStore(surfaceKey);

    renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface
          surfaceKey={surfaceKey}
          scope={{ type: "project", projectId: "proj-1", actorKind: "all" }}
          modes={["board", "list", "table", "grid", "gallery"]}
        />
      </QueryClientProvider>,
    );

    expect(await screen.findByText("Gallery task one")).toBeInTheDocument();
    expect(screen.getByText("Todo")).toBeInTheDocument();
    expect(screen.getByText("Urgent")).toBeInTheDocument();
    act(() => store.getState().toggleCardProperty("priority"));
    expect(screen.queryByText("Urgent")).toBeNull();
    expect(listIssueTableRows).toHaveBeenCalled();
    expect(listIssueTableRows.mock.calls[0]?.[0]?.query.scope.kind).toBe("project");
    expect(listGenericRecords).not.toHaveBeenCalled();
    expect(listGenericTables).not.toHaveBeenCalled();
    expect(document.querySelector('[data-issue-id="uuid-1"]')).toBeTruthy();
    expect(document.querySelector('[data-view-kind="gallery"]')).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Gallery task one" }));
    expect(mockIntentNavigate).toHaveBeenCalledWith(
      "/w/demo/issues/uuid-1",
      "push",
      "XSH-1",
    );

    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Gallery task two")).toBeInTheDocument();
    expect(listIssueTableRows).toHaveBeenCalledTimes(2);
  });

  it("drops previous cards when membership changes before the next page resolves", async () => {
    const projectAIssue = makeIssue({
      id: "uuid-a",
      identifier: "XSH-10",
      title: "Scope A card",
      project_id: "proj-a",
    });
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
    prepareGalleryViewStore(issueScopeKey(scopeA));
    prepareGalleryViewStore(issueScopeKey(scopeB));

    const qc = createQueryClient();
    const { rerender } = renderWithI18n(
      <QueryClientProvider client={qc}>
        <IssueSurface scope={scopeA} modes={["gallery"]} />
      </QueryClientProvider>,
    );

    expect(await screen.findByText("Scope A card")).toBeInTheDocument();

    rerender(
      <QueryClientProvider client={qc}>
        <IssueSurface scope={scopeB} modes={["gallery"]} />
      </QueryClientProvider>,
    );

    expect(screen.queryByText("Scope A card")).not.toBeInTheDocument();

    const projectBIssue = makeIssue({
      id: "uuid-b",
      identifier: "XSH-20",
      title: "Scope B card",
      project_id: "proj-b",
    });
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

    await waitFor(() => {
      expect(screen.getByText("Scope B card")).toBeInTheDocument();
    });
    expect(screen.queryByText("Scope A card")).not.toBeInTheDocument();
  });
});
