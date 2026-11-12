import { ApiError, errorCode } from "@multica/core/api";
import type {
  GoalPlan,
  GoalPlanCandidate,
  GoalPlanContextSnapshot,
  GoalPlanEdge,
  GoalPlanItem,
} from "@multica/core/types";

/** Editable draft content plus the server revision it was based on. */
export interface PlanDraftContent {
  items: GoalPlanItem[];
  edges: GoalPlanEdge[];
  context_snapshot: GoalPlanContextSnapshot;
}

export interface PlanDraft extends PlanDraftContent {
  revision: number;
}

export function clonePlanContent(content: PlanDraftContent): PlanDraftContent {
  return JSON.parse(JSON.stringify(content)) as PlanDraftContent;
}

export function draftFromPlan(plan: GoalPlan): PlanDraft {
  return {
    revision: plan.revision,
    ...clonePlanContent({
      items: plan.items,
      edges: plan.edges,
      context_snapshot: plan.context_snapshot,
    }),
  };
}

/**
 * Merges a generated candidate into the local draft. Node identity is the
 * UUID, never the title: a node the user edited keeps their version, a node
 * the user bound to an existing task keeps that binding, and a node added
 * after the run started is not dropped. Edges are the union of both sides and
 * are pruned to nodes that still exist; the server still validates the graph
 * on save (cycles, duplicates, reasons).
 */
export function mergeCandidate(
  draft: PlanDraftContent,
  candidate: GoalPlanCandidate,
  editedItemIds: ReadonlySet<string> = new Set(),
): PlanDraftContent {
  const draftById = new Map(draft.items.map((item) => [item.id, item]));
  const candidateIds = new Set(candidate.items.map((item) => item.id));
  const merged: GoalPlanItem[] = candidate.items.map((incoming) => {
    const local = draftById.get(incoming.id);
    if (!local) return incoming;
    // The user changed this node while the run was in flight: their text wins.
    if (editedItemIds.has(local.id)) return local;
    return {
      ...incoming,
      // An explicit existing-task binding is a user decision the model cannot
      // see; never discard it just because the candidate says "new".
      binding:
        local.binding.type === "existing" && incoming.binding.type !== "existing"
          ? local.binding
          : incoming.binding,
    };
  });
  for (const local of draft.items) {
    if (!candidateIds.has(local.id)) merged.push(local);
  }
  const keptIds = new Set(merged.map((item) => item.id));

  const seen = new Set<string>();
  const edges: GoalPlanEdge[] = [];
  for (const edge of [...candidate.edges, ...draft.edges]) {
    if (!keptIds.has(edge.dependent_item_id) || !keptIds.has(edge.prerequisite_item_id)) continue;
    if (edge.dependent_item_id === edge.prerequisite_item_id) continue;
    const key = `${edge.dependent_item_id}\u0000${edge.prerequisite_item_id}`;
    if (seen.has(key)) continue;
    seen.add(key);
    edges.push(edge);
  }

  return {
    items: merged,
    edges,
    // The candidate recorded what the run actually read; prefer it over the
    // previous basis. An empty candidate snapshot keeps the draft's basis.
    context_snapshot:
      Object.keys(candidate.context_snapshot).length > 0
        ? candidate.context_snapshot
        : draft.context_snapshot,
  };
}

/** The server revision from a `plan_revision_conflict`, or null for any other failure. */
export function planConflictRevision(error: unknown): number | null {
  if (!(error instanceof ApiError) || error.status !== 409) return null;
  if (!error.body || typeof error.body !== "object") return null;
  const body = error.body as { code?: unknown; current_revision?: unknown };
  if (body.code !== "plan_revision_conflict") return null;
  return typeof body.current_revision === "number" ? body.current_revision : null;
}

/**
 * A planning run that belongs to a project the goal has since left. This is
 * never a mergeable late candidate: its content and basis must stay hidden.
 */
export function isGenerationProjectConflict(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    error.status === 409 &&
    errorCode(error) === "generation_project_conflict"
  );
}

/**
 * A candidate is only applicable when every item has a stable UUID and every
 * edge resolves inside the candidate. The server normally guarantees both;
 * a degraded payload must not reach the merge, where it could silently drop
 * dependencies or invent a second node identity.
 */
export function isCandidateUsable(candidate: GoalPlanCandidate | null): boolean {
  if (!candidate) return false;
  const ids = new Set<string>();
  for (const item of candidate.items) {
    if (!item.id || ids.has(item.id)) return false;
    ids.add(item.id);
  }
  return candidate.edges.every(
    (edge) =>
      ids.has(edge.dependent_item_id) && ids.has(edge.prerequisite_item_id),
  );
}

export type PlanValidationIssue =
  | "title_required"
  | "dependency_reason_required"
  | "dependency_invalid"
  | "cycle";

/**
 * Local mirror of the server's graph checks, so an obvious mistake is
 * explained inline instead of costing a round trip. The server remains
 * authoritative.
 */
export function validatePlanDraft(content: PlanDraftContent): PlanValidationIssue | null {
  const ids = new Set<string>();
  for (const item of content.items) {
    if (!item.title.trim()) return "title_required";
    if (!item.id) return "title_required";
    ids.add(item.id);
  }
  const seen = new Set<string>();
  for (const edge of content.edges) {
    if (!ids.has(edge.dependent_item_id) || !ids.has(edge.prerequisite_item_id)) {
      return "dependency_invalid";
    }
    if (edge.dependent_item_id === edge.prerequisite_item_id) return "dependency_invalid";
    if (!edge.reason.trim()) return "dependency_reason_required";
    const key = `${edge.dependent_item_id}\u0000${edge.prerequisite_item_id}`;
    if (seen.has(key)) return "dependency_invalid";
    seen.add(key);
  }
  return hasCycle(content.edges) ? "cycle" : null;
}

