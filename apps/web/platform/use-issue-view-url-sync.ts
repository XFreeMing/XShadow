"use client";

import { useEffect, useRef } from "react";
import { useSearchParams } from "next/navigation";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  issueViewContainerKey,
  useActiveIssueViewStore,
} from "@multica/core/issue-views/active-view-store";
import type { IssueViewScope } from "@multica/core/issue-views/queries";
import { useIssueViewEntry } from "@multica/core/issue-views/use-issue-view-entry";

/**
 * Web-only two-way sync between the active saved view and `?view=<id>`:
 * shareable links in, durable reloads out. Next.js navigation belongs to
 * the platform layer, so this lives here and page wrappers mount it beside
 * the shared page component.
 *
 * The URL wins on mount and on every later search-param change; afterwards
 * the store drives the URL via history.replaceState — no navigation, no scroll
 * reset, no re-render of the route tree.
 */
export function useIssueViewUrlSync(scope: IssueViewScope) {
  const wsId = useWorkspaceId();
  const searchParams = useSearchParams();
  const urlViewId = searchParams.get("view");
  useIssueViewEntry(scope, urlViewId);
  const containerKey = issueViewContainerKey(wsId, scope);
  const setActive = useActiveIssueViewStore((s) => s.setActive);
  const activeViewId = useActiveIssueViewStore(
    (s) => s.active[containerKey] ?? null,
  );

  const seenUrlView = useRef<string | null | undefined>(undefined);
  useEffect(() => {
    const mounting = seenUrlView.current === undefined;
    if (!mounting && urlViewId === seenUrlView.current) return;
    seenUrlView.current = urlViewId;
    if (!mounting || urlViewId) setActive(containerKey, urlViewId);
  }, [containerKey, setActive, urlViewId]);

  // Store → URL. replaceState keeps back-button history clean: opening and
  // closing views is view state, not navigation.
  useEffect(() => {
    if (seenUrlView.current === undefined) return;
    const url = new URL(window.location.href);
    const current = url.searchParams.get("view");
    if ((activeViewId ?? null) === (current ?? null)) return;
    if (activeViewId) url.searchParams.set("view", activeViewId);
    else url.searchParams.delete("view");
    window.history.replaceState(window.history.state, "", url.toString());
  }, [activeViewId]);
}