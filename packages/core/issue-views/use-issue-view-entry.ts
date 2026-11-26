"use client";

import { useEffect, useRef, useState } from "react";
import { useAuthStore } from "../auth";
import { useWorkspaceId } from "../hooks";
import { issueViewContainerKey } from "./active-view-store";
import type { IssueViewScope } from "./queries";
import { useActiveIssueView } from "./use-active-view";
import {
  clearIssueViewVisitDismiss,
  dismissIssueViewVisit,
  isIssueViewVisitDismissed,
} from "./visit-dismiss";
import {
  resolveStartupViewId,
  useIssueStartupViewPrefsStore,
} from "./startup-view-prefs";

/**
 * After URL `?view=` sync, open the user's saved startup view on an ordinary
 * issues entry. Ephemeral navigation only — explicit URL always wins.
 */
export function useIssueViewEntry(scope: IssueViewScope, urlViewId: string | null) {
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id);
  const containerKey = issueViewContainerKey(wsId, scope);
  const { activeViewId, views, viewsSettled, setActive } = useActiveIssueView(wsId, scope);
  const startupViewId = useIssueStartupViewPrefsStore((s) =>
    userId ? s.byUser[userId]?.[containerKey] : undefined,
  );
  const clearStartupView = useIssueStartupViewPrefsStore((s) => s.clearStartupView);
  const [prefsHydrated, setPrefsHydrated] = useState(() =>
    useIssueStartupViewPrefsStore.persist.hasHydrated(),
  );
  const handledStartupId = useRef<string | undefined>(undefined);

  useEffect(() => {
    const finish = useIssueStartupViewPrefsStore.persist.onFinishHydration(() => {
      setPrefsHydrated(true);
    });
    if (useIssueStartupViewPrefsStore.persist.hasHydrated()) {
      setPrefsHydrated(true);
    }
    return finish;
  }, []);

  useEffect(() => {
    handledStartupId.current = undefined;
    return () => clearIssueViewVisitDismiss(containerKey);
  }, [containerKey, userId]);

  useEffect(() => {
    if (urlViewId) return;
    if (isIssueViewVisitDismissed(containerKey)) return;
    if (activeViewId) return;
    if (!prefsHydrated) return;
    if (!startupViewId) return;
    if (handledStartupId.current === startupViewId) return;
    if (!viewsSettled) return;

    const resolved = resolveStartupViewId(startupViewId, views, true);
    if (!resolved) {
      if (userId) clearStartupView(userId, containerKey);
      handledStartupId.current = startupViewId;
      return;
    }
    setActive(resolved);
    handledStartupId.current = startupViewId;
  }, [
    activeViewId,
    clearStartupView,
    containerKey,
    prefsHydrated,
    setActive,
    startupViewId,
    urlViewId,
    userId,
    views,
    viewsSettled,
  ]);
}

/** Back to the built-in list from a saved view without reopening startup this visit. */
export function leaveSavedIssueViewForVisit(containerKey: string, setActive: (id: string | null) => void) {
  dismissIssueViewVisit(containerKey);
  setActive(null);
}
