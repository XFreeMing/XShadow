// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createStore, type StoreApi } from "zustand/vanilla";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { propertyListOptions } from "@multica/core/properties";
import type { IssueProperty } from "@multica/core/types";
import {
  type IssueViewState,
  viewStoreSlice,
} from "@multica/core/issues/stores/view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { renderWithI18n } from "../../test/i18n";
import { IssueDisplayControls } from "./issues-header";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

function renderDisplay(
  props: {
    allowGantt?: boolean;
    allowGrid?: boolean;
    allowGallery?: boolean;
    allowCalendar?: boolean;
    allowForm?: boolean;
  } = {},
  setup: (store: StoreApi<IssueViewState>) => void = () => {},
  properties: IssueProperty[] = [],
) {
  setApiInstance(
    new Proxy({}, { get: () => async () => ({}) }) as unknown as ApiClient,
  );
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  qc.setQueryData(propertyListOptions("ws-1").queryKey, { properties, total: properties.length });
  const store = createStore<IssueViewState>()(viewStoreSlice);
  setup(store);
  renderWithI18n(
    <QueryClientProvider client={qc}>
      <ViewStoreProvider store={store}>
        <IssueDisplayControls scopedIssues={[]} {...props} />
      </ViewStoreProvider>
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Display" }));
  return store;
}

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
});

describe("Display layout", () => {
  it("switches the layout from inside the panel", async () => {
    const store = renderDisplay({}, (s) => s.getState().setViewMode("board"));

    const layouts = await screen.findByRole("group", { name: "View" });
    expect(
      within(layouts).getAllByRole("button").map((button) => button.textContent),
    ).toEqual(["Board", "List", "Table", "Swimlane"]);
    expect(within(layouts).getByRole("button", { name: "Board" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByText("Board options")).toBeInTheDocument();

    fireEvent.click(within(layouts).getByRole("button", { name: "Table" }));

    expect(store.getState().viewMode).toBe("table");
    expect(await screen.findByText("Table options")).toBeInTheDocument();
  });

  it("offers Gantt only where the surface renders it", async () => {
    renderDisplay({ allowGantt: true });

    const layouts = await screen.findByRole("group", { name: "View" });
    expect(within(layouts).getByRole("button", { name: "Gantt" })).toBeInTheDocument();
  });

  it("shows custom field selection in Gantt when it has no built-in card options", async () => {
    const store = renderDisplay({ allowGantt: true }, (s) => s.getState().setViewMode("gantt"), [{
      id: "calc-cost", workspace_id: "ws-1", name: "Calculated cost", type: "formula",
      config: { expression: "1 + 2" }, position: 0, archived: false, usage_count: 0,
      created_at: "2026-11-26T00:00:00Z", updated_at: "2026-11-26T00:00:00Z",
    }]);

    fireEvent.click(await screen.findByRole("button", { name: "Calculated cost" }));
    expect(store.getState().cardPropertyIds).toEqual(["calc-cost"]);
  });

  it("switches to Multidimensional view and back when allowGrid is enabled", async () => {
    const store = renderDisplay({ allowGrid: true }, (s) => s.getState().setViewMode("board"));

    const layouts = await screen.findByRole("group", { name: "View" });
    fireEvent.click(within(layouts).getByRole("button", { name: "Multidimensional view" }));
    expect(store.getState().viewMode).toBe("grid");

    fireEvent.click(within(layouts).getByRole("button", { name: "List" }));
    expect(store.getState().viewMode).toBe("list");
  });

  it("switches to Gallery when allowGallery is enabled", async () => {
    const store = renderDisplay(
      { allowGallery: true },
      (s) => s.getState().setViewMode("board"),
    );

    const layouts = await screen.findByRole("group", { name: "View" });
    fireEvent.click(within(layouts).getByRole("button", { name: "Gallery" }));
    expect(store.getState().viewMode).toBe("gallery");
  });

  it("switches to Calendar when allowCalendar is enabled", async () => {
    const store = renderDisplay(
      { allowCalendar: true },
      (s) => s.getState().setViewMode("board"),
    );

    const layouts = await screen.findByRole("group", { name: "View" });
    fireEvent.click(within(layouts).getByRole("button", { name: "Calendar" }));
    expect(store.getState().viewMode).toBe("calendar");
  });

  it("switches to Form when allowForm is enabled", async () => {
    const store = renderDisplay(
      { allowForm: true },
      (s) => s.getState().setViewMode("board"),
    );

    const layouts = await screen.findByRole("group", { name: "View" });
    fireEvent.click(within(layouts).getByRole("button", { name: "Form" }));
    expect(store.getState().viewMode).toBe("form");
  });

  it("keeps the table's grouping and columns in the panel", async () => {
    renderDisplay({}, (s) => s.getState().setViewMode("table"));

    await screen.findByText("Table options");
    expect(screen.getAllByRole("combobox", { name: "Grouping" })).toHaveLength(1);
    expect(screen.getByRole("button", { name: /shown$/ })).toBeInTheDocument();
    expect(screen.getByText("Hierarchy")).toBeInTheDocument();
  });
});