function hasCycle(edges: GoalPlanEdge[]): boolean {
  const next = new Map<string, string[]>();
  const nodes = new Set<string>();
  for (const edge of edges) {
    const list = next.get(edge.prerequisite_item_id);
    if (list) list.push(edge.dependent_item_id);
    else next.set(edge.prerequisite_item_id, [edge.dependent_item_id]);
    nodes.add(edge.prerequisite_item_id);
    nodes.add(edge.dependent_item_id);
  }
  const state = new Map<string, "visiting" | "done">();
  const visit = (node: string): boolean => {
    if (state.get(node) === "visiting") return true;
    if (state.get(node) === "done") return false;
    state.set(node, "visiting");
    for (const dependent of next.get(node) ?? []) {
      if (visit(dependent)) return true;
    }
    state.set(node, "done");
    return false;
  };
  return [...nodes].some(visit);
}

export function prerequisitesOf(itemId: string, edges: GoalPlanEdge[]): GoalPlanEdge[] {
  return edges.filter((edge) => edge.dependent_item_id === itemId);
}

export interface PlanResourceView {
  id: string;
  type: string;
  label: string | null;
  ref: string;
  read: boolean;
}

export interface PlanInputContextView {
  projectId: string | null;
  projectTitle: string | null;
  projectDescription: string | null;
  resources: PlanResourceView[];
  missing: string[];
  readAt: string | null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

function asString(value: unknown): string | null {
  return typeof value === "string" && value.length > 0 ? value : null;
}

/** Renders a resource pointer without pretending it holds read content. */
export function describeResourceRef(ref: unknown): string {
  if (typeof ref === "string") return ref;
  if (Array.isArray(ref)) return ref.map((entry) => describeResourceRef(entry)).join(", ");
  if (isRecord(ref)) {
    for (const key of ["url", "repo", "repository", "branch", "path", "name", "title", "id"]) {
      const value = ref[key];
      if (typeof value === "string" && value.length > 0) {
        const branch = typeof ref.branch === "string" ? `@${ref.branch}` : "";
        return key === "repo" || key === "repository" ? `${value}${branch}` : value;
      }
    }
    const json = JSON.stringify(ref);
    return json.length > 160 ? `${json.slice(0, 157)}…` : json;
  }
  return "";
}

export function readInputContext(raw: GoalPlanContextSnapshot): PlanInputContextView {
  const resources: PlanResourceView[] = [];
  if (Array.isArray(raw.resources)) {
    for (const entry of raw.resources) {
      if (!isRecord(entry)) continue;
      resources.push({
        id: asString(entry.id) ?? "",
        type: asString(entry.type) ?? "",
        label: asString(entry.label),
        ref: describeResourceRef(entry.ref),
        // Only an explicit `true` may claim the run read the content.
        read: entry.read === true,
      });
    }
  }
  return {
    projectId: asString(raw.project_id),
    projectTitle: asString(raw.project_title),
    projectDescription: asString(raw.project_description),
    resources,
    missing: Array.isArray(raw.missing)
      ? raw.missing.filter((entry): entry is string => typeof entry === "string")
      : [],
    readAt: asString(raw.read_at),
  };
}

export interface PlanActualBasisView {
  repoSha: string | null;
  files: string[];
  tasks: string[];
  readResourceIds: string[];
  missing: string[];
  readAt: string | null;
}

function describeBasisEntry(entry: unknown): string | null {
  if (typeof entry === "string") return entry;
  if (!isRecord(entry)) return null;
  const path = asString(entry.path) ?? asString(entry.file) ?? asString(entry.issue_id);
  if (!path) return null;
  const lines =
    typeof entry.lines === "string"
      ? entry.lines
      : Array.isArray(entry.lines)
        ? entry.lines.join(",")
        : null;
  const summary = asString(entry.summary) ?? asString(entry.title);
  return [lines ? `${path}:${lines}` : path, summary].filter(Boolean).join(" — ");
}

/** What a planning run recorded actually reading, never assumed. */
export function readActualBasis(raw: GoalPlanContextSnapshot): PlanActualBasisView {
  const files: string[] = [];
  for (const entry of Array.isArray(raw.files) ? raw.files : []) {
    const label = describeBasisEntry(entry);
    if (label) files.push(label);
  }
  const tasks: string[] = [];
  for (const entry of Array.isArray(raw.tasks) ? raw.tasks : []) {
    const label = describeBasisEntry(entry);
    if (label) tasks.push(label);
  }
  const readResourceIds: string[] = [];
  for (const entry of Array.isArray(raw.resources) ? raw.resources : []) {
    if (isRecord(entry) && entry.read === true) {
      const id = asString(entry.id) ?? describeResourceRef(entry.ref);
      if (id) readResourceIds.push(id);
    }
  }
  return {
    repoSha: asString(raw.repo_sha),
    files,
    tasks,
    readResourceIds,
    missing: Array.isArray(raw.missing)
      ? raw.missing.filter((entry): entry is string => typeof entry === "string")
      : [],
    readAt: asString(raw.read_at),
  };
}

export function hasActualBasis(basis: PlanActualBasisView): boolean {
  return (
    !!basis.repoSha ||
    basis.files.length > 0 ||
    basis.tasks.length > 0 ||
    basis.readResourceIds.length > 0
  );
}