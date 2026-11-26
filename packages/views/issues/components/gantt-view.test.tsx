import { createStore } from "zustand/vanilla";
import { buildIssueStatusCatalog } from "@multica/core/issue-statuses/queries";

vi.mock("@multica/core/issue-statuses/hooks", () => ({
  useIssueStatuses: () => buildIssueStatusCatalog([]),
}));
import { describe, expect, it, vi, beforeAll, afterAll, beforeEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";

const listIssueTableRowsMock = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      listIssueTableRows: listIssueTableRowsMock,
    },
  };
});
import {
  type IssueViewState,
  viewStoreSlice,
} from "@multica/core/issues/stores/view-store";
import type { StoreApi } from "zustand/vanilla";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssueContextMenuProvider } from "../actions";

const workspaceProperties = vi.hoisted(() => [
  {
    id: "prop-formula",
    workspace_id: "workspace-1",
    name: "Points",
    type: "formula",
    config: {},
    archived: false,
    position: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
]);

vi.mock("@multica/core/properties", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/properties")>();
  return {
    ...actual,
    propertyListOptions: () => ({
      queryKey: ["properties", "workspace-1", "list", false],
      queryFn: async () => workspaceProperties,
    }),
  };
});

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

// The row's start / due range lives in a tooltip. This test is about how the
// dates are formatted, not about hover timing, so render the content inline.
vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => children,
  TooltipTrigger: ({ render }: { render: React.ReactNode }) => render,
  TooltipContent: ({ children }: { children: React.ReactNode }) => children,
}));

vi.mock("../../navigation", () => ({
  AppLink: ({
    children,
    href,
  }: {
    children: React.ReactNode;
    href: string;
  }) => <a href={href}>{children}</a>,
  useNavigation: () => ({ push: vi.fn(), pathname: "/issues" }),
  resolveClickIntent: () => "push",
  useIntentNavigate: () => () => {},
  NavigationProvider: ({ children }: { children: React.ReactNode }) => children,
}));

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspaceSlug: () => "acme",
    useRequiredWorkspaceSlug: () => "acme",
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});

import { issueComputedPropertiesOptions } from "@multica/core/properties";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { IssueTableQuerySpec } from "@multica/core/types";
import { GanttView, IssueDateSchedule } from "./gantt-view";

const ISSUE = {
  id: "issue-1",
  identifier: "MUL-1",
  number: 1,
  title: "Ship the thing",
  description: "",
  status: "todo",
  priority: "medium",
  workspace_id: "workspace-1",
  project_id: null,
  parent_issue_id: null,
  assignee_id: null,
  assignee_type: null,
  creator_id: "user-1",
  creator_type: "member",
  labels: [],
  position: 0,
  stage: null,
  start_date: "2026-03-02",
  due_date: "2026-03-06",
  created_at: "2026-03-01T00:00:00Z",
  updated_at: "2026-03-01T00:00:00Z",
} as unknown as Issue;

function renderGantt(
  locale: "en" | "zh-Hans",
  setup?: (store: StoreApi<IssueViewState>) => void,
) {
  const store = createStore<IssueViewState>()(viewStoreSlice);
  setup?.(store);
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  qc.setQueryData(
    issueComputedPropertiesOptions("workspace-1", ["issue-1"]).queryKey,
    {
      "issue-1": { "prop-formula": 7 },
    },
  );
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <ViewStoreProvider store={store}>
        <IssueContextMenuProvider>
          <GanttView issues={[ISSUE]} />
        </IssueContextMenuProvider>
      </ViewStoreProvider>
    </QueryClientProvider>,
    { locale },
  );
}

