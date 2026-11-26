"use client";

import { useState, type ReactElement, type ReactNode } from "react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { matchesPinyin } from "../../../editor/extensions/pinyin-match";
import { useStatusLabel } from "../../utils/status-label";
import { StatusIcon } from "../status-icon";
import { PickerEmpty, PickerItem, PropertyPicker } from "./property-picker";

export function StatusPropertyDisplay({ statusKey }: { statusKey: string }) {
  const wsId = useWorkspaceId();
  const { entryOf } = useIssueStatuses(wsId);
  const label = useStatusLabel(wsId);
  const entry = entryOf(statusKey);
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <StatusIcon
        status={statusKey}
        category={entry?.category}
        color={entry?.color}
        icon={entry?.icon}
        className="size-3.5 shrink-0"
      />
      <span className="truncate">{label(statusKey)}</span>
    </span>
  );
}

export function StatusPropertyPicker({
  open,
  onOpenChange,
  value,
  onSelect,
  trigger,
  triggerRender,
  emptyRow,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  value?: string;
  onSelect: (key: string) => void;
  trigger: ReactNode;
  triggerRender?: ReactElement<Record<string, unknown>>;
  emptyRow: ReactNode;
}) {
  const wsId = useWorkspaceId();
  const { activeStatuses } = useIssueStatuses(wsId);
  const label = useStatusLabel(wsId);
  const [search, setSearch] = useState("");
  const query = search.trim().toLowerCase();
  const options = activeStatuses.filter(({ key }) => {
    const name = label(key);
    return key.includes(query) || name.toLowerCase().includes(query) || matchesPinyin(name, query);
  });

  return (
    <PropertyPicker
      open={open}
      onOpenChange={onOpenChange}
      align="start"
      searchable={activeStatuses.length > 7}
      onSearchChange={setSearch}
      trigger={trigger}
      triggerRender={triggerRender}
    >
      {emptyRow}
      {options.map(({ key, category, color, icon }) => (
        <PickerItem
          key={key}
          selected={value === key}
          onClick={() => {
            onSelect(key);
            onOpenChange(false);
          }}
        >
          <StatusIcon status={key} category={category} color={color} icon={icon} className="size-3.5 shrink-0" />
          <span className="truncate">{label(key)}</span>
        </PickerItem>
      ))}
      {options.length === 0 && <PickerEmpty />}
    </PropertyPicker>
  );
}
