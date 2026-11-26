// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import type { Issue, IssueProperty } from "@multica/core/types";
import { issueComputedPropertiesOptions } from "@multica/core/properties";
import { renderWithI18n } from "../../test/i18n";
import { ComputedIssuePropertyProvider } from "./computed-property-context";
import { BoardCardContent } from "./board-card";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

const workspaceProperties: IssueProperty[] = [
  {
    id: "prop-formula",
    workspace_id: "ws-1",
    name: "Points",
    type: "formula",
    config: {},
    archived: false,
    position: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "prop-lookup",
    workspace_id: "ws-1",
    name: "Deps",
    type: "lookup",
    config: {},
    archived: false,
    position: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

vi.mock("@multica/core/properties", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/properties")>();
  return {
    ...actual,
    propertyListOptions: () => ({
      queryKey: ["properties", "ws-1", "list", false],
      queryFn: async () => workspaceProperties,
    }),
  };
});

vi.mock("@multica/core/issue-types", () => ({
  issueTypeListOptions: () => ({
    queryKey: ["issue-types", "ws-1"],
    queryFn: async () => [],
  }),
}));

const viewState = vi.hoisted(() => ({
  viewMode: "board" as const,
  grouping: "status" as const,
  swimlaneGrouping: "assignee" as const,
  cardProperties: {
    priority: false,
    description: false,
    assignee: false,
    startDate: false,
    dueDate: false,
    project: false,
    childProgress: false,
    labels: false,
  },
  cardPropertyIds: ["prop-formula", "prop-lookup"],
}));

vi.mock("@multica/core/issues/stores/view-store-context", () => ({
  useViewStore: (selector: (state: typeof viewState) => unknown) =>
    selector(viewState),
}));

vi.mock("../../i18n", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../i18n")>();
  return {
    ...actual,
    useTimeAgo: () => () => "now",
  };
});

vi.mock("./issue-agent-activity-indicator", () => ({
  IssueAgentActivityIndicator: () => null,
}));

const navigation: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path) => `https://app.example${path}`,
};

describe("BoardCardContent computed fields", () => {
  it("renders selected formula and lookup values from the computed provider", async () => {
    const issue = {
      id: "issue-1",
      workspace_id: "ws-1",
      identifier: "MUL-1",
      title: "Ship",
      status: "todo",
      priority: "none",
      properties: {},
    } as Issue;

    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    qc.setQueryData(issueComputedPropertiesOptions("ws-1", ["issue-1"]).queryKey, {
      "issue-1": {
        "prop-formula": 12,
        "prop-lookup": ["Blocked", "Waiting"],
      },
    });

    renderWithI18n(
      <QueryClientProvider client={qc}>
        <NavigationProvider value={navigation}>
          <ComputedIssuePropertyProvider wsId="ws-1" issueIds={["issue-1"]}>
            <BoardCardContent issue={issue} />
          </ComputedIssuePropertyProvider>
        </NavigationProvider>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText("12")).toBeInTheDocument();
      expect(screen.getByText("Blocked")).toBeInTheDocument();
      expect(screen.getByText("Waiting")).toBeInTheDocument();
    });
  });
});