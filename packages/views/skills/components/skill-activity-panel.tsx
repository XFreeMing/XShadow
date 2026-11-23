"use client";

import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import {
  SkillRunOutcomeSchema,
  type SkillActivity,
  type SkillActivityRun,
  type SkillRunOutcome,
} from "@multica/core/api/schemas";
import { useWorkspaceId } from "@multica/core/hooks";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Skill } from "@multica/core/types";
import { skillActivityOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT, useTimeAgo } from "../../i18n";
import { ObjectSection, ObjectSectionCard } from "../../layout/object-page";
import { AppLink } from "../../navigation";

const windows = [7, 30, 90] as const;
const fieldClass = "rounded-md border bg-background px-2 py-1 text-sm";

export function SkillActivityPanel({ skill, canReview }: { skill: Skill; canReview: boolean }) {
  const { t } = useT("skills");
  const { t: common } = useT("common");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const [days, setDays] = useState<SkillActivity["days"]>(30);
  const [editing, setEditing] = useState<SkillActivityRun | null>(null);
  const query = useQuery(skillActivityOptions(wsId, skill.id, days));
  const data = query.data;
  const versionName = (version: number | null, hash: string) =>
    version === null ? hash.slice(0, 12) : t(($) => $.detail.versions.version_label, { version });

  const proposeMaintenance = () => {
    const url = paths.skillDetail(skill.id);
    useModalStore.getState().open("create-issue", {
      skill_maintenance_title: t(($) => $.activity.proposal_title, { name: skill.name }),
      skill_maintenance_description: t(($) => $.activity.proposal_description, { url }),
    });
  };

  return (
    <ObjectSection
      title={t(($) => $.activity.title)}
      action={
        <Button size="xs" variant="outline" onClick={proposeMaintenance}>
          {t(($) => $.activity.propose)}
        </Button>
      }
    >
      <ObjectSectionCard>
        <div className="space-y-3 px-4 py-3 text-sm">
          <label className="flex items-center gap-2">
            {t(($) => $.activity.window)}
            <select
              className={fieldClass}
              value={days}
              onChange={(event) => {
                const next = windows.find((value) => String(value) === event.currentTarget.value);
                if (next !== undefined) {
                  setDays(next);
                  setEditing(null);
                }
              }}
            >
              {windows.map((value) => <option key={value} value={value}>{value}</option>)}
            </select>
          </label>

          {query.isPending ? (
            <p role="status">{common(($) => $.loading)}</p>
          ) : !data ? (
            <p role="status" className="text-muted-foreground">{t(($) => $.activity.unavailable)}</p>
          ) : data.counts.available === 0 ? (
            <p className="text-muted-foreground">{t(($) => $.activity.empty)}</p>
          ) : (
            <>
              <p className="text-muted-foreground">{t(($) => $.activity.scope)}</p>
              <dl className="grid grid-cols-2 gap-2 md:grid-cols-4">
                <Metric label={t(($) => $.activity.available)} value={data.counts.available} />
                <Metric label={t(($) => $.activity.reviewed)} value={data.counts.reviewed} />
                <Metric label={t(($) => $.activity.adopted)} value={data.counts.adopted} />
                <Metric label={t(($) => $.activity.helped)} value={data.counts.helped} />
              </dl>
              <details>
                <summary className="cursor-pointer">{t(($) => $.activity.versions)}</summary>
                <ul className="mt-2 space-y-1">
                  {data.versions.map((version) => (
                    <li key={`${version.version}-${version.content_hash}`}>
                      {versionName(version.version, version.content_hash)}{" · "}
                      {t(($) => $.activity.version_counts, {
                        available: version.available, adopted: version.adopted, regressed: version.regressed,
                      })}
                    </li>
                  ))}
                </ul>
              </details>
              <details>
                <summary className="cursor-pointer">{t(($) => $.activity.recent)}</summary>
                <ul className="mt-2 divide-y">
                  {data.runs.map((run) => (
                    <ActivityRun
                      key={run.id}
                      run={run}
                      issueUrl={paths.issueDetail(run.issue_id)}
                      versionName={versionName(run.version, run.content_hash)}
                      onReview={canReview && run.status !== "running" ? () => setEditing(run) : undefined}
                    >
                      {editing?.id === run.id && (
                        <ActivityReview
                          skillId={skill.id}
                          run={editing}
                          onCancel={() => setEditing(null)}
                          onSaved={() => {
                            void queryClient.invalidateQueries({ queryKey: skillActivityOptions(wsId, skill.id, days).queryKey });
                            setEditing(null);
                          }}
                        />
                      )}
                    </ActivityRun>
                  ))}
                </ul>
              </details>
            </>
          )}
        </div>
      </ObjectSectionCard>
    </ObjectSection>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return <div><dt className="text-muted-foreground">{label}</dt><dd className="font-medium">{value}</dd></div>;
}

function ActivityRun({
  run, issueUrl, versionName, onReview, children,
}: { run: SkillActivityRun; issueUrl: string; versionName: string; onReview?: () => void; children?: ReactNode }) {
  const { t } = useT("skills");
  const timeAgo = useTimeAgo();
  const outcome = run.outcome;
  return (
    <li className="space-y-1 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <AppLink href={issueUrl} className="font-medium hover:underline">#{run.issue_number}</AppLink>
        <span>{versionName}</span>
        <time dateTime={run.provided_at} className="text-muted-foreground">{timeAgo(run.provided_at)}</time>
        <span>{outcome
          ? t(($) => $.activity.outcomes[outcome])
          : t(($) => $.activity.unreviewed)}</span>
        {onReview && (
          <Button size="xs" variant="ghost" onClick={onReview}>{t(($) => $.activity.review)}</Button>
        )}
      </div>
      {run.evidence && <p className="whitespace-pre-wrap break-words text-muted-foreground">{run.evidence}</p>}
      {children}
    </li>
  );
}

function ActivityReview({
  skillId, run, onCancel, onSaved,
}: { skillId: string; run: SkillActivityRun; onCancel: () => void; onSaved: () => void }) {
  const { t } = useT("skills");
  const { t: common } = useT("common");
  const [outcome, setOutcome] = useState<SkillRunOutcome | "">(run.outcome ?? "");
  const [evidence, setEvidence] = useState(run.evidence ?? "");
  const mutation = useMutation({
    mutationFn: (value: SkillRunOutcome) => api.reviewSkillActivity(skillId, {
      run_id: run.id, content_hash: run.content_hash, outcome: value, evidence: evidence.trim(),
    }),
    onSuccess: onSaved,
    onError: () => toast.error(t(($) => $.activity.review_failed)),
  });

  return (
    <form className="space-y-2 border-t pt-3" onSubmit={(event) => {
      event.preventDefault();
      if (outcome && evidence.trim() && !mutation.isPending) mutation.mutate(outcome);
    }}>
      <label className="flex flex-wrap items-center gap-2">
        {t(($) => $.activity.review)} #{run.issue_number}
        <select className={fieldClass} value={outcome} onChange={(event) => {
          const parsed = SkillRunOutcomeSchema.safeParse(event.currentTarget.value);
          setOutcome(parsed.success ? parsed.data : "");
        }} required>
          <option value="" disabled>{t(($) => $.activity.outcome)}</option>
          {SkillRunOutcomeSchema.options.map((value) => (
            <option key={value} value={value}>{t(($) => $.activity.outcomes[value])}</option>
          ))}
        </select>
      </label>
      <label className="block">
        <span className="mb-1 block">{t(($) => $.activity.evidence)}</span>
        <Textarea rows={3} maxLength={1000} required value={evidence} onChange={(event) => setEvidence(event.currentTarget.value)} />
      </label>
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={!outcome || !evidence.trim() || mutation.isPending} aria-busy={mutation.isPending}>
          {common(($) => $.save)}
        </Button>
        <Button type="button" size="sm" variant="outline" disabled={mutation.isPending} onClick={onCancel}>
          {common(($) => $.cancel)}
        </Button>
      </div>
    </form>
  );
}
