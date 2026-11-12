// @vitest-environment node
import { describe, expect, it } from "vitest";
import { ApiError } from "@multica/core/api";
import type {
  GoalPlanCandidate,
  GoalPlanEdge,
  GoalPlanItem,
} from "@multica/core/types";
import {
  describeResourceRef,
  isCandidateUsable,
  isGenerationProjectConflict,
  isGoalProjectConflict,
  isProjectConflict,
  mergeCandidate,
  planConflictRevision,
  readActualBasis,
  readInputContext,
  validatePlanDraft,
} from "./plan";
import { RESOURCES } from "../locales";

function item(id: string, overrides: Partial<GoalPlanItem> = {}): GoalPlanItem {
  return {
    id,
    title: `Title ${id}`,
    outcome: "",
    acceptance_criteria: "",
    evidence_refs: [],
    binding: { type: "new", issue_id: null },
    proposed_owner: null,
    stage: null,
    coordination: null,
    ...overrides,
  };
}

function edge(
  dependent: string,
  prerequisite: string,
  reason = "needs the other",
): GoalPlanEdge {
  return { dependent_item_id: dependent, prerequisite_item_id: prerequisite, reason };
}

describe("mergeCandidate", () => {
  it("keeps user edits, existing bindings and draft-only nodes", () => {
    const draft = {
      items: [
        item("a", { title: "User title", binding: { type: "existing", issue_id: "issue-1" } }),
        item("c", { title: "Added while planning" }),
      ],
      edges: [edge("c", "a")],
      context_snapshot: { repo_sha: "old" },
    };
    const candidate: GoalPlanCandidate = {
      items: [
        item("a", { title: "Model title" }),
        item("b", { title: "New model node" }),
      ],
      edges: [edge("b", "a")],
      context_snapshot: { repo_sha: "new" },
    };

    const merged = mergeCandidate(draft, candidate, new Set(["a"]));

    const a = merged.items.find((entry) => entry.id === "a");
    expect(a?.title).toBe("User title");
    // An existing-task binding is a user decision, even when the node is not
    // otherwise edited and the candidate says "new".
    expect(a?.binding).toEqual({ type: "existing", issue_id: "issue-1" });
    expect(merged.items.find((entry) => entry.id === "b")?.title).toBe("New model node");
    expect(merged.items.find((entry) => entry.id === "c")?.title).toBe(
      "Added while planning",
    );
    expect(merged.edges).toEqual([edge("b", "a"), edge("c", "a")]);
    expect(merged.context_snapshot).toEqual({ repo_sha: "new" });
  });

  it("lets the candidate win on untouched nodes and prunes unusable edges", () => {
    const draft = {
      items: [item("a"), item("gone")],
      edges: [edge("a", "gone"), edge("a", "a")],
      context_snapshot: {},
    };
    const candidate: GoalPlanCandidate = {
      items: [item("a", { title: "Candidate title" })],
      edges: [edge("a", "missing"), edge("a", "a")],
      context_snapshot: {},
    };

    const merged = mergeCandidate(draft, candidate);

    expect(merged.items.map((entry) => entry.id).sort()).toEqual(["a", "gone"]);
    expect(merged.items.find((entry) => entry.id === "a")?.title).toBe("Candidate title");
    // The draft-only node survives, so its edge survives; the self-edge and
    // the edge to a node that exists nowhere are dropped.
    expect(merged.edges).toEqual([edge("a", "gone")]);
    expect(merged.context_snapshot).toEqual({});
  });

  it("dedupes an edge present on both sides", () => {
    const draft = {
      items: [item("a"), item("b")],
      edges: [edge("b", "a", "local reason")],
      context_snapshot: {},
    };
    const merged = mergeCandidate(draft, {
      items: [item("a"), item("b")],
      edges: [edge("b", "a", "candidate reason")],
      context_snapshot: {},
    });
    expect(merged.edges).toEqual([edge("b", "a", "candidate reason")]);
  });
});

describe("candidate usability", () => {
  it("requires stable node ids and resolvable edges", () => {
    expect(
      isCandidateUsable({
        items: [item("a"), item("b")],
        edges: [edge("b", "a")],
        context_snapshot: {},
      }),
    ).toBe(true);
    expect(
      isCandidateUsable({
        items: [item("a"), item("")],
        edges: [],
        context_snapshot: {},
      }),
    ).toBe(false);
    expect(
      isCandidateUsable({
        items: [item("a")],
        edges: [edge("a", "missing")],
        context_snapshot: {},
      }),
    ).toBe(false);
    expect(isCandidateUsable(null)).toBe(false);
  });
});

describe("validatePlanDraft", () => {
  const draft = (items: GoalPlanItem[], edges: GoalPlanEdge[] = []) => ({
    items,
    edges,
    context_snapshot: {},
  });

  it("reports the first blocking problem", () => {
    expect(validatePlanDraft(draft([item("a", { title: " " })]))).toBe("title_required");
    expect(validatePlanDraft(draft([item("a"), item("b")], [edge("b", "a", " ")]))).toBe(
      "dependency_reason_required",
    );
    expect(validatePlanDraft(draft([item("a")], [edge("a", "a")]))).toBe(
      "dependency_invalid",
    );
    expect(validatePlanDraft(draft([item("a")], [edge("a", "missing")]))).toBe(
      "dependency_invalid",
    );
    expect(
      validatePlanDraft(
        draft([item("a"), item("b")], [edge("b", "a"), edge("b", "a")]),
      ),
    ).toBe("dependency_invalid");
    expect(
      validatePlanDraft(
        draft(
          [item("a"), item("b"), item("c")],
          [edge("b", "a"), edge("c", "b"), edge("a", "c")],
        ),
      ),
    ).toBe("cycle");
    expect(
      validatePlanDraft(draft([item("a"), item("b")], [edge("b", "a")])),
    ).toBeNull();
  });
});

