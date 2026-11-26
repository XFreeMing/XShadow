"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueTypeListOptions } from "@multica/core/issue-types";
import { workMethodCatalogOptions } from "@multica/core/issue-workflows/queries";
import { propertyListOptions } from "@multica/core/properties";
import type { WorkMethod } from "@multica/core/types/work-method";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../i18n";
import { AppLink } from "../navigation";
import { SettingsTab } from "../settings/components/settings-layout";
import { WorkMethodEditor } from "./work-method-editor";
import { WorkMethodSteps } from "./work-method-step";

export function WorkMethodsTab({
  canEdit,
  settingsPath,
  selectedMethodId,
}: {
  canEdit: boolean;
  settingsPath: string;
  selectedMethodId?: string | null;
}) {
  const wsId = useWorkspaceId();
  const { t } = useT("issues");
  const { t: settingsText } = useT("settings");
  const [editing, setEditing] = useState<WorkMethod | null>(null);
  const { data: methods = [], isPending, isError, refetch } = useQuery(workMethodCatalogOptions(wsId));
  const { data: types = [] } = useQuery(issueTypeListOptions(wsId, true));
  const { data: properties = [] } = useQuery(propertyListOptions(wsId, true));

  function typeLink(typeId: string) {
    const type = types.find(({ id }) => id === typeId);
    if (!type) return typeId;
    return (
      <AppLink
        href={`${settingsPath}?tab=task-types&type=${encodeURIComponent(typeId)}`}
        className="font-medium underline-offset-2 hover:underline"
      >
        {type.name}
      </AppLink>
    );
  }

  return (
    <SettingsTab title={t(($) => $.method.catalog)}>
      {isPending ? (
        <Skeleton className="h-20 w-full" />
      ) : isError && methods.length === 0 ? (
        <div role="alert" className="flex items-center gap-2 text-body text-muted-foreground">
          {t(($) => $.method.load_error)}
          <Button variant="link" size="xs" onClick={() => void refetch()}>
            {t(($) => $.method.retry)}
          </Button>
        </div>
      ) : (
        <div className="divide-y divide-surface-border overflow-hidden rounded-lg border border-surface-border bg-card">
          {methods.map((method) => (
            <details key={method.id} open={selectedMethodId === method.id || undefined} className="px-4 py-3">
              <summary className="cursor-pointer rounded-sm text-body font-medium focus-visible:outline-2 focus-visible:outline-ring">
                {method.name}
                <span className="ms-2 text-caption text-muted-foreground">{`r${method.revision}`}</span>
              </summary>
              <div className="space-y-3 pt-3 ps-4 text-label">
                <dl className="grid gap-2 sm:grid-cols-[auto_1fr] sm:gap-x-4">
                  <dt className="text-muted-foreground">{t(($) => $.method.review)}</dt>
                  <dd className="min-w-0 whitespace-pre-wrap break-words">{method.review}</dd>
                  <dt className="text-muted-foreground">{t(($) => $.method.rework_step)}</dt>
                  <dd>{method.steps[method.rework_step]?.name}</dd>
                  {method.plan_task && (
                    <>
                      <dt className="text-muted-foreground">{settingsText(($) => $.properties.issue_types.from_plan)}</dt>
                      <dd>{typeLink(method.plan_task.issue_type_id)}</dd>
                    </>
                  )}
                  {method.rework_task && (
                    <>
                      <dt className="text-muted-foreground">{settingsText(($) => $.properties.issue_types.from_rework)}</dt>
                      <dd>{typeLink(method.rework_task.issue_type_id)}</dd>
                    </>
                  )}
                  {method.github_delivery && (
                    <>
                      <dt className="text-muted-foreground">{t(($) => $.method.github_delivery)}</dt>
                      <dd className="break-all">{method.github_delivery.repository} · {method.github_delivery.base_ref}</dd>
                    </>
                  )}
                </dl>
                <WorkMethodSteps steps={method.steps} />
                {method.steps.map(({ name, table_gate: gate }, index) => gate && (
                  <p key={index} className="text-muted-foreground">
                    {settingsText(($) => $.properties.issue_types.step_gate, {
                      step: name,
                      formula: properties.find(({ id }) => id === gate.property_id)?.name ?? gate.property_id,
                    })}
                    {" · "}{typeLink(gate.issue_type_id)}
                  </p>
                ))}
                {canEdit && !editing && (
                  <Button size="xs" variant="outline" onClick={() => setEditing(method)}>
                    {t(($) => $.method.edit)}
                  </Button>
                )}
                {editing?.id === method.id && (
                  <WorkMethodEditor wsId={wsId} original={editing} onClose={() => setEditing(null)} />
                )}
              </div>
            </details>
          ))}
        </div>
      )}
    </SettingsTab>
  );
}
