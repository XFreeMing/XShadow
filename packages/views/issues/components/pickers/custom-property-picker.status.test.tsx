import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { issueStatusListOptions } from "@multica/core/issue-statuses/queries";
import type { IssueProperty, IssueStatusEntry } from "@multica/core/types";
import enIssues from "../../../locales/en/issues.json";
import { CustomPropertyValueInput } from "./custom-property-picker";

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...await importOriginal<typeof import("@multica/core/hooks")>(),
  useWorkspaceId: () => "ws-status",
}));

const property: IssueProperty = {
  id: "milestone",
  workspace_id: "ws-status",
  name: "Milestone",
  type: "status",
  config: {},
  position: 0,
  archived: false,
  created_at: "",
  updated_at: "",
};

const active: IssueStatusEntry = {
  id: "status-1",
  workspace_id: property.workspace_id,
  key: "validation",
  name: "Validating",
  description: "",
  category: "started",
  color: "#123456",
  is_system: false,
  archived_at: null,
  position: 0,
  created_at: "",
  updated_at: "",
};
const retired: IssueStatusEntry = {
  ...active,
  id: "status-2",
  key: "retired",
  name: "Retired phase",
  archived_at: "2026-11-26T00:00:00Z",
};

it("reads live status options while keeping an archived value visible", async () => {
  const client = new QueryClient();
  const queryKey = issueStatusListOptions(property.workspace_id).queryKey;
  const updateCatalog = (statuses: IssueStatusEntry[]) =>
    client.setQueryData(queryKey, { statuses, categories: [], total: statuses.length });
  updateCatalog([active, retired]);
  const onChange = vi.fn();
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={{ en: { issues: enIssues } }}>
        <CustomPropertyValueInput property={property} value="retired" defaultOpen onChange={onChange} />
      </I18nProvider>
    </QueryClientProvider>,
  );

  expect(screen.getByRole("button", { name: "Retired phase" })).toBeVisible();
  expect(screen.getAllByRole("button", { name: "Retired phase" })).toHaveLength(1);
  await waitFor(() => expect(screen.getByRole("button", { name: "Validating" })).toBeVisible());
  act(() => updateCatalog([{ ...active, name: "QA review" }, retired]));
  await user.click(await screen.findByRole("button", { name: "QA review" }));
  expect(onChange).toHaveBeenCalledWith(active.key);
});
