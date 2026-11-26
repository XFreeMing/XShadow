/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import {
  buildDependencyChartLayout,
  IssueDependencyChart,
  type IssueDependencyEdge,
} from "./issue-dependency-chart";

vi.mock("../../i18n", () => ({
  useT: (ns: string) => ({
    t: (selector: (root: Record<string, Record<string, string>>) => string) => {
      const table: Record<string, Record<string, Record<string, string>>> = {
        issues: {
          org_chart: { empty: "No issues match this view." },
        },
        goals: {
          plan: {
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

vi.mock("../../navigation", () => ({
  AppLink: ({
    children,
    href,
    ...props
  }: React.ComponentProps<"a">) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/ws/issues/${id}`,
  }),
}));

function makeIssue(id: string, identifier: string, title: string): Issue {
  return {
    id,
    workspace_id: "ws-1",
    project_id: "proj-1",
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

afterEach(() => {
  cleanup();
});

describe("buildDependencyChartLayout", () => {
  it("preserves multiple prerequisites and dependents without collapsing to a single-parent tree", () => {
    const a = makeIssue("a", "MUL-1", "API schema");
    const b = makeIssue("b", "MUL-2", "Auth guard");
    const c = makeIssue("c", "MUL-3", "UI client");
    const d = makeIssue("d", "MUL-4", "E2E test");
    const solo = makeIssue("solo", "MUL-5", "Standalone doc");

    const edges: IssueDependencyEdge[] = [
      { fromId: "a", toId: "c", kind: "required", reason: "Needs endpoint types" },
      { fromId: "b", toId: "c", kind: "conditional", reason: "Auth header if gated" },
      { fromId: "c", toId: "d", kind: "required", reason: "Tests client flow" },
      { fromId: "a", toId: "d", kind: "required", reason: "Fixtures match schema" },
      // Invisible endpoint and self-loop must be dropped.
      { fromId: "a", toId: "hidden", kind: "required", reason: "Hidden" },
      { fromId: "c", toId: "c", kind: "required", reason: "Self" },
    ];

    const layout = buildDependencyChartLayout([a, b, c, d, solo], edges);
    expect(layout.hasCycle).toBe(false);
    expect(layout.edges).toHaveLength(4);
    expect(layout.nodeById.get("a")?.layer).toBe(0);
    expect(layout.nodeById.get("b")?.layer).toBe(0);
    expect(layout.nodeById.get("c")?.layer).toBe(1);
    expect(layout.nodeById.get("d")?.layer).toBe(2);
    expect(layout.unrelatedRows.map((row) => row.id)).toEqual(["solo"]);
  });

  it("handles cycles deterministically without throwing", () => {
    const a = makeIssue("a", "MUL-1", "Task A");
    const b = makeIssue("b", "MUL-2", "Task B");
    const layout = buildDependencyChartLayout(
      [a, b],
      [
        { fromId: "a", toId: "b", kind: "required", reason: "A before B" },
        { fromId: "b", toId: "a", kind: "conditional", reason: "B feeds back to A" },
      ],
    );

    expect(layout.hasCycle).toBe(true);
    expect(layout.nodes).toHaveLength(2);
    expect(layout.edges).toHaveLength(2);
    expect(layout.edges.some((edge) => edge.isBackEdge)).toBe(true);
  });
});

describe("IssueDependencyChart", () => {
  it("renders links, distinguishes required vs conditional arrows, and reveals reasons on inspect", () => {
    const a = makeIssue("a", "MUL-1", "API schema");
    const b = makeIssue("b", "MUL-2", "Feature flag");
    const c = makeIssue("c", "MUL-3", "UI client");
    const solo = makeIssue("solo", "MUL-4", "Release notes");

    const { container } = render(
      <IssueDependencyChart
        rows={[a, b, c, solo]}
        edges={[
          { fromId: "a", toId: "c", kind: "required", reason: "Needs endpoint types" },
          { fromId: "b", toId: "c", kind: "conditional", reason: "Only when flag enabled" },
        ]}
      />,
    );

    // Original task links
    const links = screen.getAllByRole("link", { name: "MUL-3" });
    expect(links[0]?.getAttribute("href")).toBe("/ws/issues/c");

    // Required vs conditional SVG edges with direction prerequisite -> dependent
    const requiredEdge = container.querySelector('[data-from-id="a"][data-to-id="c"] path');
    const conditionalEdge = container.querySelector('[data-from-id="b"][data-to-id="c"] path');
    expect(requiredEdge?.getAttribute("stroke-dasharray")).toBeNull();
    expect(conditionalEdge?.getAttribute("stroke-dasharray")).toBe("6 4");
    expect(requiredEdge?.querySelector("title")?.textContent).toContain("Needs endpoint types");
    expect(conditionalEdge?.querySelector("title")?.textContent).toContain("Only when flag enabled");

    // Unrelated row rendered separately without fake edges
    expect(screen.getByTestId("dependency-chart-unrelated-card").getAttribute("data-issue-id")).toBe("solo");

    // Selecting card reveals upstream/downstream reasons in read-only inspector
    const cardC = container.querySelector('[data-testid="dependency-chart-card"][data-issue-id="c"]')!;
    fireEvent.click(cardC);
    const inspector = screen.getByTestId("dependency-edge-inspector");
    expect(inspector.textContent).toContain("Needs endpoint types");
    expect(inspector.textContent).toContain("Only when flag enabled");
  });

  it("renders empty state when there are no visible rows", () => {
    render(<IssueDependencyChart rows={[]} edges={[]} />);
    expect(screen.getByText("No issues match this view.")).toBeTruthy();
  });
});
