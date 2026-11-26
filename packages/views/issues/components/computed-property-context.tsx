"use client";

import { createContext, useContext, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { issueComputedPropertiesOptions } from "@multica/core/properties";
import type { IssueComputedPropertyBag, IssueComputedPropertyValue } from "@multica/core/types";

type ComputedPropertiesByIssue = Record<string, IssueComputedPropertyBag>;
const EMPTY_PROPERTIES: ComputedPropertiesByIssue = {};

const ComputedIssuePropertyContext = createContext<ComputedPropertiesByIssue>(
  EMPTY_PROPERTIES,
);

export function ComputedIssuePropertyProvider({
  wsId,
  issueIds,
  children,
}: {
  wsId: string;
  issueIds: readonly string[];
  children: ReactNode;
}) {
  const { data } = useQuery(issueComputedPropertiesOptions(wsId, issueIds));
  return (
    <ComputedIssuePropertyContext.Provider value={data ?? EMPTY_PROPERTIES}>
      {children}
    </ComputedIssuePropertyContext.Provider>
  );
}

export function useComputedIssuePropertyValue(
  issueId: string,
  propertyId: string,
): IssueComputedPropertyValue | undefined {
  const bags = useContext(ComputedIssuePropertyContext);
  return bags[issueId]?.[propertyId];
}
