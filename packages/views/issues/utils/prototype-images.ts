import { fromMarkdown } from "mdast-util-from-markdown";

export interface IssuePrototypeImage {
  alt: string;
  url: string;
}

const prototypeHeading = /^(?:prototype(?:s| images)?|outcome images|(?:任务)?原[型形][图圖]?|(?:目标|目標)?成果[图圖])$/i;

export function issuePrototypeImages(description: string | null | undefined): IssuePrototypeImage[] {
  if (!description?.includes("![")) return [];
  const { children } = fromMarkdown(description);
  const definitions = new Map(children.flatMap((node) =>
    node.type === "definition" ? [[node.identifier, node.url]] : [],
  ));
  const section = children.findIndex((node) =>
    node.type === "heading" && node.children.length === 1 &&
    node.children[0]?.type === "text" && prototypeHeading.test(node.children[0].value.trim()),
  );
  const frames: IssuePrototypeImage[] = [];
  for (const node of children.slice(section + 1)) {
    if (node.type === "heading") break;
    if (node.type === "definition") continue;
    if (node.type !== "paragraph" || !node.children.every((part) =>
      part.type === "image" || part.type === "imageReference" || (part.type === "text" && !part.value.trim()),
    )) {
      if (frames.length > 0) break;
      continue;
    }
    for (const part of node.children) {
      const url = part.type === "image" ? part.url :
        part.type === "imageReference" ? definitions.get(part.identifier) : undefined;
      if (url && (/^\/(?![/\\])/.test(url) || /^https?:\/\//i.test(url)) && "alt" in part) {
        frames.push({ url, alt: part.alt ?? "" });
      }
    }
  }
  return frames;
}
