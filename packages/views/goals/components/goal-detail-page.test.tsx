import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { Goal, Issue } from "@multica/core/types";
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

  it("completes a goal only through the confirmation dialog", async () => {
    renderPage();
    expect(await screen.findByText("Ship goal management")).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Mark completed" }));
    expect(api.updateGoal).not.toHaveBeenCalled();

    const dialog = await screen.findByRole("alertdialog");
    expect(
      within(dialog).getByText("Only this action marks the goal completed. Linked task statuses don't change it."),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Mark completed" }));

    await waitFor(() =>
      expect(api.updateGoal).toHaveBeenCalledWith("goal-1", { status: "completed" }),
    );
    expect(toast.success).toHaveBeenCalledWith("Goal completed");
  });

  it("keeps a linked task's done status from reading as goal success", async () => {
    api.listGoalIssues.mockResolvedValue({ issues: [issue()], total: 1 });
    renderPage();
    expect(await screen.findByText("MUL-1")).toBeInTheDocument();
    expect(screen.getAllByText("Planned").length).toBeGreaterThan(0);
    expect(screen.queryByText("Completed")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mark completed" })).toBeInTheDocument();
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