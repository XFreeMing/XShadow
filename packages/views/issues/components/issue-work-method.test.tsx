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

const { download } = vi.hoisted(() => ({ download: vi.fn() }));
vi.mock("../../editor/use-download-attachment", () => ({
  useDownloadAttachment: () => download,
}));

afterEach(() => {
  cleanup();
  download.mockClear();
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

it("shows the original handoff brief alongside the squad receipt", async () => {
  const sampleDigest = "d".repeat(64);
  mount({ history: [{
    action: "approve",
    step: 0,
    evidence: "Independently verified the fixture",
    actor: "member/Bella",
    at: "2026-11-19T08:00:00Z",
    squad_handoff: {
      task_id: "receiving-run",
      from_squad_id: "Design and TDD",
      to_squad_id: "General development",
      from_step: 0,
      to_step: 1,
      run_revision: 3,
      context_brief: {
        objective: "Reopen the exact passage from a saved citation",
        method_revision: 2,
        acceptance_revision: 5,
        contract_digest: "a".repeat(64),
        criteria: [{ id: "AC1", version: 2, cases: [{
          id: "B01", version: 3, artifact_sha256: sampleDigest, command: "./test B01",
        }] }],
      },
    },
  }] });
  const receipt = await screen.findByText("Squad handoff · Design and TDD → General development");
  expect(receipt).toHaveAttribute("title", "receiving-run");
  expect(screen.getByText("Independently verified the fixture")).toBeInTheDocument();
  expect(screen.getByText("Handoff brief")).toBeInTheDocument();
  expect(screen.getByText("Reopen the exact passage from a saved citation")).toBeInTheDocument();
  expect(screen.getByText("Work method v2 · Acceptance r5")).toBeInTheDocument();
  expect(screen.getByText("B01 v3")).toBeInTheDocument();
  expect(screen.getByText("./test B01")).toBeInTheDocument();
  expect(screen.getByText(sampleDigest)).toBeInTheDocument();
});

it("displays the GitHub pull request and keeps pending cleanup visible after approval", async () => {
  const merge = "c".repeat(40);
  mount({
    method: {
      ...method,
      kind: "development",
      github_delivery: { repository: "owner/project", base_ref: "dev" },
    },
    state: "closed",
    can_act: false,
    project_delivery: { resource_id: "local-resource" },
    history: [{
      action: "approve",
      step: 1,
      evidence: "Independent review passed",
      actor: "member/Bella",
      at: "2026-11-25T08:00:00Z",
      delivery_receipt: { state: "cleanup_pending", pull_request: 9, merge },
    }],
  });
  expect(await screen.findByText("Merged, awaiting cleanup")).toBeVisible();
  expect(screen.getByRole("link", { name: "PR #9" })).toHaveAttribute("href", "https://github.com/owner/project/pull/9");
});

it("submits coverage preflight receipts along with the coverage handoff", async () => {
  const [design, synthesis] = method.steps;
  assert(design && synthesis);
  const change = mount({
    state: "working",
    method: {
      ...method,
      kind: "development",
      steps: [{ ...design, require_preflight: true, acceptance_gate: "coverage" }, synthesis],
    },
  });
  const input = await screen.findByRole("textbox", { name: "Coverage receipts (JSON)" });
  const receipt = [{
    criterion_id: "AC1",
    criterion_version: 1,
    case_id: "B01",
    case_version: 1,
    command: "./test B01",
    baseline: { commit: "a".repeat(40), result: "pass", evidence: "Legacy behavior confirmed" },
  }];
  fireEvent.change(screen.getByRole("textbox", { name: "Step output and supporting evidence" }), { target: { value: "Evidence at test report" } });
  fireEvent.change(input, { target: { value: JSON.stringify(receipt) } });
  fireEvent.click(screen.getByRole("button", { name: "Submit for review" }));
  await waitFor(() => expect(change).toHaveBeenCalledWith("issue", expect.objectContaining({
    action: "advance", coverage_preflight: receipt,
  })));
});

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

it("offers rework as the decision for an independently reviewed candidate failure", async () => {
  const change = mount({
    history: [{
      action: "advance", step: 0, actor: "agent/Alice", at: "2026-11-19T08:00:00Z",
      evidence: "The candidate failed T1",
      candidate_failure: { commit: "a".repeat(40), cases: ["T1"], worker_task_id: "worker-1", trace_revision: 2 },
    }],
  });
  const rework = await screen.findByRole("button", { name: "Return for rework" });
  expect(screen.queryByRole("button", { name: "Approve and continue" })).toBeNull();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Fix the failing implementation" } });
  fireEvent.click(rework);
  await waitFor(() => expect(change).toHaveBeenCalledWith("issue", {
    action: "rework", base_revision: 3, target_step: 0, checked_items: [], evidence: "Fix the failing implementation",
  }));
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

it("shows one current result per criterion and opens the signed evidence on demand", async () => {
  const attachment = "01a20c18-444a-7a66-9b41-6daec377bf47";
  const candidate = "19dfad1" + "a".repeat(33);
  mount({
    method: { ...method, kind: "development" },
    state: "closed",
    step: 1,
    history: [
      { action: "rework", step: 0, actor: "member/Bella", at: "2026-01-01T01:00:00Z", evidence: "Previous rejected candidate" },
      {
        action: "approve",
        step: 1,
        actor: "member/Bella",
        at: "2026-01-02T01:00:00Z",
        evidence: "Signed decision",
        closeout: {
          trace_revision: 24,
          criteria: [{
            id: "US21-02",
            version: 1,
            summary: "Keep the original",
            candidate,
            passed: true,
            evidence: [{
              case_id: "B01", run_number: 7, version: 1, case_version: 2,
              result: "pass", candidate, evidence: "Verified final run",
              attachment_ids: [attachment],
            }],
          }],
        },
      },
    ],
  });
  expect(await screen.findByRole("region", { name: "Closeout" })).toBeVisible();
  expect(screen.getByText(/1\/1 passed/)).toBeVisible();
  expect(screen.getByText("19dfad1")).toBeVisible();
  expect(screen.getByText("Previous rejected candidate")).not.toBeVisible();
  expect(screen.getByText("Verified final run")).not.toBeVisible();
  fireEvent.click(screen.getByText("Final evidence"));
  expect(screen.getByText("Verified final run")).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Download attachment 1" }));
  expect(download).toHaveBeenCalledWith(attachment);
});
