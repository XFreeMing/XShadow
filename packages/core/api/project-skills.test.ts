// @vitest-environment node

import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

it("distinguishes an empty project skill catalog from an unreadable response", async () => {
  const empty = { can_manage: true, skills: [], proposals: [] };
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify(empty)))
    .mockResolvedValueOnce(new Response(JSON.stringify({ ...empty, can_manage: "true" }))));
  const api = new ApiClient("https://api.example.test");
  expect(await api.listProjectSkills("project-1")).toEqual(empty);
  expect(await api.listProjectSkills("project-1")).toBeNull();
});
