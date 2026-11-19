import type { Agent } from "../types/agent";
import { MEMBER_TODO_HANDLER } from "../issues/member-todo";

export type ChiefOfStaffResolution =
  | { status: "ok"; agent: Agent }
  | { status: "missing" }
  | { status: "ambiguous"; candidates: Agent[] };

/**
 * All active agents carrying the configured chief-of-staff name. Archived
 * agents are never receivers. The name is the recorded role identity in
 * `agent-team/team.json`; callers resolve it in the current workspace and
 * never persist or trust a machine UUID from an issue body.
 */
export function chiefOfStaffCandidates(
  agents: readonly Agent[],
  handlerName: string = MEMBER_TODO_HANDLER,
): Agent[] {
  const name = handlerName.trim();
  if (!name) return [];
  return agents.filter((agent) => !agent.archived_at && agent.name.trim() === name);
}

/**
 * Resolve the one receiver for a member answer. A single active match wins; if
 * several agents share the name, the current user's own agent is the only
 * unambiguous choice. Anything else is reported as missing or ambiguous so the
 * client can refuse to submit instead of pretending delivery.
 */
export function resolveChiefOfStaff(
  agents: readonly Agent[],
  currentUserId: string | null | undefined,
  handlerName: string = MEMBER_TODO_HANDLER,
): ChiefOfStaffResolution {
  const candidates = chiefOfStaffCandidates(agents, handlerName);
  if (candidates.length === 0) return { status: "missing" };
  if (candidates.length === 1) return { status: "ok", agent: candidates[0]! };
  if (currentUserId) {
    const owned = candidates.filter((agent) => agent.owner_id === currentUserId);
    if (owned.length === 1) return { status: "ok", agent: owned[0]! };
  }
  return { status: "ambiguous", candidates };
}