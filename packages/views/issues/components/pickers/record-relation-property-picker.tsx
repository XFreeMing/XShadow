"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { IssueProperty, IssuePropertyValue } from "@multica/core/types";
import { MAX_ISSUE_PROPERTY_SELECT_VALUES } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { genericRecordOptionsQuery, genericTableListOptions } from "@multica/core/tables";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";
import { PropertyPicker, PickerItem, PickerEmpty } from "./property-picker";
import { nextRecordRelationAfterToggle, visibleRecordRelationLabels } from "./record-relation-selection";

function recordIdsFromValue(value: IssuePropertyValue | undefined): string[] {
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === "string") : [];
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
  const optionsQuery = useQuery({
    ...genericRecordOptionsQuery(wsId, targetTableId),
    enabled: Boolean(wsId && targetTableId && ids.length > 0),
  });
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
  open,
  onOpenChange,
  trigger,
  triggerRender,
}: {
  property: IssueProperty;
  value: IssuePropertyValue | undefined;
  onChange: (value: IssuePropertyValue | undefined) => void | Promise<void>;
  required?: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  trigger: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const targetTableId = property.config.target_table_id ?? "";
  const stored = recordIdsFromValue(value);
  const [search, setSearch] = useState("");
  const [pending, setPending] = useState(false);
  const [toggleError, setToggleError] = useState<string | null>(null);
  const writePending = useRef(false);
  const optionsQuery = useQuery({
    ...genericRecordOptionsQuery(wsId, targetTableId),
    enabled: Boolean(wsId && targetTableId && open),
    refetchOnMount: "always",
  });
  const { refetch, isFetching, isSuccess: optionsLoaded, isLoading, isError, data: optionData } =
    optionsQuery;

  useEffect(() => {
    if (open && targetTableId) {
      setSearch("");
      setToggleError(null);
      void refetch();
    }
  }, [open, refetch, targetTableId]);

  const availableIds = useMemo(() => {
    if (!optionsLoaded) return new Set<string>();
    return new Set((optionData?.records ?? []).map((row) => row.id.toLowerCase()));
  }, [optionData?.records, optionsLoaded]);

  const draftSelected = useMemo(() => {
    if (!optionsLoaded) return stored;
    return stored.filter((id) => availableIds.has(id.toLowerCase()));
  }, [availableIds, optionsLoaded, stored]);

  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    const rows = optionData?.records ?? [];
    if (!needle) return rows;
    return rows.filter((row) => row.label.toLowerCase().includes(needle));
  }, [optionData?.records, search]);

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
      ) : !targetTableId ? (
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
        disabled={disabled || isLoading}
        onChange={(event) => onChange(event.target.value)}
        aria-label={tSettings(($) => $.properties.editor.record_relation_select_target_table)}
      >
        <option value="">{tSettings(($) => $.properties.editor.record_relation_select_target_table)}</option>
        {tables.map((table) => (
          <option key={table.id} value={table.id}>
            {table.name}
          </option>
        ))}
      </select>
      {!value ? (
        <p className="text-caption text-muted-foreground">
          {tSettings(($) => $.properties.editor.record_relation_table_required)}
        </p>
      ) : null}
    </div>
  );
}
