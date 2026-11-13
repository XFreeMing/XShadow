"use client";

import { useState } from "react";
import type { Goal, GoalPlanEdge, GoalPlanItem, Issue } from "@multica/core/types";
import {
  GitBranch,
  Link2,
  ListChecks,
  Plus,
  Trash2,
  Workflow,
  X,
} from "@multica/ui/icons";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";
import { IssuePickerModal } from "../../modals/issue-picker-modal";
import { prerequisitesOf } from "../plan";

export interface PlanContent {
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
}

type ItemPatch = Partial<Omit<GoalPlanItem, "id">>;

function newId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;
}

function emptyItem(): GoalPlanItem {
  return {
    id: newId(),
    title: "",
    outcome: "",
    acceptance_criteria: "",
    evidence_refs: [],
    binding: { type: "new", issue_id: null },
    proposed_owner: null,
    stage: null,
    coordination: null,
  };
}

/**
 * Edits the plan as a plain draft: the editor never writes to the server.
 * Item identity is the UUID, so renaming or reordering keeps dependencies and
 * an existing-task binding intact.
 */
export function GoalPlanEditor({
  goal,
  items,
  edges,
  disabled = false,
  onChange,
  onItemEdited,
  onItemRemoved,
  onEdgeRemoved,
  ownerLabel,
}: {
  goal: Goal;
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  disabled?: boolean;
  onChange: (next: PlanContent) => void;
  onItemEdited: (itemId: string) => void;
  onItemRemoved: (itemId: string) => void;
  onEdgeRemoved: (edge: GoalPlanEdge) => void;
  ownerLabel: (item: GoalPlanItem) => string | null;
}) {
  const { t } = useT("goals");
  const [pickingFor, setPickingFor] = useState<string | null>(null);
  const [issueLabels, setIssueLabels] = useState<Record<string, string>>({});

  // A binding type this client does not know must never read as "new task":
  // its value stays intact in the draft, and the user only changes it by
  // explicitly choosing a known type.
  const bindingLabel = (binding: GoalPlanItem["binding"]): string => {
    if (binding.type === "existing") return t(($) => $.plan.binding_existing);
    if (binding.type === "new") return t(($) => $.plan.binding_new);
    return t(($) => $.plan.binding_unknown, { type: binding.type || "?" });
  };

  const updateItem = (id: string, patch: ItemPatch) => {
    onChange({ items: items.map((item) => (item.id === id ? { ...item, ...patch } : item)), edges });
    onItemEdited(id);
  };

  const addItem = () => {
    onChange({ items: [...items, emptyItem()], edges });
  };

  const removeItem = (id: string) => {
    onItemRemoved(id);
    onChange({
      items: items.filter((item) => item.id !== id),
      edges: edges.filter(
        (edge) => edge.dependent_item_id !== id && edge.prerequisite_item_id !== id,
      ),
    });
  };

  const addEdge = (dependentItemId: string, prerequisiteItemId: string, reason: string) => {
    onChange({
      items,
      edges: [...edges, { dependent_item_id: dependentItemId, prerequisite_item_id: prerequisiteItemId, reason }],
    });
  };

  const removeEdge = (edge: GoalPlanEdge) => {
    onEdgeRemoved(edge);
    onChange({
      items,
      edges: edges.filter(
        (candidate) =>
          !(
            candidate.dependent_item_id === edge.dependent_item_id &&
            candidate.prerequisite_item_id === edge.prerequisite_item_id
          ),
      ),
    });
  };

  const applyBinding = (itemId: string, issue: Issue) => {
    setIssueLabels((labels) => ({ ...labels, [issue.id]: issue.identifier || issue.title }));
    updateItem(itemId, { binding: { type: "existing", issue_id: issue.id } });
  };

  const itemTitle = (id: string) =>
    items.find((item) => item.id === id)?.title || t(($) => $.plan.untitled_item);

  return (
    <div className="space-y-3" data-testid="goal-plan-editor">
      <div className="flex items-center justify-between gap-2">
        <p className="text-caption text-muted-foreground">
          {t(($) => $.plan.task_count, { value: items.length })}
        </p>
        <Button type="button" size="sm" variant="outline" onClick={addItem} disabled={disabled}>
          <Plus className="size-3.5" />
          {t(($) => $.plan.add_task)}
        </Button>
      </div>

      {items.length === 0 ? (
        <div className="rounded-md border border-dashed px-3 py-6 text-center">
          <ListChecks className="mx-auto mb-2 size-5 text-muted-foreground" />
          <p className="text-caption text-muted-foreground">{t(($) => $.plan.empty)}</p>
        </div>
      ) : null}

      <ol className="space-y-3">
        {items.map((item, index) => {
          const incoming = prerequisitesOf(item.id, edges);
          const boundLabel = item.binding.issue_id
            ? (issueLabels[item.binding.issue_id] ?? `#${item.binding.issue_id.slice(0, 8)}`)
            : null;
          const bindingKnown =
            item.binding.type === "new" || item.binding.type === "existing";
          return (
            <li
              key={item.id}
              className="space-y-3 rounded-md border px-3 py-3"
              data-testid={`plan-item-${item.id}`}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="text-caption font-medium text-muted-foreground">
                  {t(($) => $.plan.task_index, { index: index + 1 })}
                </span>
                <div className="flex items-center gap-1">
                  {incoming.length === 0 ? (
                    <Badge variant="outline" className="gap-1">
                      <Workflow className="size-3" />
                      {t(($) => $.plan.parallel)}
                    </Badge>
                  ) : (
                    <Badge variant="secondary" className="gap-1">
                      <GitBranch className="size-3" />
                      {t(($) => $.plan.waits_for, { value: incoming.length })}
                    </Badge>
                  )}
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.plan.remove_task, { title: item.title || t(($) => $.plan.untitled_item) })}
                    className="text-muted-foreground"
                    onClick={() => removeItem(item.id)}
                    disabled={disabled}
                  >
                    <Trash2 />
                  </Button>
                </div>
              </div>

              <div className="space-y-1">
                <Label htmlFor={`plan-title-${item.id}`}>{t(($) => $.plan.task_title)}</Label>
                <Input
                  id={`plan-title-${item.id}`}
                  value={item.title}
                  disabled={disabled}
                  onChange={(event) => updateItem(item.id, { title: event.target.value })}
                />
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-1">
                  <Label htmlFor={`plan-outcome-${item.id}`}>{t(($) => $.plan.outcome)}</Label>
                  <Textarea
                    id={`plan-outcome-${item.id}`}
                    value={item.outcome}
                    disabled={disabled}
                    className="min-h-16"
                    onChange={(event) => updateItem(item.id, { outcome: event.target.value })}
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`plan-acceptance-${item.id}`}>
                    {t(($) => $.plan.acceptance_criteria)}
                  </Label>
                  <Textarea
                    id={`plan-acceptance-${item.id}`}
                    value={item.acceptance_criteria}
                    disabled={disabled}
                    className="min-h-16"
                    onChange={(event) =>
                      updateItem(item.id, { acceptance_criteria: event.target.value })
                    }
                  />
                </div>
              </div>

              <div className="flex flex-wrap items-end gap-3">
                <div className="space-y-1">
                  <Label>{t(($) => $.plan.stage)}</Label>
                  <Input
                    type="number"
                    min={0}
                    aria-label={t(($) => $.plan.stage)}
                    className="w-24"
                    value={item.stage ?? ""}
                    disabled={disabled}
                    onChange={(event) => {
                      const raw = event.target.value.trim();
                      const parsed = raw === "" ? null : Number.parseInt(raw, 10);
                      updateItem(item.id, {
                        stage: parsed === null || Number.isNaN(parsed) ? null : Math.max(0, parsed),
                      });
                    }}
                  />
                </div>
                <div className="space-y-1">
                  <Label>{t(($) => $.plan.binding)}</Label>
                  <div className="flex items-center gap-1.5">
                    <Select
                      items={[
                        { value: "new", label: t(($) => $.plan.binding_new) },
                        { value: "existing", label: t(($) => $.plan.binding_existing) },
                        ...(bindingKnown
                          ? []
                          : [{ value: "unknown", label: bindingLabel(item.binding) }]),
                      ]}
                      value={bindingKnown ? item.binding.type : "unknown"}
                      disabled={disabled}
                      onValueChange={(next) => {
                        if (next === "existing") {
                          if (!item.binding.issue_id) setPickingFor(item.id);
                        } else if (next === "new") {
                          updateItem(item.id, { binding: { type: "new", issue_id: null } });
                        }
                      }}
                    >
                      <SelectTrigger size="sm" className="w-40">
                        <SelectValue>{bindingLabel(item.binding)}</SelectValue>
                      </SelectTrigger>
                      <SelectContent align="start">
                        <SelectItem value="new">{t(($) => $.plan.binding_new)}</SelectItem>
                        <SelectItem value="existing">
                          {t(($) => $.plan.binding_existing)}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                    {boundLabel ? (
                      <span className="flex min-w-0 items-center gap-1 text-caption text-muted-foreground">
                        <Link2 className="size-3 shrink-0" />
                        <span className="truncate">{boundLabel}</span>
                      </span>
                    ) : null}
                    {item.binding.type === "existing" ? (
                      <>
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          onClick={() => setPickingFor(item.id)}
                          disabled={disabled}
                        >
                          {t(($) => $.plan.binding_choose)}
                        </Button>
                        <Button
                          type="button"
                          size="icon-sm"
                          variant="ghost"
                          aria-label={t(($) => $.plan.binding_clear)}
                          onClick={() =>
                            updateItem(item.id, { binding: { type: "new", issue_id: null } })
                          }
                          disabled={disabled}
                        >
                          <X />
                        </Button>
                      </>
                    ) : null}
                  </div>
                </div>
              </div>

              {ownerLabel(item) ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.plan.proposed_owner)}: {ownerLabel(item)}
                </p>
              ) : null}

              {item.evidence_refs.length > 0 ? (
                <div className="space-y-1">
                  <p className="text-caption font-medium">{t(($) => $.plan.evidence)}</p>
                  <ul className="space-y-0.5 text-caption text-muted-foreground">
                    {item.evidence_refs.map((ref, refIndex) => (
                      <li key={`${ref.kind}-${ref.ref}-${refIndex}`} className="break-all">
                        <span className="font-medium">{ref.kind}</span> {ref.ref}
                        {ref.version ? ` @ ${ref.version}` : ""}
                        {ref.note ? ` — ${ref.note}` : ""}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}

              {item.coordination ? (
                <div className="space-y-0.5 text-caption text-muted-foreground">
                  {item.coordination.parallel_reason ? (
                    <p>
                      {t(($) => $.plan.parallel_reason)}: {item.coordination.parallel_reason}
                    </p>
                  ) : null}
                  {item.coordination.integration_owner ? (
                    <p>
                      {t(($) => $.plan.integration_owner)}: {item.coordination.integration_owner}
                    </p>
                  ) : null}
                  {item.coordination.write_scope.length > 0 ? (
                    <p>
                      {t(($) => $.plan.write_scope)}:{" "}
                      {item.coordination.write_scope.join(", ")}
                    </p>
                  ) : null}
                </div>
              ) : null}

              <DependencyEditor
                key={`${item.id}-deps`}
                itemId={item.id}
                items={items}
                edges={edges}
                disabled={disabled}
                itemTitle={itemTitle}
                onAdd={addEdge}
                onRemove={removeEdge}
              />
            </li>
          );
        })}
      </ol>

      <IssuePickerModal
        open={pickingFor !== null}
        onOpenChange={(open) => {
          if (!open) setPickingFor(null);
        }}
        title={t(($) => $.plan.binding_picker_title)}
        description={t(($) => $.plan.binding_picker_description)}
        excludeIds={[]}
        onSelect={(issue) => {
          if (pickingFor) applyBinding(pickingFor, issue);
          setPickingFor(null);
        }}
        isSelectable={(issue) => issue.project_id === goal.project_id}
      />
    </div>
  );
}

