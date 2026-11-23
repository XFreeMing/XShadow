// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, assert, expect, it, vi } from "vitest";
import { ApiClient, setApiInstance } from "@multica/core/api";
import type {
  WorkMethod,
  WorkMethodRun,
} from "@multica/core/types/work-method";
import { renderWithI18n } from "../../test/i18n";
import { IssueWorkMethod } from "./issue-work-method";

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_type: string, id: string) => id }),
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const method: WorkMethod = {
  id: "study",
  revision: 2,
  name: "Research interviews",
  kind: "research",
  keywords: ["research"],
  review: "Audit findings",
  rework_step: 0,
  steps: [
    {
      name: "Interview",
      output: "Meeting notes",
      checks: ["Evidence linked"],
      owner: { kind: "member", id: "Alice" },
      reviewer: { kind: "member", id: "Bella" },
    },
    { name: "Synthesis", output: "Findings" },
  ],
};

function mount(patch: Partial<WorkMethodRun> = {}) {
  const run: WorkMethodRun = {
    method,
    revision: 3,
    reason: "Matched research",
    state: "review",
    step: 0,
    history: [
      {
        action: "advance",
        step: 0,
        evidence: "Interview transcript",
        actor: "member/Alice",
        at: "2026-11-19T08:00:00Z",
      },
    ],
    current_actor: { kind: "member", id: "Bella" },
    can_act: true,
    ...patch,
  };
  const client = new ApiClient("https://example.test");
  setApiInstance(client);
  vi.spyOn(client, "getIssueWorkMethod").mockResolvedValue(run);
  const change = vi
    .spyOn(client, "changeIssueWorkMethod")
    .mockResolvedValue({ ...run, revision: run.revision + 1 });
  renderWithI18n(
    <QueryClientProvider client={new QueryClient()}>
      <IssueWorkMethod wsId="ws" issueId="issue">
        {null}
      </IssueWorkMethod>
    </QueryClientProvider>,
  );
  return change;
}

it("records checked items separately from an intermediate reviewer's evidence", async () => {
  const change = mount();
  const approval = await screen.findByRole("button", {
    name: "Approve and continue",
  });
  expect(screen.getAllByText("Interview transcript")[0]).toBeVisible();
  fireEvent.change(screen.getByRole("textbox"), {
    target: { value: "Verified against original recording" },
  });
  expect(approval).toBeDisabled();
  fireEvent.click(screen.getByRole("checkbox", { name: "Evidence linked" }));
  fireEvent.click(approval);
  await waitFor(() =>
    expect(change).toHaveBeenCalledWith("issue", {
      action: "approve",
      base_revision: 3,
      evidence: "Verified against original recording",
      checked_items: [0],
    }),
  );
});

it("offers start and handoff when the recommended first step belongs to someone else", async () => {
  const change = mount({
    revision: 0,
    history: [],
    state: "ready",
    can_act: false,
    current_actor: { kind: "member", id: "Alice" },
  });
  fireEvent.click(
    await screen.findByRole("button", { name: "Start and assign to Alice" }),
  );
  await waitFor(() =>
    expect(change).toHaveBeenCalledWith("issue", {
      action: "start",
      base_revision: 0,
      method_id: "study",
      method_revision: 2,
    }),
  );
  expect(screen.queryByRole("textbox")).toBeNull();
});

it("shows pending review evidence without another reviewer's actions", async () => {
  mount({ can_act: false });
  expect((await screen.findAllByText("Interview transcript"))[0]).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "Approve and continue" }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Return for rework" }),
  ).toBeNull();
});

it("displays approvals against the checklist the reviewer signed", async () => {
  const old = structuredClone(method);
  old.revision = 1;
  const originalStep = old.steps[0];
  assert.isDefined(originalStep);
  originalStep.checks = ["Original signed check"];
  mount({
    step: 1,
    state: "working",
    history: [
      { action: "approve", step: 0, actor: "member/Bella", at: "2026-01-01T01:00:00Z",
        checked_items: [0], evidence: "Original decision" },
      { action: "adopt_revision", step: 1, actor: "member/Alice", at: "2026-01-02T01:00:00Z",
        previous_method: old, method_revision: 2, evidence: "Apply to synthesis" },
    ],
  });
  expect(await screen.findByText(/Original signed check/)).toBeInTheDocument();
});
