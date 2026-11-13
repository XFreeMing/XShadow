/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import type { Goal, GoalPlanItem } from "@multica/core/types";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider } from "../../navigation";
import { GoalPlanExecution } from "./goal-plan-execution";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({
    queryKey: ["agents"],
    queryFn: async () => [{ id: "agent-1", name: "Planner", archived_at: null }],
  }),
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => [] }),
  squadListOptions: () => ({ queryKey: ["squads"], queryFn: async () => [] }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: (type: string, id: string) =>
      type === "agent" && id === "agent-1" ? "Planner" : "Unknown",
    getActorInitials: () => "P",
    getActorAvatarUrl: () => null,
    hasActor: () => true,
  }),
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const goal: Goal = {
  id: "goal-1",
  workspace_id: "ws-1",
  project_id: "project-1",
  title: "Ship the workbench",
  intent: "",
  scope: "",
  acceptance_criteria: "",
  owner_type: null,
  owner_id: null,
  status: "planned",
  creator_type: "member",
  creator_id: "user-1",
  created_at: "",
  updated_at: "",
};

function item(id: string, title: string): GoalPlanItem {
  return {
    id,
    title,
    outcome: "",
    acceptance_criteria: "",
    evidence_refs: [],
    binding: { type: "new", issue_id: null },
    proposed_owner: null,
    stage: null,
    coordination: null,
  };
}

function materialized(
  itemId: string,
  overrides: Partial<{
    issue_id: string;
    created: boolean;
    assignee_type: string;
    assignee_id: string | null;
    dependency: { ready: boolean; reason: string; detail: string };
    task_enqueued: boolean;
  }> = {},
) {
  return {
    item_id: itemId,
    issue_id: overrides.issue_id ?? `issue-${itemId}`,
    created: overrides.created ?? true,
    binding_type: "new",
    assignee_type: overrides.assignee_type ?? "",
    assignee_id: overrides.assignee_id ?? null,
    dependency: overrides.dependency ?? { ready: true, reason: "", detail: "" },
    task_enqueued: overrides.task_enqueued ?? false,
    task_id: null,
  };
}