function DependencyEditor({
  itemId,
  items,
  edges,
  disabled,
  itemTitle,
  onAdd,
  onRemove,
}: {
  itemId: string;
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  disabled: boolean;
  itemTitle: (id: string) => string;
  onAdd: (dependentItemId: string, prerequisiteItemId: string, reason: string) => void;
  onRemove: (edge: GoalPlanEdge) => void;
}) {
  const { t } = useT("goals");
  const [prerequisite, setPrerequisite] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);

  const incoming = prerequisitesOf(itemId, edges);
  const candidates = items.filter(
    (candidate) =>
      candidate.id !== itemId &&
      !incoming.some((edge) => edge.prerequisite_item_id === candidate.id),
  );
  const options = candidates.map((candidate) => ({
    value: candidate.id,
    label: candidate.title || t(($) => $.plan.untitled_item),
  }));

  const submit = () => {
    if (!prerequisite || !reason.trim()) {
      setError(t(($) => $.plan.dependency_required));
      return;
    }
    onAdd(itemId, prerequisite, reason.trim());
    setPrerequisite("");
    setReason("");
    setError(null);
  };

  return (
    <div className="space-y-2 border-t pt-2">
      <p className="text-caption font-medium">{t(($) => $.plan.dependencies)}</p>
      {incoming.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.plan.no_dependencies)}</p>
      ) : (
        <ul className="space-y-1">
          {incoming.map((edge) => (
            <li
              key={`${edge.dependent_item_id}-${edge.prerequisite_item_id}`}
              className="flex items-start gap-2 text-caption"
            >
              <GitBranch className="mt-0.5 size-3 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1">
                <span className="font-medium">{itemTitle(edge.prerequisite_item_id)}</span>
                {edge.reason ? <span className="text-muted-foreground"> — {edge.reason}</span> : null}
              </span>
              <Button
                type="button"
                size="icon-sm"
                variant="ghost"
                aria-label={t(($) => $.plan.remove_dependency, {
                  title: itemTitle(edge.prerequisite_item_id),
                })}
                className="text-muted-foreground"
                onClick={() => onRemove(edge)}
                disabled={disabled}
              >
                <X />
              </Button>
            </li>
          ))}
        </ul>
      )}

      {candidates.length > 0 ? (
        <div className="flex flex-wrap items-end gap-2">
          <div className="space-y-1">
            <Label htmlFor={`plan-dep-target-${itemId}`}>{t(($) => $.plan.dependency_on)}</Label>
            <Select
              items={options}
              value={prerequisite}
              disabled={disabled}
              onValueChange={(next) => setPrerequisite(next ?? "")}
            >
              <SelectTrigger size="sm" id={`plan-dep-target-${itemId}`} className="w-44">
                <SelectValue placeholder={t(($) => $.plan.dependency_choose)}>
                  {prerequisite ? itemTitle(prerequisite) : undefined}
                </SelectValue>
              </SelectTrigger>
              <SelectContent align="start">
                {options.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="min-w-40 flex-1 space-y-1">
            <Label htmlFor={`plan-dep-reason-${itemId}`}>{t(($) => $.plan.dependency_reason)}</Label>
            <Input
              id={`plan-dep-reason-${itemId}`}
              value={reason}
              disabled={disabled}
              onChange={(event) => setReason(event.target.value)}
            />
          </div>
          <Button type="button" size="sm" variant="outline" onClick={submit} disabled={disabled}>
            <Plus className="size-3.5" />
            {t(($) => $.plan.add_dependency)}
          </Button>
        </div>
      ) : null}
      {error ? (
        <p className="text-caption text-destructive" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  );
}