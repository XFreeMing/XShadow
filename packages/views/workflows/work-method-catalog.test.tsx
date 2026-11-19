// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ApiClient, ApiError, setApiInstance } from "@multica/core/api";
import type { WorkMethod } from "@multica/core/types/work-method";
import { renderWithI18n } from "../test/i18n";
import { WorkMethodCatalog } from "./work-method-catalog";

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_type: string, id: string) => id }),
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
    steps: [{ name: "Interview", output: "Meeting notes" }],
  };
  const client = new ApiClient("https://example.test");
  setApiInstance(client);
  vi.spyOn(client, "listMembers").mockResolvedValue([]);
  vi.spyOn(client, "listAgents").mockResolvedValue([]);
  vi.spyOn(client, "listSquads").mockResolvedValue([]);
  const list = vi
    .spyOn(client, "listWorkMethods")
    .mockResolvedValueOnce([original])
    .mockResolvedValue([{ ...original, revision: 2 }]);
  const update = vi
    .spyOn(client, "updateWorkMethod")
    .mockRejectedValueOnce(new ApiError("Version changed", 409, "Conflict"))
    .mockResolvedValue({ ...original, revision: 3 });
  renderWithI18n(
    <QueryClientProvider client={new QueryClient()}>
      <WorkMethodCatalog wsId="ws" canCreate />
    </QueryClientProvider>,
  );
  fireEvent.click(
    await screen.findByText("Research interviews", { exact: false }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit method" }));
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
  fireEvent.click(screen.getByRole("button", { name: "Edit method" }));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() =>
    expect(update).toHaveBeenLastCalledWith("seven-step", 2, expect.anything()),
  );
});
