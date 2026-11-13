import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { goalKeys } from "@multica/core/goals";
import type { ClosureState, Goal, Issue } from "@multica/core/types";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider } from "../../navigation";
import { GoalDetailPage } from "./goal-detail-page";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    goals: () => "/test/goals",
    goalDetail: (id: string) => `/test/goals/${id}`,
    issueDetail: (id: string) => `/test/issues/${id}`,
  }),
}));
vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: () => ({ queryKey: ["projects"], queryFn: async () => [] }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => [] }),
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: async () => [] }),
  squadListOptions: () => ({ queryKey: ["squads"], queryFn: async () => [] }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: () => "Ada",
    getActorInitials: () => "A",
    getActorAvatarUrl: () => null,
    hasActor: () => true,
  }),
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../../modals/issue-picker-modal", () => ({ IssuePickerModal: () => null }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const goal = (overrides: Partial<Goal> = {}): Goal => ({
  id: "goal-1",
  workspace_id: "ws-1",
  project_id: null,
  title: "Ship goal management",
  intent: "Let a team track a deliverable",
  scope: "Web only",
  acceptance_criteria: "An owner can confirm completion",
  owner_type: null,
  owner_id: null,
  status: "planned",
  creator_type: "member",
  creator_id: "user-1",
  created_at: "",
  updated_at: "",
  ...overrides,
});

const issue = (overrides: Partial<Issue> = {}): Issue =>
  ({
    id: "issue-1",
    identifier: "MUL-1",
    title: "Wire the goal list",
    status: "done",
    project_id: null,
    ...overrides,
  }) as Issue;

const closureState = (overrides: Partial<ClosureState> = {}): ClosureState => ({
  subject_type: "goal",
  subject_id: "goal-1",
  status: "planned",
  managed: true,
  delivery_version: "9f2c4a1",
  delivery_tree: "",
  criteria_revision: "plan-r2",
  outcome: "can_close",
  reason: "",
  detail: "",
  next_actor: "",
  already_accepted: false,
  authorization: null,
  reviews: [],
  ...overrides,
});

