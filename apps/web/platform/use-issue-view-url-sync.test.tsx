// @vitest-environment jsdom
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setCurrentWorkspace } from "@multica/core/platform";
import {
  issueViewContainerKey,
  useActiveIssueViewStore,
} from "@multica/core/issue-views/active-view-store";

const searchParams = vi.hoisted(() => new URLSearchParams());

vi.mock("next/navigation", () => ({
  useSearchParams: () => searchParams,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/issue-views/use-issue-view-entry", () => ({
  useIssueViewEntry: vi.fn(),
}));

import { useIssueViewUrlSync } from "./use-issue-view-url-sync";

const scope = { scope_type: "workspace" as const };
const KEY = issueViewContainerKey("ws-1", scope);

beforeEach(() => {
  searchParams.delete("view");
  setCurrentWorkspace("acme", "ws-1");
  useActiveIssueViewStore.setState({ active: {} });
  window.history.replaceState(null, "", "/acme/issues");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("useIssueViewUrlSync", () => {
  it("adopts ?view= on mount and follows search-param changes on a mounted page", async () => {
    const { rerender } = renderHook(() => useIssueViewUrlSync(scope));
    expect(useActiveIssueViewStore.getState().active[KEY]).toBeUndefined();

    searchParams.set("view", "view-a");
    rerender();

    await waitFor(() =>
      expect(useActiveIssueViewStore.getState().active[KEY]).toBe("view-a"),
    );

    searchParams.delete("view");
    rerender();

    await waitFor(() =>
      expect(useActiveIssueViewStore.getState().active[KEY]).toBeUndefined(),
    );
  });

  it("writes the active view back to the location without navigation", async () => {
    const replaceSpy = vi.spyOn(window.history, "replaceState");
    const { rerender } = renderHook(() => useIssueViewUrlSync(scope));

    act(() => {
      useActiveIssueViewStore.getState().setActive(KEY, "view-b");
    });
    rerender();

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalled();
      expect(new URL(window.location.href).searchParams.get("view")).toBe(
        "view-b",
      );
    });
  });
});