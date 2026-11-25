// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { useIssueDraftStore } from "@multica/core/issues/stores/draft-store";
import type { Issue, IssueProperty, IssueType } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enEditor from "../locales/en/editor.json";
import enIssues from "../locales/en/issues.json";
import enModals from "../locales/en/modals.json";
import enProjects from "../locales/en/projects.json";
import { CreateIssueModal } from "./create-issue";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
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
  useCurrentWorkspace: () => ({ id: "ws-1", name: "Demo", slug: "demo" }),
  useWorkspaceSlug: () => "demo",
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/w/demo/issues/${id}`,
    settings: () => "/w/demo/settings",
  }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: () => "Alice" }),
}));

vi.mock("@multica/core/issue-statuses/hooks", () => ({
  useIssueStatuses: () => ({
    statuses: [{ key: "todo", name: "Todo", category: "unstarted", icon: "circle", color: "#888", built_in: true, archived_at: null }],
    categoryOf: () => "unstarted",
    colorOf: () => "#888",
    iconOf: () => "circle",
    entryOf: () => ({ key: "todo", name: "Todo", category: "unstarted", icon: "circle", color: "#888", built_in: true, archived_at: null }),
  }),
}));

vi.mock("@multica/core/issue-workflows", () => ({
  useProjectWithWorkflow: () => ({ workflow: null, defaultWorkflow: null }),
  useProjectWorkflow: () => null,
  workflowAllowsStatus: () => true,
  workflowStep: () => null,
}));

vi.mock("../navigation", () => ({
  AppLink: ({ children, href, ...props }: React.ComponentProps<"a">) => (
    <a href={href} {...props}>{children}</a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/" }),
  useAppOrigin: () => "http://localhost",
  resolveClickIntent: () => "push",
}));

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    custom: vi.fn(),
    dismiss: vi.fn(),
  },
}));

vi.mock("../editor", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../editor")>();
  return {
    ...actual,
    TitleEditor: ({ placeholder, onChange, defaultValue }: { placeholder?: string; onChange?: (v: string) => void; defaultValue?: string }) => (
      <input
        placeholder={placeholder}
        defaultValue={defaultValue}
        onChange={(e) => onChange?.(e.target.value)}
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
    id: "prop-reads",
    workspace_id: "ws-1",
    name: "Read Count",
    type: "number",
    description: "",
    icon: "hash",
    config: {},
    position: 2,
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
    position: 3,
    archived: false,
    usage_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "prop-confirmed",
    workspace_id: "ws-1",
    name: "Review Confirmed",
    type: "checkbox",
    description: "",
    icon: "circle-check",
    config: {},
    position: 4,
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
    fields: [
      { property_id: "prop-url", required: true },
      { property_id: "prop-reads", required: false, default_value: 0 },
    ],
    position: 1,
    archived: false,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "type-delivery",
    workspace_id: "ws-1",
    name: "Delivery",
    fields: [
      { property_id: "prop-notes", required: true },
      { property_id: "prop-confirmed", required: false, default_value: false },
    ],
    position: 2,
    archived: false,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

const mockCreateIssue = vi.fn();

function renderModal() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider
        locale="en"
        resources={{
          en: {
            common: enCommon,
            editor: enEditor,
            issues: enIssues,
            modals: enModals,
            projects: enProjects,
          },
        }}
      >
        <CreateIssueModal onClose={vi.fn()} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("CreateIssueModal task types", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useIssueDraftStore.getState().clearDraft();
    mockCreateIssue.mockImplementation(async (data) => ({
      id: "issue-new",
      workspace_id: "ws-1",
      number: 10,
      identifier: "DEM-10",
      title: data.title,
      description: data.description ?? null,
      status: data.status ?? "todo",
      priority: data.priority ?? "none",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 0,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: data.properties ?? {},
      issue_type_id: data.issue_type_id ?? null,
      labels: [],
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    } as Issue));

    setApiInstance({
      listProperties: vi.fn().mockResolvedValue({ properties: PROPERTIES, total: PROPERTIES.length }),
      listIssueTypes: vi.fn().mockResolvedValue({ issue_types: ISSUE_TYPES, total: ISSUE_TYPES.length }),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      listLabels: vi.fn().mockResolvedValue({ labels: [], total: 0 }),
      listMembers: vi.fn().mockResolvedValue([]),
      listAgents: vi.fn().mockResolvedValue([]),
      listSquads: vi.fn().mockResolvedValue([]),
      createIssue: mockCreateIssue,
    } as unknown as ApiClient);
  });

  afterEach(() => {
    cleanup();
  });

  it("switches between Research and Delivery types, enforces required fields, and submits 0/false defaults", async () => {
    const user = userEvent.setup();
    renderModal();

    const typeSelector = await screen.findByTestId("issue-type-selector");
    await user.click(typeSelector);
    await user.click(await screen.findByRole("menuitem", { name: "Research" }));

    // Immediately shows Research fields and default 0.
    expect(screen.getByText("Source URL")).toBeTruthy();
    expect(screen.getByText("Read Count")).toBeTruthy();
    expect(screen.getByText("0")).toBeTruthy();
    expect(screen.queryByText("Due Notes")).toBeNull();

    // Enter title and attempt submit without required Source URL.
    const titleBox = screen.getByPlaceholderText(/Issue title/i);
    await user.type(titleBox, "Paper review");
    await user.click(screen.getByRole("button", { name: /Create Issue/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Source URL");
    expect(mockCreateIssue).not.toHaveBeenCalled();

    // Switch to Delivery — untouched default 0 clears, Delivery fields + false default appear immediately.
    await user.click(screen.getByTestId("issue-type-selector"));
    await user.click(await screen.findByRole("menuitem", { name: "Delivery" }));

    expect(screen.getByText("Due Notes")).toBeTruthy();
    expect(screen.getByText("Review Confirmed")).toBeTruthy();
    expect(screen.getByText("No")).toBeTruthy();
    expect(screen.queryByText("Source URL")).toBeNull();

    // Fill required Due Notes and submit.
    await user.click(screen.getByText("Due Notes"));
    const notesInput = await screen.findByPlaceholderText(/Enter value/i);
    await user.type(notesInput, "Ship Friday{Enter}");

    await user.click(screen.getByRole("button", { name: /Create Issue/i }));

    await waitFor(() => {
      expect(mockCreateIssue).toHaveBeenCalledWith(
        expect.objectContaining({
          title: "Paper review",
          issue_type_id: "type-delivery",
          properties: {
            "prop-notes": "Ship Friday",
            "prop-confirmed": false,
          },
        }),
      );
    });
  });
});