describe("GoalDetailPage", () => {
  let qc: QueryClient;
  const push = vi.fn();
  const api = {
    getGoal: vi.fn(),
    listGoalIssues: vi.fn(),
    updateGoal: vi.fn(),
    deleteGoal: vi.fn(),
    unlinkGoalIssue: vi.fn(),
    linkGoalIssue: vi.fn(),
    getGoalPlan: vi.fn(),
    getGoalClosure: vi.fn(),
  };

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    push.mockReset();
    api.getGoal.mockResolvedValue(goal());
    api.listGoalIssues.mockResolvedValue({ issues: [], total: 0 });
    api.updateGoal.mockResolvedValue(goal({ status: "completed" }));
    api.deleteGoal.mockResolvedValue(undefined);
    api.unlinkGoalIssue.mockResolvedValue(undefined);
    api.linkGoalIssue.mockResolvedValue(undefined);
    api.getGoalPlan.mockResolvedValue({
      id: null,
      goal_id: "goal-1",
      project_id: null,
      revision: 0,
      state: "draft",
      items: [],
      edges: [],
      context_snapshot: {},
      created_at: null,
      updated_at: null,
    });
    api.getGoalClosure.mockResolvedValue(closureState());
    setApiInstance(api as unknown as ApiClient);
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  afterEach(() => qc.clear());

  function renderPage(id = "goal-1") {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <NavigationProvider
          value={{
            push,
            replace: vi.fn(),
            back: vi.fn(),
            pathname: `/test/goals/${id}`,
            searchParams: new URLSearchParams(),
            hash: "",
            getShareableUrl: (p) => p,
          }}
        >
          <GoalDetailPage goalId={id} />
        </NavigationProvider>
      </QueryClientProvider>,
    );
  }

  it("accepts a goal only through the confirmation dialog", async () => {
    renderPage();
    expect(await screen.findByText("Ship goal management")).toBeInTheDocument();
    expect(await screen.findByTestId("closure-outcome")).toHaveTextContent("Ready to accept");

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Accept goal" }));
    expect(api.updateGoal).not.toHaveBeenCalled();

    const dialog = await screen.findByRole("alertdialog");
    expect(
      within(dialog).getByText(
        "Accepting marks this goal completed. It's your decision as a person; linked task statuses and run exits don't make it.",
      ),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Accept goal" }));

    await waitFor(() =>
      expect(api.updateGoal).toHaveBeenCalledWith("goal-1", { status: "completed" }),
    );
    expect(toast.success).toHaveBeenCalledWith("Goal accepted");
  });

  it("warns before accepting against the server's decision and sends the human override", async () => {
    api.getGoalClosure.mockResolvedValue(
      closureState({ outcome: "wait_review", reason: "no_independent_review" }),
    );
    renderPage();
    expect(await screen.findByTestId("closure-outcome")).toHaveTextContent("Waiting for review");

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Accept goal" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(
      within(dialog).getByText('The server\'s current decision is "Waiting for review".'),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText("No valid review covers this delivery and scope."),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(
        "Accepting now goes against the server's decision. The goal is completed, but its acceptance evidence stays incomplete.",
      ),
    ).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Accept anyway" }));
    await waitFor(() =>
      expect(api.updateGoal).toHaveBeenCalledWith("goal-1", { status: "completed" }),
    );
  });

  it("shows the server's refusal instead of reporting a completed goal", async () => {
    api.updateGoal.mockRejectedValue(
      new ApiError("closure refused", 409, "Conflict", {
        code: "closure_not_allowed",
        outcome: "blocked",
        reason: "no_independent_review",
        detail: "the review names an older delivery",
        next_actor: "",
      }),
    );
    renderPage();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Accept goal" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Accept goal" }));

    const refusal = await screen.findByTestId("closure-refusal");
    expect(within(refusal).getByText("The server refused this acceptance.")).toBeInTheDocument();
    expect(
      within(refusal).getByText("Blocked · No valid review covers this delivery and scope."),
    ).toBeInTheDocument();
    expect(within(refusal).getByText("the review names an older delivery")).toBeInTheDocument();
    expect(toast.success).not.toHaveBeenCalled();
    expect(screen.getAllByText("Planned").length).toBeGreaterThan(0);
  });

  it("never renders an unrecognized outcome as ready to accept", async () => {
    api.getGoalClosure.mockResolvedValue(closureState({ outcome: "accepted_by_future_server" }));
    renderPage();
    expect(await screen.findByTestId("closure-outcome")).toHaveTextContent("Unknown decision");
    expect(screen.queryByText("Ready to accept")).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Accept goal" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("button", { name: "Accept anyway" })).toBeInTheDocument();
  });

  it("keeps a linked task's done status from reading as goal success", async () => {
    api.listGoalIssues.mockResolvedValue({ issues: [issue()], total: 1 });
    api.getGoalClosure.mockResolvedValue(
      closureState({ outcome: "wait_review", reason: "no_independent_review" }),
    );
    renderPage();
    expect(await screen.findByText("MUL-1")).toBeInTheDocument();
    expect(screen.getAllByText("Planned").length).toBeGreaterThan(0);
    expect(screen.queryByText("Completed")).not.toBeInTheDocument();
    expect(await screen.findByTestId("closure-outcome")).toHaveTextContent("Waiting for review");
    expect(screen.getByRole("button", { name: "Accept goal" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reopen" })).not.toBeInTheDocument();
  });

  it("never treats an unrecognized server status as completed", async () => {
    api.getGoal.mockResolvedValue(goal({ status: "archived" as Goal["status"] }));
    renderPage();
    expect(await screen.findByText("Ship goal management")).toBeInTheDocument();
    expect(screen.getAllByText("Unknown").length).toBeGreaterThan(0);
    expect(screen.queryByText("Completed")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reopen" })).not.toBeInTheDocument();
  });

  it("reopens a completed goal after confirmation", async () => {
    api.getGoal.mockResolvedValue(goal({ status: "completed" }));
    api.updateGoal.mockResolvedValue(goal({ status: "planned" }));
    renderPage();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Reopen" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Reopen" }));
    await waitFor(() =>
      expect(api.updateGoal).toHaveBeenCalledWith("goal-1", { status: "planned" }),
    );
    expect(toast.success).toHaveBeenCalledWith("Goal reopened");
  });

  it("unlinks a task only after the server confirms it", async () => {
    api.listGoalIssues.mockResolvedValue({ issues: [issue()], total: 1 });
    renderPage();
    expect(await screen.findByText("MUL-1")).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Unlink" }));
    await waitFor(() =>
      expect(api.unlinkGoalIssue).toHaveBeenCalledWith("goal-1", "issue-1"),
    );
    expect(toast.success).toHaveBeenCalledWith("Task unlinked");
  });

  it("reports a failed unlink instead of removing the row", async () => {
    api.listGoalIssues.mockResolvedValue({ issues: [issue()], total: 1 });
    api.unlinkGoalIssue.mockRejectedValue(new Error("Forbidden"));
    renderPage();
    expect(await screen.findByText("MUL-1")).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Unlink" }));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("Couldn't unlink the task. Try again."),
    );
    expect(screen.getByText("MUL-1")).toBeInTheDocument();
  });

  it("deletes the goal after confirmation and returns to the list", async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Delete goal" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Delete goal" }));
    await waitFor(() => expect(api.deleteGoal).toHaveBeenCalledWith("goal-1"));
    expect(toast.success).toHaveBeenCalledWith("Goal deleted");
    expect(push).toHaveBeenCalledWith("/test/goals");
  });

  it("shows not-found for a goal the server says is gone or forbidden", async () => {
    api.getGoal.mockRejectedValue(new ApiError("Not found", 404, "Not Found"));
    renderPage();
    expect(
      await screen.findByText("This goal doesn't exist or you don't have access to it."),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to goals" })).toHaveAttribute(
      "href",
      "/test/goals",
    );
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });

  it("drops the cached goal identity when a later load loses access", async () => {
    renderPage();
    expect(await screen.findByText("Ship goal management")).toBeInTheDocument();

    // React Query keeps the last successful payload when a refetch fails.
    // Losing project visibility must still clear the stale title.
    api.getGoal.mockRejectedValue(new ApiError("Forbidden", 403, "Forbidden"));
    await act(async () => {
      await qc.refetchQueries({ queryKey: goalKeys.detail("ws-1", "goal-1") });
    });

    await waitFor(() =>
      expect(screen.queryByText("Ship goal management")).not.toBeInTheDocument(),
    );
    expect(
      screen.getByText("This goal doesn't exist or you don't have access to it."),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to goals" })).toHaveAttribute(
      "href",
      "/test/goals",
    );
    // The breadcrumb falls back to the neutral label, not the old title.
    expect(screen.getByText("Goal")).toBeInTheDocument();
  });

  it("keeps the last known title when a refetch fails transiently", async () => {
    renderPage();
    expect(await screen.findByText("Ship goal management")).toBeInTheDocument();

    api.getGoal.mockRejectedValue(new ApiError("Server error", 500, "Internal Server Error"));
    await act(async () => {
      await qc.refetchQueries({ queryKey: goalKeys.detail("ws-1", "goal-1") });
    });

    expect(await screen.findByText("Couldn't load this goal.")).toBeInTheDocument();
    expect(screen.getByText("Ship goal management")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("offers a retry on a transport failure", async () => {
    api.getGoal
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValue(goal());
    renderPage();
    expect(await screen.findByText("Couldn't load this goal.")).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Ship goal management")).toBeInTheDocument();
  });

  it("explains that a workspace-level goal is visible to everyone", async () => {
    renderPage();
    expect(
      await screen.findByText("This goal has no project, so everyone in the workspace can see it."),
    ).toBeInTheDocument();
  });
});