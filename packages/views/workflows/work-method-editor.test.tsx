// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ApiClient, ApiError, setApiInstance } from "@multica/core/api";
import type { WorkMethod } from "@multica/core/types/work-method";
import { renderWithI18n } from "../test/i18n";
import { WorkMethodsTab } from "./work-methods-tab";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_type: string, id: string) => id }),
}));

vi.mock("../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("edits a built-in method and preserves the open draft when its version changes", async () => {
  const original: WorkMethod = {
    id: "seven-step",
    revision: 1,
    name: "Research interviews",
    kind: "research",
    keywords: ["research"],
    review: "Review citations",
    rework_step: 0,
    plan_task: { issue_type_id: "research-template", seed_property_id: "outcome" },
    steps: [{ name: "Interview", output: "Meeting notes" }],
  };
  const unbound: WorkMethod = {
    ...original,
    id: "independent",
    name: "Independent method",
    plan_task: undefined,
  };
  const client = new ApiClient("https://example.test");
  setApiInstance(client);
  vi.spyOn(client, "listIssueTypes").mockResolvedValue({
    total: 1,
    issue_types: [{
      id: "research-template",
      name: "Research template",
      workspace_id: "ws",
      fields: [],
      position: 1,
      archived: false,
      created_at: "",
      updated_at: "",
    }],
  });
  vi.spyOn(client, "listProperties").mockResolvedValue({ properties: [], total: 0 });
  vi.spyOn(client, "listIssueStatuses").mockResolvedValue({ statuses: [], categories: [], total: 0 });
  vi.spyOn(client, "listMembers").mockResolvedValue([]);
  vi.spyOn(client, "listAgents").mockResolvedValue([]);
  vi.spyOn(client, "listSquads").mockResolvedValue([]);
  const list = vi
    .spyOn(client, "listWorkMethods")
    .mockResolvedValueOnce([original, unbound])
    .mockResolvedValue([{ ...original, revision: 2 }, unbound]);
  const update = vi
    .spyOn(client, "updateWorkMethod")
    .mockRejectedValueOnce(new ApiError("Version changed", 409, "Conflict"))
    .mockResolvedValue({ ...original, revision: 3 });
  renderWithI18n(
    <QueryClientProvider client={new QueryClient()}>
      <WorkMethodsTab canEdit settingsPath="/demo/settings" />
    </QueryClientProvider>,
  );
  const independent = await screen.findByText("Independent method");
  fireEvent.click(independent);
  expect(within(independent.closest("details")!).getByRole("button", { name: "Edit method" })).toBeVisible();
  fireEvent.click(independent);
  const research = screen.getByText("Research interviews");
  fireEvent.click(research);
  const method = within(research.closest("details")!);
  expect(await screen.findByRole("link", { name: "Research template" }))
    .toHaveAttribute("href", "/demo/settings?tab=task-types&type=research-template");
  fireEvent.click(method.getByRole("button", { name: "Edit method" }));
  const save = screen.getByRole("button", { name: "Save" });
  fireEvent.change(screen.getByRole("combobox", { name: /Step owner/ }), {
    target: { value: "creator" },
  });
  expect(save).toBeDisabled();
  fireEvent.change(screen.getByRole("combobox", { name: /Reviewer/ }), {
    target: { value: "project_lead" },
  });
  fireEvent.change(screen.getByRole("textbox", { name: "Acceptance checks" }), {
    target: { value: "Link transcript\nName interviewee" },
  });
  fireEvent.click(save);
  expect(await screen.findByRole("alert")).toHaveTextContent("Version changed");
  expect(
    screen.getByRole("textbox", { name: "Acceptance checks" }),
  ).toHaveValue("Link transcript\nName interviewee");
  expect(update).toHaveBeenCalledWith(
    "seven-step",
    1,
    expect.objectContaining({
      plan_task: original.plan_task,
      steps: [
        {
          name: "Interview",
          output: "Meeting notes",
          checks: ["Link transcript", "Name interviewee"],
          owner: { kind: "creator" },
          reviewer: { kind: "project_lead" },
        },
      ],
    }),
  );
  await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  fireEvent.click(method.getByRole("button", { name: "Edit method" }));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() =>
    expect(update).toHaveBeenLastCalledWith("seven-step", 2, expect.anything()),
  );
});
