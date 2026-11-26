// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { IssueProperty, IssueType } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import enSettings from "../../locales/en/settings.json";
import { TaskTemplatesSection } from "./issue-types-section";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

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
    id: "prop-ready",
    workspace_id: "ws-1",
    name: "Evidence ready",
    type: "formula",
    description: "Checks that the source was added.",
    config: { expression: "IF({Source URL}, TRUE, FALSE)" },
    position: 3,
    archived: false,
    usage_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

const EXISTING_TYPES: IssueType[] = [
  {
    id: "type-research",
    workspace_id: "ws-1",
    name: "Research",
    fields: [
      { property_id: "prop-url", required: true },
      { property_id: "prop-reads", required: false, default_value: 0 },
      { property_id: "prop-ready", required: false },
    ],
    position: 1,
    archived: false,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

const mockListIssueTypes = vi.fn();
const mockCreateIssueType = vi.fn();
const mockUpdateIssueType = vi.fn();

function renderSection(canManage: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider
        locale="en"
        resources={{ en: { common: enCommon, issues: enIssues, settings: enSettings } }}
      >
        <TaskTemplatesSection canManage={canManage} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("TaskTemplatesSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockListIssueTypes.mockResolvedValue({ issue_types: EXISTING_TYPES, total: 1 });
    mockCreateIssueType.mockResolvedValue({
      id: "type-delivery",
      workspace_id: "ws-1",
      name: "Delivery",
      fields: [{ property_id: "prop-url", required: true }],
      position: 2,
      archived: false,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    });
    setApiInstance({
      listIssueTypes: mockListIssueTypes,
      createIssueType: mockCreateIssueType,
      updateIssueType: mockUpdateIssueType,
      listProperties: vi.fn(async () => ({ properties: PROPERTIES, total: PROPERTIES.length })),
      listIssueStatuses: vi.fn(async () => ({ statuses: [{
        key: "custom_review",
        name: "Peer review",
        category: "started",
        is_system: false,
      }] })),
      listWorkMethods: vi.fn(async () => [{
        id: "research-method",
        name: "Research method",
        revision: 1,
        kind: "research",
        keywords: [],
        review: "Assess the evidence",
        rework_step: 0,
        plan_task: { issue_type_id: "type-research", seed_property_id: "prop-url" },
        steps: [{
          name: "Submit evidence",
          output: "Research findings",
          table_gate: { issue_type_id: "type-research", property_id: "prop-ready" },
        }],
      }]),
    } as unknown as ApiClient);
  });

  afterEach(() => {
    cleanup();
  });

  it("shows template fields, formulas and method bindings to non-admins", async () => {
    const user = userEvent.setup();
    renderSection(false);
    await user.click(await screen.findByText("Task (abstract)"));
    expect(screen.getByText("General task")).toBeVisible();
    expect(screen.getByText("Assignee")).toBeVisible();
    await user.click(screen.getByText("Task (abstract)"));
    await user.click(await screen.findByText("Research"));
    expect(screen.getByRole("table", { name: "Table preview" })).toBeVisible();
    expect(screen.getByRole("columnheader", { name: /Status/ })).toBeVisible();
    expect(await screen.findByText(/Peer review/)).toBeVisible();
    expect(screen.getByText("Source URL")).toBeVisible();
    expect(screen.getByText("Read Count")).toBeVisible();
    expect(screen.getByText("0")).toBeVisible();
    expect(screen.getByText("IF({Source URL}, TRUE, FALSE)")).toBeVisible();
    expect(screen.getByText("Research method")).toBeVisible();
    expect(screen.getByText(/Submit evidence.*Evidence ready/)).toBeVisible();
    expect(screen.queryByRole("button", { name: "New task type" })).toBeNull();
  });

  it("allows admin to create a new task type with ordered fields and required toggle", async () => {
    const user = userEvent.setup();
    renderSection(true);
    await screen.findByText("Research");

    await user.click(screen.getByRole("button", { name: "New task type" }));
    const nameInput = await screen.findByLabelText("Name");
    await user.type(nameInput, "Delivery");

    await user.click(screen.getByRole("button", { name: /Add field/i }));
    await user.click(await screen.findByRole("menuitem", { name: "Source URL" }));

    const requiredSwitch = screen.getByRole("switch");
    await user.click(requiredSwitch);

    await user.click(screen.getByRole("button", { name: /Save/i }));

    await waitFor(() => {
      expect(mockCreateIssueType).toHaveBeenCalledWith({
        name: "Delivery",
        fields: [{ property_id: "prop-url", required: true }],
      });
    });
  });
});
