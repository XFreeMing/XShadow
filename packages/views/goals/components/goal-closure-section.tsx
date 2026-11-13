"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type {
  ClosureAuthorization,
  ClosureDecision,
  ClosureReview,
  ClosureState,
  Goal,
} from "@multica/core/types";
import {
  goalClosureOptions,
  useGrantGoalClosureAuthorization,
  useRejectGoalClosure,
  useUpdateGoal,
} from "@multica/core/goals";
import { useWorkspaceId } from "@multica/core/hooks";
import { useActorName } from "@multica/core/workspace/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { CircleAlert } from "@multica/ui/icons";
import { useT } from "../../i18n";
import { useTimeAgo } from "../../i18n/use-time-ago";
import { isGoalUnavailableError } from "../status";
import {
  DECISION_COPY_KEY,
  OUTCOME_COPY_KEY,
  OUTCOME_HINT_KEY,
  REASON_COPY_KEY,
  VERDICT_COPY_KEY,
  closureOutcomeDisplay,
  closureOutcomeTone,
  closureReasonGroup,
  closureToneClass,
  decisionKindDisplay,
  decisionTone,
  isClosureAcceptable,
  isSupersededReview,
  parseClosureRefusal,
  reviewVerdictDisplay,
  verdictTone,
  type ClosureRefusal,
} from "../closure";

const ROW_CLASS = "flex flex-wrap items-baseline gap-x-2 gap-y-1 text-caption";
const CODE_CLASS = "break-all font-mono text-caption";

function ReviewRow({ review }: { review: ClosureReview }) {
  const { t } = useT("goals");
  const { getActorName } = useActorName();
  const timeAgo = useTimeAgo();
  const verdict = reviewVerdictDisplay(review.verdict);
  const passed = review.criteria.filter((criterion) => criterion.result === "pass").length;
  const reviewer =
    review.reviewer_id && review.reviewer_type
      ? getActorName(review.reviewer_type, review.reviewer_id)
      : "";
  const acceptedBy =
    review.accepted_by_id && review.accepted_by_type
      ? getActorName(review.accepted_by_type, review.accepted_by_id)
      : "";

  return (
    <li className="space-y-1 rounded-md border px-3 py-2" data-testid="closure-review">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline" className={closureToneClass(verdictTone(review.verdict))}>
          {t(($) => $.closure[VERDICT_COPY_KEY[verdict]])}
        </Badge>
        {isSupersededReview(review) ? (
          <Badge variant="outline" className="text-muted-foreground">
            {t(($) => $.closure.review_superseded)}
          </Badge>
        ) : null}
        {review.accepted_at ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.closure.review_accepted)} · {timeAgo(review.accepted_at)}
          </span>
        ) : null}
        <span className="min-w-0 flex-1 truncate text-caption text-muted-foreground">
          {review.delivery_version || t(($) => $.closure.review_no_delivery)}
        </span>
      </div>
      <div className={ROW_CLASS}>
        <span className="text-muted-foreground">{t(($) => $.closure.review_reviewer)}</span>
        <span>{reviewer || review.reviewer_type || t(($) => $.closure.review_unknown_reviewer)}</span>
        {review.reviewer_model ? (
          <span className={CODE_CLASS}>{review.reviewer_model}</span>
        ) : null}
        {review.recorded_at ? (
          <span className="text-muted-foreground">{timeAgo(review.recorded_at)}</span>
        ) : null}
      </div>
      {review.criteria_revision ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.review_scope)}</span>
          <code className={CODE_CLASS}>{review.criteria_revision}</code>
        </div>
      ) : null}
      {review.criteria.length > 0 ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.review_criteria)}</span>
          <span>
            {t(($) => $.closure.review_criteria_results, {
              passed,
              total: review.criteria.length,
            })}
          </span>
        </div>
      ) : null}
      {review.missing.length > 0 ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.review_missing)}</span>
          <span>{review.missing.join(", ")}</span>
        </div>
      ) : null}
      {review.platforms.length > 0 ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.review_platforms)}</span>
          <span>{review.platforms.join(", ")}</span>
        </div>
      ) : null}
      {review.reason ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.review_reason)}</span>
          <span className="whitespace-pre-wrap">{review.reason}</span>
        </div>
      ) : null}
      {acceptedBy ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.review_accepted_by)}</span>
          <span>{acceptedBy}</span>
        </div>
      ) : null}
    </li>
  );
}

