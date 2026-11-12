/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { goalKeys } from "@multica/core/goals";
import type {
  Goal,
  GoalPlan,
  GoalPlanGeneration,
  GoalPlanItem,
} from "@multica/core/types";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";
import { GoalPlanSection } from "./goal-plan-section";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({
    queryKey: ["agents"],
    queryFn: async () => [{ id: "agent-1", name: "Planner", archived_at: null }],
  }),
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => [] }),
  squadListOptions: () => ({ queryKey: ["squads"], queryFn: async () => [] }),
}));
vi.mock("../../modals/issue-picker-modal", () => ({
  IssuePickerModal: () => null,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const goal: Goal = {
  id: "goal-1",
  workspace_id: "ws-1",
  project_id: "project-1",
  title: "Ship the workbench",
  intent: "Let a team deliver a goal",
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

function plan(overrides: Partial<GoalPlan> = {}): GoalPlan {
  return {
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
    ...overrides,
  };
}

function generation(overrides: Partial<GoalPlanGeneration> = {}): GoalPlanGeneration {
  return {
    id: "gen-1",
    goal_id: "goal-1",
    project_id: "project-1",
    request_key: "request-1",
    intent: "Plan it",
    agent_id: "agent-1",
    base_revision: 2,
    state: "queued",
    chat_session_id: null,
    task_id: null,
    input_context: {},
    candidate: null,
    applied: false,
    applied_revision: null,
    observed_revision: null,
    current_revision: 2,
    stale: false,
    error: "",
    created_at: "",
    updated_at: "",
    ...overrides,
  };
}

describe("GoalPlanSection", () => {
  let qc: QueryClient;
  const api = {
    getGoalPlan: vi.fn(),
    saveGoalPlan: vi.fn(),
    createGoalPlanGeneration: vi.fn(),
    getGoalPlanGeneration: vi.fn(),
  };

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    api.getGoalPlan.mockReset().mockResolvedValue(plan());
    api.saveGoalPlan.mockReset().mockImplementation(async (_goalId, data) =>
      plan({
        revision: 3,
        items: data.items,
        edges: data.edges,
        context_snapshot: data.context_snapshot,
      }),
    );
    api.createGoalPlanGeneration.mockReset().mockResolvedValue(generation());
    api.getGoalPlanGeneration.mockReset().mockResolvedValue(generation());
    setApiInstance(api as unknown as ApiClient);
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  afterEach(() => qc.clear());

  function renderSection() {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <GoalPlanSection goal={goal} />
      </QueryClientProvider>,
    );
  }

  it("starts from an empty draft and saves an added task with its revision", async () => {
    const user = userEvent.setup();
    renderSection();

    expect(await screen.findByTestId("goal-plan-section")).toBeInTheDocument();
    expect(
      screen.getByText("No tasks yet. Add one, or generate a plan."),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Add task" }));
    await user.type(screen.getByLabelText("Title"), "Deliver the first increment");
    await user.click(screen.getByRole("button", { name: "Save plan" }));

    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(1));
    const [, saved] = api.saveGoalPlan.mock.calls[0]!;
    expect(saved).toMatchObject({
      expected_revision: 2,
      context_snapshot: {},
      generation_id: null,
    });
    expect(saved.items).toHaveLength(1);
    expect(saved.items[0].title).toBe("Deliver the first increment");
    expect(await screen.findByText("Revision 3")).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Plan saved");
  });

  it("keeps local input on a revision conflict and only overwrites on request", async () => {
    api.saveGoalPlan.mockReset();
    api.saveGoalPlan
      .mockRejectedValueOnce(
        new ApiError("conflict", 409, "Conflict", {
          code: "plan_revision_conflict",
          current_revision: 7,
        }),
      )
      .mockImplementationOnce(async (_goalId, data) =>
        plan({
          revision: 8,
          items: data.items,
          edges: data.edges,
          context_snapshot: data.context_snapshot,
        }),
      );

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    await user.click(screen.getByRole("button", { name: "Add task" }));
    const title = screen.getByLabelText("Title");
    await user.type(title, "My local plan");
    await user.click(screen.getByRole("button", { name: "Save plan" }));

    expect(await screen.findByTestId("plan-revision-conflict")).toBeInTheDocument();
    expect(screen.getByLabelText("Title")).toHaveValue("My local plan");
    expect(screen.getByRole("button", { name: "Save plan" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Save my version" }));
    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(2));
    expect(api.saveGoalPlan.mock.calls[1]![1]).toMatchObject({
      expected_revision: 7,
    });
    expect(api.saveGoalPlan.mock.calls[1]![1].items[0].title).toBe("My local plan");
  });

  it("merges a late candidate without discarding edits or existing bindings", async () => {
    api.getGoalPlan.mockResolvedValue(
      plan({
        items: [
          {
            ...item("node-1", "Local title"),
            binding: { type: "existing", issue_id: "issue-1" },
          },
        ],
      }),
    );
    api.createGoalPlanGeneration.mockResolvedValue(
      generation({ request_key: "late-request" }),
    );
    api.getGoalPlanGeneration.mockResolvedValue(
      generation({
        state: "succeeded",
        base_revision: 2,
        current_revision: 4,
        stale: true,
        candidate: {
          items: [item("node-1", "Model title"), item("node-2", "Model addition")],
          edges: [
            {
              dependent_item_id: "node-2",
              prerequisite_item_id: "node-1",
              reason: "needs the first node",
            },
          ],
          context_snapshot: { repo_sha: "abc123" },
        },
      }),
    );

    const user = userEvent.setup();
    renderSection();

    const title = await screen.findByLabelText("Title");
    await user.clear(title);
    await user.type(title, "User title");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);

    const candidate = await screen.findByTestId("plan-candidate");
    expect(screen.getByText("Outdated")).toBeInTheDocument();
    await user.click(within(candidate).getByRole("button", { name: "Review and apply" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Apply and save" }));

    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(1));
    const [, saved] = api.saveGoalPlan.mock.calls[0]!;
    expect(saved.generation_id).toBe("gen-1");
    expect(saved.expected_revision).toBe(2);
    const merged = saved.items.find((entry: GoalPlanItem) => entry.id === "node-1");
    expect(merged.title).toBe("User title");
    expect(merged.binding).toEqual({ type: "existing", issue_id: "issue-1" });
    expect(saved.items.some((entry: GoalPlanItem) => entry.id === "node-2")).toBe(true);
    expect(saved.edges).toHaveLength(1);
  });

  it("removes a dependency and prunes edges when its task is deleted", async () => {
    api.getGoalPlan.mockResolvedValue(
      plan({
        items: [item("node-1", "First"), item("node-2", "Second")],
        edges: [
          {
            dependent_item_id: "node-2",
            prerequisite_item_id: "node-1",
            reason: "needs the first",
          },
        ],
      }),
    );
    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("plan-item-node-2");
    await user.click(
      screen.getByRole("button", { name: "Remove dependency on First" }),
    );
    await user.click(screen.getByRole("button", { name: "Save plan" }));
    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(1));
    expect(api.saveGoalPlan.mock.calls[0]![1].edges).toEqual([]);

    // A deleted node takes its edges with it.
    api.saveGoalPlan.mockClear();
    await user.click(screen.getByRole("button", { name: "Delete First" }));
    await user.click(screen.getByRole("button", { name: "Save plan" }));
    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(1));
    expect(api.saveGoalPlan.mock.calls[0]![1].edges).toEqual([]);
  });

  it("hides the whole plan area when the goal is no longer visible", async () => {
    api.getGoalPlan.mockRejectedValue(new ApiError("Forbidden", 403, "Forbidden"));
    renderSection();

    await waitFor(() => {
      expect(screen.queryByTestId("goal-plan-section")).not.toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Save plan" }),
      ).not.toBeInTheDocument();
    });
  });

  it("hides a candidate whose run belongs to another project", async () => {
    api.getGoalPlanGeneration.mockRejectedValue(
      new ApiError("conflict", 409, "Conflict", {
        code: "generation_project_conflict",
      }),
    );
    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);

    expect(await screen.findByTestId("plan-project-conflict")).toBeInTheDocument();
    expect(screen.queryByTestId("plan-candidate")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Start a new run" }),
    ).toBeInTheDocument();
    // The current plan is re-read rather than merged with the old candidate.
    expect(api.getGoalPlan.mock.calls.length).toBeGreaterThan(1);
  });

  it("refuses a start when the goal moved project and only a new run reuses a key", async () => {
    api.createGoalPlanGeneration
      .mockRejectedValueOnce(
        new ApiError("conflict", 409, "Conflict", {
          code: "goal_project_conflict",
        }),
      )
      .mockResolvedValueOnce(generation({ id: "gen-2" }));

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);

    // Nothing was written into the goal's new project.
    expect(await screen.findByTestId("plan-project-conflict")).toBeInTheDocument();
    expect(screen.queryByTestId("plan-revision-conflict")).not.toBeInTheDocument();
    expect(api.saveGoalPlan).not.toHaveBeenCalled();
    // The goal and its plan were re-read after the refusal.
    expect(api.getGoalPlan.mock.calls.length).toBeGreaterThan(1);

    const firstKey = api.createGoalPlanGeneration.mock.calls[0]![1].request_key;
    expect(typeof firstKey).toBe("string");

    await user.click(screen.getByRole("button", { name: "Start a new run" }));
    await waitFor(() => expect(api.createGoalPlanGeneration).toHaveBeenCalledTimes(2));
    const secondKey = api.createGoalPlanGeneration.mock.calls[1]![1].request_key;
    expect(secondKey).not.toBe(firstKey);
    expect(api.saveGoalPlan).not.toHaveBeenCalled();
  });

  it("does not replay a run from a project the goal has left", async () => {
    api.createGoalPlanGeneration.mockRejectedValue(
      new ApiError("conflict", 409, "Conflict", {
        code: "generation_project_conflict",
      }),
    );

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);

    expect(await screen.findByTestId("plan-project-conflict")).toBeInTheDocument();
    expect(screen.queryByTestId("plan-generation-status")).not.toBeInTheDocument();
    expect(api.saveGoalPlan).not.toHaveBeenCalled();
    // The stale request key was forgotten, so a later run cannot replay it.
    await user.click(
      within(screen.getByTestId("plan-project-conflict")).getByRole("button", {
        name: "Reload plan",
      }),
    );
    await waitFor(() => expect(api.createGoalPlanGeneration).toHaveBeenCalledTimes(1));
    expect(
      screen.getByRole("button", { name: "Generate plan" }),
    ).toBeEnabled();
  });

  it("keeps an ordinary start-time revision conflict out of the project notice", async () => {
    api.createGoalPlanGeneration.mockRejectedValue(
      new ApiError("conflict", 409, "Conflict", {
        code: "plan_revision_conflict",
        current_revision: 5,
      }),
    );

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);

    expect(
      await screen.findByText(
        "The plan changed before the run started. Reload the plan and start again.",
      ),
    ).toBeInTheDocument();
    expect(await screen.findByTestId("plan-revision-conflict")).toBeInTheDocument();
    expect(screen.queryByTestId("plan-project-conflict")).not.toBeInTheDocument();
    expect(screen.getByText(/Reload revision 5/)).toBeInTheDocument();
  });

  it("drops a displayed candidate and its basis when the goal stops being visible", async () => {
    api.getGoalPlanGeneration.mockResolvedValue(
      generation({
        state: "succeeded",
        input_context: { project_title: "Project A" },
        candidate: {
          items: [item("node-1", "Model task")],
          edges: [],
          context_snapshot: {
            repo_sha: "abc123",
            files: [{ path: "src/plan.ts" }],
          },
        },
      }),
    );

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);

    expect(await screen.findByTestId("plan-candidate")).toBeInTheDocument();
    expect(screen.getByText("Project A")).toBeInTheDocument();

    // Losing the project (404 for this viewer) must hide the cached server
    // data, not keep rendering what the client still has in memory.
    api.getGoalPlan.mockRejectedValue(new ApiError("Not found", 404, "Not found"));
    void qc.invalidateQueries({ queryKey: goalKeys.plan("ws-1", "goal-1") }).catch(() => {});

    await waitFor(() => {
      expect(screen.queryByTestId("goal-plan-section")).not.toBeInTheDocument();
      expect(screen.queryByTestId("plan-candidate")).not.toBeInTheDocument();
      expect(screen.queryByTestId("plan-actual-basis")).not.toBeInTheDocument();
    });
  });
});