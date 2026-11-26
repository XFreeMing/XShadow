// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
import { useIssueDraftStore } from "@multica/core/issues/stores/draft-store";
import type { Issue, IssueProperty, IssueType } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssueSurface } from "../surface/issue-surface";

const mockWsId = vi.hoisted(() => ({ current: "ws-1" }));
const mockIntentNavigate = vi.hoisted(() => vi.fn());
const mockCreateIssue = vi.hoisted(() => vi.fn());

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

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), custom: vi.fn(), dismiss: vi.fn() },
}));

vi.mock("../../editor", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../editor")>();
  return {
    ...actual,
    TitleEditor: ({
      placeholder,
      onChange,
      defaultValue,
      onSubmitShortcut,
    }: {
      placeholder?: string;
      onChange?: (v: string) => void;
      defaultValue?: string;
      onSubmitShortcut?: () => void;
    }) => (
      <input
        aria-label={placeholder}
        placeholder={placeholder}
        defaultValue={defaultValue}
        onChange={(e) => onChange?.(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) onSubmitShortcut?.();
        }}
      />
    ),
    ContentEditor: () => <div data-testid="mock-content-editor" />,
  };
});

const PROPERTIES: IssueProperty[] = [
  {
    id: "prop-url",
    workspace_id: "ws-1",
    name: "Source URL",
    type: "url",
    description: "",
    icon: "link",
    config: {},
    position: 1,
    archived: false,
    usage_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "prop-notes",
    workspace_id: "ws-1",
    name: "Due Notes",
    type: "text",
    description: "",
    icon: "tag",
    config: {},
    position: 2,
    archived: false,
    usage_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

const ISSUE_TYPES: IssueType[] = [
  {
    id: "type-research",
    workspace_id: "ws-1",
    name: "Research",
    fields: [{ property_id: "prop-url", required: true }],
    position: 1,
    archived: false,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "type-delivery",
    workspace_id: "ws-1",
    name: "Delivery",
    fields: [{ property_id: "prop-notes", required: true }],
    position: 2,
    archived: false,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
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
  const listGenericRecords = vi.fn();
  const listGenericTables = vi.fn();
  mockCreateIssue.mockImplementation(async (data) => ({
    id: "uuid-new",
    workspace_id: "ws-1",
    number: 42,
    identifier: "XSH-42",
    title: data.title,
    description: data.description ?? null,
    status: data.status ?? "todo",
    status_category: "unstarted",
    priority: data.priority ?? "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: data.project_id ?? null,
    position: 0,
    stage: null,
    metadata: {},
    properties: data.properties ?? {},
    issue_type_id: data.issue_type_id ?? null,
    labels: [],
    start_date: null,
    due_date: null,
    created_at: "2026-11-25T00:00:00Z",
    updated_at: "2026-11-25T00:00:00Z",
    revision: 1,
  } satisfies Issue));

  const base: Record<string, unknown> = {
    getBaseUrl: vi.fn(() => "http://localhost"),
    listIssueStatuses: vi.fn().mockResolvedValue([
      { key: "todo", label: "Todo", category: "unstarted", color: "#999", icon: "circle", position: 1 },
    ]),
    listIssueWorkflows: vi.fn().mockResolvedValue([]),
    listIssueTypes: vi.fn().mockResolvedValue({ issue_types: ISSUE_TYPES, total: ISSUE_TYPES.length }),
    listProperties: vi.fn().mockResolvedValue({ properties: PROPERTIES, total: PROPERTIES.length }),
    listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
    listMembers: vi.fn().mockResolvedValue([]),
    listAgents: vi.fn().mockResolvedValue([]),
    listSquads: vi.fn().mockResolvedValue([]),
    listLabels: vi.fn().mockResolvedValue({ labels: [], total: 0 }),
    getAssigneeFrequency: vi.fn().mockResolvedValue([]),
    listWorkspaceWakeupSummaries: vi.fn().mockResolvedValue([]),
    listIssueTableFacets: vi.fn().mockResolvedValue({ facets: [], total: 0 }),
    listIssues: vi.fn().mockResolvedValue({ issues: [], total: 0 }),
    listGenericRecords,
    listGenericTables,
    createIssue: mockCreateIssue,
    ...overrides,
  };
  setApiInstance(base as unknown as ApiClient);
  return { api: base, listGenericRecords, listGenericTables, createIssue: mockCreateIssue };
}

function prepareFormViewStore(surfaceKey: string) {
  const store = getIssueSurfaceViewStore(surfaceKey);
  store.getState().setViewMode("form");
  return store;
}

describe("IssueFormView", () => {
  beforeEach(() => {
    mockWsId.current = "ws-1";
    mockIntentNavigate.mockReset();
    mockCreateIssue.mockClear();
    useIssueDraftStore.getState().clearDraft();
    pruneIssueSurfaceViewStates(new Set());
  });

  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  it("parses and round-trips form in saved view display serialization", () => {
    const store = createStore(viewStoreSlice);
    store.getState().setViewMode("form");
    const definition = issueViewDefinitionFromState(store.getState());
    expect(definition.display.viewMode).toBe("form");

    const seeded = issueViewSeedDefinition({
      query: {},
      display: { viewMode: "form" },
    });
    expect(seeded.display.viewMode).toBe("form");
  });

  it("rejects missing required type fields, creates once via native API, opens detail, and never calls generic record APIs", async () => {
    const { listGenericRecords, listGenericTables, createIssue } = setupApi();
    const user = userEvent.setup();
    const surfaceKey = issueScopeKey({ type: "workspace", actorKind: "all" });
    prepareFormViewStore(surfaceKey);

    renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface
          scope={{ type: "workspace", actorKind: "all" }}
          modes={["form"]}
          surfaceKey={surfaceKey}
        />
      </QueryClientProvider>,
    );

    expect(document.querySelector('[data-view-kind="form"]')).toBeTruthy();

    const titleInput = await screen.findByPlaceholderText(/Issue title/i);
    await user.type(titleInput, "Form surface task");

    await user.click(screen.getByTestId("issue-type-selector"));
    await user.click(await screen.findByRole("menuitem", { name: "Research" }));
    expect(screen.getByText("Source URL")).toBeTruthy();

    const submit = screen.getByRole("button", { name: /Create Issue/i });
    await user.click(submit);
    expect((await screen.findByRole("alert")).textContent).toContain("Source URL");
    expect(createIssue).not.toHaveBeenCalled();

    await user.click(screen.getByTestId("issue-type-selector"));
    await user.click(await screen.findByRole("menuitem", { name: "Delivery" }));

    await user.click(screen.getByText("Due Notes"));
    const notesInput = await screen.findByPlaceholderText(/Enter value/i);
    await user.type(notesInput, "Friday{Enter}");
    expect(createIssue).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByPlaceholderText(/Enter value/i)).toBeNull());
    await user.click(screen.getByText("Due Notes"));
    const existingNotes = await screen.findByPlaceholderText(/Enter value/i);
    await user.clear(existingNotes);
    await user.type(existingNotes, "Monday{Enter}");
    expect(createIssue).not.toHaveBeenCalled();

    await user.dblClick(submit);
    await waitFor(() => expect(createIssue).toHaveBeenCalledTimes(1));
    expect(createIssue).toHaveBeenCalledWith(
      expect.objectContaining({
        title: "Form surface task",
        issue_type_id: "type-delivery",
        properties: { "prop-notes": "Monday" },
      }),
    );
    expect(listGenericRecords).not.toHaveBeenCalled();
    expect(listGenericTables).not.toHaveBeenCalled();

    const success = await screen.findByRole("status");
    expect(success.getAttribute("data-form-status")).toBe("success");
    await user.click(screen.getByRole("button", { name: /View issue/i }));
    expect(mockIntentNavigate).toHaveBeenCalledWith(
      "/w/demo/issues/uuid-new",
      "push",
      "XSH-42",
    );
  });

  it("locks project scope on project surfaces without disturbing the modal draft store", async () => {
    const { createIssue } = setupApi();
    useIssueDraftStore.getState().setManual({ title: "modal draft" });
    const draftBefore = useIssueDraftStore.getState().draft.manual.title;

    const surfaceKey = issueScopeKey({ type: "project", projectId: "proj-1" });
    prepareFormViewStore(surfaceKey);

    renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface
          scope={{ type: "project", projectId: "proj-1" }}
          modes={["form"]}
          surfaceKey={surfaceKey}
        />
      </QueryClientProvider>,
    );

    const user = userEvent.setup();
    await user.type(await screen.findByPlaceholderText(/Issue title/i), "Project scoped");
    await user.click(screen.getByRole("button", { name: /Create Issue/i }));

    await waitFor(() => expect(createIssue).toHaveBeenCalledTimes(1));
    expect(createIssue).toHaveBeenCalledWith(
      expect.objectContaining({ project_id: "proj-1", title: "Project scoped" }),
    );
    expect(useIssueDraftStore.getState().draft.manual.title).toBe(draftBefore);
  });

  it("keeps a preselected type while its fields load and applies its defaults", async () => {
    const issueTypes = ISSUE_TYPES.map((type) => type.id === "type-delivery"
      ? { ...type, fields: [{ ...type.fields[0]!, default_value: "Review Monday" }] }
      : type);
    const { promise, resolve } = Promise.withResolvers<{ issue_types: IssueType[]; total: number }>();
    const { createIssue } = setupApi({ listIssueTypes: vi.fn().mockReturnValue(promise) });
    const scope = { type: "workspace", actorKind: "all" } as const;
    const surfaceKey = issueScopeKey(scope);
    prepareFormViewStore(surfaceKey);

    renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface
          scope={scope}
          modes={["form"]}
          surfaceKey={surfaceKey}
          createDefaults={{ title: "Prepared task", issue_type_id: "type-delivery" }}
        />
      </QueryClientProvider>,
    );

    const submit = screen.getByRole("button", { name: /Create Issue/i });
    expect(submit).toBeDisabled();
    resolve({ issue_types: issueTypes, total: issueTypes.length });
    await screen.findByText("Review Monday");
    await userEvent.setup().click(submit);
    await waitFor(() => expect(createIssue).toHaveBeenCalledWith(expect.objectContaining({
      title: "Prepared task",
      issue_type_id: "type-delivery",
      properties: { "prop-notes": "Review Monday" },
    })));
  });

  it("uses distinct upload registry keys per embedded surface scope", async () => {
    setupApi();
    const wsKey = issueScopeKey({ type: "workspace", actorKind: "all" });
    const projKey = issueScopeKey({ type: "project", projectId: "proj-old" });
    prepareFormViewStore(wsKey);

    const { unmount } = renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface
          scope={{ type: "workspace", actorKind: "all" }}
          modes={["form"]}
          surfaceKey={wsKey}
        />
      </QueryClientProvider>,
    );

    await screen.findByPlaceholderText(/Issue title/i);
    unmount();

    prepareFormViewStore(projKey);
    renderWithI18n(
      <QueryClientProvider client={createQueryClient()}>
        <IssueSurface
          scope={{ type: "project", projectId: "proj-old" }}
          modes={["form"]}
          surfaceKey={projKey}
        />
      </QueryClientProvider>,
    );

    const projectForm = await screen.findByPlaceholderText(/Issue title/i);
    expect(
      projectForm.closest("[data-form-upload-key]")?.getAttribute("data-form-upload-key"),
    ).toContain("proj-old");
  });
});
