"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  ArrowDown,
  ArrowUp,
  MoreHorizontal,
  Pencil,
  Plus,
  Shapes,
  X,
} from "@multica/ui/icons";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  issueTypeListOptions,
  useCreateIssueType,
  useUpdateIssueType,
} from "@multica/core/issue-types";
import type {
  IssueProperty,
  IssuePropertyValue,
  IssueType,
  IssueTypeField,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { IconButton } from "@multica/ui/components/common/icon-button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Input } from "@multica/ui/components/ui/input";
import { Label as FieldLabel } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { PropertyIcon } from "../../common/property-icon";
import {
  CustomPropertyValueDisplay,
  CustomPropertyValueInput,
} from "../../issues/components/pickers/custom-property-picker";
import { useT } from "../../i18n";

const MAX_ACTIVE_ISSUE_TYPES = 50;

export function IssueTypesSection({
  canManage,
  showArchived,
  properties,
}: {
  canManage: boolean;
  showArchived: boolean;
  properties: IssueProperty[];
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<IssueType | null>(null);

  const { data: issueTypes = [], isLoading } = useQuery(
    issueTypeListOptions(wsId, true),
  );
  const update = useUpdateIssueType();

  const propertyById = useMemo(
    () => new Map(properties.map((p) => [p.id, p])),
    [properties],
  );
  const activeProperties = useMemo(
    () => properties.filter((p) => !p.archived),
    [properties],
  );
  const activeCount = useMemo(
    () => issueTypes.filter((it) => !it.archived).length,
    [issueTypes],
  );
  const visible = useMemo(
    () => issueTypes.filter((it) => (showArchived ? true : !it.archived)),
    [issueTypes, showArchived],
  );

  return (
    <section id="task-types" className="mt-8 space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <h3 className="text-subheading font-medium">
            {t(($) => $.properties.issue_types.title)}
          </h3>
          <span className="text-caption tabular-nums text-muted-foreground">
            {activeCount}/{MAX_ACTIVE_ISSUE_TYPES}
          </span>
        </div>
        {canManage && (
          <Button
            size="sm"
            variant="outline"
            className="gap-1.5"
            onClick={() => setCreateOpen(true)}
            disabled={activeCount >= MAX_ACTIVE_ISSUE_TYPES}
          >
            <Plus className="size-3.5" />
            {t(($) => $.properties.issue_types.new_type)}
          </Button>
        )}
      </div>

      <div className="overflow-hidden rounded-lg border border-surface-border bg-card">
        {isLoading ? (
          <div className="px-4 py-8 text-center text-body text-muted-foreground">
            {t(($) => $.properties.loading)}
          </div>
        ) : visible.length === 0 ? (
          <div className="px-4 py-8 text-center">
            <Shapes className="mx-auto size-5 text-faint-foreground" />
            <p className="mt-2 text-body font-medium">
              {t(($) => $.properties.issue_types.empty)}
            </p>
          </div>
        ) : (
          <div className="divide-y divide-surface-border">
            {visible.map((issueType) => (
              <div
                key={issueType.id}
                className="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="min-w-0 space-y-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-body font-medium">
                      {issueType.name}
                    </span>
                    {issueType.archived && (
                      <Badge variant="outline" className="shrink-0 text-micro">
                        {t(($) => $.properties.archived_badge)}
                      </Badge>
                    )}
                  </div>
                  {issueType.fields.length > 0 && (
                    <div className="flex flex-wrap items-center gap-1.5">
                      {issueType.fields.map((field) => {
                        const prop = propertyById.get(field.property_id);
                        return (
                          <span
                            key={field.property_id}
                            className="inline-flex items-center gap-1 rounded-full border border-surface-border px-2 py-0.5 text-caption"
                          >
                            {prop && (
                              <PropertyIcon property={prop} className="size-3" />
                            )}
                            <span>{prop?.name ?? field.property_id}</span>
                            {field.required && (
                              <span
                                className="text-destructive"
                                aria-label={t(($) => $.properties.issue_types.required)}
                              >
                                *
                              </span>
                            )}
                            {prop && field.default_value !== undefined && (
                              <span className="text-muted-foreground">
                                =
                                <CustomPropertyValueDisplay
                                  property={prop}
                                  value={field.default_value}
                                />
                              </span>
                            )}
                          </span>
                        );
                      })}
                    </div>
                  )}
                </div>

                {canManage && (
                  <DropdownMenu>
                    <DropdownMenuTrigger
                      render={
                        <IconButton
                          variant="ghost"
                          size="icon-sm"
                          label={t(($) => $.properties.issue_types.actions_aria, {
                            name: issueType.name,
                          })}
                        >
                          <MoreHorizontal className="size-4" />
                        </IconButton>
                      }
                    />
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={() => setEditing(issueType)}>
                        <Pencil className="size-3.5" />
                        {t(($) => $.properties.issue_types.edit)}
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        onClick={() =>
                          update.mutate(
                            { id: issueType.id, archived: !issueType.archived },
                            {
                              onError: (err) =>
                                toast.error(
                                  err instanceof Error
                                    ? err.message
                                    : t(($) => $.properties.save_failed),
                                ),
                            },
                          )
                        }
                      >
                        {issueType.archived ? (
                          <>
                            <ArchiveRestore className="size-3.5" />
                            {t(($) => $.properties.issue_types.restore)}
                          </>
                        ) : (
                          <>
                            <Archive className="size-3.5" />
                            {t(($) => $.properties.issue_types.archive)}
                          </>
                        )}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                )}
              </div>
            ))}
          </div>
        )}
      </div>

      <IssueTypeEditorDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        activeProperties={activeProperties}
        propertyById={propertyById}
      />
      <IssueTypeEditorDialog
        open={Boolean(editing)}
        onOpenChange={(open) => !open && setEditing(null)}
        issueType={editing}
        activeProperties={activeProperties}
        propertyById={propertyById}
      />
    </section>
  );
}

function IssueTypeEditorDialog({
  open,
  onOpenChange,
  issueType,
  activeProperties,
  propertyById,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  issueType?: IssueType | null;
  activeProperties: IssueProperty[];
  propertyById: Map<string, IssueProperty>;
}) {
  const { t } = useT("settings");
  const create = useCreateIssueType();
  const update = useUpdateIssueType();
  const [name, setName] = useState("");
  const [fields, setFields] = useState<IssueTypeField[]>([]);

  useEffect(() => {
    if (!open) return;
    setName(issueType?.name ?? "");
    setFields(
      issueType
        ? issueType.fields.map((f) => ({
            property_id: f.property_id,
            required: f.required,
            ...(f.default_value !== undefined
              ? { default_value: f.default_value }
              : {}),
          }))
        : [],
    );
  }, [issueType, open]);

  const selectedIds = useMemo(
    () => new Set(fields.map((f) => f.property_id)),
    [fields],
  );
  const availableToAdd = useMemo(
    () => activeProperties.filter((p) => !selectedIds.has(p.id)),
    [activeProperties, selectedIds],
  );

  const canSubmit = name.trim().length > 0;

  const moveField = (index: number, delta: -1 | 1) => {
    setFields((current) => {
      const target = index + delta;
      if (target < 0 || target >= current.length) return current;
      const next = [...current];
      const [item] = next.splice(index, 1);
      if (!item) return current;
      next.splice(target, 0, item);
      return next;
    });
  };

  const setFieldDefault = (
    propertyId: string,
    defaultValue: IssuePropertyValue | undefined,
  ) => {
    setFields((current) =>
      current.map((field) => {
        if (field.property_id !== propertyId) return field;
        if (defaultValue === undefined) {
          const { default_value: _omit, ...rest } = field;
          return rest;
        }
        return { ...field, default_value: defaultValue };
      }),
    );
  };

  const submit = () => {
    if (!canSubmit) return;
    const payloadFields: IssueTypeField[] = fields.map((field) => ({
      property_id: field.property_id,
      required: field.required,
      ...(field.default_value !== undefined
        ? { default_value: field.default_value }
        : {}),
    }));
    const onError = (error: unknown) =>
      toast.error(
        error instanceof Error ? error.message : t(($) => $.properties.save_failed),
      );
    if (issueType) {
      update.mutate(
        { id: issueType.id, name: name.trim(), fields: payloadFields },
        { onSuccess: () => onOpenChange(false), onError },
      );
      return;
    }
    create.mutate(
      { name: name.trim(), fields: payloadFields },
      { onSuccess: () => onOpenChange(false), onError },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85dvh] flex-col sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {issueType
              ? t(($) => $.properties.issue_types.edit_title)
              : t(($) => $.properties.issue_types.create_title)}
          </DialogTitle>
        </DialogHeader>

        <div className="-mx-1 min-h-0 flex-1 space-y-5 overflow-y-auto px-1 py-2">
          <div className="space-y-2">
            <FieldLabel htmlFor="issue-type-name">
              {t(($) => $.properties.issue_types.name)}
            </FieldLabel>
            <Input
              id="issue-type-name"
              autoFocus
              maxLength={64}
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={t(($) => $.properties.issue_types.name_placeholder)}
            />
          </div>

          <div className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <FieldLabel>{t(($) => $.properties.issue_types.fields)}</FieldLabel>
              {availableToAdd.length > 0 && (
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <Button type="button" variant="outline" size="sm" className="gap-1.5">
                        <Plus className="size-3.5" />
                        {t(($) => $.properties.issue_types.add_field)}
                      </Button>
                    }
                  />
                  <DropdownMenuContent align="end" className="w-56">
                    {availableToAdd.map((prop) => (
                      <DropdownMenuItem
                        key={prop.id}
                        onClick={() =>
                          setFields((current) => [
                            ...current,
                            { property_id: prop.id, required: false },
                          ])
                        }
                      >
                        <PropertyIcon property={prop} className="size-3.5" />
                        <span className="truncate">{prop.name}</span>
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </div>

            {fields.length === 0 ? (
              <p className="rounded-md border border-dashed border-surface-border px-3 py-4 text-center text-caption text-muted-foreground">
                {t(($) => $.properties.issue_types.no_fields)}
              </p>
            ) : (
              <div className="space-y-2">
                {fields.map((field, index) => {
                  const prop = propertyById.get(field.property_id);
                  return (
                    <div
                      key={field.property_id}
                      className="flex flex-wrap items-center gap-2 rounded-md border border-surface-border px-2.5 py-2"
                    >
                      <div className="flex items-center gap-0.5">
                        <IconButton
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          label={t(($) => $.properties.issue_types.move_up)}
                          disabled={index === 0}
                          onClick={() => moveField(index, -1)}
                        >
                          <ArrowUp className="size-3.5" />
                        </IconButton>
                        <IconButton
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          label={t(($) => $.properties.issue_types.move_down)}
                          disabled={index === fields.length - 1}
                          onClick={() => moveField(index, 1)}
                        >
                          <ArrowDown className="size-3.5" />
                        </IconButton>
                      </div>

                      <div className="flex min-w-0 flex-1 items-center gap-1.5">
                        {prop && <PropertyIcon property={prop} className="size-3.5 shrink-0" />}
                        <span className="truncate text-body font-medium">
                          {prop?.name ?? field.property_id}
                        </span>
                      </div>

                      <label className="flex items-center gap-1.5 text-caption text-muted-foreground">
                        <Switch
                          size="sm"
                          checked={field.required}
                          onCheckedChange={(checked) =>
                            setFields((current) =>
                              current.map((item) =>
                                item.property_id === field.property_id
                                  ? { ...item, required: checked }
                                  : item,
                              ),
                            )
                          }
                        />
                        {t(($) => $.properties.issue_types.required)}
                      </label>

                      {prop && (
                        <div className="flex items-center gap-1">
                          <CustomPropertyValueInput
                            property={prop}
                            value={field.default_value}
                            onChange={(next) => setFieldDefault(field.property_id, next)}
                            trigger={
                              <span className="inline-flex items-center gap-1 rounded border border-surface-border px-2 py-0.5 text-caption">
                                <span className="text-muted-foreground">
                                  {t(($) => $.properties.issue_types.default_label)}:
                                </span>
                                {field.default_value !== undefined ? (
                                  <CustomPropertyValueDisplay
                                    property={prop}
                                    value={field.default_value}
                                  />
                                ) : (
                                  <span className="text-muted-foreground">
                                    {t(($) => $.properties.issue_types.no_default)}
                                  </span>
                                )}
                              </span>
                            }
                          />
                        </div>
                      )}

                      <IconButton
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        label={t(($) => $.properties.issue_types.remove_field)}
                        onClick={() =>
                          setFields((current) =>
                            current.filter((item) => item.property_id !== field.property_id),
                          )
                        }
                      >
                        <X className="size-3.5" />
                      </IconButton>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t(($) => $.properties.editor.cancel)}
          </Button>
          <Button
            onClick={submit}
            disabled={!canSubmit || create.isPending || update.isPending}
          >
            {create.isPending || update.isPending
              ? t(($) => $.workspace.saving)
              : t(($) => $.workspace.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
