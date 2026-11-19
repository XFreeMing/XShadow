// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
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