function AuthorizationRow({ authorization }: { authorization: ClosureAuthorization }) {
  const { t } = useT("goals");
  const state =
    authorization.state === "active" || authorization.state === "revoked"
      ? authorization.state
      : "unknown";
  return (
    <div className="space-y-1 rounded-md bg-muted/50 px-3 py-2" data-testid="closure-authorization">
      <div className={ROW_CLASS}>
        <span className="text-muted-foreground">{t(($) => $.closure.authorization_state)}</span>
        <Badge
          variant="outline"
          className={
            state === "active"
              ? "border-success/40 text-success"
              : "text-muted-foreground"
          }
        >
          {state === "active"
            ? t(($) => $.closure.authorization_state_active)
            : state === "revoked"
              ? t(($) => $.closure.authorization_state_revoked)
              : t(($) => $.closure.authorization_state_unknown)}
        </Badge>
        {authorization.covers_children ? (
          <span className="text-muted-foreground">
            {t(($) => $.closure.authorization_covers_children)}
          </span>
        ) : null}
      </div>
      <div className={ROW_CLASS}>
        <span className="text-muted-foreground">{t(($) => $.closure.authorization_actions)}</span>
        <span>{authorization.actions.join(", ") || t(($) => $.closure.criteria_none)}</span>
      </div>
      {authorization.criteria_revision ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.authorization_criteria)}</span>
          <code className={CODE_CLASS}>{authorization.criteria_revision}</code>
        </div>
      ) : null}
      {authorization.source_ref ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.authorization_source)}</span>
          <span className="whitespace-pre-wrap">{authorization.source_ref}</span>
        </div>
      ) : null}
      {authorization.expires_at ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.authorization_expires)}</span>
          <span>{authorization.expires_at}</span>
        </div>
      ) : null}
    </div>
  );
}

/**
 * One human accept/reject. Rendered apart from reviews on purpose: an accept
 * is a person's decision, not independent verification, and it never feeds the
 * server's evaluated outcome.
 */
function DecisionRow({ decision }: { decision: ClosureDecision }) {
  const { t } = useT("goals");
  const { getActorName } = useActorName();
  const timeAgo = useTimeAgo();
  const kind = decisionKindDisplay(decision.decision);
  const actor = decision.actor_type
    ? getActorName(decision.actor_type, decision.actor_id ?? "")
    : "";

  return (
    <li className="space-y-1 rounded-md border px-3 py-2" data-testid="closure-decision">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline" className={closureToneClass(decisionTone(decision.decision))}>
          {t(($) => $.closure[DECISION_COPY_KEY[kind]])}
        </Badge>
        {decision.recorded_at ? (
          <span className="text-caption text-muted-foreground">{timeAgo(decision.recorded_at)}</span>
        ) : null}
      </div>
      <div className={ROW_CLASS}>
        <span className="text-muted-foreground">{t(($) => $.closure.decision_actor)}</span>
        <span>
          {actor || decision.actor_type || t(($) => $.closure.review_unknown_reviewer)}
        </span>
      </div>
      {decision.reason ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.decision_reason)}</span>
          <span className="whitespace-pre-wrap">{decision.reason}</span>
        </div>
      ) : null}
      {decision.criteria_revision ? (
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.decision_scope)}</span>
          <code className={CODE_CLASS}>{decision.criteria_revision}</code>
        </div>
      ) : null}
    </li>
  );
}

