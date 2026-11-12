import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { Goal } from "@multica/core/types";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider } from "../../navigation";
import { GoalsPage } from "./goals-page";

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
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const goal = (id: string, title: string, status = "planned"): Goal => ({
  id,
  workspace_id: "ws-1",
  project_id: null,
  title,
  intent: "",
  scope: "",
  acceptance_criteria: "",
  owner_type: null,
  owner_id: null,
  status: status as Goal["status"],
  creator_type: "member",
  creator_id: "user-1",
  created_at: "",
  updated_at: "",
});

describe("GoalsPage", () => {
  let qc: QueryClient;
  const push = vi.fn();
  const api = {
    listGoals: vi.fn(),
    createGoal: vi.fn(),
    getGoal: vi.fn(),
    listGoalIssues: vi.fn(),
  };

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    push.mockReset();
    api.listGoals.mockResolvedValue({ goals: [goal("g1", "First goal")], total: 1 });
    api.createGoal.mockResolvedValue(goal("g2", "Second goal"));
    api.listGoalIssues.mockResolvedValue({ issues: [], total: 0 });
    setApiInstance(api as unknown as ApiClient);
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  afterEach(() => qc.clear());

  function renderPage() {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <NavigationProvider
          value={{
            push,
            replace: vi.fn(),
            back: vi.fn(),
            pathname: "/test/goals",
            searchParams: new URLSearchParams(),
            hash: "",
            getShareableUrl: (p) => p,
          }}
        >
          <GoalsPage />
        </NavigationProvider>
      </QueryClientProvider>,
    );
  }

  it("lists goals with their lifecycle state", async () => {
    api.listGoals.mockResolvedValue({
      goals: [goal("g1", "First goal", "in_progress")],
      total: 1,
    });
    renderPage();
    expect(await screen.findByText("First goal")).toBeInTheDocument();
    expect(screen.getByText("In progress")).toBeInTheDocument();
  });

  it("pages through a long goal list", async () => {
    api.listGoals.mockImplementation(async (params?: { offset?: number }) =>
      params?.offset
        ? { goals: [goal("g2", "Second goal")], total: 2 }
        : { goals: [goal("g1", "First goal")], total: 2 },
    );
    renderPage();
    expect(await screen.findByText("First goal")).toBeInTheDocument();
    expect(screen.queryByText("Second goal")).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Load more" }));

    expect(await screen.findByText("Second goal")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
    expect(api.listGoals).toHaveBeenLastCalledWith(
      expect.objectContaining({ offset: 1, limit: 50 }),
    );
  });

  it("does not navigate or toast success when the server confirms no goal", async () => {
    api.createGoal.mockResolvedValue({ ...goal("", "") });
    renderPage();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New goal" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Title"), "Draft goal");
    await user.click(within(dialog).getByRole("button", { name: "Create goal" }));

    expect(await within(dialog).findByText(/Couldn't create the goal/)).toBeInTheDocument();
    expect(push).not.toHaveBeenCalled();
    expect(toast.success).not.toHaveBeenCalled();
    // The typed input survives a failed save.
    expect(within(dialog).getByLabelText("Title")).toHaveValue("Draft goal");
  });

  it("creates a goal, then opens its detail page", async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New goal" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Title"), "Draft goal");
    await user.click(within(dialog).getByRole("button", { name: "Create goal" }));

    await waitFor(() => expect(push).toHaveBeenCalledWith("/test/goals/g2"));
    expect(api.createGoal).toHaveBeenCalledWith(
      expect.objectContaining({ title: "Draft goal" }),
    );
    expect(toast.success).toHaveBeenCalledWith("Goal created");
  });

  it("shows the empty state with a create action", async () => {
    api.listGoals.mockResolvedValue({ goals: [], total: 0 });
    renderPage();
    expect(await screen.findByText("No goals yet")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "New goal" }).length).toBeGreaterThan(0);
  });
});