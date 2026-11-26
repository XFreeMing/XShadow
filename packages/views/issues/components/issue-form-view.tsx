"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, ChevronRight, Loader2, Shapes } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { toast } from "sonner";
import { contentReferencesAttachment } from "@multica/core/types";
import type {
  Issue,
  IssueAssigneeType,
  IssuePriority,
  IssuePropertyValue,
  IssueStatus,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCreateIssue } from "@multica/core/issues/mutations";
import { issueTypeListOptions } from "@multica/core/issue-types";
import { propertyListOptions } from "@multica/core/properties";
import { useProjectWithWorkflow, workflowAllowsStatus } from "@multica/core/issue-workflows";
import { useWorkspacePaths } from "@multica/core/paths";
import { useShortcut } from "@multica/core/shortcuts";
import {
  ApiError,
  DuplicateIssueErrorBodySchema,
  parseWithFallback,
  type DuplicateIssueErrorBody,
} from "@multica/core/api";
import { resolveClickIntent, useIntentNavigate } from "../../navigation";
import {
  ContentEditor,
  TitleEditor,
  type ContentEditorRef,
  type TitleEditorRef,
  useComposerSubmit,
  useFileDropZone,
  FileDropOverlay,
  useUploadGate,
} from "../../editor";
import { useT } from "../../i18n";
import { ShortcutKeycaps } from "../../common/shortcut-keycaps";
import { PillButton } from "../../common/pill-button";
import { PropertyIcon } from "../../common/property-icon";
import {
  AssigneePicker,
  DueDatePicker,
  LabelPicker,
  PriorityIcon,
  PriorityPicker,
  StartDatePicker,
  StatusPicker,
} from ".";
import { ProjectPicker } from "../../projects/components/project-picker";
import {
  CustomPropertyValueDisplay,
  CustomPropertyValueInput,
} from "./pickers/custom-property-picker";
import type { IssueCreateDefaults } from "../surface/types";
import type { IssueScope } from "@multica/core/issues/surface/scope";
import {
  applyIssueTypeSelection,
  missingRequiredIssueTypeFields,
  orderedIssueTypePropertyEntries,
} from "../issue-type-create-fields";
import { useIssueFormUploads } from "./use-issue-form-uploads";
import { useIssueLimitUpgradePrompt } from "../../modals/use-issue-limit-upgrade-prompt";

export interface IssueFormViewProps {
  scope: IssueScope;
  createDefaults?: IssueCreateDefaults;
  /** Upload/editor binding identity — must differ per embedded surface instance. */
  formUploadKey: string;
}

function lockedProjectId(scope: IssueScope, createDefaults?: IssueCreateDefaults): string | undefined {
  if (scope.type === "project") return scope.projectId;
  if (createDefaults?.project_id) return createDefaults.project_id;
  return undefined;
}

