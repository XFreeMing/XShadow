/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { ClosureReview, ClosureState, Goal } from "@multica/core/types";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";
import { GoalClosureSection } from "./goal-closure-section";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: () => "Ada",
    getActorInitials: () => "A",
    getActorAvatarUrl: () => null,
    hasActor: () => true,
  }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const goal: Goal = {
  id: "goal-1",
  workspace_id: "ws-1",
  project_id: null,
  title: "Ship the workbench",
  intent: "",
  scope: "",
  acceptance_criteria: "",
  owner_type: null,
  owner_id: null,
  status: "in_progress",
  creator_type: "member",
  creator_id: "user-1",
  created_at: "",
  updated_at: "",
};

function review(overrides: Partial<ClosureReview> = {}): ClosureReview {
  return {
    id: "review-1",
    subject_type: "goal",
    subject_id: "goal-1",
    action: "goal_accept",
    delivery_version: "9f2c4a1",
    delivery_tree: "tree-1",
    criteria_revision: "plan-r2",
    author_type: "agent",
    author_id: "agent-1",
    author_model: "deepseek-v4.1-flash",
    reviewer_type: "agent",
    reviewer_id: "agent-2",
    reviewer_model: "gpt-5-codex",
    verdict: "pass",
    criteria: [{ criterion: "It runs", result: "pass", evidence: "tests" }],
    platforms: ["linux"],
    missing: [],
    reason: "",
    superseded_by: null,
    accepted_at: null,
    accepted_by_type: "",
    accepted_by_id: null,
    recorded_at: "2026-11-10T08:00:00Z",
    ...overrides,
  };
}

function closureState(overrides: Partial<ClosureState> = {}): ClosureState {
  return {
    subject_type: "goal",
    subject_id: "goal-1",
    status: "in_progress",
    managed: true,
    delivery_version: "9f2c4a1",
    delivery_tree: "tree-1",
    criteria_revision: "plan-r2",
    outcome: "can_close",
    reason: "",
    detail: "",
    next_actor: "",
    already_accepted: false,
    authorization: null,
    reviews: [review()],
    ...overrides,
  };
}

