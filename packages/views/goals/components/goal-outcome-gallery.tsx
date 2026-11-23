"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { goalKeys } from "@multica/core/goals";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n";

export function GoalOutcomeGallery({ wsId, goalId }: { wsId: string; goalId: string }) {
  const { t } = useT("goals");
  const [openIndex, setOpenIndex] = useState<number | null>(null);
  const { data: images } = useQuery({
    queryKey: [...goalKeys.all(wsId), "outcome-images", goalId],
    queryFn: () => api.getGoalOutcomeImages(goalId),
  });
  if (!images?.length) return null;
  const selected = openIndex === null ? undefined : images[openIndex];
  return (
    <section className="space-y-3">
      <h2 className="text-title-sm font-semibold">{t(($) => $.outcome_gallery)}</h2>
      <div className="grid gap-3 sm:grid-cols-2">
        {images.map((image, index) => (
          <button key={index} type="button" aria-haspopup="dialog" onClick={() => setOpenIndex(index)}
            className="overflow-hidden rounded-lg border bg-surface-raised text-left hover:border-foreground/40">
            <img src={image.src} alt="" className="aspect-[3/2] w-full object-contain" />
            <span className="block p-3 text-body font-medium">{image.caption}</span>
          </button>
        ))}
      </div>
      {selected && <Dialog open onOpenChange={(open) => { if (!open) setOpenIndex(null); }}>
        <DialogContent className="sm:max-w-5xl">
          <DialogTitle className="pr-8">{selected.caption}</DialogTitle>
          <img src={selected.src} alt={selected.caption} className="max-h-[76dvh] w-full object-contain" />
        </DialogContent>
      </Dialog>}
    </section>
  );
}
