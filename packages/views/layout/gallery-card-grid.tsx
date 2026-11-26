"use client";

import type { ReactNode } from "react";

/** Shared responsive card grid used by native task gallery and generic table gallery. */
export function GalleryCardGrid({
  children,
  empty,
}: {
  children?: ReactNode;
  empty?: ReactNode;
}) {
  if (empty !== undefined) {
    return (
      <div className="p-6 text-body text-muted-foreground" data-view-kind="gallery">
        {empty}
      </div>
    );
  }

  return (
    <div
      className="grid grid-cols-1 gap-3 p-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4"
      data-view-kind="gallery"
    >
      {children}
    </div>
  );
}
