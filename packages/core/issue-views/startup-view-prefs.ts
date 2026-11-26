"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";
import type { IssueView } from "../api/schemas";

export const ISSUE_STARTUP_VIEW_STORAGE_KEY = "multica_issue_startup_views";

interface StartupViewPrefsState {
  /** userId → containerKey → saved view id */
  byUser: Record<string, Record<string, string>>;
  setStartupView: (userId: string, containerKey: string, viewId: string) => void;
  clearStartupView: (userId: string, containerKey: string) => void;
}

export const useIssueStartupViewPrefsStore = create<StartupViewPrefsState>()(
  persist(
    (set) => ({
      byUser: {},
      setStartupView: (userId, containerKey, viewId) =>
        set((state) => ({
          byUser: {
            ...state.byUser,
            [userId]: { ...state.byUser[userId], [containerKey]: viewId },
          },
        })),
      clearStartupView: (userId, containerKey) =>
        set((state) => {
          const user = state.byUser[userId];
          if (!user?.[containerKey]) return state;
          const { [containerKey]: _removed, ...rest } = user;
          const nextUser = Object.keys(rest).length > 0 ? rest : undefined;
          const byUser = { ...state.byUser };
          if (nextUser) byUser[userId] = nextUser;
          else delete byUser[userId];
          return { byUser };
        }),
    }),
    {
      name: ISSUE_STARTUP_VIEW_STORAGE_KEY,
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: ({ byUser }) => ({ byUser }),
      merge: (persisted, current) => ({
        ...current,
        byUser:
          persisted && typeof persisted === "object" && "byUser" in persisted
            ? ((persisted as StartupViewPrefsState).byUser ?? {})
            : {},
      }),
    },
  ),
);

registerForWorkspaceRehydration(() => {
  void useIssueStartupViewPrefsStore.persist.rehydrate();
});

export function readStartupViewId(userId: string | undefined, containerKey: string): string | null {
  if (!userId) return null;
  return useIssueStartupViewPrefsStore.getState().byUser[userId]?.[containerKey] ?? null;
}

export function resolveStartupViewId(
  viewId: string | null,
  views: readonly IssueView[],
  viewsReady: boolean,
): string | null {
  if (!viewId || !viewsReady) return null;
  return views.some((view) => view.id === viewId) ? viewId : null;
}