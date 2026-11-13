"use client";

import { useEffect, useState, type FormEvent } from "react";
import { toast } from "sonner";
import type { Goal, UpdateGoalRequest } from "@multica/core/types";
import { errorCode } from "@multica/core/api";
import { useUpdateGoal } from "@multica/core/goals";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useActorName } from "@multica/core/workspace/hooks";
import { useT } from "../../i18n";
import { GoalProjectPicker } from "./goal-project-picker";
import { GoalOwnerPicker, type GoalOwnerValue } from "./goal-owner-picker";
import { GoalStatusBadge } from "./goal-status-badge";
import {
  EDITABLE_GOAL_STATUSES,
} from "../status";

const FIELD_CLASS = "space-y-1.5";

export function GoalFormDialog({
  open,
  onOpenChange,
  goal,
  linkedIssueCount = 0,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  goal: Goal;
  /** Server refuses a project change while a goal still has linked tasks. */
  linkedIssueCount?: number;
  onSaved?: (goal: Goal) => void;
}) {
  const { t } = useT("goals");
  const updateGoal = useUpdateGoal();
  const { hasActor } = useActorName();

  const [title, setTitle] = useState("");
  const [intent, setIntent] = useState("");
  const [scope, setScope] = useState("");
  const [acceptance, setAcceptance] = useState("");
  const [projectId, setProjectId] = useState<string | null>(null);
  const [owner, setOwner] = useState<GoalOwnerValue>({ type: null, id: null });
  const [status, setStatus] = useState<string>("planned");
  const [titleError, setTitleError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);

  // Re-seed whenever the dialog opens for a different goal. Closing keeps the
  // last input on purpose: a failed save must not erase what was typed.
  useEffect(() => {
    if (!open) return;
    setTitle(goal.title);
    setIntent(goal.intent);
    setScope(goal.scope);
    setAcceptance(goal.acceptance_criteria);
    setProjectId(goal.project_id);
    setOwner({ type: goal.owner_type, id: goal.owner_id });
    setStatus(goal.status);
    setTitleError(null);
    setSubmitError(null);
  }, [open, goal]);

  const pending = updateGoal.isPending;
  // Completed goals change status only through the detail page's confirmed
  // action. A status this client does not know renders read-only too, so an
  // edit can never send an unrecognized value back to the server.
  const statusEditable =
    EDITABLE_GOAL_STATUSES.some((value) => value === goal.status);
  const projectLocked = linkedIssueCount > 0;
  // Clearing the project opens the goal to the whole workspace. Warn before
  // the save, since the post-save list refresh cannot undo the decision.
  const movingToWorkspace = !!goal.project_id && projectId === null;

  const statusItems = EDITABLE_GOAL_STATUSES.map((value) => ({
    value,
    label: t(($) => $.status[value]),
  }));

  const ownerUnavailable = !!(
    owner.type &&
    owner.id &&
    hasActor(owner.type, owner.id) === false
  );

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (pending) return;
    const trimmed = title.trim();
    if (!trimmed) {
      setTitleError(t(($) => $.form.title_required));
      return;
    }
    setTitleError(null);
    setSubmitError(null);

    try {
      const changes: UpdateGoalRequest = {
        title: trimmed,
        intent,
        scope,
        acceptance_criteria: acceptance,
        project_id: projectId,
        owner_type: owner.type,
        owner_id: owner.id,
      };
      const nextStatus = EDITABLE_GOAL_STATUSES.find((value) => value === status);
      if (statusEditable && nextStatus) changes.status = nextStatus;
      const saved = await updateGoal.mutateAsync({ id: goal.id, ...changes });
      toast.success(t(($) => $.toast.updated));
      onOpenChange(false);
      onSaved?.(saved);
    } catch (err) {
      setSubmitError(
        errorCode(err) === "goal_has_linked_issues"
          ? t(($) => $.form.project_locked)
          : t(($) => $.form.save_failed),
      );
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!pending) onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {t(($) => $.form.edit_title)}
          </DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-3">
          <div className={FIELD_CLASS}>
            <Label htmlFor="goal-title">{t(($) => $.form.title)}</Label>
            <Input
              id="goal-title"
              value={title}
              autoFocus
              aria-invalid={!!titleError}
              placeholder={t(($) => $.form.title_placeholder)}
              onChange={(e) => {
                setTitle(e.target.value);
                if (titleError) setTitleError(null);
              }}
            />
            {titleError && <p className="text-caption text-destructive">{titleError}</p>}
          </div>

          <div className={FIELD_CLASS}>
            <Label htmlFor="goal-intent">{t(($) => $.form.intent)}</Label>
            <Textarea
              id="goal-intent"
              value={intent}
              rows={2}
              onChange={(e) => setIntent(e.target.value)}
            />
          </div>

          <div className={FIELD_CLASS}>
            <Label htmlFor="goal-scope">{t(($) => $.form.scope)}</Label>
            <Textarea
              id="goal-scope"
              value={scope}
              rows={2}
              onChange={(e) => setScope(e.target.value)}
            />
          </div>

          <div className={FIELD_CLASS}>
            <Label htmlFor="goal-acceptance">{t(($) => $.form.acceptance_criteria)}</Label>
            <Textarea
              id="goal-acceptance"
              value={acceptance}
              rows={3}
              onChange={(e) => setAcceptance(e.target.value)}
            />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className={FIELD_CLASS}>
              <Label>{t(($) => $.form.project)}</Label>
              <GoalProjectPicker
                projectId={projectId}
                onChange={setProjectId}
                disabled={projectLocked}
              />
              {projectLocked && (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.form.project_locked)}
                </p>
              )}
              {movingToWorkspace && (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.detail.project_moved_scope)}
                </p>
              )}
            </div>

            <div className={FIELD_CLASS}>
              <Label>{t(($) => $.form.owner)}</Label>
              <GoalOwnerPicker value={owner} onChange={setOwner} />
              {ownerUnavailable && (
                <p className="text-caption text-warning">
                  {t(($) => $.form.owner_cleared)}
                </p>
              )}
            </div>
          </div>

          <div className={FIELD_CLASS}>
            <Label>{t(($) => $.form.status)}</Label>
            {statusEditable ? (
              <Select
                items={statusItems}
                value={status}
                onValueChange={(next) => next && setStatus(next)}
              >
                <SelectTrigger size="sm" className="w-full">
                  <SelectValue>
                    {statusItems.find((item) => item.value === status)?.label}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {statusItems.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {t(($) => $.status[item.value])}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <GoalStatusBadge status={goal.status} className="w-fit" />
            )}
          </div>

          {submitError && (
            <p role="alert" className="text-body text-destructive">
              {submitError}
            </p>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => onOpenChange(false)}
            >
              {t(($) => $.form.cancel)}
            </Button>
            <Button type="submit" disabled={pending} aria-busy={pending}>
              {pending && <Spinner className="size-3.5" aria-hidden="true" />}
              {pending ? t(($) => $.form.saving) : t(($) => $.form.save)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}