"use client";

import type { AcceptanceCloseout } from "@multica/core/types/acceptance";
import { useDownloadAttachment } from "../../editor/use-download-attachment";
import { useT } from "../../i18n";

export function IssueCloseout({
  closeout,
  actorName,
}: {
  closeout: AcceptanceCloseout;
  actorName: (actor: string) => string;
}) {
  const { t } = useT("issues");
  const download = useDownloadAttachment();
  const passed = closeout.criteria.filter((criterion) => criterion.passed).length;
  const candidates = new Set(closeout.criteria.flatMap(({ candidate }) => candidate ? [candidate] : []));
  const sharedCandidate = candidates.size === 1 ? candidates.values().next().value : undefined;

  return (
    <section aria-label={t(($) => $.method.closeout)} className="grid min-w-0 gap-3 rounded-md border border-border bg-muted/20 p-3">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h4 className="font-medium">{t(($) => $.method.closeout)}</h4>
        <span className="text-muted-foreground">
          {t(($) => $.method.closeout_passed, { passed, total: closeout.criteria.length })}
          {sharedCandidate && <>{" · "}<code title={sharedCandidate}>{sharedCandidate.slice(0, 7)}</code></>}
        </span>
        {closeout.reconstructed && (
          <span className="text-caption text-muted-foreground">{t(($) => $.method.closeout_reconstructed)}</span>
        )}
      </header>
      <ul className="divide-y divide-border">
        {closeout.criteria.map((criterion) => (
          <li key={criterion.id} className="py-2">
            <details>
              <summary className="cursor-pointer">
                <span className="inline-flex w-[calc(100%-1.1rem)] min-w-0 items-baseline gap-2 align-top">
                  <strong className="shrink-0 font-medium">{criterion.id}</strong>
                  <span className="min-w-0 flex-1 truncate" title={criterion.summary}>{criterion.summary}</span>
                  <span className={criterion.passed ? "text-success" : "text-muted-foreground"}>
                    {criterion.passed ? t(($) => $.acceptance.pass) : t(($) => $.method.closeout_unknown)}
                  </span>
                  {criterion.evidence.length > 0 && (
                    <span className="shrink-0 text-primary">{t(($) => $.method.closeout_evidence)}</span>
                  )}
                </span>
              </summary>
              <div className="mt-2 grid gap-2 pl-4">
                <p className="break-words">{criterion.summary}</p>
                {criterion.evidence.map((entry) => (
                  <div key={entry.case_id} className="grid gap-1 rounded-md border border-border p-2">
                    <p className="font-medium">
                      {entry.case_id} v{entry.case_version ?? 1}
                      {entry.actor && <> · {actorName(entry.actor)}</>}
                      {candidates.size > 1 && <> · <code title={entry.candidate}>{entry.candidate.slice(0, 7)}</code></>}
                    </p>
                    {!!entry.attachment_ids?.length && (
                      <div className="flex flex-wrap gap-x-3">
                        {entry.attachment_ids.map((id, index) => (
                          <button
                            type="button"
                            key={id}
                            className="text-primary underline underline-offset-2"
                            onClick={() => void download(id)}
                          >
                            {t(($) => $.method.closeout_attachment, { number: index + 1 })}
                          </button>
                        ))}
                      </div>
                    )}
                    <p className="break-words whitespace-pre-wrap text-muted-foreground">{entry.evidence}</p>
                  </div>
                ))}
              </div>
            </details>
          </li>
        ))}
      </ul>
    </section>
  );
}
