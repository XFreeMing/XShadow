/**
 * @vitest-environment jsdom
 */
import { useState, type ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { IssuePeekActionsContext, IssuePeekIdContext } from "../surface/peek-context";
import {
  buildDependencyChartLayout,
  IssueDependencyChart,
  type IssueDependencyEdge,
} from "./issue-dependency-chart";

vi.mock(import("@tanstack/react-query"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    useQuery: vi.fn(() => ({
      data: [{ id: "proj-1", identifier: "PRJ", title: "Core App" }],
    })),
  };
});

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/issue-statuses/hooks", () => ({
  useIssueStatuses: () => ({
    colorOf: () => "#10b981",
    iconOf: () => null,
  }),
}));

vi.mock("../../projects/components/project-icon", () => ({
  ProjectIcon: () => <span data-testid="project-icon" />,
}));

vi.mock("./status-icon", () => ({
  StatusIcon: () => <span data-testid="status-icon" />,
}));

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

function TestPeekHost({
  children,
  initialId = null,
}: {
  children: ReactNode;
  initialId?: string | null;
}) {
  const [issueId, setIssueId] = useState<string | null>(initialId);
  return (
    <IssuePeekActionsContext.Provider value={{
      open: setIssueId,
      close: () => setIssueId(null),
      toggle: (id) => setIssueId((current) => current === id ? null : id),
      publishColumns: () => {},
    }}>
      <IssuePeekIdContext.Provider value={issueId}>
        {children}
      </IssuePeekIdContext.Provider>
    </IssuePeekActionsContext.Provider>
  );
}

function makeIssue(
  id: string,
  identifier: string,
  title: string,
  status: string = "todo",
  projectId: string | null = "proj-1",
): Issue {
  return {
    id,
    workspace_id: "ws-1",
    project_id: projectId,
    number: 1,
    identifier,
    title,
    description: null,
    status,
    status_category: status === "done" ? "done" : "unstarted",
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
  it("renders compact cards with status icon, identifier, title, and project", () => {
    const a = makeIssue("a", "MUL-1", "API schema", "done", "proj-1");
    const b = makeIssue("b", "MUL-2", "Feature flag");
    const c = makeIssue("c", "MUL-3", "UI client");
    const solo = makeIssue("solo", "MUL-4", "Release notes", "todo", "proj-1");

    const { container } = render(
      <TestPeekHost>
        <IssueDependencyChart
          rows={[a, b, c, solo]}
          edges={[
            { fromId: "a", toId: "c", kind: "required", reason: "Needs endpoint types" },
            { fromId: "b", toId: "c", kind: "conditional", reason: "Only when flag enabled" },
          ]}
        />
      </TestPeekHost>,
    );

    // Card identifiers, titles, and projects rendered
    expect(screen.getByText("MUL-3")).toBeTruthy();
    expect(screen.getByText("UI client")).toBeTruthy();
    expect(screen.getAllByText("Core App").length).toBeGreaterThan(0);

    // Done card has green border tone
    const cardA = container.querySelector('[data-testid="dependency-chart-card"][data-issue-id="a"]')!;
    expect(cardA.className).toContain("border-success/40");

    // Required vs conditional SVG edges with direction prerequisite -> dependent and title tooltips
    const requiredEdge = container.querySelector('[data-from-id="a"][data-to-id="c"] path');
    const conditionalEdge = container.querySelector('[data-from-id="b"][data-to-id="c"] path');
    expect(requiredEdge?.getAttribute("stroke-dasharray")).toBeNull();
    expect(conditionalEdge?.getAttribute("stroke-dasharray")).toBe("6 4");
    expect(requiredEdge?.querySelector("title")?.textContent).toContain("Needs endpoint types");
    expect(conditionalEdge?.querySelector("title")?.textContent).toContain("Only when flag enabled");

    // Unrelated card rendered with the same compact card style
    const unrelatedCard = screen.getByTestId("dependency-chart-unrelated-card");
    expect(unrelatedCard.getAttribute("data-issue-id")).toBe("solo");
    expect(unrelatedCard.textContent).toContain("MUL-4");
    expect(unrelatedCard.textContent).toContain("Release notes");
    expect(unrelatedCard.textContent).toContain("Core App");

    // Edge inspector is removed
    expect(screen.queryByTestId("dependency-edge-inspector")).toBeNull();

    // Clicking card opens peek and sets aria-pressed & selection ring
    const cardC = container.querySelector('[data-testid="dependency-chart-card"][data-issue-id="c"]')!;
    fireEvent.click(cardC);
    expect(cardC.getAttribute("aria-pressed")).toBe("true");
    expect(cardC.className).toContain("ring-brand");

    // Upstream node 'a' is highlighted
    expect(cardA.className).not.toContain("opacity-35");
  });

  it("supports hover focusing when peek drawer is not open", () => {
    const a = makeIssue("a", "MUL-1", "Task A");
    const b = makeIssue("b", "MUL-2", "Task B");
    const c = makeIssue("c", "MUL-3", "Task C");

    const { container } = render(
      <TestPeekHost>
        <IssueDependencyChart
          rows={[a, b, c]}
          edges={[{ fromId: "a", toId: "b", kind: "required" }]}
        />
      </TestPeekHost>,
    );

    const cardA = container.querySelector('[data-testid="dependency-chart-card"][data-issue-id="a"]')!;
    const cardC = screen.getByTestId("dependency-chart-unrelated-card");

    // Hover over node A
    fireEvent.mouseEnter(cardA);
    // Node C should be dimmed because it's not connected
    expect(cardC.className).toContain("opacity-35");

    // Mouse leave restores
    fireEvent.mouseLeave(cardA);
    expect(cardC.className).not.toContain("opacity-35");
  });

  it("prioritizes peek selection over hover when peek drawer is open", () => {
    const a = makeIssue("a", "MUL-1", "Task A");
    const b = makeIssue("b", "MUL-2", "Task B");

    const { container } = render(
      <TestPeekHost initialId="a">
        <IssueDependencyChart
          rows={[a, b]}
          edges={[{ fromId: "a", toId: "b", kind: "required" }]}
        />
      </TestPeekHost>,
    );

    const cardA = container.querySelector('[data-testid="dependency-chart-card"][data-issue-id="a"]')!;
    const cardB = container.querySelector('[data-testid="dependency-chart-card"][data-issue-id="b"]')!;

    expect(cardA.getAttribute("aria-pressed")).toBe("true");

    // Hover over B should not overwrite active selection of A
    fireEvent.mouseEnter(cardB);
    expect(cardA.getAttribute("aria-pressed")).toBe("true");
    expect(cardA.className).toContain("ring-brand");
  });

  it("renders empty state when there are no visible rows", () => {
    render(<IssueDependencyChart rows={[]} edges={[]} />);
    expect(screen.getByText("No issues match this view.")).toBeTruthy();
  });
});
