import { ApiError } from "@multica/core/api";
import type { GoalStatus } from "@multica/core/types";

/** Every status this client knows, in lifecycle order. */
export const GOAL_STATUS_ORDER = [
  "planned",
  "in_progress",
  "paused",
  "completed",
  "cancelled",
] as const;

/**
 * Statuses the edit form may select. `completed` is deliberately absent: the
 * only way to complete a goal is the detail page's confirmed action, so a
 * stray form save can never mark a goal achieved.
 */
export const EDITABLE_GOAL_STATUSES = [
  "planned",
  "in_progress",
  "paused",
  "cancelled",
] as const;

export type GoalStatusLabelKey = GoalStatus | "unknown";

/** An unrecognizable server status renders as unknown, never as achieved. */
export function goalStatusLabelKey(status: string): GoalStatusLabelKey {
  return (GOAL_STATUS_ORDER as readonly string[]).includes(status)
    ? (status as GoalStatus)
    : "unknown";
}

/**
 * Whether a goal is completed. Deliberately an exact match: a status this
 * client does not know must not read as achieved.
 */
export function isGoalCompleted(status: string): boolean {
  return status === "completed";
}

/**
 * A goal read that failed terminally: the goal is gone for this viewer
 * (deleted, access revoked, or unreachable from this workspace). Transient
 * failures — offline, 5xx — are not terminal and keep the last known identity.
 */
export function isGoalUnavailableError(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 400 || error.status === 403 || error.status === 404);
}

export const GOAL_STATUS_DOT_CLASS: Record<GoalStatusLabelKey, string> = {
  planned: "bg-muted-foreground/60",
  in_progress: "bg-warning",
  paused: "bg-muted-foreground/60",
  completed: "bg-info",
  cancelled: "bg-muted-foreground/40",
  unknown: "bg-muted-foreground/40",
};

export const GOAL_STATUS_BADGE_CLASS: Record<GoalStatusLabelKey, string> = {
  planned: "bg-muted text-muted-foreground",
  in_progress: "bg-warning text-white",
  paused: "bg-muted text-muted-foreground",
  completed: "bg-info text-white",
  cancelled: "bg-muted text-muted-foreground",
  unknown: "bg-muted text-muted-foreground",
};