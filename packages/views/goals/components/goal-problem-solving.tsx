"use client";

import { useId, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError, errorCode } from "@multica/core/api";
import { goalProblemSolvingOptions, useChangeGoalProblemSolving, type GoalProblemAction, type GoalProblemState } from "@multica/core/goals";
import type { Issue } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import {
  canOpenAction, currentRevision, problemActions, problemDomains, readProblemForm, type ProblemDomain,
} from "./goal-problem-actions";

type Dictionary = Readonly<Record<string, string>>;
function nameOf(labels: Dictionary, key: string) {
  return labels[key] ?? key;
}

type RecordEntry = {
  key: string;
  title: string;
  status?: string;
  pages: { key: string; title: string; notes: string[] }[];
  issueID?: string;
};

function problemRecords(state: GoalProblemState, currentResults: Set<string>, versions: string) {
  const revisionLabel = (revision: number) => `${versions} ${revision}`;
  return {
    problem: state.problems.map((item): RecordEntry => ({
      key: item.id, title: currentRevision(item)?.statement ?? item.id, status: currentRevision(item)?.status,
      pages: item.revisions.map((page) => ({
        key: String(page.revision), title: revisionLabel(page.revision),
        notes: [page.statement, page.baseline, page.success_criterion, page.reopen_reason, ...page.affected_users, ...page.constraints],
      })),
    })),
    decomposition: [
      ...state.branches.map((item): RecordEntry => ({
        key: item.id, title: currentRevision(item)?.title ?? item.id, status: item.status,
        pages: [
          ...item.revisions.map((page) => ({
            key: String(page.revision), title: revisionLabel(page.revision),
            notes: [page.scope, page.boundary, page.rationale, page.change_reason],
          })),
          ...item.reassessments.map((entry) => ({
            key: entry.challenged_by_result_id, title: entry.challenge_reason,
            notes: [entry.resolution_rationale, entry.reopening_condition],
          })),
        ],
      })),
      ...state.hypotheses.map((item): RecordEntry => ({
        key: item.id, title: currentRevision(item)?.statement ?? item.id,
        pages: item.revisions.map((page) => ({
          key: String(page.revision), title: revisionLabel(page.revision),
          notes: [page.statement, page.falsification_criterion, page.change_reason, ...page.assumptions],
        })),
      })),
    ],
    priority: state.prioritizations.map((item): RecordEntry => ({
      key: item.id, title: item.rationale, status: item.disposition,
      pages: [{ key: item.id, title: revisionLabel(item.branch_ref.revision), notes: [item.reopening_condition] }],
    })),
    validation: [
      ...state.protocols.map((item): RecordEntry => ({
        key: item.id, title: currentRevision(item)?.predicted_result ?? item.id, status: currentRevision(item)?.status,
        pages: item.revisions.map((page) => ({
          key: String(page.revision), title: revisionLabel(page.revision),
          notes: [page.sample_source, page.control_or_comparison, page.decision_threshold, page.stop_condition, page.change_reason],
        })),
      })),
      ...state.validation_batches.map((item): RecordEntry => ({
        key: item.id, issueID: item.task.issue_id, title: item.id, status: item.status, pages: [],
      })),
    ],
    analysis: state.validation_results.map((item): RecordEntry => ({
      key: item.id, title: item.observations[0] ?? item.id,
      status: item.review && !currentResults.has(item.id) ? "historical" : item.review?.verdict,
      pages: [
        { key: item.id, title: revisionLabel(item.protocol_ref.revision), notes: [...item.observations, item.review?.rationale ?? ""] },
        ...item.evidence.map((receipt) => ({ key: receipt.id, title: receipt.summary, notes: [receipt.locator || receipt.issue_id] })),
      ],
    })),
    synthesis: state.syntheses.map((item): RecordEntry => ({
      key: item.id, title: currentRevision(item)?.rationale ?? item.id, status: currentRevision(item)?.status,
      pages: item.revisions.map((page) => ({
        key: String(page.revision), title: revisionLabel(page.revision),
        notes: [...page.claims.map((claim) => claim.statement), ...page.tradeoffs, ...page.limitations,
          ...page.challenged_by.map((challenge) => challenge.reason), page.review_notes, page.reopen_reason],
      })),
    })),
    decision: [
      ...state.proposals.map((item): RecordEntry => ({
        key: item.id, title: item.change_summary, status: item.status,
        pages: [{ key: item.id, title: revisionLabel(item.base_goal_version),
          notes: [item.proposed_charter.title, item.proposed_charter.intent, item.proposed_charter.scope, ...item.proposed_charter.acceptance_criteria] }],
      })),
      ...state.decisions.map((item): RecordEntry => ({
        key: item.id, title: item.reason, status: item.kind,
        pages: [{ key: item.id, title: revisionLabel(item.goal_version), notes: [item.review_trigger] }],
      })),
      ...state.goal_versions.toReversed().map((item): RecordEntry => ({
        key: `goal-version-${item.number}`, title: `${revisionLabel(item.number)} · ${item.charter.title}`,
        pages: [{ key: String(item.number), title: item.charter.title, notes: [
          item.charter.intent, item.charter.scope, ...item.charter.acceptance_criteria,
        ] }],
      })),
    ],
  } satisfies Record<ProblemDomain, RecordEntry[]>;
}

