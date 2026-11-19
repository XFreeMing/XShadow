// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Agent } from "../types/agent";
import { chiefOfStaffCandidates, resolveChiefOfStaff } from "./chief-of-staff";

function agent(overrides: Partial<Agent>): Agent {
  return {
    id: "agent-1",
    workspace_id: "ws-1",
    runtime_id: "runtime-1",
    name: "幕僚长",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    status: "idle",
    max_concurrent_tasks: 3,
    model: "gpt-6-astra",
    owner_id: "user-1",
    skills: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

describe("chief of staff resolution", () => {
  it("resolves the single active agent by name in the workspace", () => {
    const result = resolveChiefOfStaff([agent({ id: "chief" })], "user-1");
    expect(result.status).toBe("ok");
    if (result.status === "ok") expect(result.agent.id).toBe("chief");
  });

  it("ignores archived agents with the same name", () => {
    const result = resolveChiefOfStaff([
      agent({ id: "old", archived_at: "2026-01-01T00:00:00Z" }),
      agent({ id: "current" }),
    ], "user-1");
    expect(result.status).toBe("ok");
    if (result.status === "ok") expect(result.agent.id).toBe("current");
  });

  it("prefers the current user's own agent when names collide", () => {
    const result = resolveChiefOfStaff([
      agent({ id: "mine", owner_id: "user-1" }),
      agent({ id: "theirs", owner_id: "user-2" }),
    ], "user-1");
    expect(result.status).toBe("ok");
    if (result.status === "ok") expect(result.agent.id).toBe("mine");
  });

  it("refuses to guess when several agents match and none is owned by the user", () => {
    const result = resolveChiefOfStaff([
      agent({ id: "a", owner_id: "user-2" }),
      agent({ id: "b", owner_id: "user-3" }),
    ], "user-1");
    expect(result.status).toBe("ambiguous");
    expect(chiefOfStaffCandidates([
      agent({ id: "a", owner_id: "user-2" }),
      agent({ id: "b", owner_id: "user-3" }),
    ])).toHaveLength(2);
  });

  it("reports a missing receiver instead of silently routing elsewhere", () => {
    expect(resolveChiefOfStaff([agent({ name: "项目经理" })], "user-1").status).toBe("missing");
    expect(resolveChiefOfStaff([], "user-1").status).toBe("missing");
  });

  it("does not let an arbitrary handler name reassign the receiver", () => {
    const agents = [agent({ id: "chief" }), agent({ id: "pm", name: "项目经理" })];
    const asRecorded = resolveChiefOfStaff(agents, "user-1", "项目经理");
    // A caller may inspect a recorded name, but the product path always passes
    // MEMBER_TODO_HANDLER, so an issue body cannot choose a different role.
    expect(asRecorded.status).toBe("ok");
    const product = resolveChiefOfStaff(agents, "user-1");
    expect(product.status).toBe("ok");
    if (product.status === "ok") expect(product.agent.id).toBe("chief");
  });
});