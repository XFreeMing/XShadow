import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { Goal, Issue } from "@multica/core/types";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";
import { GoalLinkTaskDialog } from "./goal-link-task-dialog";

const picker = vi.hoisted(() => ({
  current: null as null | {
    excludeIds: string[];
    isSelectable?: (issue: Issue) => boolean;
    onSelect: (issue: Issue) => void;
  },
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("../../modals/issue-picker-modal", () => ({
  IssuePickerModal: (props: NonNullable<typeof picker.current>) => {
    picker.current = props;
    return null;
  },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const goal: Goal = {
  id: "goal-1",
  workspace_id: "ws-1",
  project_id: "project-1",
  title: "Goal",
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

const issue = (id: string, projectId: string | null): Issue =>
  ({ id, identifier: `MUL-${id}`, title: "Task", status: "todo", project_id: projectId }) as Issue;

describe("GoalLinkTaskDialog", () => {
  let qc: QueryClient;
  const linkGoalIssue = vi.fn();

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    picker.current = null;
    linkGoalIssue.mockReset().mockResolvedValue(undefined);
    setApiInstance({ linkGoalIssue } as unknown as ApiClient);
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  afterEach(() => qc.clear());

  function renderDialog() {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <GoalLinkTaskDialog
          open
          onOpenChange={vi.fn()}
          goal={goal}
          linkedIssueIds={["issue-1"]}
        />
      </QueryClientProvider>,
    );
  }

  it("searches the existing task index within the goal's project", () => {
    renderDialog();
    expect(picker.current?.excludeIds).toEqual(["issue-1"]);
    expect(picker.current?.isSelectable?.(issue("issue-2", "project-1"))).toBe(true);
    expect(picker.current?.isSelectable?.(issue("issue-3", "project-2"))).toBe(false);
    expect(picker.current?.isSelectable?.(issue("issue-4", null))).toBe(false);
  });

  it("links a selected task and confirms with the server", async () => {
    renderDialog();
    await act(async () => {
      picker.current!.onSelect(issue("issue-2", "project-1"));
    });
    await waitFor(() =>
      expect(linkGoalIssue).toHaveBeenCalledWith("goal-1", "issue-2"),
    );
    expect(toast.success).toHaveBeenCalledWith("Task linked");
  });

  it("explains the two server-side link conflicts", async () => {
    renderDialog();
    linkGoalIssue.mockRejectedValue(
      new ApiError("already", 409, "Conflict", { code: "issue_already_linked" }),
    );
    await act(async () => {
      picker.current!.onSelect(issue("issue-2", "project-1"));
    });
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "That task is already linked to another goal.",
      ),
    );

    linkGoalIssue.mockRejectedValue(
      new ApiError("mismatch", 409, "Conflict", { code: "goal_project_mismatch" }),
    );
    await act(async () => {
      picker.current!.onSelect(issue("issue-3", "project-1"));
    });
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "That task belongs to a different project than this goal.",
      ),
    );
  });

  it("falls back to a generic failure for an unclassified error", async () => {
    renderDialog();
    linkGoalIssue.mockRejectedValue(new Error("Forbidden"));
    await act(async () => {
      picker.current!.onSelect(issue("issue-2", "project-1"));
    });
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("Couldn't link that task."),
    );
  });
});