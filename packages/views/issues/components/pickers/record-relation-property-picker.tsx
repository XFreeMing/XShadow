"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import type { IssueProperty, IssuePropertyValue } from "@multica/core/types";
import { MAX_ISSUE_PROPERTY_SELECT_VALUES } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { genericRecordOptionsQuery, genericTableListOptions } from "@multica/core/tables";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";
import { PropertyPicker, PickerItem, PickerEmpty } from "./property-picker";
import { nextRecordRelationAfterToggle, visibleRecordRelationLabels } from "./record-relation-selection";

export const NATIVE_ISSUE_RELATION_TARGET = "issues";

function recordIdsFromValue(value: IssuePropertyValue | undefined): string[] {
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === "string") : [];
}

function useNativeIssueRelationOptions(
  wsId: string,
  ids: string[],
  search: string,
  enabled: boolean,
  includeCandidates: boolean,
) {
  const queryIds = [...ids].sort();
  const { data: projects, isError: projectsError, isPending: projectsPending, refetch: refetchProjects } = useQuery({
    ...projectListOptions(wsId),
    enabled,
  });
  const result = useQuery({
    queryKey: [...issueKeys.list(wsId), "native-relation", queryIds, search, includeCandidates],
    enabled,
    queryFn: async ({ signal }) => {
      const [selected, candidates] = await Promise.all([
        queryIds.length ? api.listIssues({ ids: queryIds, limit: 50 }) : null,
        !includeCandidates ? null : search
          ? api.searchIssues({ q: search, limit: 30, include_closed: true, signal })
          : api.listIssues({ limit: 30 }),
      ]);
      return [...new Map(
        [...(selected?.issues ?? []), ...(candidates?.issues ?? [])].map((issue) => [issue.id, issue]),
      ).values()];
    },
  });
  const records = useMemo(() => {
    const publicProjects = new Set(projects?.filter((project) => project.visibility !== "private").map((project) => project.id));
    return (result.data ?? [])
      .filter((issue) => !issue.project_id || publicProjects.has(issue.project_id))
      .map((issue) => ({ id: issue.id, label: `${issue.identifier} · ${issue.title}` }));
  }, [projects, result.data]);
  return {
    data: { records },
    isFetching: result.isFetching,
    isLoading: result.isLoading || (enabled && projectsPending),
    isSuccess: result.isSuccess && !projectsPending && !projectsError,
    isError: result.isError || projectsError,
    refetch: () => Promise.all([result.refetch(), refetchProjects()]),
  };
}

export function RecordRelationPropertyDisplay({
  property,
  value,
}: {
  property: IssueProperty;
  value: IssuePropertyValue | undefined;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const targetTableId = property.config.target_table_id ?? "";
  const ids = recordIdsFromValue(value);
  const native = property.config.target_kind === NATIVE_ISSUE_RELATION_TARGET;
  const genericOptions = useQuery({
    ...genericRecordOptionsQuery(wsId, targetTableId),
    enabled: Boolean(wsId && targetTableId && ids.length > 0),
  });
  const nativeOptions = useNativeIssueRelationOptions(wsId, ids, "", Boolean(wsId && native && ids.length), false);
  const optionsQuery = native ? nativeOptions : genericOptions;
  const labelById = useMemo(() => {
    const map = new Map<string, string>();
    for (const row of optionsQuery.data?.records ?? []) {
      map.set(row.id.toLowerCase(), row.label);
    }
    return map;
  }, [optionsQuery.data?.records]);

  if (ids.length === 0) {
    return (
      <span className="text-muted-foreground">
        {t(($) => $.pickers.custom_property.empty)}
      </span>
    );
  }
  const visible = visibleRecordRelationLabels(ids, labelById);
  if (visible.length === 0) {
    return (
      <span className="text-muted-foreground">
        {t(($) => $.pickers.custom_property.record_relation_stale)}
      </span>
    );
  }
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-1">
      {visible.map((entry) => (
        <span
          key={entry.id}
          className="inline-flex max-w-48 items-center rounded-full border border-surface-border px-1.5 py-px text-micro"
        >
          <span className="truncate">{entry.label}</span>
        </span>
      ))}
    </span>
  );
}

