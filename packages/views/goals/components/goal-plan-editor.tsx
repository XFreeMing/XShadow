"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueWorkMethodOptions, workMethodCatalogOptions } from "@multica/core/issue-workflows/queries";
import type { WorkMethod } from "@multica/core/types/work-method";
import type { Goal, GoalPlanEdge, GoalPlanItem, Issue } from "@multica/core/types";
import {
  GitBranch,
  Link2,
  Plus,
  Trash2,
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
import { AppLink } from "../../navigation";
import { PlanDeliveryEditor } from "./goal-plan-delivery";

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

export function newPlanItem(): GoalPlanItem {
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
  item,
  items,
  edges,
  linkedIssues = [],
  issueHref,
  disabled = false,
  onChange,
  onItemEdited,
  onItemRemoved,
  onEdgeRemoved,
  ownerLabel,
}: {
  goal: Goal;
  item: GoalPlanItem;
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  linkedIssues?: Issue[];
  issueHref?: (issue: Issue) => string;
  disabled?: boolean;
  onChange: (next: PlanContent) => void;
  onItemEdited: (itemId: string) => void;
  onItemRemoved: (itemId: string) => void;
  onEdgeRemoved: (edge: GoalPlanEdge) => void;
  ownerLabel: (item: GoalPlanItem) => string | null;
}) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const { data: methods = [] } = useQuery({
    ...workMethodCatalogOptions(wsId),
    enabled: items.some((item) => !!item.delivery),
  });
  const issuesById = new Map(linkedIssues.map((issue) => [issue.id, issue]));
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

  const removeItem = (id: string) => {
    onItemRemoved(id);
    onChange({
      items: items.filter((item) => item.id !== id),
      edges: edges.filter(
        (edge) => edge.dependent_item_id !== id && edge.prerequisite_item_id !== id,
      ),
    });
  };

  const addEdge = (edge: GoalPlanEdge) => {
    onChange({ items, edges: [...edges, edge] });
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

  const itemById = new Map(items.map((item) => [item.id, item]));
  const prerequisiteIssue = (id: string) => {
    const issueId = itemById.get(id)?.binding.issue_id;
    return issueId ? issuesById.get(issueId) : undefined;
  };
  const itemTitle = (id: string) => {
    const title = itemById.get(id)?.title || t(($) => $.plan.untitled_item);
    const issue = prerequisiteIssue(id);
    return issue ? `${issue.identifier} · ${title}` : title;
  };

  const issue = item.binding.issue_id ? issuesById.get(item.binding.issue_id) : undefined;
  const boundLabel = item.binding.issue_id
    ? (issue?.identifier || issueLabels[item.binding.issue_id] || `#${item.binding.issue_id.slice(0, 8)}`)
    : null;
  const bindingKnown = item.binding.type === "new" || item.binding.type === "existing";

  return (
    <div className="space-y-4" data-testid={`plan-item-${item.id}`}>
      <div className="space-y-1">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor={`plan-title-${item.id}`}>{t(($) => $.plan.task_title)}</Label>
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
        <Input
          id={`plan-title-${item.id}`}
          value={item.title}
          disabled={disabled}
          onChange={(event) => updateItem(item.id, { title: event.target.value })}
        />
      </div>

      <div className={item.delivery ? "space-y-1" : "grid gap-3 @4xl:grid-cols-2"}>
        <div className="space-y-1">
          <Label htmlFor={`plan-outcome-${item.id}`}>{t(($) => $.plan.outcome)}</Label>
          <Textarea
            id={`plan-outcome-${item.id}`}
            value={item.outcome}
            disabled={disabled}
            className="min-h-20 max-h-56 overflow-y-auto"
            onChange={(event) => updateItem(item.id, { outcome: event.target.value })}
          />
        </div>
        {!item.delivery && <div className="space-y-1">
          <Label htmlFor={`plan-acceptance-${item.id}`}>
            {t(($) => $.plan.acceptance_criteria)}
          </Label>
          <Textarea
            id={`plan-acceptance-${item.id}`}
            value={item.acceptance_criteria}
            disabled={disabled}
            className="min-h-20 max-h-56 overflow-y-auto"
            onChange={(event) =>
              updateItem(item.id, { acceptance_criteria: event.target.value })
            }
          />
        </div>}
      </div>

      <PlanMethodStatus
        wsId={wsId}
        issue={issue}
        planned={methods.find(({ id }) => id === item.delivery?.method_id)}
      />
      <PlanDeliveryEditor
        delivery={item.delivery}
        methods={methods}
        disabled={disabled}
        onChange={(delivery) => updateItem(item.id, { delivery })}
      />

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
          <div className="flex flex-wrap items-center gap-1.5">
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
                {issue && issueHref
                  ? <AppLink href={issueHref(issue)}>{boundLabel}</AppLink>
                  : <span className="truncate">{boundLabel}</span>}
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
        <details className="rounded-md border bg-surface/60 px-3 py-2">
          <summary className="cursor-pointer text-label font-medium">
            {t(($) => $.plan.evidence)}
            <span className="ms-2 font-normal tabular-nums text-muted-foreground">{item.evidence_refs.length}</span>
          </summary>
          <ul className="mt-2 space-y-2 text-label text-muted-foreground">
            {item.evidence_refs.map((ref, refIndex) => (
              <li key={`${ref.kind}-${ref.ref}-${refIndex}`} className="break-all">
                <span className="font-medium">{ref.kind}</span> {ref.ref}
                {ref.version ? ` @ ${ref.version}` : ""}
                {ref.note ? ` — ${ref.note}` : ""}
              </li>
            ))}
          </ul>
        </details>
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
        isSatisfied={(id) => {
          const prerequisite = prerequisiteIssue(id);
          return prerequisite?.status_category === "done" || prerequisite?.status === "done";
        }}
        onAdd={addEdge}
        onRemove={removeEdge}
      />


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

function PlanMethodStatus({ wsId, issue, planned }: { wsId: string; issue?: Issue; planned?: WorkMethod }) {
  const { t } = useT("goals");
  const { data: run } = useQuery({
    ...issueWorkMethodOptions(wsId, issue?.id ?? ""),
    enabled: !!issue,
  });
  const selected = run && run.state !== "ready" ? run.method : null;
  const method = selected ?? planned ?? run?.method;
  if (!method) return null;
  const step = selected && run?.state !== "closed" ? run?.method?.steps[run.step]?.name : undefined;
  return (
    <p className="text-caption text-muted-foreground">
      {selected ? t(($) => $.plan.delivery_selected)
        : planned ? t(($) => $.plan.delivery_plan)
        : t(($) => $.plan.delivery_recommended)}
      {" · "}{method.name}
      {selected && ` · v${selected.revision}`}
      {step && ` · ${step}`}
    </p>
  );
}

function DependencyEditor({
  itemId,
  items,
  edges,
  disabled,
  itemTitle,
  isSatisfied,
  onAdd,
  onRemove,
}: {
  itemId: string;
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  disabled: boolean;
  itemTitle: (id: string) => string;
  isSatisfied: (id: string) => boolean;
  onAdd: (edge: GoalPlanEdge) => void;
  onRemove: (edge: GoalPlanEdge) => void;
}) {
  const { t } = useT("goals");
  const [prerequisite, setPrerequisite] = useState("");
  const [reason, setReason] = useState("");
  const [kind, setKind] = useState<NonNullable<GoalPlanEdge["kind"]>>("required");
  const [error, setError] = useState<string | null>(null);

  const incoming = prerequisitesOf(itemId, edges);
  const candidates = items.filter(
    (candidate) =>
      candidate.id !== itemId &&
      !incoming.some((edge) => edge.prerequisite_item_id === candidate.id),
  );
  const options = candidates.map((candidate) => ({
    value: candidate.id,
    label: itemTitle(candidate.id),
  }));

  const submit = () => {
    if (!prerequisite || !reason.trim()) {
      setError(t(($) => $.plan.dependency_required));
      return;
    }
    onAdd({ dependent_item_id: itemId, prerequisite_item_id: prerequisite, reason: reason.trim(), kind });
    setKind("required");
    setPrerequisite("");
    setReason("");
    setError(null);
  };

  return (
    <div className="space-y-2 border-t pt-2">
      <p className="text-caption font-medium">{t(($) => $.plan.dependencies)}</p>
      {incoming.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.plan.dependency_unrecorded)}</p>
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
                {isSatisfied(edge.prerequisite_item_id) && (
                  <Badge variant="outline" className="ml-1">{t(($) => $.plan.dependency_satisfied)}</Badge>
                )}
                {edge.kind === "conditional" && (
                  <Badge variant="outline" className="ml-1">{t(($) => $.plan.dependency_conditional)}</Badge>
                )}
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
          <div className="space-y-1">
            <Label>{t(($) => $.plan.dependency_kind)}</Label>
            <Select
              value={kind}
              disabled={disabled}
              items={[
                { value: "required", label: t(($) => $.plan.dependency_hard) },
                { value: "conditional", label: t(($) => $.plan.dependency_conditional) },
              ]}
              onValueChange={(value) => {
                if (value === "required" || value === "conditional") setKind(value);
              }}
            >
              <SelectTrigger size="sm" aria-label={t(($) => $.plan.dependency_kind)}><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="required">{t(($) => $.plan.dependency_hard)}</SelectItem>
                <SelectItem value="conditional">{t(($) => $.plan.dependency_conditional)}</SelectItem>
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
