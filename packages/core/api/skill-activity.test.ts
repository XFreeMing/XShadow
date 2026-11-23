// @vitest-environment node

import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

it("keeps a valid zero and treats a malformed activity response as unavailable", async () => {
  const response = {
    days: 30,
    counts: { available: 0, reviewed: 0, adopted: 0, helped: 0, neutral: 0, regressed: 0, not_applied: 0 },
    versions: [],
    runs: [],
  };
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify(response)))
    .mockResolvedValueOnce(new Response(JSON.stringify({
      ...response,
      counts: { ...response.counts, available: "0" },
    })));
  vi.stubGlobal("fetch", fetchMock);
  const api = new ApiClient("https://api.example.test");
  expect(await api.getSkillActivity("skill-1", 30)).toEqual(response);
  expect(await api.getSkillActivity("skill-1", 30)).toBeNull();
});
