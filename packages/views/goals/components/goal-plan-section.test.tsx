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

  function renderSection(goalOverride: Goal = goal) {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <GoalPlanSection goal={goalOverride} />
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

  it("hides a cached candidate when its run stops being readable", async () => {
    api.getGoalPlanGeneration.mockResolvedValue(
      generation({
        state: "succeeded",
        input_context: { project_title: "Project A" },
        candidate: {
          items: [item("node-1", "Model task")],
          edges: [],
          context_snapshot: { repo_sha: "abc123" },
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

    api.getGoalPlanGeneration.mockRejectedValue(
      new ApiError("Not found", 404, "Not found"),
    );
    void qc
      .invalidateQueries({ queryKey: goalKeys.planGeneration("ws-1", "goal-1", "gen-1") })
      .catch(() => {});

    expect(await screen.findByTestId("plan-run-unavailable")).toBeInTheDocument();
    expect(screen.queryByTestId("plan-candidate")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Review and apply" }),
    ).not.toBeInTheDocument();
  });

  it("isolates a draft from the project the goal has left", async () => {
    api.getGoalPlan.mockResolvedValue(plan({ project_id: "project-1", revision: 0 }));
    const user = userEvent.setup();
    const view = renderSection();

    await screen.findByTestId("goal-plan-section");
    await user.click(screen.getByRole("button", { name: "Add task" }));
    await user.type(screen.getByLabelText("Title"), "Only in project A");

    // The goal moves to project B while both plans are still revision 0.
    api.getGoalPlan.mockResolvedValue(plan({ project_id: "project-2", revision: 0 }));
    view.rerender(
      <QueryClientProvider client={qc}>
        <GoalPlanSection goal={{ ...goal, project_id: "project-2" }} />
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId("plan-section-project-conflict")).toBeInTheDocument();
    // The old draft and its basis are isolated, and can't be saved as B's plan.
    expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save plan" })).toBeDisabled();
    expect(api.saveGoalPlan).not.toHaveBeenCalled();

    await user.click(
      within(screen.getByTestId("plan-section-project-conflict")).getByRole("button", {
        name: "Reload plan",
      }),
    );

    await waitFor(() =>
      expect(screen.queryByTestId("plan-section-project-conflict")).not.toBeInTheDocument(),
    );
    expect(
      await screen.findByText("No tasks yet. Add one, or generate a plan."),
    ).toBeInTheDocument();
    expect(api.saveGoalPlan).not.toHaveBeenCalled();
  });

  it("hides the merged draft and its old basis on an apply project conflict", async () => {
    api.getGoalPlanGeneration.mockResolvedValue(
      generation({
        state: "succeeded",
        candidate: {
          items: [item("node-1", "Model task")],
          edges: [],
          context_snapshot: { repo_sha: "old-project-sha" },
        },
      }),
    );
    api.saveGoalPlan.mockRejectedValueOnce(
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

    const candidate = await screen.findByTestId("plan-candidate");
    await user.click(within(candidate).getByRole("button", { name: "Review and apply" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Apply and save" }));

    expect(await screen.findByTestId("plan-section-project-conflict")).toBeInTheDocument();
    expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();
    expect(screen.queryByTestId("plan-actual-basis")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save plan" })).toBeDisabled();
    expect(api.saveGoalPlan).toHaveBeenCalledTimes(1);
    expect(api.saveGoalPlan.mock.calls[0]![1].generation_id).toBe("gen-1");
  });

  it("keeps deleting a node or dependency out of a late candidate", async () => {
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
    api.getGoalPlanGeneration.mockResolvedValue(
      generation({
        state: "succeeded",
        candidate: {
          items: [item("node-1", "First"), item("node-2", "Second")],
          edges: [
            {
              dependent_item_id: "node-2",
              prerequisite_item_id: "node-1",
              reason: "candidate reason",
            },
          ],
          context_snapshot: {},
        },
      }),
    );

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("plan-item-node-2");
    // The user removes the dependency and the second node while the run is
    // still in flight; applying its candidate must not bring them back.
    await user.click(screen.getByRole("button", { name: "Remove dependency on First" }));
    await user.click(screen.getByRole("button", { name: "Delete Second" }));

    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);

    const candidate = await screen.findByTestId("plan-candidate");
    await user.click(within(candidate).getByRole("button", { name: "Review and apply" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Apply and save" }));

    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(1));
    const [, saved] = api.saveGoalPlan.mock.calls[0]!;
    expect(saved.items.map((entry: GoalPlanItem) => entry.id)).toEqual(["node-1"]);
    expect(saved.edges).toEqual([]);
  });

  it("requires the explicit revision choice before applying a candidate", async () => {
    api.getGoalPlanGeneration.mockResolvedValue(
      generation({
        state: "succeeded",
        base_revision: 2,
        candidate: {
          items: [item("node-1", "Model task")],
          edges: [],
          context_snapshot: {},
        },
      }),
    );
    api.saveGoalPlan
      .mockRejectedValueOnce(
        new ApiError("conflict", 409, "Conflict", {
          code: "plan_revision_conflict",
          current_revision: 3,
        }),
      )
      .mockImplementationOnce(async (_goalId, data) =>
        plan({
          revision: 4,
          items: data.items,
          edges: data.edges,
          context_snapshot: data.context_snapshot,
        }),
      )
      .mockImplementationOnce(async (_goalId, data) =>
        plan({
          revision: 5,
          items: data.items,
          edges: data.edges,
          context_snapshot: data.context_snapshot,
        }),
      );

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);
    await screen.findByTestId("plan-candidate");

    await user.click(screen.getByRole("button", { name: "Add task" }));
    await user.type(screen.getByLabelText("Title"), "Local edit");
    await user.click(screen.getByRole("button", { name: "Save plan" }));
    expect(await screen.findByTestId("plan-revision-conflict")).toBeInTheDocument();

    // Reading the newer revision is not permission to overwrite it: the
    // candidate's apply action stays disabled until the user chooses.
    expect(
      screen.getByRole("button", { name: "Review and apply" }),
    ).toBeDisabled();
    expect(api.saveGoalPlan).toHaveBeenCalledTimes(1);
    expect(api.saveGoalPlan.mock.calls[0]![1].expected_revision).toBe(2);

    await user.click(screen.getByRole("button", { name: "Save my version" }));
    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(2));
    expect(api.saveGoalPlan.mock.calls[1]![1].expected_revision).toBe(3);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Review and apply" })).toBeEnabled(),
    );
    const candidate = screen.getByTestId("plan-candidate");
    await user.click(within(candidate).getByRole("button", { name: "Review and apply" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Apply and save" }));
    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(3));
    expect(api.saveGoalPlan.mock.calls[2]![1].expected_revision).toBe(4);
    expect(api.saveGoalPlan.mock.calls[2]![1].generation_id).toBe("gen-1");
  });

  it("replays the original body for a stored request key", async () => {
    api.createGoalPlanGeneration
      .mockRejectedValueOnce(new ApiError("boom", 500, "Boom"))
      .mockResolvedValueOnce(generation());

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const intent = screen.getByLabelText("What should the plan deliver?");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.clear(intent);
    await user.type(intent, "First intent");
    await user.click(generate);
    await waitFor(() => expect(api.createGoalPlanGeneration).toHaveBeenCalledTimes(1));

    const first = api.createGoalPlanGeneration.mock.calls[0]![1];
    // The lost response may still have created the run, so a retry replays
    // the recorded body even though the form has changed since.
    await user.clear(intent);
    await user.type(intent, "Changed intent");
    await user.click(generate);
    await waitFor(() => expect(api.createGoalPlanGeneration).toHaveBeenCalledTimes(2));

    const second = api.createGoalPlanGeneration.mock.calls[1]![1];
    expect(second.request_key).toBe(first.request_key);
    expect(second.intent).toBe("First intent");
    expect(second.agent_id).toBe(first.agent_id);
    expect(second.expected_revision).toBe(first.expected_revision);
  });

  it("retries an unconfirmed run with the same key and body", async () => {
    api.createGoalPlanGeneration
      .mockRejectedValueOnce(
        new ApiError("unavailable", 503, "Unavailable", {
          code: "goal_plan_generation_retryable",
        }),
      )
      .mockResolvedValueOnce(generation());

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const intent = screen.getByLabelText("What should the plan deliver?");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.clear(intent);
    await user.type(intent, "First intent");
    await user.click(generate);

    expect(
      await screen.findByText(
        "The planning run couldn't be confirmed yet. Retry this request; your text is kept.",
      ),
    ).toBeInTheDocument();

    const first = api.createGoalPlanGeneration.mock.calls[0]![1];
    // The commit outcome is unknown, so the retry has to resolve the same
    // request even though the form no longer says the same thing.
    await user.clear(intent);
    await user.type(intent, "Changed intent");
    await user.click(generate);
    await waitFor(() => expect(api.createGoalPlanGeneration).toHaveBeenCalledTimes(2));

    const second = api.createGoalPlanGeneration.mock.calls[1]![1];
    expect(second.request_key).toBe(first.request_key);
    expect(second.intent).toBe("First intent");
    expect(second.agent_id).toBe(first.agent_id);
    expect(second.expected_revision).toBe(first.expected_revision);
  });

  it("uses a new key and body only for an explicit new run", async () => {
    api.createGoalPlanGeneration.mockResolvedValue(generation({ state: "failed" }));
    api.getGoalPlanGeneration.mockResolvedValue(generation({ state: "failed" }));

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const intent = screen.getByLabelText("What should the plan deliver?");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);

    const status = await screen.findByTestId("plan-generation-status");
    const first = api.createGoalPlanGeneration.mock.calls[0]![1];
    await user.clear(intent);
    await user.type(intent, "Second intent");
    await user.click(within(status).getByRole("button", { name: "Generate again" }));
    await waitFor(() => expect(api.createGoalPlanGeneration).toHaveBeenCalledTimes(2));

    const second = api.createGoalPlanGeneration.mock.calls[1]![1];
    expect(second.request_key).not.toBe(first.request_key);
    expect(second.intent).toBe("Second intent");
  });

  it("shows an apply refusal in the reader's language", async () => {
    api.getGoalPlanGeneration.mockResolvedValue(
      generation({
        state: "succeeded",
        candidate: {
          items: [item("node-1", "Model task")],
          edges: [],
          context_snapshot: {},
        },
      }),
    );
    api.saveGoalPlan.mockRejectedValueOnce(
      new ApiError("conflict", 409, "Conflict", {
        code: "generation_already_applied",
      }),
    );

    const user = userEvent.setup();
    renderSection();

    await screen.findByTestId("goal-plan-section");
    const generate = screen.getByRole("button", { name: "Generate plan" });
    await waitFor(() => expect(generate).toBeEnabled());
    await user.click(generate);
    const candidate = await screen.findByTestId("plan-candidate");
    await user.click(within(candidate).getByRole("button", { name: "Review and apply" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Apply and save" }));

    expect(
      await screen.findByText(
        "This plan was already applied, so this attempt saved nothing. Reload the plan to see the current version.",
      ),
    ).toBeInTheDocument();
  });

  it("shows an unknown binding as unknown and preserves it on save", async () => {
    api.getGoalPlan.mockResolvedValue(
      plan({
        items: [{ ...item("node-1", "External task"), binding: { type: "external", issue_id: null } }],
      }),
    );

    const user = userEvent.setup();
    renderSection();

    expect(await screen.findByText("Other task link (external)")).toBeInTheDocument();
    expect(screen.queryByText("New task")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Save plan" }));
    await waitFor(() => expect(api.saveGoalPlan).toHaveBeenCalledTimes(1));
    expect(api.saveGoalPlan.mock.calls[0]![1].items[0].binding).toEqual({
      type: "external",
      issue_id: null,
    });
  });
});