/**
 * The goal's acceptance surface. Every verdict shown here comes from the
 * server's closure read model; the panel never derives readiness from linked
 * task statuses, run exits or the absence of a scheduled time. Accepting is
 * still the person's explicit decision, so it is always confirmed in a dialog
 * and a refusal is displayed with the server's own reason.
 */
export function GoalClosureSection({ goal }: { goal: Goal }) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const { hasActor, getActorName } = useActorName();
  const closureQuery = useQuery(goalClosureOptions(wsId, goal.id));
  const updateGoal = useUpdateGoal();
  const grantAuthorization = useGrantGoalClosureAuthorization();
  const rejectGoal = useRejectGoalClosure();

  // A degraded reply (or a cached payload for another goal) is not a decision.
  const closure: ClosureState | null =
    closureQuery.data && closureQuery.data.subject_id === goal.id ? closureQuery.data : null;
  const degraded = !!closureQuery.data && !closure;

  const [acceptOpen, setAcceptOpen] = useState(false);
  const [rejectOpen, setRejectOpen] = useState(false);
  const [grantOpen, setGrantOpen] = useState(false);
  const [refusal, setRefusal] = useState<ClosureRefusal | null>(null);
  const [acceptError, setAcceptError] = useState<string | null>(null);
  const [rejectReason, setRejectReason] = useState("");
  const [rejectError, setRejectError] = useState<string | null>(null);
  const [grantSource, setGrantSource] = useState("");
  const [grantCoversChildren, setGrantCoversChildren] = useState(false);
  const [grantError, setGrantError] = useState<string | null>(null);

  const completed = goal.status === "completed";
  const cancelled = goal.status === "cancelled";
  const outcome = closureOutcomeDisplay(closure?.outcome ?? "");
  const acceptable = isClosureAcceptable(closure?.outcome ?? "");
  const reasonGroup = closureReasonGroup(closure?.reason ?? "");
  const nextActor = closure?.next_actor
    ? (["member", "agent", "squad"].find(
        (type) => hasActor(type, closure.next_actor),
      ) ?? null)
    : null;
  const nextActorName = nextActor ? getActorName(nextActor, closure?.next_actor ?? "") : null;

  const resetAcceptError = () => setAcceptError(null);

  const handleAccept = () => {
    setAcceptError(null);
    setRefusal(null);
    updateGoal.mutate(
      { id: goal.id, status: "completed" },
      {
        onSuccess: () => {
          toast.success(t(($) => $.closure.accepted));
          setAcceptOpen(false);
        },
        onError: (error) => {
          const refused = parseClosureRefusal(error);
          if (refused) {
            // The write did not happen. Show the server's decision instead of
            // a generic failure, and keep the goal open.
            setRefusal(refused);
            setAcceptOpen(false);
            return;
          }
          setAcceptError(t(($) => $.closure.accept_failed));
        },
      },
    );
  };

  /**
   * A refusal is a human decision: it needs a reason, not a delivery version,
   * and it writes no review. The server keeps the goal open and the evaluated
   * outcome unchanged, so nothing here is shown as accepted.
   */
  const handleReject = async () => {
    const reason = rejectReason.trim();
    if (!reason) {
      setRejectError(t(($) => $.closure.reject_needs_reason));
      return;
    }
    setRejectError(null);
    try {
      await rejectGoal.mutateAsync({ goalId: goal.id, data: { reason } });
      toast.success(t(($) => $.closure.rejected));
      setRejectOpen(false);
      setRejectReason("");
    } catch {
      setRejectError(t(($) => $.closure.reject_failed));
    }
  };

  const handleGrant = async () => {
    if (!closure) return;
    setGrantError(null);
    try {
      const grant = await grantAuthorization.mutateAsync({
        goalId: goal.id,
        data: {
          actions: ["goal_accept"],
          covers_children: grantCoversChildren,
          criteria_revision: closure.criteria_revision || undefined,
          source_ref: grantSource.trim() || undefined,
        },
      });
      if (!grant.id) throw new Error("The server did not confirm the authorization.");
      toast.success(t(($) => $.closure.granted));
      setGrantOpen(false);
      setGrantSource("");
      setGrantCoversChildren(false);
    } catch {
      setGrantError(t(($) => $.closure.grant_failed));
    }
  };

  const header = (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="flex items-baseline gap-2">
        <h2 className="text-title-sm font-semibold">{t(($) => $.closure.title)}</h2>
        {completed ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.closure.completed_hint)}
          </span>
        ) : null}
      </div>
      {closure && !completed && !cancelled ? (
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => setRejectOpen(true)}>
            {t(($) => $.closure.reject)}
          </Button>
          <Button size="sm" onClick={() => setAcceptOpen(true)}>
            {t(($) => $.closure.accept)}
          </Button>
        </div>
      ) : null}
    </div>
  );

  if (closureQuery.isLoading) {
    return (
      <section className="space-y-3" data-testid="goal-closure-section">
        {header}
        <Skeleton className="h-24 w-full" />
      </section>
    );
  }

  if (!closure) {
    const unavailable = closureQuery.isError && isGoalUnavailableError(closureQuery.error);
    return (
      <section className="space-y-2" data-testid="goal-closure-section">
        <h2 className="text-title-sm font-semibold">{t(($) => $.closure.title)}</h2>
        <div
          className="flex flex-wrap items-center gap-2 text-caption text-destructive"
          role="alert"
          data-testid="closure-unavailable"
        >
          <span>
            {unavailable
              ? t(($) => $.closure.unavailable)
              : degraded
                ? t(($) => $.closure.degraded)
                : t(($) => $.closure.load_failed)}
          </span>
          <Button size="sm" variant="outline" onClick={() => void closureQuery.refetch()}>
            {t(($) => $.closure.retry)}
          </Button>
        </div>
      </section>
    );
  }

  const accepted = closure.already_accepted || completed;

  return (
    <section className="space-y-3" data-testid="goal-closure-section">
      {header}

      <p className="text-caption text-muted-foreground">{t(($) => $.closure.description)}</p>

      {closureQuery.isRefetchError ? (
        <div
          className="flex flex-wrap items-center gap-2 text-caption text-destructive"
          role="alert"
          data-testid="closure-stale"
        >
          <span>{t(($) => $.closure.stale)}</span>
          <Button size="sm" variant="outline" onClick={() => void closureQuery.refetch()}>
            {t(($) => $.closure.retry)}
          </Button>
        </div>
      ) : null}

      {cancelled ? (
        <p
          className="rounded-md bg-muted px-3 py-2 text-caption text-muted-foreground"
          data-testid="closure-cancelled"
        >
          {t(($) => $.closure.cancelled_hint)}
        </p>
      ) : null}

      <div className="space-y-1 rounded-md border px-3 py-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-caption text-muted-foreground">
            {t(($) => $.closure.outcome_label)}
          </span>
          <Badge
            variant="outline"
            className={closureToneClass(closureOutcomeTone(closure.outcome))}
            data-testid="closure-outcome"
          >
            {t(($) => $.closure[OUTCOME_COPY_KEY[outcome]])}
          </Badge>
          {accepted ? (
            <Badge variant="outline" className="border-success/40 text-success">
              {t(($) => $.closure.review_accepted)}
            </Badge>
          ) : null}
          {closure.managed ? (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.closure.managed_yes)}
            </span>
          ) : (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.closure.managed_no)}
            </span>
          )}
        </div>
        <p className="text-caption text-muted-foreground">
          {t(($) => $.closure[OUTCOME_HINT_KEY[outcome]])}
        </p>
        {closure.reason ? (
          <p className="text-caption" data-testid="closure-reason">
            {t(($) => $.closure[REASON_COPY_KEY[reasonGroup]])}
          </p>
        ) : null}
        {closure.detail ? (
          <div className={ROW_CLASS}>
            <span className="text-muted-foreground">{t(($) => $.closure.detail_label)}</span>
            <span className="break-words">{closure.detail}</span>
          </div>
        ) : null}
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.delivery)}</span>
          {closure.delivery_version ? (
            <code className={CODE_CLASS}>{closure.delivery_version}</code>
          ) : (
            <span className="text-muted-foreground">{t(($) => $.closure.delivery_none)}</span>
          )}
        </div>
        <div className={ROW_CLASS}>
          <span className="text-muted-foreground">{t(($) => $.closure.criteria)}</span>
          {closure.criteria_revision ? (
            <code className={CODE_CLASS}>{closure.criteria_revision}</code>
          ) : (
            <span className="text-muted-foreground">{t(($) => $.closure.criteria_none)}</span>
          )}
        </div>
        {closure.next_actor ? (
          <div className={ROW_CLASS}>
            <span className="text-muted-foreground">{t(($) => $.closure.next_actor_label)}</span>
            <span>
              {nextActorName ?? t(($) => $.closure.next_actor_fallback)}
            </span>
          </div>
        ) : null}
      </div>

      {refusal ? (
        <div
          className="space-y-1 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2"
          role="alert"
          data-testid="closure-refusal"
        >
          <p className="flex items-center gap-1.5 text-body font-medium">
            <CircleAlert className="size-4 text-destructive" />
            {t(($) => $.closure.accept_refused)}
          </p>
          <p className="text-caption">
            {t(($) => $.closure[OUTCOME_COPY_KEY[closureOutcomeDisplay(refusal.outcome)]])} ·{" "}
            {t(($) => $.closure[REASON_COPY_KEY[closureReasonGroup(refusal.reason)]])}
          </p>
          {refusal.detail ? <p className="text-caption text-muted-foreground">{refusal.detail}</p> : null}
        </div>
      ) : null}

      <div className="space-y-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-body font-medium">{t(($) => $.closure.authorization_title)}</h3>
          {!completed && !cancelled ? (
            <Button size="sm" variant="outline" onClick={() => setGrantOpen(true)}>
              {t(($) => $.closure.grant)}
            </Button>
          ) : null}
        </div>
        {closure.authorization ? (
          <AuthorizationRow authorization={closure.authorization} />
        ) : (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.closure.authorization_none)}
          </p>
        )}
      </div>

      <div className="space-y-2">
        <h3 className="text-body font-medium">{t(($) => $.closure.decisions_title)}</h3>
        <p className="text-caption text-muted-foreground">{t(($) => $.closure.decisions_note)}</p>
        {closure.decisions.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.closure.decisions_empty)}</p>
        ) : (
          <ul className="space-y-2">
            {closure.decisions.map((decision) => (
              <DecisionRow key={decision.id} decision={decision} />
            ))}
          </ul>
        )}
      </div>

      <div className="space-y-2">
        <h3 className="text-body font-medium">{t(($) => $.closure.reviews_title)}</h3>
        <p className="text-caption text-muted-foreground">{t(($) => $.closure.reviews_note)}</p>
        {closure.reviews.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.closure.reviews_empty)}</p>
        ) : (
          <ul className="space-y-2">
            {closure.reviews.map((review) => (
              <ReviewRow key={review.id} review={review} />
            ))}
          </ul>
        )}
      </div>

      <AlertDialog
        open={acceptOpen}
        onOpenChange={(open) => {
          if (!updateGoal.isPending) {
            setAcceptOpen(open);
            if (!open) resetAcceptError();
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.closure.accept_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.closure.accept_description)}</AlertDialogDescription>
          </AlertDialogHeader>
          {!acceptable ? (
            <div className="space-y-1 rounded-md border border-warning/40 bg-warning/5 px-3 py-2">
              <p className="text-caption">
                {t(($) => $.closure.accept_readiness, {
                  outcome: t(($) => $.closure[OUTCOME_COPY_KEY[outcome]]),
                })}
              </p>
              <p className="text-caption">{t(($) => $.closure[REASON_COPY_KEY[reasonGroup]])}</p>
              {closure.detail ? (
                <p className="text-caption text-muted-foreground">{closure.detail}</p>
              ) : null}
              <p className="text-caption">{t(($) => $.closure.accept_warning)}</p>
            </div>
          ) : null}
          {acceptError ? (
            <p className="text-caption text-destructive" role="alert">
              {acceptError}
            </p>
          ) : null}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={updateGoal.isPending}>
              {t(($) => $.form.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={updateGoal.isPending}
              aria-busy={updateGoal.isPending}
              onClick={handleAccept}
            >
              {updateGoal.isPending ? <Spinner className="size-3.5" aria-hidden="true" /> : null}
              {updateGoal.isPending
                ? t(($) => $.closure.accepting)
                : acceptable
                  ? t(($) => $.closure.accept_action)
                  : t(($) => $.closure.accept_anyway)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Dialog
        open={rejectOpen}
        onOpenChange={(open) => {
          if (!rejectGoal.isPending) {
            setRejectOpen(open);
            if (!open) setRejectError(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.closure.reject_title)}</DialogTitle>
            <DialogDescription>{t(($) => $.closure.reject_description)}</DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="goal-reject-reason">{t(($) => $.closure.reject_reason)}</Label>
            <Textarea
              id="goal-reject-reason"
              value={rejectReason}
              onChange={(event) => setRejectReason(event.target.value)}
              placeholder={t(($) => $.closure.reject_reason_placeholder)}
              rows={3}
            />
          </div>
          {rejectError ? (
            <p className="text-caption text-destructive" role="alert">
              {rejectError}
            </p>
          ) : null}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={rejectGoal.isPending}
              onClick={() => setRejectOpen(false)}
            >
              {t(($) => $.form.cancel)}
            </Button>
            <Button
              variant="destructive"
              disabled={rejectGoal.isPending}
              aria-busy={rejectGoal.isPending}
              onClick={() => void handleReject()}
            >
              {rejectGoal.isPending ? <Spinner className="size-3.5" aria-hidden="true" /> : null}
              {rejectGoal.isPending
                ? t(($) => $.closure.rejecting)
                : t(($) => $.closure.reject_action)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={grantOpen}
        onOpenChange={(open) => {
          if (!grantAuthorization.isPending) {
            setGrantOpen(open);
            if (!open) setGrantError(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.closure.grant_title)}</DialogTitle>
            <DialogDescription>{t(($) => $.closure.grant_description)}</DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="goal-grant-source">{t(($) => $.closure.grant_source)}</Label>
            <Input
              id="goal-grant-source"
              value={grantSource}
              onChange={(event) => setGrantSource(event.target.value)}
              placeholder={t(($) => $.closure.grant_source_placeholder)}
            />
          </div>
          <label className="flex items-start gap-2 text-caption">
            <Checkbox
              checked={grantCoversChildren}
              onCheckedChange={(checked) => setGrantCoversChildren(checked === true)}
            />
            <span>{t(($) => $.closure.grant_covers_children)}</span>
          </label>
          <div className={ROW_CLASS}>
            <span className="text-muted-foreground">{t(($) => $.closure.authorization_criteria)}</span>
            {closure.criteria_revision ? (
              <code className={CODE_CLASS}>{closure.criteria_revision}</code>
            ) : (
              <span className="text-muted-foreground">{t(($) => $.closure.criteria_none)}</span>
            )}
          </div>
          {grantError ? (
            <p className="text-caption text-destructive" role="alert">
              {grantError}
            </p>
          ) : null}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={grantAuthorization.isPending}
              onClick={() => setGrantOpen(false)}
            >
              {t(($) => $.form.cancel)}
            </Button>
            <Button
              disabled={grantAuthorization.isPending}
              aria-busy={grantAuthorization.isPending}
              onClick={() => void handleGrant()}
            >
              {grantAuthorization.isPending ? (
                <Spinner className="size-3.5" aria-hidden="true" />
              ) : null}
              {grantAuthorization.isPending
                ? t(($) => $.closure.granting)
                : t(($) => $.closure.grant_action)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}