describe("GoalClosureSection", () => {
  let qc: QueryClient;
  const api = {
    getGoalClosure: vi.fn(),
    updateGoal: vi.fn(),
    createGoalClosureReview: vi.fn(),
    grantGoalClosureAuthorization: vi.fn(),
  };

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    api.getGoalClosure.mockReset().mockResolvedValue(closureState());
    api.updateGoal.mockReset().mockResolvedValue({ ...goal, status: "completed" });
    api.createGoalClosureReview.mockReset().mockResolvedValue(review({ verdict: "needs_changes" }));
    api.grantGoalClosureAuthorization.mockReset().mockResolvedValue({
      id: "grant-1",
      subject_type: "goal",
      subject_id: "goal-1",
      actions: ["goal_accept"],
      covers_children: false,
      criteria_revision: "plan-r2",
      state: "active",
      revision: 1,
      source_ref: "thread",
      granted_by_type: "member",
      granted_by_id: "user-1",
      expires_at: null,
      revoked_at: null,
    });
    setApiInstance(api as unknown as ApiClient);
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  afterEach(() => qc.clear());

  function renderSection() {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <GoalClosureSection goal={goal} />
      </QueryClientProvider>,
    );
  }

  it("shows the server's decision, reason and review basis", async () => {
    api.getGoalClosure.mockResolvedValue(
      closureState({
        outcome: "wait_review",
        reason: "no_independent_review",
        detail: "the newest review names an older delivery",
        reviews: [review({ verdict: "needs_changes", reason: "the tests did not run" })],
      }),
    );
    renderSection();

    expect(await screen.findByTestId("closure-outcome")).toHaveTextContent("Waiting for review");
    expect(screen.getByTestId("closure-reason")).toHaveTextContent(
      "No valid review covers this delivery and scope.",
    );
    expect(screen.getByText("the newest review names an older delivery")).toBeInTheDocument();
    expect(screen.getByTestId("closure-review")).toHaveTextContent("Changes required");
    expect(screen.getByTestId("closure-review")).toHaveTextContent("the tests did not run");
    expect(screen.queryByText("Ready to accept")).not.toBeInTheDocument();
  });

  it("reports a degraded read instead of treating it as accepted", async () => {
    api.getGoalClosure.mockResolvedValue(
      closureState({ subject_id: "", outcome: "", managed: false, reviews: [] }),
    );
    renderSection();

    expect(await screen.findByTestId("closure-unavailable")).toHaveTextContent(
      "The server's acceptance reply couldn't be read. Nothing is treated as accepted.",
    );
    expect(screen.queryByRole("button", { name: "Accept goal" })).not.toBeInTheDocument();
  });

  it("accepts through the dialog and waits for the server write", async () => {
    renderSection();
    await screen.findByTestId("closure-outcome");
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Accept goal" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(
      within(dialog).getByText(
        "Accepting marks this goal completed. It's your decision as a person; linked task statuses and run exits don't make it.",
      ),
    ).toBeInTheDocument();
    expect(api.updateGoal).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole("button", { name: "Accept goal" }));
    await waitFor(() =>
      expect(api.updateGoal).toHaveBeenCalledWith("goal-1", { status: "completed" }),
    );
    expect(toast.success).toHaveBeenCalledWith("Goal accepted");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("shows the server's refusal and keeps the goal open", async () => {
    api.updateGoal.mockRejectedValue(
      new ApiError("refused", 409, "Conflict", {
        code: "closure_not_allowed",
        outcome: "needs_changes",
        reason: "review_needs_changes",
        detail: "review-1 asked for rework",
        next_actor: "",
      }),
    );
    renderSection();
    await screen.findByTestId("closure-outcome");
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Accept goal" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Accept goal" }));

    const refusal = await screen.findByTestId("closure-refusal");
    expect(within(refusal).getByText("The server refused this acceptance.")).toBeInTheDocument();
    expect(
      within(refusal).getByText("Changes required · An independent review requires changes."),
    ).toBeInTheDocument();
    expect(within(refusal).getByText("review-1 asked for rework")).toBeInTheDocument();
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("keeps an ordinary accept failure inside the dialog", async () => {
    api.updateGoal.mockRejectedValue(new TypeError("Failed to fetch"));
    renderSection();
    await screen.findByTestId("closure-outcome");
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Accept goal" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Accept goal" }));

    expect(await within(dialog).findByText("Couldn't accept the goal. Try again.")).toBeVisible();
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    expect(screen.queryByTestId("closure-refusal")).not.toBeInTheDocument();
  });

  it("records a rejection against the server's delivery and scope", async () => {
    renderSection();
    await screen.findByTestId("closure-outcome");
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Reject" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("What must change"), "Cover the failure path");
    await user.type(within(dialog).getByLabelText("Rework scope"), "goal-plan-execution");
    await user.click(within(dialog).getByRole("button", { name: "Record rejection" }));

    await waitFor(() =>
      expect(api.createGoalClosureReview).toHaveBeenCalledWith("goal-1", {
        delivery_version: "9f2c4a1",
        delivery_tree: "tree-1",
        criteria_revision: "plan-r2",
        verdict: "needs_changes",
        reason: "Cover the failure path",
        missing: ["goal-plan-execution"],
      }),
    );
    expect(toast.success).toHaveBeenCalledWith("Goal sent back for changes");
  });

  it("requires a reason and refuses to fabricate a delivery version", async () => {
    api.getGoalClosure.mockResolvedValue(
      closureState({ delivery_version: "", delivery_tree: "", criteria_revision: "", reviews: [] }),
    );
    renderSection();
    await screen.findByTestId("closure-outcome");
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Reject" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(
        "A rejection is recorded against a fixed delivery version. None is registered yet, so nothing was recorded and the goal stays open.",
      ),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Record rejection" })).toBeDisabled();
    expect(api.createGoalClosureReview).not.toHaveBeenCalled();
  });

  it("explains a stale rejection and reloads the acceptance state", async () => {
    api.createGoalClosureReview.mockRejectedValue(
      new ApiError("stale", 409, "Conflict", { code: "closure_delivery_stale" }),
    );
    renderSection();
    await screen.findByTestId("closure-outcome");
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Reject" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("What must change"), "Refresh first");
    await user.click(within(dialog).getByRole("button", { name: "Record rejection" }));

    expect(
      await within(dialog).findByText(
        "The delivery or scope changed. Reload the acceptance state and reject again.",
      ),
    ).toBeVisible();
    await waitFor(() =>
      expect(api.getGoalClosure.mock.calls.length).toBeGreaterThan(1),
    );
  });

  it("grants only the explicit acceptance action for the current scope", async () => {
    renderSection();
    await screen.findByTestId("closure-outcome");
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Grant acceptance" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Source reference"), "thread 123");
    await user.click(within(dialog).getByRole("button", { name: "Grant" }));

    await waitFor(() =>
      expect(api.grantGoalClosureAuthorization).toHaveBeenCalledWith("goal-1", {
        actions: ["goal_accept"],
        covers_children: false,
        criteria_revision: "plan-r2",
        source_ref: "thread 123",
      }),
    );
    expect(toast.success).toHaveBeenCalledWith("Acceptance authorization recorded");
  });

  it("shows the recorded authorization and links the next actor", async () => {
    api.getGoalClosure.mockResolvedValue(
      closureState({
        outcome: "wait_human",
        next_actor: "user-9",
        authorization: {
          id: "grant-1",
          subject_type: "goal",
          subject_id: "goal-1",
          actions: ["goal_accept"],
          covers_children: true,
          criteria_revision: "plan-r2",
          state: "active",
          revision: 1,
          source_ref: "planning thread",
          granted_by_type: "member",
          granted_by_id: "user-1",
          expires_at: null,
          revoked_at: null,
        },
      }),
    );
    renderSection();

    const authorization = await screen.findByTestId("closure-authorization");
    expect(authorization).toHaveTextContent("Active");
    expect(authorization).toHaveTextContent("Covers linked tasks");
    expect(authorization).toHaveTextContent("planning thread");
    expect(screen.getAllByText("Ada").length).toBeGreaterThan(0);
    expect(screen.queryByText("A person named by the server")).not.toBeInTheDocument();
  });
});