describe("GanttView date localization", () => {
  beforeAll(() => {
    // The browser speaks English while the UI does not. Dates must follow the
    // UI language, not this — see useLocale in packages/views/i18n.
    vi.stubGlobal("navigator", {
      ...globalThis.navigator,
      language: "en-US",
      languages: ["en-US"],
    });
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-03-04T12:00:00Z"));
  });

  afterAll(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("formats axis dates in the UI language, not the browser's", () => {
    const { container, unmount } = renderGantt("zh-Hans");

    expect(screen.getByText("2026年3月")).toBeTruthy();
    // Nothing may fall back to the browser language.
    expect(container.textContent ?? "").not.toMatch(/Mar\b/);

    unmount();
  });

  it("formats the row's start / due range in the UI language", () => {
    const { unmount } = renderGantt("zh-Hans");

    const range = screen.getByText(/2026年3月2日/);
    expect(range.textContent).toContain("2026年3月6日");

    unmount();
  });

  it("still formats in English when the UI is English", () => {
    const { container } = renderGantt("en");

    expect(screen.getByText("Mar 2026")).toBeTruthy();
    expect(container.textContent ?? "").not.toMatch(/年/);
  });

  it("shows selected computed fields beside the hover date range", async () => {
    vi.useRealTimers();
    try {
      renderGantt("en", (store) => {
        store.getState().toggleCardPropertyId("prop-formula");
      });

      expect(await screen.findByText("Points")).toBeInTheDocument();
      expect(screen.getAllByText("7").length).toBeGreaterThan(0);
    } finally {
      vi.useFakeTimers();
      vi.setSystemTime(new Date("2026-03-04T12:00:00Z"));
    }
  });
});

describe("IssueDateSchedule table pagination", () => {
  const serverQuery: IssueTableQuerySpec = {
    scope: { kind: "project", project_id: "proj-1" },
    filters: { include_sub_issues: false },
    sort: { field: "start_date", direction: "asc" },
  };
  const datedIssue: Issue = {
    ...ISSUE,
    id: "issue-dated",
    identifier: "MUL-1",
    title: "Dated task A",
    project_id: "proj-1",
    start_date: "2026-03-02",
    due_date: "2026-03-06",
  };
  const undatedIssue: Issue = {
    ...ISSUE,
    id: "issue-undated",
    identifier: "MUL-2",
    title: "Undated task B",
    project_id: "proj-1",
    start_date: null,
    due_date: null,
  };
  const page2Issue: Issue = {
    ...ISSUE,
    id: "issue-page2",
    identifier: "MUL-3",
    title: "Page two task",
    project_id: "proj-1",
    start_date: "2026-03-10",
    due_date: "2026-03-12",
  };

  beforeEach(() => {
    listIssueTableRowsMock.mockReset();
  });

  function renderSchedule(setup?: (store: StoreApi<IssueViewState>) => void) {
    const store = createStore<IssueViewState>()(viewStoreSlice);
    setup?.(store);
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <ViewStoreProvider store={store}>
          <IssueContextMenuProvider>
            <IssueDateSchedule serverQuery={serverQuery} />
          </IssueContextMenuProvider>
        </ViewStoreProvider>
      </QueryClientProvider>,
      { locale: "en" },
    );
  }

  it("renders dated and undated issues from table pages and recovers from head and tail errors", async () => {
    listIssueTableRowsMock
      .mockRejectedValueOnce(new Error("head failed"))
      .mockResolvedValueOnce({
        query_fingerprint: "fp-1",
        total: 3,
        group_key: null,
        parent_id: null,
        rows: [
          { issue: datedIssue, has_children: false, child_count: 0 },
          { issue: undatedIssue, has_children: false, child_count: 0 },
        ],
        next_cursor: "cursor-2",
      })
      .mockRejectedValueOnce(new Error("tail failed"))
      .mockResolvedValueOnce({
        query_fingerprint: "fp-1",
        total: 3,
        group_key: null,
        parent_id: null,
        rows: [{ issue: page2Issue, has_children: false, child_count: 0 }],
        next_cursor: null,
      });

    renderSchedule();

    // Head error recovery
    const retryHead = await screen.findByRole("button", { name: /retry/i });
    fireEvent.click(retryHead);

    expect((await screen.findAllByText("Dated task A")).length).toBeGreaterThan(0);
    expect(screen.getByText("Undated task B")).toBeInTheDocument();

    // Load page 2 -> tail error -> retry tail -> page 2 appears
    const loadMore = await screen.findByRole("button", { name: /load more/i });
    fireEvent.click(loadMore);

    const retryTail = await screen.findByRole("button", { name: /retry/i });
    fireEvent.click(retryTail);

    expect((await screen.findAllByText("Page two task")).length).toBeGreaterThan(0);
    await waitFor(() =>
      expect(document.querySelectorAll("[data-gantt-issue-id]")).toHaveLength(3),
    );
  });

  it("preserves server order when sorting by a custom property", async () => {
    const first = { ...datedIssue, id: "issue-first", title: "Zebra first", start_date: "2026-03-20" };
    const second = { ...undatedIssue, id: "issue-second", title: "Alpha second", start_date: "2026-03-01" };
    listIssueTableRowsMock.mockResolvedValue({
      query_fingerprint: "fp-prop",
      total: 2,
      group_key: null,
      parent_id: null,
      rows: [
        { issue: first, has_children: false, child_count: 0 },
        { issue: second, has_children: false, child_count: 0 },
      ],
      next_cursor: null,
    });

    renderSchedule((store) => {
      store.getState().setSortBy("property:prop-formula");
    });

    await screen.findAllByText("Zebra first");
    const renderedIds = Array.from(
      document.querySelectorAll<HTMLElement>("[data-gantt-issue-id]"),
    ).map((el) => el.dataset.ganttIssueId);
    expect(renderedIds).toEqual(["issue-first", "issue-second"]);
  });
});
