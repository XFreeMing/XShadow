// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { useActiveIssueViewStore } from "./active-view-store";
import { issueViewKeys } from "./queries";
import {
  ISSUE_STARTUP_VIEW_STORAGE_KEY,
  readStartupViewId,
  useIssueStartupViewPrefsStore,
} from "./startup-view-prefs";
import { useIssueViewEntry } from "./use-issue-view-entry";
import {
  __resetIssueViewVisitDismissForTests,
  dismissIssueViewVisit,
} from "./visit-dismiss";
import type { IssueView } from "../api/schemas";

vi.mock("../auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } | null }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("../hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

const listQuery = vi.hoisted(() =>
  vi.fn(async (): Promise<IssueView[]> => [
    {
      id: "view-valid",
      name: "Valid",
      scope_type: "workspace",
      query: {},
      display: {},
    } as IssueView,
  ]),
);

vi.mock("../api", () => ({
  api: {
    listIssueViews: listQuery,
  },
}));

const scope = { scope_type: "workspace" as const };

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  localStorage.clear();
  setCurrentWorkspace("acme", "ws-1");
  useActiveIssueViewStore.setState({ active: {} });
  useIssueStartupViewPrefsStore.setState({ byUser: {} });
  __resetIssueViewVisitDismissForTests();
  listQuery.mockClear();
});

afterEach(() => {
  cleanup();
});

describe("useIssueViewEntry", () => {
  it("opens a persisted preference when workspace storage hydrates after the route mounts", async () => {
    renderHook(() => useIssueViewEntry(scope, null), { wrapper });
    await waitFor(() => expect(listQuery).toHaveBeenCalled());
    expect(useActiveIssueViewStore.getState().active["ws-1:workspace"]).toBeUndefined();

    localStorage.setItem(`${ISSUE_STARTUP_VIEW_STORAGE_KEY}:acme`, JSON.stringify({
      state: { byUser: { "user-1": { "ws-1:workspace": "view-valid" } } },
      version: 0,
    }));
    await act(async () => {
      await useIssueStartupViewPrefsStore.persist.rehydrate();
    });

    await waitFor(() =>
      expect(useActiveIssueViewStore.getState().active["ws-1:workspace"]).toBe("view-valid"),
    );
  });

  it("opens a saved startup view on revisit when none is active", async () => {
    useIssueStartupViewPrefsStore
      .getState()
      .setStartupView("user-1", "ws-1:workspace", "view-valid");

    renderHook(() => useIssueViewEntry(scope, null), { wrapper });

    await waitFor(() =>
      expect(useActiveIssueViewStore.getState().active["ws-1:workspace"]).toBe("view-valid"),
    );
  });

  it("leaves an explicit URL in control", async () => {
    useIssueStartupViewPrefsStore
      .getState()
      .setStartupView("user-1", "ws-1:workspace", "view-valid");

    renderHook(() => useIssueViewEntry(scope, "view-other"), { wrapper });

    await waitFor(() =>
      expect(useActiveIssueViewStore.getState().active["ws-1:workspace"]).toBeUndefined(),
    );
  });

  it("does not reopen startup during a dismissed visit", async () => {
    useIssueStartupViewPrefsStore
      .getState()
      .setStartupView("user-1", "ws-1:workspace", "view-valid");
    dismissIssueViewVisit("ws-1:workspace");

    renderHook(() => useIssueViewEntry(scope, null), { wrapper });

    await waitFor(() =>
      expect(useActiveIssueViewStore.getState().active["ws-1:workspace"]).toBeUndefined(),
    );
  });

  it("clears an invalid saved startup id after the view list settles", async () => {
    useIssueStartupViewPrefsStore
      .getState()
      .setStartupView("user-1", "ws-1:workspace", "view-gone");

    renderHook(() => useIssueViewEntry(scope, null), { wrapper });

    await waitFor(() =>
      expect(readStartupViewId("user-1", "ws-1:workspace")).toBeNull(),
    );
  });

  it("keeps the saved preference when the scoped views request fails", async () => {
    listQuery.mockRejectedValueOnce(new Error("views temporarily unavailable"));
    useIssueStartupViewPrefsStore.getState().setStartupView("user-1", "ws-1:workspace", "view-valid");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderHook(() => useIssueViewEntry(scope, null), {
      wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
    });

    await waitFor(() => expect(client.getQueryState(issueViewKeys.list("ws-1", scope))?.status).toBe("error"));
    expect(readStartupViewId("user-1", "ws-1:workspace")).toBe("view-valid");
    expect(useActiveIssueViewStore.getState().active["ws-1:workspace"]).toBeUndefined();
  });
});
