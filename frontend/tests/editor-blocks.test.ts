import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import {
  blocksToDoc,
  docToBlocks,
  inlineNodes,
  type Block,
} from "../lib/editor-blocks";
const doc = new JSDOM("").window.document;
test("existing media, tables, lists, quotes, embeds and callouts keep their exact payload", () => {
  const blocks: Block[] = [
    {
      id: "image-1",
      type: "image",
      data: {
        file: { media_id: "media-1", url: "/media/blog/media-1/960" },
        caption: "Diagram",
        alt: "Architecture",
        alignment: "wide",
      },
    },
    {
      type: "table",
      data: {
        withHeadings: true,
        content: [
          ["Name", "Value"],
          ["a", "<b>1</b>"],
        ],
      },
    },
    {
      type: "list",
      data: {
        style: "ordered",
        items: [
          { content: "Parent", items: [{ content: "Child", items: [] }] },
        ],
      },
    },
    {
      type: "quote",
      data: { text: "<b>Quoted</b>", caption: "Author", alignment: "left" },
    },
    {
      type: "embed",
      data: {
        service: "youtube",
        source: "https://youtube.com/watch?v=abc",
        embed: "https://www.youtube.com/embed/abc",
      },
    },
    {
      type: "warning",
      data: { title: "Note", message: "Be careful <script>bad()</script>" },
    },
  ];
  assert.deepEqual(docToBlocks(blocksToDoc(blocks, doc)), blocks);
});
test("rich paragraphs, headings, code and dividers retain content", () => {
  const blocks: Block[] = [
    { type: "paragraph", data: { text: "A <strong>bold</strong> idea" } },
    { type: "header", data: { text: "Title", level: 3 } },
    {
      type: "code",
      data: { code: 'if (a < b) { return "ok"; }', language: "go" },
    },
    { type: "delimiter", data: {} },
  ];
  assert.deepEqual(docToBlocks(blocksToDoc(blocks, doc)), blocks);
});
test("new nested lists are serialized in the API block shape", () => {
  const [block] = docToBlocks({
    type: "doc",
    content: [
      {
        type: "orderedList",
        content: [
          {
            type: "listItem",
            content: [
              {
                type: "paragraph",
                content: [{ type: "text", text: "Parent" }],
              },
              {
                type: "bulletList",
                content: [
                  {
                    type: "listItem",
                    content: [
                      {
                        type: "paragraph",
                        content: [{ type: "text", text: "Child" }],
                      },
                    ],
                  },
                ],
              },
            ],
          },
        ],
      },
    ],
  });
  assert.equal(block.data.style, "ordered");
  assert.equal(block.data.items[0].content, "Parent");
  assert.equal(block.data.items[0].items[0].content, "Child");
});

test("unsafe inline nodes and link protocols are removed", () => {
  const nodes = inlineNodes(
    '<script>bad()</script><img src=x onerror=bad()><a href="javascript:bad()">unsafe</a><a href="https://example.com">safe</a>',
    doc,
  );
  assert.equal(nodes.map((n) => n.text).join(""), "unsafesafe");
  assert.equal(nodes[0].marks?.length, 0);
  assert.equal(nodes[1].marks?.[0].attrs?.href, "https://example.com");
});
