import type { Issue } from "./issue";

/**
 * A goal is a first-class deliverable object. It owns its intent, scope,
 * acceptance criteria, owner and lifecycle; it is not an issue, never inherits
 * issue status, and no issue transition changes it.
 *
 * The server may grow statuses ahead of this client. Callers must treat an
 * unrecognized status as unknown rather than mapping it to `completed`.
 */
export type GoalStatus =
  | "planned"
  | "in_progress"
  | "paused"
  | "completed"
  | "cancelled";

/** Known goal owner kinds. An owner is responsibility only; it starts no run. */
export type GoalOwnerType = "member" | "agent" | "squad";

export interface Goal {
  id: string;
  workspace_id: string;
  /** null means a workspace-level goal rather than a project goal. */
  project_id: string | null;
  title: string;
  intent: string;
  scope: string;
  acceptance_criteria: string;
  owner_type: GoalOwnerType | null;
  owner_id: string | null;
  status: GoalStatus;
  creator_type: string;
  creator_id: string;
  created_at: string;
  updated_at: string;
}

export interface CreateGoalRequest {
  title: string;
  intent?: string;
  scope?: string;
  acceptance_criteria?: string;
  /** Omit or null for a workspace-level goal. */
  project_id?: string | null;
  /** owner_type and owner_id are set or cleared together. */
  owner_type?: GoalOwnerType | null;
  owner_id?: string | null;
  /** Defaults to the server's `planned`. */
  status?: GoalStatus;
}

/**
 * Partial update. Only keys present in the body change: omitting a key keeps
 * the current value, while `null` clears `project_id`/owner and `""` clears a
 * body field.
 */
export interface UpdateGoalRequest {
  title?: string;
  intent?: string | null;
  scope?: string | null;
  acceptance_criteria?: string | null;
  project_id?: string | null;
  owner_type?: GoalOwnerType | null;
  owner_id?: string | null;
  status?: GoalStatus;
}

export interface ListGoalsParams {
  /** A project UUID, `"none"` for workspace-level goals, or omitted for all. */
  project_id?: string;
  status?: GoalStatus;
  limit?: number;
  offset?: number;
}

export interface ListGoalsResponse {
  goals: Goal[];
  total: number;
}

export interface ListGoalIssuesParams {
  limit?: number;
  offset?: number;
}

export interface ListGoalIssuesResponse {
  issues: Issue[];
  total: number;
}