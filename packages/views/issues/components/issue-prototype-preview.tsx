"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Issue } from "@multica/core/types";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { ImageIcon } from "@multica/ui/icons";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import { useResignedInlineMedia } from "../../editor/hooks/use-inline-media-url";
import { issuePrototypeImages, type IssuePrototypeImage } from "../utils/prototype-images";

export function PrototypeImage({ image, className }: { image: IssuePrototypeImage; className?: string }) {
  const { t } = useT("issues");
  const { url, pending } = useResignedInlineMedia(undefined, image.url, true);
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  if (pending) return <Skeleton className={className} />;
  if (!url || failedUrl === url) {
    return (
      <span className={cn("grid place-items-center text-muted-foreground", className)}
        role="img" aria-label={t(($) => $.prototype.image_failed)}>
        <ImageIcon className="size-5" />
      </span>
    );
  }
  return (
    <img src={url} alt={image.alt} loading="lazy" decoding="async" draggable={false}
      onError={() => setFailedUrl(url)} className={cn("object-contain", className)} />
  );
}

function PrototypeFrames({ images }: { images: IssuePrototypeImage[] }) {
  const { t } = useT("issues");
  const [index, setIndex] = useState(0);
  const activeIndex = Math.min(index, images.length - 1);
  const active = images[activeIndex];
  if (!active) return <p className="text-muted-foreground">{t(($) => $.prototype.empty)}</p>;
  return (
    <div className="min-w-0 space-y-3">
      <figure className="space-y-2">
        <div className="grid min-h-36 place-items-center rounded-lg border bg-muted/30">
          <PrototypeImage image={active} className="max-h-[62dvh] min-h-32 w-full rounded-lg" />
        </div>
        {active.alt && <figcaption className="text-caption text-muted-foreground">{active.alt}</figcaption>}
      </figure>
      {images.length > 1 && (
        <div className="flex gap-2 overflow-x-auto p-1">
          {images.map((image, frame) => (
            <button key={frame} type="button" aria-pressed={frame === activeIndex}
              aria-label={image.alt || t(($) => $.prototype.frame, { number: frame + 1 })}
              onClick={() => setIndex(frame)}
              className={cn("shrink-0 overflow-hidden rounded-md border bg-muted/30 p-0.5 hover:border-foreground",
                frame === activeIndex && "border-brand ring-1 ring-brand")}>
              <PrototypeImage image={{ ...image, alt: "" }} className="h-12 w-20" />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function IssuePrototypePreview({
  issue, onClose,
}: {
  issue: Issue;
  onClose: () => void;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const detail = useQuery({
    ...issueDetailOptions(wsId, issue.id),
    enabled: issue.description == null,
  });
  const description = issue.description ?? detail.data?.description;
  const images = useMemo(() => issuePrototypeImages(description), [description]);

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="min-w-0 sm:max-w-4xl">
        <header className="min-w-0 space-y-2 pr-8">
          <div className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
            <span className="inline-flex items-center gap-1.5"><ImageIcon className="size-4" />{t(($) => $.prototype.title)}</span>
            <span>{issue.identifier}</span>
          </div>
          <DialogTitle className="break-words leading-snug">{issue.title}</DialogTitle>
        </header>
        {detail.isLoading ? (
          <Skeleton className="aspect-video w-full" />
        ) : issue.description == null && detail.isError ? (
          <div role="alert" className="flex flex-wrap items-center gap-2 text-muted-foreground">
            {t(($) => $.prototype.load_failed)}
            <Button variant="ghost" size="sm" onClick={() => void detail.refetch()}>{t(($) => $.prototype.retry)}</Button>
          </div>
        ) : <PrototypeFrames images={images} />}
        <AppLink href={paths.issueDetail(issue.identifier || issue.id)}
          className={cn(buttonVariants({ variant: "outline", size: "sm" }), "justify-self-start")}
          onClick={onClose}>
          {t(($) => $.prototype.open_task)}
        </AppLink>
      </DialogContent>
    </Dialog>
  );
}