export function RecordRelationPropertyPicker({
  property,
  value,
  onChange,
  required = false,
  issueId,
  open,
  onOpenChange,
  trigger,
  triggerRender,
}: {
  property: IssueProperty;
  value: IssuePropertyValue | undefined;
  onChange: (value: IssuePropertyValue | undefined) => void | Promise<void>;
  required?: boolean;
  issueId?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  trigger: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const targetTableId = property.config.target_table_id ?? "";
  const native = property.config.target_kind === NATIVE_ISSUE_RELATION_TARGET;
  const stored = recordIdsFromValue(value);
  const [search, setSearch] = useState("");
  const [remoteSearch, setRemoteSearch] = useState("");
  const [pending, setPending] = useState(false);
  const [toggleError, setToggleError] = useState<string | null>(null);
  const writePending = useRef(false);
  useEffect(() => {
    if (!open || !native) return;
    const timer = setTimeout(() => setRemoteSearch(search.trim()), 180);
    return () => clearTimeout(timer);
  }, [native, open, search]);
  const genericOptions = useQuery({
    ...genericRecordOptionsQuery(wsId, targetTableId),
    enabled: Boolean(wsId && targetTableId && open),
    refetchOnMount: "always",
  });
  const nativeOptions = useNativeIssueRelationOptions(
    wsId, stored, remoteSearch, Boolean(wsId && native && open), true,
  );
  const refetchGeneric = genericOptions.refetch;
  const { refetch, isFetching, isSuccess: optionsLoaded, isLoading, isError, data: optionData } =
    native ? nativeOptions : genericOptions;

  useEffect(() => {
    if (open && (native || targetTableId)) {
      setSearch("");
      setRemoteSearch("");
      setToggleError(null);
      if (!native) void refetchGeneric();
    }
  }, [native, open, refetchGeneric, targetTableId]);

  const availableIds = useMemo(() => {
    if (!optionsLoaded) return new Set<string>();
    return new Set((optionData?.records ?? []).filter((row) => row.id !== issueId).map((row) => row.id.toLowerCase()));
  }, [issueId, optionData?.records, optionsLoaded]);

  const draftSelected = useMemo(() => {
    if (!optionsLoaded) return stored;
    return stored.filter((id) => availableIds.has(id.toLowerCase()));
  }, [availableIds, optionsLoaded, stored]);

  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    const rows = (optionData?.records ?? []).filter((row) => row.id !== issueId);
    if (!needle || native) return rows;
    return rows.filter((row) => row.label.toLowerCase().includes(needle));
  }, [issueId, native, optionData?.records, search]);

  const commit = useCallback(
    async (next: string[], close = false) => {
      if (writePending.current || (required && next.length === 0)) return;
      writePending.current = true;
      setPending(true);
      setToggleError(null);
      try {
        await onChange(next.length === 0 ? undefined : next);
        if (close) onOpenChange(false);
      } catch (error) {
        setToggleError(error instanceof Error ? error.message : String(error));
      } finally {
        writePending.current = false;
        setPending(false);
      }
    },
    [onChange, onOpenChange, required],
  );

  const toggle = (id: string) => {
    if (isFetching || !optionsLoaded) return;
    const next = nextRecordRelationAfterToggle(stored, id, availableIds, MAX_ISSUE_PROPERTY_SELECT_VALUES);
    void commit(next);
  };

  const atCapacity =
    draftSelected.length >= MAX_ISSUE_PROPERTY_SELECT_VALUES;

  return (
    <PropertyPicker
      open={open}
      onOpenChange={onOpenChange}
      align="start"
      searchable
      onSearchChange={setSearch}
      trigger={trigger}
      triggerRender={triggerRender}
    >
      {!required && (
        <PickerItem emptyValue selected={stored.length === 0} disabled={pending} onClick={() => void commit([], true)}>
          <span className="text-muted-foreground">{t(($) => $.pickers.custom_property.none)}</span>
        </PickerItem>
      )}
      {isLoading || isFetching ? (
        <p className="px-2 py-1.5 text-caption text-muted-foreground">
          {t(($) => $.pickers.custom_property.record_relation_loading)}
        </p>
      ) : isError ? (
        <div className="flex flex-col gap-2 px-2 py-1.5">
          <p className="text-caption text-destructive" role="alert">
            {t(($) => $.pickers.custom_property.record_relation_load_failed)}
          </p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={isFetching}
            onClick={() => void refetch()}
          >
            {t(($) => $.pickers.custom_property.record_relation_retry)}
          </Button>
        </div>
      ) : !native && !targetTableId ? (
        <p className="px-2 py-1.5 text-caption text-muted-foreground">
          {t(($) => $.pickers.custom_property.record_relation_no_table)}
        </p>
      ) : filtered.length === 0 ? (
        <PickerEmpty />
      ) : (
        filtered.map((option) => {
          const checked = draftSelected.some((id) => id.toLowerCase() === option.id.toLowerCase());
          const disabled = pending || (atCapacity && !checked);
          return (
            <PickerItem
              key={option.id}
              selected={checked}
              disabled={disabled}
              onClick={() => toggle(option.id)}
            >
              <span className="min-w-0 flex-1 truncate">{option.label}</span>
            </PickerItem>
          );
        })
      )}
      {toggleError ? (
        <p className="px-2 py-1.5 text-caption text-destructive" role="alert">
          {toggleError}
        </p>
      ) : null}
    </PropertyPicker>
  );
}

/** Settings editor: pick the target generic table when creating record_relation. */
export function RecordRelationTargetTableSelect({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (tableId: string) => void;
  disabled?: boolean;
}) {
  const { t: tSettings } = useT("settings");
  const wsId = useWorkspaceId();
  const { data, isLoading } = useQuery(genericTableListOptions(wsId));
  const tables = data?.tables ?? [];
  return (
    <div className="space-y-2">
      <label className="text-body font-medium" htmlFor="property-target-table">
        {tSettings(($) => $.properties.editor.record_relation_target_table)}
      </label>
      <select
        id="property-target-table"
        className="h-9 w-full rounded-md border border-surface-border bg-background px-3 text-body disabled:opacity-50"
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        aria-label={tSettings(($) => $.properties.editor.record_relation_select_target_table)}
      >
        <option value="">{tSettings(($) => $.properties.editor.record_relation_select_target_table)}</option>
        <option value={NATIVE_ISSUE_RELATION_TARGET}>{tSettings(($) => $.properties.editor.record_relation_native_tasks)}</option>
        {tables.map((table) => (
          <option key={table.id} value={table.id}>
            {table.name}
          </option>
        ))}
      </select>
      {isLoading && <span role="status" className="text-caption text-muted-foreground">{tSettings(($) => $.properties.editor.record_relation_select_target_table)}</span>}
      {!value ? (
        <p className="text-caption text-muted-foreground">
          {tSettings(($) => $.properties.editor.record_relation_table_required)}
        </p>
      ) : null}
    </div>
  );
}