describe("GoalPlanExecution", () => {
  let qc: QueryClient;
  const api = {
    getGoalPlan: vi.fn(),
    materializeGoalPlan: vi.fn(),
  };

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    api.getGoalPlan.mockReset().mockResolvedValue({
      id: "plan-1",
      goal_id: "goal-1",
      project_id: "project-1",
      revision: 2,
      state: "draft",
      items: [],
      edges: [],
      context_snapshot: {},
      created_at: "",
      updated_at: "",
    });
    api.materializeGoalPlan.mockReset();
    setApiInstance(api as unknown as ApiClient);
    vi.mocked(toast.success).mockClear();
  });

  afterEach(() => qc.clear());

  function renderExecution(
    options: {
      items?: GoalPlanItem[];
      saved?: boolean;
      dirty?: boolean;
      blocked?: boolean;
    } = {},
  ) {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <WorkspaceSlugProvider slug="test">
          <NavigationProvider
            value={{
              push: vi.fn(),
              replace: vi.fn(),
              back: vi.fn(),
              pathname: "/test/goals/goal-1",
              searchParams: new URLSearchParams(),
              hash: "",
              getShareableUrl: (path) => path,
            }}
          >
            <GoalPlanExecution
              goal={goal}
              items={options.items ?? [item("a", "Deliver A"), item("b", "Deliver B")]}
              planRevision={2}
              saved={options.saved ?? true}
              dirty={options.dirty ?? false}
              blocked={options.blocked ?? false}
            />
          </NavigationProvider>
        </WorkspaceSlugProvider>
      </QueryClientProvider>,
    );
  }

  it("reuses the same task when the same plan is executed twice", async () => {
    api.materializeGoalPlan
      .mockResolvedValueOnce({
        goal_id: "goal-1",
        plan_revision: 2,
        items: [materialized("a"), materialized("b")],
      })
      .mockResolvedValueOnce({
        goal_id: "goal-1",
        plan_revision: 2,
        items: [materialized("a", { created: false }), materialized("b", { created: false })],
      });

    const user = userEvent.setup();
    renderExecution();

    await user.click(screen.getByRole("button", { name: "Create tasks" }));
    expect(await screen.findAllByText("Task created")).toHaveLength(2);
    expect(screen.queryAllByText("Task already existed")).toHaveLength(0);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Create tasks" })).toBeEnabled(),
    );
    await user.click(screen.getByRole("button", { name: "Create tasks" }));
    await waitFor(() => expect(screen.getAllByText("Task already existed")).toHaveLength(2));
    expect(screen.queryByText("Task created")).not.toBeInTheDocument();

    // One row per planned item: the panel never renders a second task row for a
    // repeated execution.
    expect(screen.getAllByTestId("execution-item-a")).toHaveLength(1);
    expect(screen.getAllByTestId("execution-item-b")).toHaveLength(1);
    expect(api.materializeGoalPlan).toHaveBeenCalledTimes(2);
    expect(api.materializeGoalPlan.mock.calls[1]![1]).toEqual({
      items: [{ item_id: "a" }, { item_id: "b" }],
    });
    expect(toast.success).toHaveBeenLastCalledWith("Tasks created: 0; already existed: 2");
  });

  it("shows the server's dependency verdict instead of assuming a parked task starts", async () => {
    api.materializeGoalPlan.mockResolvedValue({
      goal_id: "goal-1",
      plan_revision: 2,
      items: [
        materialized("a", {
          created: true,
          assignee_type: "agent",
          assignee_id: "agent-1",
          dependency: {
            ready: false,
            reason: "dependency_not_accepted",
            detail: "prerequisite issue is done but has no accepted review",
          },
        }),
      ],
    });

    const user = userEvent.setup();
    renderExecution({ items: [item("a", "Deliver A")] });
    await user.click(screen.getByRole("button", { name: "Create tasks" }));

    const row = await screen.findByTestId("execution-result-a");
    expect(
      within(row).getByText(
        "The prerequisite is done but has no currently valid acceptance.",
      ),
    ).toBeInTheDocument();
    expect(
      within(row).getByText(
        "Finishing and accepting the prerequisite releases this through a real dependency event; refreshing only rereads the state.",
      ),
    ).toBeInTheDocument();
    expect(
      within(row).getByText("No run yet: it starts once its prerequisites are accepted."),
    ).toBeInTheDocument();
    expect(within(row).queryByText("A run was started for this task.")).not.toBeInTheDocument();
    // Created is still reported: the task exists, only its release is waiting.
    expect(within(row).getByText("Task created")).toBeInTheDocument();
  });

  it("reports a task with no assignee as not enqueued", async () => {
    api.materializeGoalPlan.mockResolvedValue({
      goal_id: "goal-1",
      plan_revision: 2,
      items: [materialized("a", { task_enqueued: false, assignee_id: null })],
    });

    const user = userEvent.setup();
    renderExecution({ items: [item("a", "Deliver A")] });
    await user.click(screen.getByRole("button", { name: "Create tasks" }));

    const row = await screen.findByTestId("execution-result-a");
    expect(within(row).getByText("No run: the task has no assignee.")).toBeInTheDocument();
    expect(within(row).getByText("Unassigned")).toBeInTheDocument();
  });

  it("never shows a binding as created when the reply is incomplete", async () => {
    api.materializeGoalPlan.mockResolvedValue({
      goal_id: "goal-1",
      plan_revision: 2,
      items: [materialized("a")],
    });

    const user = userEvent.setup();
    renderExecution({ items: [item("a", "Deliver A"), item("b", "Deliver B")] });
    await user.click(screen.getByRole("button", { name: "Create tasks" }));

    expect(await screen.findByTestId("execution-error")).toHaveTextContent(
      "The server didn't confirm these bindings. Reload the plan before trying again.",
    );
    expect(screen.queryByText("Task created")).not.toBeInTheDocument();
    expect(screen.getByText("Nothing was shown as created because the reply was incomplete."))
      .toBeInTheDocument();
  });

  it("rejects a reply about another goal or an older plan", async () => {
    api.materializeGoalPlan.mockResolvedValue({
      goal_id: "goal-1",
      plan_revision: 1,
      items: [materialized("a")],
    });

    const user = userEvent.setup();
    renderExecution({ items: [item("a", "Deliver A")] });
    await user.click(screen.getByRole("button", { name: "Create tasks" }));

    expect(await screen.findByTestId("execution-error")).toHaveTextContent(
      "The server didn't confirm these bindings. Reload the plan before trying again.",
    );
    expect(screen.queryByText("Task created")).not.toBeInTheDocument();
  });

  it("reports a failed materialize without claiming tasks exist", async () => {
    api.materializeGoalPlan.mockRejectedValue(new ApiError("plan required", 409, "Conflict", {
      code: "plan_required",
    }));

    const user = userEvent.setup();
    renderExecution({ items: [item("a", "Deliver A")] });
    await user.click(screen.getByRole("button", { name: "Create tasks" }));

    expect(await screen.findByTestId("execution-error")).toHaveTextContent(
      "Couldn't create the tasks. Nothing is shown as created; try again.",
    );
    expect(screen.queryByText("Task created")).not.toBeInTheDocument();
  });

  it("keeps an unrecognized dependency reason visible", async () => {
    api.materializeGoalPlan.mockResolvedValue({
      goal_id: "goal-1",
      plan_revision: 2,
      items: [
        materialized("a", {
          dependency: { ready: false, reason: "dependency_gated_by_new_server", detail: "" },
        }),
      ],
    });

    const user = userEvent.setup();
    renderExecution({ items: [item("a", "Deliver A")] });
    await user.click(screen.getByRole("button", { name: "Create tasks" }));

    const row = await screen.findByTestId("execution-result-a");
    expect(
      within(row).getByText("Unknown dependency state. (dependency_gated_by_new_server)"),
    ).toBeInTheDocument();
  });

  it("blocks execution until the saved plan is current", async () => {
    renderExecution({ items: [item("a", "Deliver A")], dirty: true });
    expect(screen.getByTestId("execution-save-first")).toHaveTextContent(
      "Save the plan before creating tasks. Execution always uses the saved plan.",
    );
    expect(screen.getByRole("button", { name: "Create tasks" })).toBeDisabled();
  });

  it("lists existing bindings without offering to recreate them", async () => {
    const bound: GoalPlanItem = {
      ...item("a", "Deliver A"),
      binding: { type: "existing", issue_id: "issue-existing" },
    };
    renderExecution({ items: [bound] });

    const row = screen.getByTestId("execution-existing-a");
    expect(within(row).getByText("Existing task")).toBeInTheDocument();
    expect(within(row).getByRole("link", { name: "Open task" })).toHaveAttribute(
      "href",
      "/test/issues/issue-existing",
    );
    expect(screen.getByRole("button", { name: "Create tasks" })).toBeDisabled();
  });
});