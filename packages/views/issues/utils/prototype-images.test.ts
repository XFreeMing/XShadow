// @vitest-environment node
import { describe, expect, it } from "vitest";
import { issuePrototypeImages } from "./prototype-images";

describe("task prototype images", () => {
  it("keeps the consecutive outcome frames at the top of existing task descriptions", () => {
    const description = [
      "[Goal](/goals/1) · Later milestone",
      "**I can open the source from a result.**",
      "![Search](/api/attachments/first/download)",
      "![Open the exact source][source]",
      "**Acceptance** Follow the citation.",
      "![Run evidence](/evidence.png)",
      "[source]: https://example.com/source.png",
    ].join("\n\n");

    expect(issuePrototypeImages(description)).toEqual([
      { alt: "Search", url: "/api/attachments/first/download" },
      { alt: "Open the exact source", url: "https://example.com/source.png" },
    ]);
  });

  it("uses an explicitly labeled prototype section and excludes unrelated pictures", () => {
    const description = "![Logo](/logo.png)\n\n## 原型图\n\n![First](/first.png) ![Second](/second.png)\n\n## 验收\n\n![Proof](/proof.png)";

    expect(issuePrototypeImages(description).map(({ alt }) => alt)).toEqual(["First", "Second"]);
  });

  it("ignores inline illustrations, code samples, and images under a later evidence heading", () => {
    const description = "A logo ![Logo](/logo.png) is included.\n\n```md\n![Sample](/sample.png)\n```\n\n## Evidence\n\n![Proof](/proof.png)";

    expect(issuePrototypeImages(description)).toEqual([]);
  });

  it("does not promote unsafe image URLs to the prototype gallery", () => {
    expect(issuePrototypeImages("![Invalid](javascript:alert(1)) ![Valid](/shot.png)")).toEqual([
      { alt: "Valid", url: "/shot.png" },
    ]);
  });
});
