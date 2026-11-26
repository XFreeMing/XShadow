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
import { propertyListOptions } from "@multica/core/properties";
import { workMethodCatalogOptions } from "@multica/core/issue-workflows/queries";
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
import type { WorkMethod } from "@multica/core/types/work-method";
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

export function TaskTemplatesSection({ canManage }: { canManage: boolean }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<IssueType | null>(null);
  const [showArchived, setShowArchived] = useState(false);

  const { data: issueTypes = [], isLoading, isError, refetch } = useQuery(
    issueTypeListOptions(wsId, true),
  );
  const { data: properties = [] } = useQuery(propertyListOptions(wsId, true));
  const { data: methods = [] } = useQuery({
    ...workMethodCatalogOptions(wsId),
    enabled: !!wsId,
  });
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
  const visible = showArchived ? issueTypes : issueTypes.filter((it) => !it.archived);

  return (
    <section id="task-templates" aria-labelledby="task-templates-heading" className="space-y-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <h2 id="task-templates-heading" className="text-heading font-semibold">
            {t(($) => $.properties.issue_types.title)}
          </h2>
          <span className="text-caption tabular-nums text-muted-foreground">
            {activeCount}/{MAX_ACTIVE_ISSUE_TYPES}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {issueTypes.length > activeCount && (
            <Button size="sm" variant="ghost" aria-pressed={showArchived} onClick={() => setShowArchived(!showArchived)}>
              {t(($) => $.properties.show_archived)}
            </Button>
          )}
          {canManage && (
            <Button
              size="sm"
              variant="outline"
              className="gap-1.5"
              onClick={() => setCreateOpen(true)}
              disabled={isLoading || isError || activeCount >= MAX_ACTIVE_ISSUE_TYPES}
            >
              <Plus className="size-3.5" />
              {t(($) => $.properties.issue_types.new_type)}
            </Button>
          )}
        </div>
      </div>

      <div className="overflow-hidden rounded-lg border border-surface-border bg-card">
        {isLoading ? (
          <div className="px-4 py-8 text-center text-body text-muted-foreground">
            {t(($) => $.properties.loading)}
          </div>
        ) : isError ? (
          <div role="alert" className="px-4 py-3 text-body text-muted-foreground">
            {t(($) => $.properties.issue_types.load_failed)}{" "}
            <Button variant="link" size="xs" onClick={() => void refetch()}>
              {t(($) => $.properties.issue_types.retry)}
            </Button>
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
                className="flex items-start justify-between gap-2 px-4 py-2"
              >
                <details className="min-w-0 flex-1">
                  <summary className="cursor-pointer rounded-sm py-1 text-body font-medium focus-visible:outline-2 focus-visible:outline-ring">
                    {issueType.name}
                    {issueType.archived && (
                      <Badge variant="outline" className="ms-2 text-micro">
                        {t(($) => $.properties.archived_badge)}
                      </Badge>
                    )}
                  </summary>
                  <TaskTemplateDetails issueType={issueType} propertyById={propertyById} methods={methods} />
                </details>

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

function TaskTemplateDetails({
  issueType,
  propertyById,
  methods,
}: {
  issueType: IssueType;
  propertyById: ReadonlyMap<string, IssueProperty>;
  methods: WorkMethod[];
}) {
  const { t } = useT("settings");
  const bindings = methods.flatMap((method) => {
    const fromPlan = method.plan_task?.issue_type_id === issueType.id;
    const fromRework = method.rework_task?.issue_type_id === issueType.id;
    const gates = method.steps.flatMap((step) =>
      step.table_gate?.issue_type_id === issueType.id
        ? [{ name: step.name, propertyId: step.table_gate.property_id }]
        : [],
    );
    return fromPlan || fromRework || gates.length > 0 ? [{ method, fromPlan, fromRework, gates }] : [];
  });

  return (
    <div className="space-y-4 py-3 ps-4 text-label">
      {bindings.length > 0 && (
        <div className="space-y-2">
          <h3 className="font-medium">{t(($) => $.properties.issue_types.method_bindings)}</h3>
          {bindings.map(({ method, fromPlan, fromRework, gates }) => (
            <div key={method.id} className="space-y-1">
              <p className="font-medium">{method.name}</p>
              {fromPlan && <p className="text-muted-foreground">{t(($) => $.properties.issue_types.from_plan)}</p>}
              {fromRework && <p className="text-muted-foreground">{t(($) => $.properties.issue_types.from_rework)}</p>}
              {gates.length > 0 && (
                <ul className="list-disc space-y-1 ps-5 text-muted-foreground">
                  {gates.map(({ name, propertyId }, index) => (
                    <li key={index}>
                      {t(($) => $.properties.issue_types.step_gate, {
                        step: name,
                        formula: propertyById.get(propertyId)?.name ?? propertyId,
                      })}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          ))}
        </div>
      )}
      <div className="space-y-2">
        <h3 className="font-medium">{t(($) => $.properties.issue_types.fields)}</h3>
        {issueType.fields.length === 0 ? (
          <p className="text-muted-foreground">{t(($) => $.properties.issue_types.empty_fields)}</p>
        ) : (
          <ul className="space-y-2">
            {issueType.fields.map((field) => {
              const property = propertyById.get(field.property_id);
              return (
                <li key={field.property_id} className="space-y-1">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                    {property && <PropertyIcon property={property} className="size-3.5 text-muted-foreground" />}
                    <span className="font-medium">{property?.name ?? field.property_id}</span>
                    {field.required && <span className="text-caption text-muted-foreground">{t(($) => $.properties.issue_types.required)}</span>}
                    {property && field.default_value !== undefined && (
                      <span className="inline-flex items-center gap-1 text-caption text-muted-foreground">
                        {t(($) => $.properties.issue_types.default_label)}
                        <CustomPropertyValueDisplay property={property} value={field.default_value} />
                      </span>
                    )}
                  </div>
                  {property?.description && <p className="text-muted-foreground">{property.description}</p>}
                  {property?.type === "formula" && property.config.expression && (
                    <code className="block whitespace-pre-wrap break-words rounded-md bg-muted px-2 py-1 text-caption">
                      {property.config.expression}
                    </code>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
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
