export type Block = { id?: string; type: string; data: Record<string, any> };
export type RichNode = {
  type: string;
  text?: string;
  attrs?: Record<string, any>;
  marks?: { type: string; attrs?: Record<string, any> }[];
  content?: RichNode[];
};
const escape = (value: string) =>
  value.replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ]!,
  );
export function inlineNodes(html: string, doc?: Document): RichNode[] {
  const parserDoc =
    doc || (typeof document !== "undefined" ? document : undefined);
  if (!parserDoc)
    return [
      { type: "text", text: String(html || "").replace(/<[^>]*>/g, "") },
    ].filter((n) => n.text);
  const parsed = new parserDoc.defaultView!.DOMParser().parseFromString(
    String(html || ""),
    "text/html",
  );
  function walk(node: Node, marks: RichNode["marks"] = []): RichNode[] {
    if (node.nodeType === 3)
      return node.textContent
        ? [{ type: "text", text: node.textContent, marks }]
        : [];
    if (node.nodeType !== 1) return [];
    const el = node as Element,
      tag = el.tagName.toLowerCase();
    if (["script", "style", "iframe", "object", "svg", "img"].includes(tag))
      return [];
    if (tag === "br") return [{ type: "hardBreak" }];
    const type = (
      {
        b: "bold",
        strong: "bold",
        i: "italic",
        em: "italic",
        u: "underline",
        s: "strike",
        del: "strike",
        code: "code",
        mark: "highlight",
      } as Record<string, string>
    )[tag];
    let next = marks;
    if (type) next = [...(marks || []), { type }];
    if (tag === "a") {
      const href = el.getAttribute("href") || "";
      if (/^(https?:|mailto:|\/(?!\/)|#)/i.test(href))
        next = [...(marks || []), { type: "link", attrs: { href } }];
    }
    return Array.from(node.childNodes).flatMap((child) => walk(child, next));
  }
  return Array.from(parsed.body.childNodes).flatMap((node) => walk(node));
}
function serializeInline(nodes: RichNode[] = []): string {
  return nodes
    .map((node) => {
      if (node.type === "hardBreak") return "<br>";
      let text = escape(node.text || "");
      for (const mark of node.marks || []) {
        const tag = (
          {
            bold: "strong",
            italic: "em",
            underline: "u",
            strike: "s",
            code: "code",
            highlight: "mark",
          } as Record<string, string>
        )[mark.type];
        if (tag) text = `<${tag}>${text}</${tag}>`;
        else if (
          mark.type === "link" &&
          /^(https?:|mailto:|\/(?!\/)|#)/i.test(mark.attrs?.href || "")
        )
          text = `<a href="${escape(mark.attrs!.href)}">${text}</a>`;
      }
      return text;
    })
    .join("");
}
export function blocksToDoc(blocks: Block[], doc?: Document): RichNode {
  return {
    type: "doc",
    content: blocks.length
      ? blocks.map((block) => {
          switch (block.type) {
            case "paragraph":
              return {
                type: "paragraph",
                attrs: { blockId: block.id },
                content: inlineNodes(block.data.text, doc),
              };
            case "header":
              return {
                type: "heading",
                attrs: {
                  level: Math.min(
                    4,
                    Math.max(2, Number(block.data.level) || 2),
                  ),
                  blockId: block.id,
                },
                content: inlineNodes(block.data.text, doc),
              };
            case "code":
              return {
                type: "codeBlock",
                attrs: {
                  language: block.data.language || null,
                  blockId: block.id,
                },
                content: block.data.code
                  ? [{ type: "text", text: block.data.code }]
                  : [],
              };
            case "delimiter":
              return { type: "horizontalRule", attrs: { blockId: block.id } };
            default:
              return { type: "preservedBlock", attrs: { block } };
          }
        })
      : [{ type: "paragraph" }],
  };
}
export function docToBlocks(doc: RichNode): Block[] {
  function listItems(nodes: RichNode[] = []): any[] {
    return nodes.map((item) => ({
      content: serializeInline(
        (item.content || [])
          .filter((n) => n.type === "paragraph")
          .flatMap((n) => n.content || []),
      ),
      meta: {},
      items: (item.content || [])
        .filter((n) => n.type === "bulletList" || n.type === "orderedList")
        .flatMap((n) => listItems(n.content)),
    }));
  }
  return (doc.content || []).map((node) => {
    const id = node.attrs?.blockId ? { id: node.attrs.blockId } : {};
    switch (node.type) {
      case "preservedBlock":
        return node.attrs!.block;
      case "heading":
        return {
          ...id,
          type: "header",
          data: {
            level: node.attrs?.level || 2,
            text: serializeInline(node.content),
          },
        };
      case "horizontalRule":
        return { ...id, type: "delimiter", data: {} };
      case "codeBlock":
        return {
          ...id,
          type: "code",
          data: {
            code: (node.content || []).map((n) => n.text || "").join(""),
            language: node.attrs?.language || "",
          },
        };
      case "bulletList":
      case "orderedList":
        return {
          ...id,
          type: "list",
          data: {
            style: node.type === "orderedList" ? "ordered" : "unordered",
            items: listItems(node.content),
          },
        };
      case "blockquote":
        return {
          ...id,
          type: "quote",
          data: {
            text: serializeInline(
              (node.content || []).flatMap((n) => n.content || []),
            ),
            caption: "",
            alignment: "left",
          },
        };
      default:
        return {
          ...id,
          type: "paragraph",
          data: { text: serializeInline(node.content) },
        };
    }
  });
}
