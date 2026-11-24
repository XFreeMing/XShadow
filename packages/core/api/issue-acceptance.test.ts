// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { AcceptanceTraceSchema } from "../types/acceptance";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

it("rejects an acceptance response with a criterion that has no version", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json({
        revision: 1,
        criteria: [{ id: "C1", versions: [], cases: [], implementations: [] }],
      }),
    ),
  );
  const client = new ApiClient("https://example.test");
  await expect(
    client.changeIssueAcceptance("DEV-1", {
      action: "criterion",
      base_revision: 0,
      criterion_id: "C1",
      text: "Deliver a report",
    }),
  ).rejects.toThrow("Malformed acceptance records");
});

it("keeps case versions and counterexample outcomes without changing legacy runs", () => {
  const run = { version: 3, candidate: "b".repeat(40), result: "pass", evidence: "first run" };
  const caseRun = { ...run, case_version: 2, run_kind: "counterexample", expected_result: "fail", result: "fail" };
  const body = {
    revision: 8,
    criteria: [{
      id: "AC1",
      versions: ["Opens the same book task"],
      implementations: [],
      cases: [{ id: "T1", specification: "Check the location", runs: [run, caseRun] }],
    }],
  };
  expect(AcceptanceTraceSchema.parse(body).criteria[0]?.cases[0]?.runs).toEqual([run, caseRun]);
  caseRun.case_version = 0;
  expect(AcceptanceTraceSchema.safeParse(body).success).toBe(false);
});
