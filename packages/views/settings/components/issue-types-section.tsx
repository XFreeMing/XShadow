"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  ArrowDown,
  ArrowUp,
  CircleDot,
  MoreHorizontal,
  Pencil,
  Plus,
  X,
} from "@multica/ui/icons";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { DEFAULT_GRID_COLUMN_ORDER } from "@multica/core/issues/grid-columns";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
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
import { specializedIssuePropertyIds } from "../../issues/issue-type-create-fields";
import { useStatusLabel } from "../../issues/utils/status-label";
import { WorkMethodEditor } from "../../workflows/work-method-editor";
import { WorkMethodSteps } from "../../workflows/work-method-step";
import { PropertyTypeLabel } from "./properties-tab";
import { TaskTemplateGrid, type TemplateGridColumn } from "./task-template-grid";

const MAX_ACTIVE_ISSUE_TYPES = 50;

export function TaskTemplatesSection({
  canManage,
  canEditMethods = false,
}: {
  canManage: boolean;
  canEditMethods?: boolean;
}) {
  const { t } = useT("settings");
  const { t: issueText } = useT("issues");
  const wsId = useWorkspaceId();
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<IssueType | null>(null);
  const [showArchived, setShowArchived] = useState(false);

  const { data: issueTypes = [], isLoading, isError, refetch } = useQuery(
    issueTypeListOptions(wsId, true),
  );
  const { data: properties = [], isSuccess: propertiesLoaded } = useQuery(propertyListOptions(wsId, true));
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
  const specializedPropertyIds = specializedIssuePropertyIds(issueTypes);
  const commonFields = activeProperties.filter((property) => !specializedPropertyIds.has(property.id));

  return (
    <section id="task-templates" aria-labelledby="task-templates-heading" className="space-y-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <h2 id="task-templates-heading" className="text-heading font-semibold">
            {t(($) => $.properties.issue_types.title)}
          </h2>
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
        ) : (
          <div className="space-y-2 p-4">
            <details>
              <summary className="cursor-pointer rounded-sm py-1 text-body font-medium focus-visible:outline-2 focus-visible:outline-ring">
                {t(($) => $.properties.issue_types.abstract_task)}
              </summary>
              <p className="mt-2 text-caption text-muted-foreground">
                {t(($) => $.properties.issue_types.inherited_fields)}
              </p>
              <ul className="my-2 flex flex-wrap gap-1.5">
                {DEFAULT_GRID_COLUMN_ORDER.filter((key) => key !== "type").map((key) => (
                  <li key={key}><Badge variant="outline">{issueText(($) => $.table.columns[key])}</Badge></li>
                ))}
                {propertiesLoaded && commonFields.map((property) => (
                  <li key={property.id}><Badge variant="outline">{property.name}</Badge></li>
                ))}
              </ul>
            </details>
            <div className="ms-2 divide-y divide-surface-border border-s border-surface-border">
              <div className="px-4 py-2 text-body font-medium">
                {issueText(($) => $.table.general_task_type)}
              </div>
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
                    <TaskTemplateDetails issueType={issueType} propertyById={propertyById} methods={methods} canEditMethods={canEditMethods} />
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
  canEditMethods,
}: {
  issueType: IssueType;
  propertyById: ReadonlyMap<string, IssueProperty>;
  methods: WorkMethod[];
  canEditMethods: boolean;
}) {
  const { t } = useT("settings");
  const { t: issueText } = useT("issues");
  const wsId = useWorkspaceId();
  const { activeStatuses } = useIssueStatuses(wsId);
  const statusLabel = useStatusLabel(wsId);
  const [editing, setEditing] = useState<WorkMethod | null>(null);
  const columns: TemplateGridColumn[] = [
    {
      id: "status",
      name: issueText(($) => $.table.columns.status),
      typeLabel: (
        <button
          type="button"
          className="max-w-full truncate rounded-sm underline-offset-2 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
          onClick={() => document.getElementById("status-library")?.scrollIntoView({ block: "start" })}
        >
          {issueText(($) => $.workflows.settings.statuses_title)}
        </button>
      ),
      icon: <CircleDot className="size-3.5" />,
      required: false,
      description: activeStatuses.map(({ key }) => statusLabel(key)).join(" · "),
    },
    ...issueType.fields.map((field) => {
      const property = propertyById.get(field.property_id);
      return {
        id: field.property_id,
        name: property?.name ?? field.property_id,
        typeLabel: property ? <PropertyTypeLabel type={property.type} /> : undefined,
        icon: property ? <PropertyIcon property={property} className="size-3.5" /> : undefined,
        required: field.required,
        defaultValue: property && field.default_value !== undefined
          ? <CustomPropertyValueDisplay property={property} value={field.default_value} />
          : undefined,
        formula: property?.type === "formula" ? property.config.expression : undefined,
        description: property?.description,
      };
    }),
  ];
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
      <TaskTemplateGrid columns={columns} labels={{
        title: t(($) => $.properties.issue_types.grid_preview),
        required: t(($) => $.properties.issue_types.required),
        defaults: t(($) => $.properties.issue_types.default_label),
        formulas: t(($) => $.properties.types.formula),
        description: t(($) => $.properties.editor.description),
        empty: t(($) => $.properties.issue_types.empty_fields),
      }} />
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
              {(fromPlan || gates.length > 0) && (
                <>
                  <WorkMethodSteps steps={method.steps} />
                  {canEditMethods && !editing && (
                    <Button size="xs" variant="outline" className="mt-2" onClick={() => setEditing(method)}>
                      {issueText(($) => $.method.edit)}
                    </Button>
                  )}
                  {editing?.id === method.id && (
                    <WorkMethodEditor wsId={wsId} original={editing} onClose={() => setEditing(null)} />
                  )}
                </>
              )}
            </div>
          ))}
        </div>
      )}
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
