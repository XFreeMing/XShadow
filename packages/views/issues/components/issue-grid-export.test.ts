// @vitest-environment node

import { describe, expect, it } from "vitest";
import { gridPropertyColumnId } from "@multica/core/issues/grid-columns";
import type { Issue, IssueProperty } from "@multica/core/types";
import { issueGridExportRow } from "./issue-grid-export";

function makeIssue(overrides: Partial<Issue> & Pick<Issue, "id" | "title">): Issue {
  return {
    workspace_id: "ws-1",
    number: 1,
    identifier: "XSH-1",
    description: null,
    status: "todo",
    status_category: "unstarted",
    priority: "medium",
    assignee_type: "member",
    assignee_id: "user-1",
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position: 1000,
    stage: null,
    metadata: {},
    properties: {},
    start_date: null,
    due_date: null,
    created_at: "2026-11-25T00:00:00Z",
    updated_at: "2026-11-25T00:00:00Z",
    revision: 1,
    ...overrides,
  };
}

describe("issueGridExportRow", () => {
  const estimate: IssueProperty = {
    id: "prop-est",
    workspace_id: "ws-1",
    name: "Estimate",
    type: "number",
    description: "",
    icon: "hash",
    config: {},
    position: 0,
    archived: false,
    usage_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("maps visible grid columns to canonical CSV values", () => {
    const issue = makeIssue({
      id: "uuid-1",
      title: "Ship grid export",
      issue_type_id: "type-1",
      properties: { "prop-est": 5 },
    });
    const row = issueGridExportRow(
      issue,
      ["title", "type", "status", "assignee", gridPropertyColumnId("prop-est")],
      {
        resolveStatusLabel: () => "Todo",
        issueTypeName: "Task",
        propertyById: new Map([[estimate.id, estimate]]),
        getActorName: () => "Alice",
      },
    );
    expect(row).toEqual(["Ship grid export", "Task", "Todo", "Alice", "5"]);
  });
});