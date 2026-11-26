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
import { calendarGridDateRange, formatCalendarMonthLabel, todayCalendarDate } from "../../layout/calendar-dates";
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
  return { api: base, listGenericRecords, listGenericTables, listIssueTableRows: base.listIssueTableRows as ReturnType<typeof vi.fn> };
}

function prepareCalendarViewStore(surfaceKey: string) {
  const store = getIssueSurfaceViewStore(surfaceKey);
  store.getState().setViewMode("calendar");
  return store;
}

describe("IssueCalendarView", () => {
  beforeEach(() => {
    mockWsId.current = "ws-1";
    mockIntentNavigate.mockReset();
    pruneIssueSurfaceViewStates(new Set());
  });

  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  it("parses and round-trips calendar in saved view display serialization", () => {
    const store = createStore(viewStoreSlice);
    store.getState().setViewMode("calendar");
    const definition = issueViewDefinitionFromState(store.getState());
    expect(definition.display.viewMode).toBe("calendar");

    const seeded = issueViewSeedDefinition({
      query: {},
      display: { viewMode: "calendar" },
    });
    expect(seeded.display.viewMode).toBe("calendar");
  });

  it("keeps month navigation while the first page loads and labels November in America/Los_Angeles", async () => {
    const previousTz = process.env.TZ;
    process.env.TZ = "America/Los_Angeles";
    let settle!: (value: IssueTableRowsResponse) => void;
    const pending = new Promise<IssueTableRowsResponse>((resolve) => {
      settle = resolve;
    });
    const listIssueTableRows = vi.fn(() => pending);
    setupApi({ listIssueTableRows });
    prepareCalendarViewStore("project:proj-1:calendar-tz");

    vi.setSystemTime(new Date("2026-11-26T20:00:00Z"));

    try {
      renderWithI18n(
        <QueryClientProvider client={createQueryClient()}>
          <IssueSurface
            scope={{ type: "project", projectId: "proj-1", actorKind: "all" }}
            modes={["calendar"]}
            surfaceKey="project:proj-1:calendar-tz"
          />
        </QueryClientProvider>,
      );

      expect(screen.getByRole("button", { name: "Next month" })).toBeEnabled();
      const anchor = document.querySelector("[data-calendar-month]");
      expect(anchor?.getAttribute("data-calendar-month")).toBe("2026-11");
      expect(formatCalendarMonthLabel(2026, 11, "en-US")).toMatch(/November 2026/);
      expect(anchor?.getAttribute("data-calendar-month-label")).toMatch(/November 2026/);

      settle({
        rows: [],
        total: 0,
        branch_total: 0,
        next_cursor: null,
        group_key: null,
        parent_id: null,
        query_fingerprint: "fp-tz",
      });

      await waitFor(() => expect(listIssueTableRows).toHaveBeenCalled());
    } finally {
      vi.useRealTimers();
      if (previousTz === undefined) delete process.env.TZ;
      else process.env.TZ = previousTz;
    }
  });

  it("queries the visible month window, auto-continues pages, and opens canonical detail", async () => {
    vi.setSystemTime(new Date("2026-11-26T12:00:00Z"));
    const gridWindow = calendarGridDateRange(2026, 11);
    const onScreenDue = "2026-11-18";
    const issue1 = makeIssue({
      id: "uuid-1",
      identifier: "XSH-1",
      title: "November task",
      due_date: onScreenDue,
    });
    const issue2 = makeIssue({
      id: "uuid-2",
      identifier: "XSH-2",
      title: "Trailing page task",
      due_date: "2026-11-20",
    });

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
    prepareCalendarViewStore(surfaceKey);

    renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface
          surfaceKey={surfaceKey}
          scope={{ type: "project", projectId: "proj-1", actorKind: "all" }}
          modes={["board", "list", "table", "grid", "gallery", "calendar"]}
        />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(listIssueTableRows).toHaveBeenCalled());
    expect(listIssueTableRows.mock.calls[0]?.[0]?.query.filters?.issue_date_bound).toEqual({
      field: "due_date",
      start: gridWindow.start,
      end: gridWindow.end,
    });
    await waitFor(() => expect(listIssueTableRows).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole("button", { name: /November task/ })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: /Trailing page task/ })).toBeInTheDocument();
    expect(listGenericRecords).not.toHaveBeenCalled();
    expect(listGenericTables).not.toHaveBeenCalled();
    expect(document.querySelector('[data-view-kind="calendar"]')).toBeTruthy();
    expect(document.querySelector(`[data-calendar-date="${onScreenDue}"]`)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /XSH-1 November task/ }));
    expect(mockIntentNavigate).toHaveBeenCalledWith(
      "/w/demo/issues/uuid-1",
      "push",
      "XSH-1",
    );

    const crowdedCell = document.querySelector('[data-calendar-date="2026-11-18"] .overflow-y-auto');
    expect(crowdedCell?.className).toContain("max-h-28");

    vi.useRealTimers();
  });

  it("refetches with a new month window after navigation", async () => {
    const listIssueTableRows = vi.fn().mockResolvedValue({
      rows: [],
      total: 0,
      branch_total: 0,
      next_cursor: null,
      group_key: null,
      parent_id: null,
      query_fingerprint: "fp-1",
    } satisfies IssueTableRowsResponse);
    setupApi({ listIssueTableRows });
    prepareCalendarViewStore(issueScopeKey({ type: "project", projectId: "proj-1", actorKind: "all" }));

    renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface
          scope={{ type: "project", projectId: "proj-1", actorKind: "all" }}
          modes={["calendar"]}
        />
      </QueryClientProvider>,
    );

    await screen.findByText(/scheduled|No issues in this month/);
    const firstBound = listIssueTableRows.mock.calls[0]?.[0]?.query.filters?.issue_date_bound;

    fireEvent.click(await screen.findByRole("button", { name: "Next month" }));

    await waitFor(() => {
      const latest = listIssueTableRows.mock.calls.at(-1)?.[0]?.query.filters?.issue_date_bound;
      expect(latest?.start).not.toBe(firstBound?.start);
    });
  });

  it("clears stale cards when filters change", async () => {
    const issue = makeIssue({
      id: "uuid-a",
      identifier: "XSH-8",
      title: "Filtered card",
      due_date: todayCalendarDate().iso,
    });
    const listIssueTableRows = vi.fn().mockResolvedValue({
      rows: [{ issue, direct_child_count: 0 }],
      total: 1,
      branch_total: 1,
      next_cursor: null,
      group_key: null,
      parent_id: null,
      query_fingerprint: "fp-filter",
    } satisfies IssueTableRowsResponse);
    setupApi({ listIssueTableRows });

    const surfaceKey = issueScopeKey({ type: "workspace", actorKind: "all" });
    const store = prepareCalendarViewStore(surfaceKey);
    renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface scope={{ type: "workspace", actorKind: "all" }} modes={["calendar"]} surfaceKey={surfaceKey} />
      </QueryClientProvider>,
    );

    expect(await screen.findByRole("button", { name: /Filtered card/ })).toBeInTheDocument();
    act(() => store.getState().togglePriorityFilter("urgent"));
    listIssueTableRows.mockResolvedValueOnce({
      rows: [],
      total: 0,
      branch_total: 0,
      next_cursor: null,
      group_key: null,
      parent_id: null,
      query_fingerprint: "fp-filter-2",
    });
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Filtered card/ })).not.toBeInTheDocument();
    });
  });
});