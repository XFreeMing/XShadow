"use client";

import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import {
  GOAL_STATUS_BADGE_CLASS,
  GOAL_STATUS_DOT_CLASS,
  goalStatusLabelKey,
} from "../status";

/**
 * A goal's lifecycle badge. An unrecognized status shows as "Unknown"; it is
 * never mapped onto a known state, and in particular never onto completed.
 */
export function GoalStatusBadge({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const { t } = useT("goals");
  const key = goalStatusLabelKey(status);
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-xs px-1.5 py-0.5 text-caption font-medium",
        GOAL_STATUS_BADGE_CLASS[key],
        className,
      )}
    >
      <span className={cn("size-1.5 rounded-full", GOAL_STATUS_DOT_CLASS[key])} aria-hidden="true" />
      {t(($) => $.status[key])}
    </span>
  );
}