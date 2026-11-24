"use client";

import { useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type {
  AcceptanceChange,
  AcceptanceTrace,
} from "@multica/core/types/acceptance";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";

type Editor = {
  action: AcceptanceChange["action"];
  base: number;
  criterion?: AcceptanceTrace["criteria"][number];
  caseId?: string;
};

function caseVersion(testCase: AcceptanceTrace["criteria"][number]["cases"][number]) {
  return (testCase.previous_specifications?.length ?? 0) + 1;
}

function Field({
  label,
  name,
  initial = "",
  pattern,
}: {
  label: string;
  name: string;
  initial?: string;
  pattern?: string;
}) {
  return (
    <label className="grid gap-1 text-label">
      {label}
      <Input
        required
        name={name}
        defaultValue={initial}
        pattern={pattern}
        autoComplete="off"
      />
    </label>
  );
}

export function IssueAcceptance({
  issueId,
  trace,
  refresh,
}: {
  issueId: string;
  trace: AcceptanceTrace;
  refresh: () => Promise<unknown>;
}) {
  const { t } = useT("issues");
  const [editor, setEditor] = useState<Editor | null>(null);
  const [filter, setFilter] = useState("");
  const [formError, setFormError] = useState("");
  const mutation = useMutation({
    mutationFn: (op: AcceptanceChange) =>
      api.changeIssueAcceptance(issueId, op),
    onSuccess: async () => {
      setEditor(null);
      await refresh();
    },
    onError: () => {
      void refresh();
    },
  });
  function edit(
    action: Editor["action"],
    criterion?: Editor["criterion"],
    caseId?: string,
  ) {
    mutation.reset();
    setFormError("");
    setEditor({ action, criterion, caseId, base: trace.revision });
  }
  const editedCase = editor?.criterion?.cases.find((testCase) => testCase.id === editor.caseId);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editor) return;
    const data = new FormData(event.currentTarget);
    const field = (name: string) => String(data.get(name) ?? "").trim();
    const criterion_id = editor.criterion?.id ?? field("criterion");
    if (
      !editor.criterion &&
      trace.criteria.some((c) => c.id === criterion_id)
    ) {
      setFormError(t(($) => $.acceptance.duplicate));
      return;
    }
    const base = { base_revision: editor.base, criterion_id };
    switch (editor.action) {
      case "criterion":
        mutation.mutate({ ...base, action: "criterion", text: field("text") });
        break;
      case "case":
        mutation.mutate({
          ...base,
          action: "case",
          case_id: field("case"),
          text: field("text"),
        });
        break;
      case "implementation":
        mutation.mutate({
          ...base,
          action: "implementation",
          repository: field("repository"),
          branch: field("branch"),
          commit: field("commit"),
        });
        break;
      case "run": {
        if (!editedCase) return;
        const version = caseVersion(editedCase);
        mutation.mutate({
          ...base,
          action: "run",
          case_id: editedCase.id,
          ...(version > 1 && { case_version: version }),
          ...(editedCase.artifact_sha256 && { artifact_sha256: field("artifact_sha256") }),
          result: field("result") === "pass" ? "pass" : "fail",
          candidate: field("candidate"),
          evidence: field("evidence"),
        });
        break;
      }
    }
  }
  const button = (label: string, callback: () => void) => (
    <Button
      type="button"
      variant="outline"
      size="xs"
      disabled={mutation.isPending}
      onClick={callback}
    >
      {label}
    </Button>
  );
  const shaPattern = "([a-f0-9]{40}|[a-f0-9]{64})";
  const term = filter.trim().toLowerCase();
  const visible = trace.criteria.filter((c) =>
    [
      c.id,
      ...c.versions,
      ...c.cases.flatMap((tc) => [tc.id, tc.specification]),
      ...c.implementations.flatMap((link) => [
        link.repository,
        link.branch,
        link.commit,
      ]),
    ].some((field) => field.toLowerCase().includes(term)),
  );
  return (
    <details className="mt-5 rounded-lg border border-border px-3 py-2">
      <summary className="cursor-pointer text-label font-medium">
        {t(($) => $.acceptance.title)} ({trace.criteria.length})
      </summary>
      <div className="mt-3 grid gap-4">
        {trace.criteria.length > 0 && (
          <Input
            type="search"
            aria-label={t(($) => $.acceptance.search)}
            placeholder={t(($) => $.acceptance.search)}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        )}
        {term && visible.length === 0 && (
          <p className="text-label">{t(($) => $.acceptance.no_match)}</p>
        )}
        {visible.map((c) => {
          const version = c.versions.length;
          const candidate = c.implementations.filter((link) => link.version === version).at(-1)?.commit;
          return (
            <section key={c.id} className="grid gap-2 break-words">
              <h4 className="text-label font-medium">
                {c.id} · {`v${version}`} · {c.versions.at(-1)}
              </h4>
              {c.cases.length === 0 && (
                <p className="text-label text-muted-foreground">
                  {t(($) => $.acceptance.uncovered)}
                </p>
              )}
              {c.cases.map((tc) => {
                const latest = tc.runs.filter((run) => !run.run_kind).at(-1);
                const testVersion = caseVersion(tc);
                const status = !latest
                  ? t(($) => $.acceptance.untested)
                  : latest.version !== version || (latest.case_version ?? 1) !== testVersion || (candidate && latest.candidate !== candidate)
                    ? t(($) => $.acceptance.retest)
                    : latest.result === "pass"
                      ? t(($) => $.acceptance.pass)
                      : t(($) => $.acceptance.fail);
                return (
                  <div
                    key={tc.id}
                    className="border-l border-border pl-3 text-label"
                  >
                    <p>
                      <strong>{tc.id} v{testVersion}</strong> · {status} · {tc.specification}
                    </p>
                    {tc.runs.map((r, i) => (
                      <p key={i} className="break-all text-muted-foreground">
                        {c.id} v{r.version} · {tc.id} v{r.case_version ?? 1} ·{" "}
                        {r.run_kind && <><code>{r.run_kind}</code> · </>}
                        {r.result === "pass"
                          ? t(($) => $.acceptance.pass)
                          : t(($) => $.acceptance.fail)}{" "}
                        · <code>{r.candidate}</code> · {r.evidence}
                      </p>
                    ))}
                    {button(
                      t(($) => $.acceptance.run),
                      () => edit("run", c, tc.id),
                    )}
                  </div>
                );
              })}
              {!c.implementations.some((link) => link.version === version) && (
                <p className="text-label text-muted-foreground">
                  {t(($) => $.acceptance.no_impl)}
                </p>
              )}
              {c.implementations.map((link) => (
                <p className="break-all text-label" key={JSON.stringify(link)}>
                  {t(($) => $.acceptance.implementation)} · {`v${link.version}`}{" "}
                  · {link.repository} · {link.branch} ·{" "}
                  <code>{link.commit}</code>
                </p>
              ))}
              {version > 1 && (
                <details className="text-label">
                  <summary className="cursor-pointer">
                    {t(($) => $.acceptance.history)}
                  </summary>
                  {c.versions.map((text, i) => (
                    <p key={i}>
                      {`v${i + 1}`} · {text}
                    </p>
                  ))}
                </details>
              )}
              <div className="flex flex-wrap gap-2">
                {button(
                  t(($) => $.acceptance.revise),
                  () => edit("criterion", c),
                )}
                {button(
                  t(($) => $.acceptance.add_case),
                  () => edit("case", c),
                )}
                {button(
                  t(($) => $.acceptance.link),
                  () => edit("implementation", c),
                )}
              </div>
            </section>
          );
        })}
        <div>
          {button(
            t(($) => $.acceptance.add),
            () => edit("criterion"),
          )}
        </div>
        {editor && (
          <form
            key={[editor.action, editor.criterion?.id, editor.caseId].join("/")}
            className="grid gap-2 rounded-md bg-muted/30 p-3"
            onSubmit={submit}
          >
            {editor.criterion ? (
              <strong>
                {editor.criterion.id} {editor.caseId}
              </strong>
            ) : (
              <Field
                label={t(($) => $.acceptance.criterion_id)}
                name="criterion"
              />
            )}
            {editor.action === "case" && (
              <Field label={t(($) => $.acceptance.case_id)} name="case" />
            )}
            {(editor.action === "criterion" || editor.action === "case") && (
              <label className="grid gap-1 text-label">
                {editor.action === "criterion"
                  ? t(($) => $.acceptance.criterion)
                  : t(($) => $.acceptance.specification)}
                <textarea
                  required
                  className="rounded-md border border-border bg-background p-2"
                  name="text"
                  defaultValue={
                    editor.action === "criterion"
                      ? editor.criterion?.versions.at(-1)
                      : ""
                  }
                />
              </label>
            )}
            {editor.action === "criterion" && editor.criterion && (
              <p className="text-label">
                {t(($) => $.acceptance.impact)} ·{" "}
                {editor.criterion.cases.map((tc) => tc.id).join(", ") || "—"}
              </p>
            )}
            {editor.action === "implementation" && (
              <>
                <Field label={t(($) => $.acceptance.repo)} name="repository" />
                <Field label={t(($) => $.acceptance.branch)} name="branch" />
                <Field
                  label={t(($) => $.acceptance.implementation)}
                  name="commit"
                  pattern={shaPattern}
                />
              </>
            )}
            {editor.action === "run" && (
              <>
                <Field
                  label={t(($) => $.acceptance.candidate)}
                  name="candidate"
                  pattern={shaPattern}
                />
                {editedCase?.artifact_sha256 && (
                  <Field label="SHA-256" name="artifact_sha256" pattern="[a-f0-9]{64}" />
                )}
                <Field
                  label={t(($) => $.acceptance.evidence)}
                  name="evidence"
                />
                <label className="grid gap-1 text-label">
                  {t(($) => $.acceptance.result)}
                  <select
                    name="result"
                    required
                    defaultValue=""
                    className="rounded-md border border-border bg-background p-1"
                  >
                    <option disabled value="">
                      —
                    </option>
                    <option value="pass">{t(($) => $.acceptance.pass)}</option>
                    <option value="fail">{t(($) => $.acceptance.fail)}</option>
                  </select>
                </label>
              </>
            )}
            {(formError || mutation.error) && (
              <p role="alert" className="text-label text-destructive">
                {formError || mutation.error?.message}
              </p>
            )}
            {editor.base !== trace.revision && (
              <p role="alert" className="text-label">
                {t(($) => $.acceptance.conflict)}
              </p>
            )}
            <div className="flex gap-2">
              <Button
                size="sm"
                type="submit"
                disabled={mutation.isPending || editor.base !== trace.revision}
              >
                {t(($) => $.acceptance.save)}
              </Button>
              {button(
                t(($) => $.acceptance.cancel),
                () => setEditor(null),
              )}
            </div>
          </form>
        )}
      </div>
    </details>
  );
}