describe("conflict classification", () => {
  it("only treats the dedicated project conflict as such", () => {
    expect(
      isGenerationProjectConflict(
        new ApiError("conflict", 409, "Conflict", {
          code: "generation_project_conflict",
        }),
      ),
    ).toBe(true);
    expect(
      isGenerationProjectConflict(
        new ApiError("conflict", 409, "Conflict", {
          code: "plan_revision_conflict",
          current_revision: 4,
        }),
      ),
    ).toBe(false);
    expect(isGenerationProjectConflict(new ApiError("gone", 404, "Not Found"))).toBe(false);
    expect(isGenerationProjectConflict(new Error("offline"))).toBe(false);
  });

  it("reads the current revision from a revision conflict", () => {
    expect(
      planConflictRevision(
        new ApiError("conflict", 409, "Conflict", {
          code: "plan_revision_conflict",
          current_revision: 9,
        }),
      ),
    ).toBe(9);
    expect(
      planConflictRevision(
        new ApiError("conflict", 409, "Conflict", {
          code: "generation_project_conflict",
        }),
      ),
    ).toBeNull();
    expect(planConflictRevision(new Error("offline"))).toBeNull();
  });

  it("recognizes a goal that moved while a request was being prepared", () => {
    const moved = new ApiError("conflict", 409, "Conflict", {
      code: "goal_project_conflict",
    });
    expect(isGoalProjectConflict(moved)).toBe(true);
    expect(isProjectConflict(moved)).toBe(true);
    expect(isProjectConflict(
      new ApiError("conflict", 409, "Conflict", {
        code: "generation_project_conflict",
      }),
    )).toBe(true);
    // An ordinary revision conflict is contention on the saved plan, not a
    // project boundary refusal.
    const revision = new ApiError("conflict", 409, "Conflict", {
      code: "plan_revision_conflict",
      current_revision: 4,
    });
    expect(isGoalProjectConflict(revision)).toBe(false);
    expect(isProjectConflict(revision)).toBe(false);
    expect(isGoalProjectConflict(new Error("offline"))).toBe(false);
  });
});

describe("plan basis parsing", () => {
  it("never presents an unread resource pointer as read", () => {
    const view = readInputContext({
      project_id: "p1",
      project_title: "Project",
      resources: [
        {
          id: "r1",
          type: "repo",
          label: "Code",
          ref: { repo: "org/x", branch: "main" },
          read: false,
        },
        { id: "r2", type: "doc", ref: "docs/x.md", read: true },
        { id: "r3", type: "doc", ref: "docs/y.md" },
      ],
      missing: ["project_resources"],
    });

    expect(view.projectTitle).toBe("Project");
    expect(view.resources.map((resource) => resource.read)).toEqual([false, true, false]);
    expect(view.resources[0]?.ref).toBe("org/x@main");
    expect(view.missing).toEqual(["project_resources"]);
  });

  it("describes actual reads and degrades unknown refs", () => {
    const actual = readActualBasis({
      repo_sha: "abc123",
      files: [{ path: "app.ts", lines: "1-5", summary: "entry" }],
      tasks: [{ issue_id: "MUL-1", title: "Existing task" }],
      resources: [{ id: "r2", read: true }, { id: "r3", read: false }],
      missing: ["tests"],
    });
    expect(actual.repoSha).toBe("abc123");
    expect(actual.files).toEqual(["app.ts:1-5 — entry"]);
    expect(actual.tasks).toEqual(["MUL-1 — Existing task"]);
    expect(actual.readResourceIds).toEqual(["r2"]);
    expect(actual.missing).toEqual(["tests"]);
    expect(describeResourceRef({ url: "https://example.com/x" })).toBe(
      "https://example.com/x",
    );
    expect(describeResourceRef(null)).toBe("");
  });
});

describe("plan copy", () => {
  it("ships a non-empty string for every key in all eight locales", () => {
    const locales = Object.keys(RESOURCES).sort();
    expect(locales).toEqual([
      "en",
      "es",
      "fr",
      "ja",
      "ko",
      "ru",
      "zh-Hans",
      "zh-Hant",
    ]);
    const enPlan = (RESOURCES.en.goals?.plan ?? {}) as Record<string, string>;
    expect(Object.keys(enPlan).length).toBeGreaterThan(0);
    for (const locale of locales) {
      const bundle = RESOURCES[locale as keyof typeof RESOURCES] as {
        goals: { plan?: Record<string, string> };
      };
      expect(bundle.goals.plan, locale).toBeDefined();
      for (const key of Object.keys(enPlan)) {
        expect(bundle.goals.plan?.[key]?.trim(), `${locale} ${key}`).toBeTruthy();
      }
    }
  });
});