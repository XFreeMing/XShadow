"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { projectSkillsOptions } from "@multica/core/projects/queries";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";

export function ProjectSkillsTab({ projectId }: { projectId: string }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const options = projectSkillsOptions(wsId, projectId);
  const { data, isPending, refetch } = useQuery(options);
  const decide = useMutation({
    mutationFn: ({ issueId, revision, decision }: {
      issueId: string;
      revision: number;
      decision: "publish" | "dismiss";
    }) => api.decideProjectSkill(projectId, issueId, revision, decision),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: options.queryKey }),
        queryClient.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) }),
      ]);
    },
    onError: (error) => toast.error(error.message || t(($) => $.skills.save_failed)),
  });

  if (isPending) return <div className="p-4"><Spinner /></div>;
  if (!data) {
    return (
      <div className="flex items-center gap-3 p-4 text-body">
        {t(($) => $.skills.load_failed)}
        <Button size="sm" variant="outline" onClick={() => void refetch()}>
          {t(($) => $.skills.retry)}
        </Button>
      </div>
    );
  }

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="mx-auto max-w-4xl space-y-7 p-5 md:p-8">
        <section aria-labelledby="project-skills-published" className="space-y-2">
          <h2 id="project-skills-published" className="font-medium">{t(($) => $.skills.published)}</h2>
          {data.skills.length === 0 && (
            <p className="text-body text-muted-foreground">{t(($) => $.skills.empty)}</p>
          )}
          {data.skills.map((skill) => (
            <AppLink
              key={skill.id}
              href={paths.skillDetail(skill.id)}
              className="block rounded-lg border px-3 py-2 hover:bg-muted"
            >
              <div className="break-all font-medium">{skill.name}</div>
              <div className="break-words text-body text-muted-foreground">{skill.description}</div>
            </AppLink>
          ))}
        </section>
        {data.can_manage && (
          <section aria-labelledby="project-skills-proposals" className="space-y-2">
            <h2 id="project-skills-proposals" className="font-medium">{t(($) => $.skills.proposals)}</h2>
            {data.proposals.length === 0 && (
              <p className="text-body text-muted-foreground">{t(($) => $.skills.no_proposals)}</p>
            )}
            {data.proposals.map((proposal) => {
              const { lesson, state } = proposal;
              const isUpdate = data.skills.some((skill) => skill.name === lesson.name);
              const status = state === "pending"
                ? t(($) => $.skills.pending)
                : state === "published"
                  ? t(($) => $.skills.version, { version: proposal.skill_version })
                  : t(($) => $.skills.dismissed);
              return (
                <details key={`${proposal.issue_id}/${proposal.method_revision}`} className="rounded-lg border">
                  <summary className="cursor-pointer break-words px-3 py-2">
                    <span className="font-medium">{lesson.name}</span>
                    <span className="ml-2 text-caption text-muted-foreground">{status}</span>
                  </summary>
                  <div className="space-y-3 border-t p-3 text-body">
                    <div className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
                      <AppLink className="underline" href={paths.issueDetail(proposal.issue_id)}>
                        {t(($) => $.skills.source, { number: proposal.issue_number })}
                      </AppLink>
                      <code>{lesson.criterion_id}/{lesson.case_id}</code>
                      <code title={proposal.candidate_commit}>{proposal.candidate_commit.slice(0, 12)}</code>
                    </div>
                    <p className="whitespace-pre-wrap break-words">{lesson.when}</p>
                    <ol className="list-decimal space-y-1 pl-5">
                      {lesson.steps.map((step, index) => (
                        <li key={index} className="whitespace-pre-wrap break-words">{step}</li>
                      ))}
                    </ol>
                    <dl className="grid gap-1">
                      <dt className="font-medium">{t(($) => $.skills.verify)}</dt>
                      <dd className="whitespace-pre-wrap break-words">{lesson.verify}</dd>
                      <dt className="font-medium">{t(($) => $.skills.avoid)}</dt>
                      <dd className="whitespace-pre-wrap break-words">{lesson.avoid}</dd>
                    </dl>
                    {state === "pending" && (
                      <div className="flex flex-wrap justify-end gap-2">
                        <Button size="sm" variant="outline" disabled={decide.isPending}
                          onClick={() => decide.mutate({ issueId: proposal.issue_id, revision: proposal.method_revision, decision: "dismiss" })}>
                          {t(($) => $.skills.dismiss)}
                        </Button>
                        <Button size="sm" disabled={decide.isPending} aria-busy={decide.isPending}
                          onClick={() => decide.mutate({ issueId: proposal.issue_id, revision: proposal.method_revision, decision: "publish" })}>
                          {isUpdate ? t(($) => $.skills.publish_version) : t(($) => $.skills.publish)}
                        </Button>
                      </div>
                    )}
                  </div>
                </details>
              );
            })}
          </section>
        )}
      </div>
    </div>
  );
}
