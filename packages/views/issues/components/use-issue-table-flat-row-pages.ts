"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { hashKey, useQueries } from "@tanstack/react-query";
import { issueTableRowPageOptions } from "@multica/core/issues/queries";
import type { Issue, IssueTableQuerySpec } from "@multica/core/types";

const INITIAL_CURSORS: Array<string | null> = [null];

export function useIssueTableFlatRowPages(
  wsId: string,
  serverQuery: IssueTableQuerySpec,
  pageLimit: number,
) {
  const queryIdentity = useMemo(
    () => hashKey([wsId, serverQuery]),
    [serverQuery, wsId],
  );
  const [cursorState, setCursorState] = useState<{
    identity: string;
    cursors: Array<string | null>;
  }>(() => ({ identity: queryIdentity, cursors: INITIAL_CURSORS }));
  const cursors =
    cursorState.identity === queryIdentity
      ? cursorState.cursors
      : INITIAL_CURSORS;

  const pageQueries = useQueries({
    queries: cursors.map((cursor) => ({
      ...issueTableRowPageOptions(wsId, {
        query: serverQuery,
        group: { kind: "none" },
        group_key: null,
        hierarchy: { enabled: false },
        parent_id: null,
        page: { limit: pageLimit, cursor },
      }),
      placeholderData: undefined,
    })),
  });

  const flatPageState = useMemo(() => {
    const deduped: Issue[] = [];
    const seen = new Set<string>();
    let resolvedTotal = 0;
    let resolvedNextCursor: string | null = null;
    let prefixLength = 1;
    let tail: (typeof pageQueries)[number] | undefined;

    for (let i = 0; i < pageQueries.length; i += 1) {
      const query = pageQueries[i];
      if (!query) break;
      if (i > 0) {
        if (cursors[i] !== resolvedNextCursor) break;
        prefixLength = i + 1;
        tail = query;
      }
      const data = query.data;
      if (!data) break;
      if (i === 0) resolvedTotal = data.total;
      for (const row of data.rows) {
        if (seen.has(row.issue.id)) continue;
        seen.add(row.issue.id);
        deduped.push(row.issue);
      }
      resolvedNextCursor = data.next_cursor;
    }

    return {
      issues: deduped,
      total: resolvedTotal,
      nextCursor: resolvedNextCursor,
      validPrefixLength: prefixLength,
      activeTailQuery: tail,
    };
  }, [cursors, pageQueries]);

  const headQuery = pageQueries[0];
  const isHeadLoading = Boolean(headQuery?.isPending && !headQuery.data);
  const isHeadError = Boolean(headQuery?.isError && !headQuery.data);
  const isTailLoading = Boolean(
    flatPageState.activeTailQuery &&
      (flatPageState.activeTailQuery.isPending ||
        flatPageState.activeTailQuery.isFetching) &&
      !flatPageState.activeTailQuery.data,
  );
  const isTailError = Boolean(
    flatPageState.activeTailQuery &&
      flatPageState.activeTailQuery.isError &&
      !flatPageState.activeTailQuery.data,
  );

  const validCursors = cursors.slice(0, flatPageState.validPrefixLength);
  const canLoadMore = Boolean(
    flatPageState.nextCursor &&
      !validCursors.includes(flatPageState.nextCursor) &&
      !isTailLoading &&
      !isTailError,
  );

  const hasStaleTail = flatPageState.validPrefixLength < cursors.length;
  useEffect(() => {
    if (!hasStaleTail) return;
    setCursorState((prev) => {
      if (
        prev.identity !== queryIdentity ||
        prev.cursors.length <= flatPageState.validPrefixLength
      ) {
        return prev;
      }
      return {
        identity: queryIdentity,
        cursors: prev.cursors.slice(0, flatPageState.validPrefixLength),
      };
    });
  }, [flatPageState.validPrefixLength, hasStaleTail, queryIdentity]);

  const handleLoadMore = useCallback(() => {
    const nextCursor = flatPageState.nextCursor;
    if (!nextCursor) return;
    setCursorState((prev) => {
      const current =
        prev.identity === queryIdentity
          ? prev.cursors.slice(0, flatPageState.validPrefixLength)
          : [null];
      if (current.includes(nextCursor)) return prev;
      return { identity: queryIdentity, cursors: [...current, nextCursor] };
    });
  }, [flatPageState.nextCursor, flatPageState.validPrefixLength, queryIdentity]);

  return {
    issues: flatPageState.issues,
    total: flatPageState.total,
    isHeadLoading,
    isHeadError,
    isTailLoading,
    isTailError,
    canLoadMore,
    handleLoadMore,
    retryHead: () => void headQuery?.refetch(),
    retryTail: () => void flatPageState.activeTailQuery?.refetch(),
  };
}
