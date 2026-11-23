"use client";

import { useState } from "react";
import type { Issue } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { ImageIcon } from "@multica/ui/icons";
import { useT } from "../../i18n";
import { IssuePrototypePreview, PrototypeImage } from "../../issues/components/issue-prototype-preview";
import { StatusIcon } from "../../issues/components/status-icon";
import { issuePrototypeImages } from "../../issues/utils/prototype-images";

const INITIAL_CARDS = 6;

export function GoalTaskPrototypes({ issues }: { issues: Issue[] }) {
  const { t } = useT("issues");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [expanded, setExpanded] = useState(false);
  const cards = issues.flatMap((issue) => {
    const images = issuePrototypeImages(issue.description);
    const [cover] = images;
    return cover ? [{ issue, cover, count: images.length }] : [];
  });
  const selected = cards.find(({ issue }) => issue.id === selectedId)?.issue;
  if (cards.length === 0) return null;
  const visibleCards = expanded ? cards : cards.slice(0, INITIAL_CARDS);

  return (
    <section className="space-y-3">
      <h2 className="text-title-sm font-semibold">{t(($) => $.prototype.goal_title)}</h2>
      <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {visibleCards.map(({ issue, cover, count }) => (
          <li key={issue.id} className="min-w-0">
            <button type="button" aria-haspopup="dialog" onClick={() => setSelectedId(issue.id)}
              className="group h-full w-full overflow-hidden rounded-lg border bg-surface-raised text-left transition-colors hover:border-foreground/40 focus-visible:outline-2 focus-visible:outline-offset-2">
              <div className="relative flex aspect-video items-center justify-center bg-muted/40">
                <PrototypeImage image={{ ...cover, alt: "" }} className="h-full w-full" />
                {count > 1 && (
                  <span className="absolute right-1.5 bottom-1.5 inline-flex items-center gap-1 rounded-md bg-background px-1.5 py-0.5 text-micro text-foreground">
                    <ImageIcon className="size-3" aria-hidden="true" />
                    <span aria-label={t(($) => $.prototype.frame_count, { count })}>{count}</span>
                  </span>
                )}
              </div>
              <div className="space-y-1 p-2.5">
                <div className="flex items-center gap-1.5 text-caption text-muted-foreground">
                  <StatusIcon status={issue.status} className="size-3 shrink-0" />
                  {issue.identifier}
                </div>
                <p className="line-clamp-2 text-body font-medium">{issue.title}</p>
              </div>
            </button>
          </li>
        ))}
      </ul>
      {cards.length > INITIAL_CARDS && (
        <Button variant="outline" size="sm" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>
          {expanded ? t(($) => $.prototype.show_fewer) : t(($) => $.prototype.show_all, { count: cards.length })}
        </Button>
      )}
      {selected && <IssuePrototypePreview key={selected.id} issue={selected} onClose={() => setSelectedId(null)} />}
    </section>
  );
}
