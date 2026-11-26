"use client";

import { useMemo, type ReactNode } from "react";
import type { IssueProperty } from "@multica/core/types";
import { cardPropertyIdsNeedComputedFetch } from "../utils/card-custom-properties";
import { ComputedIssuePropertyProvider } from "./computed-property-context";

export function ComputedPropertyViewBoundary({
  wsId,
  issueIds,
  cardPropertyIds,
  workspaceProperties,
  children,
}: {
  wsId: string;
  issueIds: readonly string[];
  cardPropertyIds: readonly string[];
  workspaceProperties: readonly IssueProperty[];
  children: ReactNode;
}) {
  const needsComputed = useMemo(
    () => cardPropertyIdsNeedComputedFetch(cardPropertyIds, workspaceProperties),
    [cardPropertyIds, workspaceProperties],
  );
  if (!needsComputed || issueIds.length === 0) {
    return children;
  }
  return (
    <ComputedIssuePropertyProvider wsId={wsId} issueIds={issueIds}>
      {children}
    </ComputedIssuePropertyProvider>
  );
}