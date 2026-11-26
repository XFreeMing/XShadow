// @vitest-environment jsdom
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { GraphCanvasEdges } from "./graph-visuals";

describe("GraphCanvasEdges", () => {
  it("renders SVG edges with arrows and preserved data-org-edge", () => {
    const { container } = render(
      <svg>
        <GraphCanvasEdges
          edges={[
            {
              id: "p1->c1",
              "data-org-edge": "p1->c1",
              path: "M 0 0 L 10 10",
              isHighlighted: true,
            },
            {
              id: "p1->c2",
              "data-org-edge": "p1->c2",
              path: "M 0 0 L 20 20",
              isDimmed: true,
              isDashed: true,
            },
          ]}
        />
      </svg>,
    );

    const highlightedEdge = container.querySelector('[data-org-edge="p1->c1"]');
    const dimmedEdge = container.querySelector('[data-org-edge="p1->c2"]');

    expect(highlightedEdge).toBeInTheDocument();
    expect(highlightedEdge).toHaveClass("text-brand");
    expect(highlightedEdge?.getAttribute("stroke-width")).toBe("2.25");
    expect(highlightedEdge?.getAttribute("marker-end")).toContain("url(#");

    expect(dimmedEdge).toBeInTheDocument();
    expect(dimmedEdge?.getAttribute("opacity")).toBe("0.16");
    expect(highlightedEdge).not.toHaveAttribute("stroke-dasharray");
    expect(dimmedEdge).toHaveAttribute("stroke-dasharray");
  });
});