export function GoalProblemSolvingSection({
  goalId, wsId, linkedIssues, issueHref, onLinkTask,
}: {
  goalId: string;
  wsId: string;
  linkedIssues: Issue[];
  issueHref: (issueID: string) => string;
  onLinkTask: () => void;
}) {
  const { t } = useT("goals");
  const labels = t(($) => $.problem_solving, { returnObjects: true });
  const { hash } = useNavigation();
  const formID = useId();
  const [open, setOpen] = useState(hash === "#goal-problem-solving");
  const [domain, setDomain] = useState<ProblemDomain>("problem");
  const [draft, setDraft] = useState<{ action: GoalProblemAction; revision: number } | null>(null);
  const query = useQuery(goalProblemSolvingOptions(wsId, goalId, open));
  const change = useChangeGoalProblemSolving();
  const snapshot = query.data;
  const actions = snapshot ? problemActions(snapshot, linkedIssues)[domain] : [];
  const records = snapshot ? problemRecords(snapshot.state, new Set(snapshot.current_result_ids), labels.version)[domain] : [];
  const active = actions.find((item) => item.action === draft?.action);
  const stale = !!draft && !!snapshot && draft.revision !== snapshot.revision;

  function choose(next: GoalProblemAction | null) {
    change.reset();
    setDraft(next && snapshot ? { action: next, revision: snapshot.revision } : null);
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!active || !draft || stale) return;
    const { new_task_title, ...fields } = readProblemForm(event.currentTarget, active);
    change.mutate({
      id: goalId, revision: draft.revision, action: active.action, fields,
      newTaskTitle: typeof new_task_title === "string" ? new_task_title : undefined,
    }, { onSuccess: () => choose(null) });
  }

  const error = change.error instanceof ApiError && change.error.status < 500 ?
    errorCode(change.error) === "goal_problem_revision_conflict" ? labels.conflict : change.error.message :
    labels.save_failed;
  return (
    <section id="goal-problem-solving" aria-label={labels.title} className="space-y-3 rounded-lg border bg-card px-4 py-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-baseline gap-2">
          <h2 className="text-title-sm font-medium">{labels.title}</h2>
          {snapshot && <span className="text-caption text-muted-foreground">
            {labels.version} {snapshot.state.goal_versions.at(-1)?.number}
          </span>}
        </div>
        <Button type="button" size="sm" variant="outline" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? labels.close : labels.open}
        </Button>
      </div>
      {open && query.isLoading && <Spinner className="size-4" role="status" aria-label={labels.loading} />}
      {open && (query.isError || (!query.isPending && !snapshot)) && <div className="flex flex-wrap items-center gap-2">
        <p role="alert" className="text-body text-muted-foreground">{labels.load_failed}</p>
        <Button size="sm" variant="outline" onClick={() => void query.refetch()}>{t(($) => $.list.retry)}</Button>
      </div>}
      {open && snapshot && !query.isError && <>
        <div role="group" aria-label={labels.domains_label} className="flex flex-wrap gap-1">
          {problemDomains.map((item) => <Button key={item} size="sm" type="button"
            variant={domain === item ? "secondary" : "ghost"} aria-pressed={domain === item}
            disabled={change.isPending} onClick={() => { setDomain(item); choose(null); }}>
            {labels.domains[item]}
          </Button>)}
        </div>
        {records.length === 0 && <p className="text-caption text-muted-foreground">{labels.empty}</p>}
        <div className="divide-y">
          {records.map((item) => (
            <div key={item.key} className="space-y-1 py-2">
              <div className="flex flex-wrap items-baseline gap-x-2">
                {item.issueID ? <AppLink className="text-body font-medium underline" href={issueHref(item.issueID)}>
                  {linkedIssues.find((issue) => issue.id === item.issueID)?.title ?? labels.verification_task}
                </AppLink> : <p className="text-body font-medium">{item.title}</p>}
                {item.status && <span className="text-caption text-muted-foreground">{nameOf(labels.values, item.status)}</span>}
              </div>
              {item.pages.length > 0 && <details className="text-caption">
                <summary className="cursor-pointer text-muted-foreground">{labels.details}</summary>
                {item.pages.map((page) => <div className="space-y-1 py-1" key={page.key}>
                  <h3 className="font-medium">{page.title}</h3>
                  {page.notes.filter(Boolean).map((note, index) => <p key={index} className="whitespace-pre-wrap break-words">{note}</p>)}
                </div>)}
              </details>}
            </div>
          ))}
        </div>
        <div className="flex flex-wrap gap-1">
          {actions.filter(canOpenAction).map((action) =>
            <Button key={action.action} type="button" size="sm" variant="outline"
              aria-pressed={draft?.action === action.action} disabled={change.isPending} onClick={() => choose(action.action)}>
              {labels.actions[action.action]}
            </Button>)}
          {domain === "validation" && <Button type="button" size="sm" variant="ghost" onClick={onLinkTask}>
            {t(($) => $.detail.link_task)}
          </Button>}
        </div>
        {active && <form key={active.action} onSubmit={submit} className="space-y-3 border-t pt-3" aria-label={labels.actions[active.action]}>
          <div className="grid gap-3 sm:grid-cols-2">
            {active.fields.map((field) => <div key={field.name} className="flex min-w-0 flex-col gap-1 text-caption">
              <label htmlFor={`${formID}-${field.name}`}>{nameOf(labels.fields, field.name)}</label>
              {field.options ? <select id={`${formID}-${field.name}`} name={field.name} required={!field.optional} multiple={field.mode === "multi"}
                defaultValue={field.mode === "multi" ? [] : ""}
                className="min-w-0 rounded-input border border-input bg-background px-2 py-1.5 text-body">
                {field.mode !== "multi" && <option value="">{labels.select}</option>}
                {field.options.map((option) => <option key={option.value} value={option.value}>
                  {field.translated ? nameOf(labels.values, option.label) : option.label}
                </option>)}
              </select> : field.mode === "paragraph" || field.mode === "list" ?
                <Textarea id={`${formID}-${field.name}`} name={field.name} required={!field.optional} defaultValue={field.initial}
                  placeholder={field.mode === "list" ? labels.one_per_line : undefined} maxLength={4000} /> :
                <Input id={`${formID}-${field.name}`} name={field.name} required={!field.optional} defaultValue={field.initial}
                  type={field.mode === "number" ? "number" : "text"} min={field.mode === "number" ? 1 : undefined} maxLength={500} />}
            </div>)}
          </div>
          {stale && <div className="flex flex-wrap items-center gap-2">
            <p role="status" className="text-body text-muted-foreground">{labels.conflict}</p>
            <Button size="sm" type="button" variant="outline" onClick={() => choose(active.action)}>{labels.use_current}</Button>
          </div>}
          {change.isError && !stale && <p role="alert" className="text-body text-destructive">{error}</p>}
          <div className="flex gap-2">
            <Button size="sm" type="submit" disabled={change.isPending || stale}>{labels.actions[active.action]}</Button>
            <Button size="sm" type="button" variant="ghost" disabled={change.isPending} onClick={() => choose(null)}>
              {t(($) => $.form.cancel)}
            </Button>
          </div>
        </form>}
      </>}
    </section>
  );
}