export function IssueFormView({ scope, createDefaults, formUploadKey }: IssueFormViewProps) {
  const wsId = useWorkspaceId();
  const { t } = useT("issues");
  const { t: tModals } = useT("modals");
  const paths = useWorkspacePaths();
  const intentNavigate = useIntentNavigate();
  const sendShortcut = useShortcut("send");
  const titleEditorRef = useRef<TitleEditorRef>(null);
  const descEditorRef = useRef<ContentEditorRef>(null);
  const appliedTypeDefaultsRef = useRef(new Set<string>());
  const initializedTypeRef = useRef(false);

  const initialProjectId = lockedProjectId(scope, createDefaults);
  const [title, setTitle] = useState(createDefaults?.title ?? "");
  const [description, setDescription] = useState(createDefaults?.description ?? "");
  const [status, setStatus] = useState<IssueStatus>(
    (createDefaults?.status as IssueStatus | undefined) ?? "todo",
  );
  const [priority, setPriority] = useState<IssuePriority>(
    (createDefaults?.priority as IssuePriority | undefined) ?? "none",
  );
  const [assigneeType, setAssigneeType] = useState<IssueAssigneeType | undefined>(
    createDefaults?.assignee_type ?? undefined,
  );
  const [assigneeId, setAssigneeId] = useState<string | undefined>(
    createDefaults?.assignee_id ?? undefined,
  );
  const [startDate, setStartDate] = useState<string | null>(
    createDefaults?.start_date ?? null,
  );
  const [dueDate, setDueDate] = useState<string | null>(createDefaults?.due_date ?? null);
  const [labelIds, setLabelIds] = useState<string[]>([]);
  const [issueTypeId, setIssueTypeId] = useState<string | undefined>(
    createDefaults?.issue_type_id as string | undefined,
  );
  const [propertyValues, setPropertyValues] = useState<Record<string, IssuePropertyValue>>(
    (createDefaults?.properties as Record<string, IssuePropertyValue> | undefined) ?? {},
  );
  const [projectId, setProjectId] = useState<string | undefined>(initialProjectId);
  const [customPropertyPickerId, setCustomPropertyPickerId] = useState<string | null>(null);
  const [propertyErrorId, setPropertyErrorId] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [createdIssue, setCreatedIssue] = useState<Issue | null>(null);
  const [editorResetKey, setEditorResetKey] = useState(0);

  const projectLocked = scope.type === "project" || !!createDefaults?.project_id;

  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(wsId));
  const { data: workspaceIssueTypes = [], isSuccess: issueTypesLoaded } = useQuery(
    issueTypeListOptions(wsId),
  );
  const selectedIssueType = workspaceIssueTypes.find((it) => it.id === issueTypeId) ?? null;

  const { workflow: projectWorkflow, defaultWorkflow: workspaceDefault } = useProjectWithWorkflow(
    wsId,
    projectId,
  );
  const createWorkflow = projectId ? projectWorkflow : workspaceDefault;
  useEffect(() => {
    if (createWorkflow && !workflowAllowsStatus(createWorkflow, status)) {
      setStatus("todo");
    }
  }, [createWorkflow, status]);

  useEffect(() => {
    if (!issueTypesLoaded || initializedTypeRef.current) return;
    initializedTypeRef.current = true;
    const initialTypeId = issueTypeId ?? (workspaceIssueTypes.length === 1
      ? workspaceIssueTypes[0]?.id
      : undefined);
    if (!initialTypeId) return;
    const next = applyIssueTypeSelection(
      initialTypeId,
      workspaceIssueTypes,
      propertyValues,
      appliedTypeDefaultsRef.current,
    );
    appliedTypeDefaultsRef.current = next.appliedTypeDefaults;
    setIssueTypeId(next.issueTypeId);
    setPropertyValues(next.propertyValues);
  }, [issueTypeId, issueTypesLoaded, propertyValues, workspaceIssueTypes]);

  const uploadGate = useUploadGate(descEditorRef);
  const { isDragOver: descDragOver, dropZoneProps: descDropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((f) => descEditorRef.current?.uploadFile(f)),
  });
  const { attachments: formAttachments, handleUpload, gate, clearUploads } = useIssueFormUploads(
    formUploadKey,
    uploadGate,
    descEditorRef,
    description,
    setDescription,
  );

  const createIssueMutation = useCreateIssue();
  const showIssueLimitUpgradePrompt = useIssueLimitUpgradePrompt();

  const resetForm = useCallback(() => {
    setTitle("");
    setDescription("");
    setStatus("todo");
    setPriority("none");
    setAssigneeType(undefined);
    setAssigneeId(undefined);
    setStartDate(null);
    setDueDate(null);
    setLabelIds([]);
    setIssueTypeId(undefined);
    initializedTypeRef.current = false;
    appliedTypeDefaultsRef.current = new Set();
    setPropertyValues({});
    setCustomPropertyPickerId(null);
    setPropertyErrorId(null);
    setFormError(null);
    if (!projectLocked) setProjectId(undefined);
    else setProjectId(initialProjectId);
    clearUploads();
    descEditorRef.current?.clearContent();
    setEditorResetKey((key) => key + 1);
  }, [clearUploads, initialProjectId, projectLocked]);

  const composer = useComposerSubmit({
    editorRef: descEditorRef,
    uploadGate: gate,
    normalize: () => title.trim(),
    onSubmit: async (): Promise<boolean> => {
      setFormError(null);
      const pendingDesc = descEditorRef.current?.flushPendingUpdate?.();
      const body = pendingDesc ?? (descEditorRef.current?.getMarkdown()?.trim() || undefined);
      const activeAttachmentIds = formAttachments
        .filter((a) => contentReferencesAttachment(body ?? "", a))
        .map((a) => a.id);
      try {
        const issue = await createIssueMutation.mutateAsync({
          title: title.trim(),
          description: body,
          status,
          priority,
          assignee_type: assigneeType,
          assignee_id: assigneeId,
          start_date: startDate || undefined,
          due_date: dueDate || undefined,
          attachment_ids: activeAttachmentIds.length > 0 ? activeAttachmentIds : undefined,
          label_ids: labelIds.length > 0 ? labelIds : undefined,
          ...(issueTypeId ? { issue_type_id: issueTypeId } : {}),
          ...(Object.keys(propertyValues).length > 0 ? { properties: propertyValues } : {}),
          parent_issue_id: createDefaults?.parent_issue_id ?? undefined,
          project_id: projectId,
        });
        setCreatedIssue(issue);
        return true;
      } catch (err) {
        const sourceCode =
          err instanceof ApiError && err.body && typeof err.body === "object"
            ? (err.body as { code?: unknown }).code
            : undefined;
        if (sourceCode === "issue_limit_reached") {
          showIssueLimitUpgradePrompt();
          return false;
        }
        if (err instanceof ApiError && err.status === 409) {
          const dup = parseWithFallback<DuplicateIssueErrorBody | null>(
            err.body,
            DuplicateIssueErrorBodySchema,
            null,
            { endpoint: "POST /api/workspaces/:wsId/issues (active_duplicate_issue)" },
          );
          if (dup) {
            toast.error(tModals(($) => $.create_issue.toast_duplicate_title));
            return false;
          }
        }
        setFormError(
          err instanceof ApiError
            ? err.message
            : err instanceof Error && err.message
              ? err.message
              : tModals(($) => $.create_issue.toast_failed),
        );
        return false;
      }
    },
    onAccepted: () => {
      resetForm();
    },
    afterAccepted: "none",
  });

  const selectIssueType = (nextTypeId: string | undefined) => {
    const next = applyIssueTypeSelection(
      nextTypeId,
      workspaceIssueTypes,
      propertyValues,
      appliedTypeDefaultsRef.current,
    );
    appliedTypeDefaultsRef.current = next.appliedTypeDefaults;
    setIssueTypeId(next.issueTypeId);
    setPropertyValues(next.propertyValues);
    setPropertyErrorId(null);
    setFormError(null);
    setCustomPropertyPickerId(null);
  };

  const updatePropertyValue = (propertyId: string, value: IssuePropertyValue | undefined) => {
    appliedTypeDefaultsRef.current.delete(propertyId);
    const next = { ...propertyValues };
    if (value === undefined) delete next[propertyId];
    else next[propertyId] = value;
    setPropertyValues(next);
    if (propertyErrorId === propertyId) setPropertyErrorId(null);
    if (formError) setFormError(null);
  };

  const handleSubmit = () => {
    if (issueTypeId && !issueTypesLoaded) return;
    if (!title.trim()) {
      titleEditorRef.current?.focus();
      return;
    }
    if (selectedIssueType) {
      const missingFields = missingRequiredIssueTypeFields(selectedIssueType, propertyValues);
      if (missingFields.length > 0) {
        const firstMissing = missingFields[0]!;
        const propById = new Map(workspaceProperties.map((prop) => [prop.id, prop]));
        const names = missingFields
          .map((field) => propById.get(field.property_id)?.name ?? field.property_id)
          .join(", ");
        setPropertyErrorId(firstMissing.property_id);
        setCustomPropertyPickerId(firstMissing.property_id);
        setFormError(tModals(($) => $.create_issue.required_fields_missing, { fields: names }));
        return;
      }
    }
    void composer.submit();
  };

  const propertyEntries = useMemo(
    () =>
      orderedIssueTypePropertyEntries(
        selectedIssueType,
        workspaceProperties,
        propertyValues,
        customPropertyPickerId,
      ),
    [customPropertyPickerId, propertyValues, selectedIssueType, workspaceProperties],
  );

  const openCreatedIssue = (event: React.MouseEvent) => {
    if (!createdIssue) return;
    intentNavigate(
      paths.issueDetail(createdIssue.id),
      resolveClickIntent(event),
      createdIssue.identifier,
    );
  };

  return (
    <div
      className="flex flex-1 min-h-0 flex-col overflow-y-auto"
      data-view-kind="form"
      data-form-upload-key={formUploadKey}
    >
      <div className="mx-auto w-full max-w-xl p-6">
        {createdIssue ? (
          <div
            className="rounded-lg border border-border bg-background p-5 shadow-2xs"
            data-form-status="success"
            role="status"
          >
            <div className="flex items-start gap-3">
              <Check className="mt-0.5 size-5 shrink-0 text-brand" aria-hidden />
              <div className="min-w-0 flex-1 space-y-3">
                <p className="text-body font-medium">{tModals(($) => $.create_issue.toast_created)}</p>
                <p className="text-caption text-muted-foreground truncate">
                  {createdIssue.identifier} · {createdIssue.title}
                </p>
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" onClick={openCreatedIssue}>
                    {tModals(($) => $.create_issue.view_issue)}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setCreatedIssue(null)}
                  >
                    {t(($) => $.form.create_another)}
                  </Button>
                </div>
              </div>
            </div>
          </div>
        ) : (
          <form
            className="rounded-lg border border-border bg-background p-5 shadow-2xs"
            data-form-status={composer.submitting ? "loading" : formError ? "error" : undefined}
            role="form"
            aria-label={t(($) => $.form.sr_label)}
            onSubmit={(event) => {
              event.preventDefault();
              // Scalar property pickers commit inside a portal <form>. That submit
              // bubbles through React to this envelope — ignore anything that did
              // not originate on this form element.
              if (event.target !== event.currentTarget) return;
              handleSubmit();
            }}
          >
            <h2 className="mb-4 text-title-sm font-semibold">{t(($) => $.form.heading)}</h2>

            {formError ? (
              <div className="mb-4 text-body text-destructive" role="alert">
                {formError}
              </div>
            ) : null}

            <div className="mb-4">
              <TitleEditor
                ref={titleEditorRef}
                key={`title-${editorResetKey}`}
                placeholder={tModals(($) => $.create_issue.title_placeholder)}
                defaultValue={title}
                onChange={setTitle}
                onSubmitShortcut={() => {
                  if (!composer.submitting && !gate.isBlocked()) handleSubmit();
                }}
              />
            </div>

            <div className="relative mb-4 min-h-24" {...descDropZoneProps}>
              <ContentEditor
                ref={descEditorRef}
                key={`desc-${editorResetKey}`}
                defaultValue={description}
                placeholder={tModals(($) => $.create_issue.description_placeholder)}
                onUpdate={(md) => setDescription(md)}
                onSubmit={handleSubmit}
                onUploadFile={handleUpload}
                onUploadingChange={uploadGate.onUploadingChange}
                attachments={formAttachments}
                debounceMs={500}
              />
              {descDragOver ? <FileDropOverlay /> : null}
            </div>

            {workspaceIssueTypes.length > 0 ? (
              <div className="mb-4">
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        className="gap-1.5"
                        data-testid="issue-type-selector"
                      >
                        <Shapes className="size-3.5" />
                        {selectedIssueType?.name ?? t(($) => $.form.type_placeholder)}
                        <ChevronRight className="size-3.5 rotate-90 text-muted-foreground" />
                      </Button>
                    }
                  />
                  <DropdownMenuContent align="start">
                    <DropdownMenuItem onClick={() => selectIssueType(undefined)}>
                      {t(($) => $.table.general_task_type)}
                    </DropdownMenuItem>
                    {workspaceIssueTypes.map((issueType) => (
                      <DropdownMenuItem
                        key={issueType.id}
                        onClick={() => selectIssueType(issueType.id)}
                      >
                        <span className="truncate">{issueType.name}</span>
                        {selectedIssueType?.id === issueType.id ? (
                          <Check className="ml-auto size-3.5" />
                        ) : null}
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            ) : null}

            <div className="mb-4 flex flex-wrap items-center gap-1.5">
              <StatusPicker
                status={status}
                onUpdate={(u) => {
                  if (u.status) setStatus(u.status);
                }}
                projectId={projectId}
                triggerRender={<PillButton />}
              />
              <PriorityPicker
                priority={priority}
                onUpdate={(u) => {
                  if (u.priority) setPriority(u.priority);
                }}
                triggerRender={
                  <PillButton>
                    <PriorityIcon priority={priority} />
                  </PillButton>
                }
              />
              <AssigneePicker
                assigneeType={assigneeType ?? null}
                assigneeId={assigneeId ?? null}
                onUpdate={(u) => {
                  setAssigneeType(u.assignee_type ?? undefined);
                  setAssigneeId(u.assignee_id ?? undefined);
                }}
                triggerRender={<PillButton />}
              />
              <LabelPicker
                selectedIds={labelIds}
                onSelectedIdsChange={setLabelIds}
                triggerRender={<PillButton />}
              />
              {!projectLocked ? (
                <ProjectPicker
                  projectId={projectId ?? null}
                  onUpdate={(u) => setProjectId(u.project_id ?? undefined)}
                  triggerRender={<PillButton />}
                />
              ) : null}
              <StartDatePicker
                startDate={startDate}
                onUpdate={(u) => setStartDate(u.start_date ?? null)}
                triggerRender={<PillButton />}
              />
              <DueDatePicker
                dueDate={dueDate}
                onUpdate={(u) => setDueDate(u.due_date ?? null)}
                triggerRender={<PillButton />}
              />
            </div>

            {propertyEntries.length > 0 ? (
              <div className="mb-4 flex flex-wrap items-center gap-1.5">
                {propertyEntries.map(({ property, required }) => {
                  const value = propertyValues[property.id];
                  const isMissingRequired = required && value === undefined;
                  return (
                    <div
                      key={property.id}
                      data-property-error={propertyErrorId === property.id || undefined}
                      data-property-required={required || undefined}
                      className={cn(
                        propertyErrorId === property.id && "rounded-md ring-2 ring-destructive",
                      )}
                    >
                      <CustomPropertyValueInput
                        property={property}
                        value={value}
                        required={required}
                        onChange={(next) => updatePropertyValue(property.id, next)}
                        open={customPropertyPickerId === property.id}
                        onOpenChange={(open) =>
                          setCustomPropertyPickerId(open ? property.id : null)
                        }
                        triggerRender={<PillButton />}
                        trigger={
                          <>
                            <PropertyIcon property={property} className="size-3.5 text-caption" />
                            <span className="max-w-32 truncate">
                              {property.name}
                              {required ? (
                                <span className="ml-0.5 text-destructive" aria-hidden="true">
                                  *
                                </span>
                              ) : null}
                            </span>
                            {value !== undefined ? (
                              <span className="max-w-40 truncate text-muted-foreground">
                                <CustomPropertyValueDisplay property={property} value={value} />
                              </span>
                            ) : isMissingRequired ? (
                              <span className="text-micro text-destructive">
                                {tModals(($) => $.create_issue.required_badge)}
                              </span>
                            ) : null}
                          </>
                        }
                      />
                    </div>
                  );
                })}
              </div>
            ) : null}

            <div className="flex items-center justify-end gap-2 border-t border-border pt-4">
              {sendShortcut ? (
                <ShortcutKeycaps
                  shortcut={sendShortcut}
                  decorative
                  className="text-caption text-muted-foreground"
                />
              ) : null}
              <Button
                type="submit"
                disabled={composer.submitting || gate.isBlocked() || (!!issueTypeId && !issueTypesLoaded)}
              >
                {composer.submitting ? (
                  <Loader2 className="size-3.5 animate-spin" aria-hidden />
                ) : null}
                {composer.submitting
                  ? tModals(($) => $.create_issue.submitting)
                  : tModals(($) => $.create_issue.submit)}
              </Button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
