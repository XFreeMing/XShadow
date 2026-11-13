"use client";

import { useState } from "react";
import { FolderKanban } from "@multica/ui/icons";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions } from "@multica/core/projects/queries";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { ProjectIcon } from "../../projects/components/project-icon";
import { useT } from "../../i18n";
import {
  PickerEmpty,
  PickerItem,
  PropertyPicker,
} from "../../issues/components/pickers/property-picker";

/**
 * The goal's project. "Workspace" (null) is a real choice: such a goal is
 * visible to everyone in the workspace, and only issues without a project can
 * be linked to it.
 */
export function GoalProjectPicker({
  projectId,
  onChange,
  disabled = false,
  align = "start",
}: {
  projectId: string | null;
  onChange: (next: string | null) => void;
  disabled?: boolean;
  align?: "start" | "center" | "end";
}) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const current = projectId ? projects.find((p) => p.id === projectId) : null;

  const query = filter.trim().toLowerCase();
  const filtered = projects.filter(
    (p) => p.title.toLowerCase().includes(query) || matchesPinyin(p.title, query),
  );

  return (
    <PropertyPicker
      open={disabled ? false : open}
      onOpenChange={(next) => {
        if (disabled) return;
        setOpen(next);
        if (!next) setFilter("");
      }}
      width="w-56"
      align={align}
      searchable
      searchPlaceholder={t(($) => $.form.project)}
      onSearchChange={setFilter}
      trigger={
        current ? (
          <>
            <ProjectIcon project={current} size="sm" />
            <span className="truncate">{current.title}</span>
          </>
        ) : (
          <>
            <FolderKanban className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <span className="truncate text-muted-foreground">{t(($) => $.form.no_project)}</span>
          </>
        )
      }
    >
      <PickerItem
        emptyValue
        selected={!projectId}
        onClick={() => {
          onChange(null);
          setOpen(false);
        }}
      >
        <FolderKanban className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-muted-foreground">{t(($) => $.form.no_project)}</span>
      </PickerItem>

      {filtered.map((p) => (
        <PickerItem
          key={p.id}
          selected={p.id === projectId}
          truncationTooltip={p.title}
          onClick={() => {
            onChange(p.id);
            setOpen(false);
          }}
        >
          <ProjectIcon project={p} size="sm" />
          <span className="truncate">{p.title}</span>
        </PickerItem>
      ))}

      {projects.length > 0 && filtered.length === 0 && query && <PickerEmpty />}
    </PropertyPicker>
  );
}