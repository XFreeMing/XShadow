// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import {
  ISSUE_STARTUP_VIEW_STORAGE_KEY,
  readStartupViewId,
  resolveStartupViewId,
  useIssueStartupViewPrefsStore,
} from "./startup-view-prefs";
import type { IssueView } from "../api/schemas";

const view = (id: string): IssueView =>
  ({
    id,
    name: id,
    scope_type: "workspace",
    query: {},
    display: {},
  }) as IssueView;

beforeEach(() => {
  localStorage.clear();
  setCurrentWorkspace("acme", "ws-1");
  useIssueStartupViewPrefsStore.setState({ byUser: {} });
});

describe("startup view prefs", () => {
  it("persists per user and container within the workspace namespace", () => {
    const { setStartupView, clearStartupView } = useIssueStartupViewPrefsStore.getState();
    setStartupView("user-a", "ws-1:workspace", "view-1");
    setStartupView("user-b", "ws-1:workspace", "view-2");

    expect(readStartupViewId("user-a", "ws-1:workspace")).toBe("view-1");
    expect(readStartupViewId("user-b", "ws-1:workspace")).toBe("view-2");

    clearStartupView("user-a", "ws-1:workspace");
    expect(readStartupViewId("user-a", "ws-1:workspace")).toBeNull();
    expect(readStartupViewId("user-b", "ws-1:workspace")).toBe("view-2");

    const saved = localStorage.getItem(`${ISSUE_STARTUP_VIEW_STORAGE_KEY}:acme`);
    expect(saved).toContain("view-2");
  });

  it("treats a pending list as non-proof of deletion", () => {
    expect(resolveStartupViewId("view-missing", [], false)).toBeNull();
    expect(resolveStartupViewId("view-missing", [], true)).toBeNull();
    expect(resolveStartupViewId("view-1", [view("view-1")], true)).toBe("view-1");
  });
});