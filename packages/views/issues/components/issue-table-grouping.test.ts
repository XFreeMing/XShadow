// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { IssueTableGroupDescriptor } from "@multica/core/types";
import { issueTableGroupLabel, tableGroupSpec } from "./issue-table-grouping";

describe("issue-table-grouping", () => {
  it("maps table grouping keys to native IssueTableGroupSpec values", () => {
    expect(tableGroupSpec("none").kind).toBe("none");
    expect(tableGroupSpec("status")).toEqual({ kind: "status" });
    expect(tableGroupSpec("property:prop-1")).toEqual({
      kind: "property",
      property_id: "prop-1",
    });
  });

  it("formats native status group labels", () => {
    const descriptor: IssueTableGroupDescriptor = {
      key: "status:todo",
      count: 3,
      value: { kind: "status", status: "todo" },
    };
    expect(
      issueTableGroupLabel(descriptor, {
        resolveStatusLabel: (status) => `Label:${status}`,
        getActorName: () => "Actor",
        groupProjectMap: new Map(),
        propertyById: new Map(),
        translate: {
          unassigned: "None",
          noProject: "No project",
          noParent: "No parent",
          noValue: "No value",
          valueUnavailable: "Unavailable",
          customPropertyTrue: "True",
          customPropertyFalse: "False",
        },
      }),
    ).toBe("Label:todo");
  });
});