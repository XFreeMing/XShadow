import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { Goal } from "@multica/core/types";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";
import { GoalFormDialog } from "./goal-form-dialog";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
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
    getActorName: () => "Ghost user",
    getActorInitials: () => "G",
    getActorAvatarUrl: () => null,
    hasActor: () => false,
  }),
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const goal = (overrides: Partial<Goal> = {}): Goal => ({
  id: "goal-1",
  workspace_id: "ws-1",
  project_id: null,
  title: "Ship goal management",
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
  ...overrides,
});

describe("GoalFormDialog", () => {
  let qc: QueryClient;
  const createGoal = vi.fn();
  const updateGoal = vi.fn();
  const onSaved = vi.fn();
  const onOpenChange = vi.fn();

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    createGoal.mockResolvedValue(goal({ id: "goal-2" }));
    updateGoal.mockResolvedValue(goal());
    onSaved.mockReset();
    onOpenChange.mockReset();
    setApiInstance({ createGoal, updateGoal } as unknown as ApiClient);
    vi.mocked(toast.success).mockClear();
  });

  afterEach(() => qc.clear());

  function renderForm(
    props: Partial<React.ComponentProps<typeof GoalFormDialog>> = {},
  ) {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <GoalFormDialog
          open
          onOpenChange={onOpenChange}
          onSaved={onSaved}
          {...props}
        />
      </QueryClientProvider>,
    );
  }

  it("requires a title before creating", async () => {
    renderForm();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Create goal" }));
    expect(await screen.findByText("Enter a title.")).toBeInTheDocument();
    expect(createGoal).not.toHaveBeenCalled();
  });

  it("keeps the typed input and the dialog open when creation fails", async () => {
    createGoal.mockRejectedValue(new Error("Forbidden"));
    renderForm();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Title"), "Draft goal");
    await user.click(screen.getByRole("button", { name: "Create goal" }));

    expect(await screen.findByText(/Couldn't create the goal/)).toBeInTheDocument();
    expect(screen.getByLabelText("Title")).toHaveValue("Draft goal");
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("keeps a 409 project conflict visible on the form", async () => {
    updateGoal.mockRejectedValue(
      new ApiError("Conflict", 409, "Conflict", { code: "goal_has_linked_issues" }),
    );
    renderForm({ goal: goal() });
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Unlink every task before moving this goal to another project.",
      ),
    );
  });

  it("locks the project picker while the goal has linked tasks", async () => {
    renderForm({ goal: goal(), linkedIssueCount: 2 });
    expect(
      screen.getByText("Unlink every task before moving this goal to another project."),
    ).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /Workspace/ }));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("does not offer a status select for a completed goal", () => {
    renderForm({ goal: goal({ status: "completed" }) });
    expect(screen.getAllByText("Completed").length).toBeGreaterThan(0);
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });

  it("lets an unavailable owner be cleared without starting a run", async () => {
    renderForm({
      goal: goal({ owner_type: "member", owner_id: "missing-user" }),
    });
    expect(screen.getByText("Owner unavailable. Pick another owner or leave it empty.")).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /Ghost user/ }));
    await user.click(await screen.findByRole("button", { name: "No owner" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(updateGoal).toHaveBeenCalledWith(
        "goal-1",
        expect.objectContaining({ owner_type: null, owner_id: null }),
      ),
    );
    expect(onSaved).toHaveBeenCalled();
  });
});