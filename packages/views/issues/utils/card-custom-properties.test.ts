// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { Issue, IssueProperty } from "@multica/core/types";
import {
  cardPropertyIdsNeedComputedFetch,
  resolveCardCustomProperties,
} from "./card-custom-properties";

const formula: IssueProperty = {
  id: "prop-formula",
  workspace_id: "ws-1",
  name: "Story points",
  type: "formula",
  config: {},
  archived: false,
  position: 0,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const lookup: IssueProperty = {
  id: "prop-lookup",
  workspace_id: "ws-1",
  name: "Blocked by titles",
  type: "lookup",
  config: {},
  archived: false,
  position: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const text: IssueProperty = {
  id: "prop-text",
  workspace_id: "ws-1",
  name: "Note",
  type: "text",
  config: {},
  archived: false,
  position: 2,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const issue = {
  id: "issue-1",
  properties: { "prop-text": "hello" },
} as unknown as Issue;

describe("resolveCardCustomProperties", () => {
  it("keeps toggle order and includes computed fields without issue.properties entries", () => {
    const resolved = resolveCardCustomProperties(
      ["prop-lookup", "prop-text", "prop-formula"],
      [formula, lookup, text],
      issue,
    );
    expect(resolved.map((property) => property.id)).toEqual([
      "prop-lookup",
      "prop-text",
      "prop-formula",
    ]);
  });

  it("drops unset editable fields and grouping column property ids", () => {
    const resolved = resolveCardCustomProperties(
      ["prop-formula", "prop-text"],
      [formula, text],
      { ...issue, properties: {} },
      { excludePropertyId: "prop-formula" },
    );
    expect(resolved).toEqual([]);
  });
});

describe("cardPropertyIdsNeedComputedFetch", () => {
  it("is true only when a selected id is lookup or formula", () => {
    expect(
      cardPropertyIdsNeedComputedFetch(["prop-text"], [text, formula]),
    ).toBe(false);
    expect(
      cardPropertyIdsNeedComputedFetch(
        ["prop-text", "prop-lookup"],
        [text, lookup],
      ),
    ).toBe(true);
  });
});