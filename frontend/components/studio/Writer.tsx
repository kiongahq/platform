"use client";
import TitleIcon from "@mui/icons-material/Title";
import TextFieldsIcon from "@mui/icons-material/TextFields";
import FormatBoldIcon from "@mui/icons-material/FormatBold";
import FormatItalicIcon from "@mui/icons-material/FormatItalic";
import FormatUnderlinedIcon from "@mui/icons-material/FormatUnderlined";
import FormatStrikethroughIcon from "@mui/icons-material/FormatStrikethrough";
import HighlightIcon from "@mui/icons-material/Highlight";
import FormatListBulletedIcon from "@mui/icons-material/FormatListBulleted";
import FormatListNumberedIcon from "@mui/icons-material/FormatListNumbered";
import FormatQuoteIcon from "@mui/icons-material/FormatQuote";
import CodeIcon from "@mui/icons-material/Code";
import DataObjectIcon from "@mui/icons-material/DataObject";
import HorizontalRuleIcon from "@mui/icons-material/HorizontalRule";
import LinkIcon from "@mui/icons-material/Link";
import ChecklistIcon from "@mui/icons-material/Checklist";
import InfoOutlinedIcon from "@mui/icons-material/InfoOutlined";
import TableChartOutlinedIcon from "@mui/icons-material/TableChartOutlined";
import ImageOutlinedIcon from "@mui/icons-material/ImageOutlined";
import SmartDisplayOutlinedIcon from "@mui/icons-material/SmartDisplayOutlined";
import UndoIcon from "@mui/icons-material/Undo";
import RedoIcon from "@mui/icons-material/Redo";
const toolIcons = {
  "Heading": TitleIcon,
  "Subheading": TextFieldsIcon,
  "Bold": FormatBoldIcon,
  "Italic": FormatItalicIcon,
  "Underline": FormatUnderlinedIcon,
  "Strike": FormatStrikethroughIcon,
  "Highlight": HighlightIcon,
  "Bullets": FormatListBulletedIcon,
  "Numbered list": FormatListNumberedIcon,
  "Quote": FormatQuoteIcon,
  "Inline code": CodeIcon,
  "Code": DataObjectIcon,
  "Divider": HorizontalRuleIcon,
  "Link": LinkIcon,
  "Checklist": ChecklistIcon,
  "Callout": InfoOutlinedIcon,
  "Table": TableChartOutlinedIcon,
  "Image": ImageOutlinedIcon,
  "Embed": SmartDisplayOutlinedIcon,
  "Undo": UndoIcon,
  "Redo": RedoIcon,
};
import React, { useEffect, useRef, useState } from "react";
import {
  Editor,
  EditorContent,
  Extension,
  Node,
  useEditor,
  useEditorState,
  ReactNodeViewRenderer,
} from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import Highlight from "@tiptap/extension-highlight";
import Placeholder from "@tiptap/extension-placeholder";
import { PreservedBlockView } from "./PreservedBlockView";
import {
  blocksToDoc,
  docToBlocks,
  type Block,
  type RichNode,
} from "@/lib/editor-blocks";
import { uploadMedia } from "@/lib/editorial";
const BlockIdentity = Extension.create({
  name: "blockIdentity",
  addGlobalAttributes() {
    return [
      {
        types: ["paragraph", "heading", "codeBlock", "horizontalRule"],
        attributes: { blockId: { default: null } },
      },
    ];
  },
});
const PreservedBlock = Node.create({
  name: "preservedBlock",
  group: "block",
  atom: true,
  draggable: true,
  addAttributes() {
    return { block: { default: null } };
  },
  parseHTML() {
    return [];
  },
  renderHTML({ node }) {
    return [
      "div",
      { "data-content-block": node.attrs.block?.type || "block" },
      node.attrs.block?.type || "Content block",
    ];
  },
  addNodeView() {
    return ReactNodeViewRenderer(PreservedBlockView);
  },
});
export function Writer({
  blocks,
  readOnly,
  loadKey,
  onChange,
  onReady,
  notify,
}: {
  blocks: Block[];
  readOnly: boolean;
  loadKey: number;
  onChange: (blocks: Block[]) => void;
  onReady: (editor: Editor) => void;
  notify: (message: string) => void;
}) {
  const changed = useRef(onChange);
  changed.current = onChange;
  const file = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const editor = useEditor({
    immediatelyRender: false,
    extensions: [
      StarterKit.configure({
        heading: { levels: [2, 3, 4] },
        link: { openOnClick: false, protocols: ["https", "http", "mailto"] },
      }),
      Highlight,
      Placeholder.configure({
        placeholder:
          "Write your article. Use the toolbar for formatting and content blocks.",
      }),
      BlockIdentity,
      PreservedBlock,
    ],
    content: blocksToDoc(blocks),
    editable: !readOnly,
    editorProps: {
      attributes: {
        class: "studio-prose",
        "aria-label": "Article body",
        role: "textbox",
        "aria-multiline": "true",
      },
    },
    onCreate: ({ editor }) => onReady(editor),
    onUpdate: ({ editor }) =>
      changed.current(docToBlocks(editor.getJSON() as RichNode)),
  });
  useEffect(() => {
    if (editor) editor.setEditable(!readOnly);
  }, [editor, readOnly]);
  useEffect(() => {
    if (editor)
      editor.commands.setContent(blocksToDoc(blocks), { emitUpdate: false });
  }, [editor, loadKey]);
  useEditorState({ editor, selector: ({ editor }) => editor?.state });
  if (!editor) return <p role="status">Opening editor…</p>;
  const add = (block: Block) =>
    editor
      .chain()
      .focus()
      .insertContent({ type: "preservedBlock", attrs: { block } })
      .run();
  const buttons: [string, () => void, string][] = [
    [
      "Heading",
      () => {
        editor.chain().focus().toggleHeading({ level: 2 }).run();
      },
      "heading",
    ],
    [
      "Subheading",
      () => {
        editor.chain().focus().toggleHeading({ level: 3 }).run();
      },
      "",
    ],
    [
      "Bold",
      () => {
        editor.chain().focus().toggleBold().run();
      },
      "bold",
    ],
    [
      "Italic",
      () => {
        editor.chain().focus().toggleItalic().run();
      },
      "italic",
    ],
    [
      "Underline",
      () => {
        editor.chain().focus().toggleUnderline().run();
      },
      "underline",
    ],
    [
      "Strike",
      () => {
        editor.chain().focus().toggleStrike().run();
      },
      "strike",
    ],
    [
      "Highlight",
      () => {
        editor.chain().focus().toggleHighlight().run();
      },
      "highlight",
    ],
    [
      "Bullets",
      () => {
        editor.chain().focus().toggleBulletList().run();
      },
      "bulletList",
    ],
    [
      "Numbered list",
      () => {
        editor.chain().focus().toggleOrderedList().run();
      },
      "orderedList",
    ],
    [
      "Quote",
      () => {
        editor.chain().focus().toggleBlockquote().run();
      },
      "blockquote",
    ],
    [
      "Inline code",
      () => {
        editor.chain().focus().toggleCode().run();
      },
      "code",
    ],
    [
      "Code",
      () => {
        editor.chain().focus().toggleCodeBlock().run();
      },
      "codeBlock",
    ],
    [
      "Divider",
      () => {
        editor.chain().focus().setHorizontalRule().run();
      },
      "",
    ],
    [
      "Link",
      () => {
        const href = window.prompt(
          "Link address (https://…)",
          editor.getAttributes("link").href || "https://",
        );
        if (href === "") editor.chain().focus().unsetLink().run();
        else if (href && /^(https?:|mailto:)/i.test(href))
          editor.chain().focus().setLink({ href }).run();
      },
      "link",
    ],
    [
      "Checklist",
      () =>
        add({
          type: "list",
          data: {
            style: "checklist",
            items: [
              { content: "List item", meta: { checked: false }, items: [] },
            ],
          },
        }),
      "",
    ],
    [
      "Callout",
      () =>
        add({
          type: "warning",
          data: { title: "Note", message: "Write your note here." },
        }),
      "",
    ],
    [
      "Table",
      () =>
        add({
          type: "table",
          data: {
            withHeadings: true,
            content: [
              ["Column 1", "Column 2"],
              ["", ""],
            ],
          },
        }),
      "",
    ],
    ["Image", () => file.current?.click(), ""],
    [
      "Embed",
      () => {
        const source = window.prompt("YouTube, Vimeo or GitHub Gist address");
        if (!source) return;
        try {
          const url = new URL(source);
          const service =
            url.hostname.includes("youtube") || url.hostname === "youtu.be"
              ? "youtube"
              : url.hostname.includes("vimeo")
                ? "vimeo"
                : url.hostname === "gist.github.com"
                  ? "github"
                  : null;
          if (url.protocol !== "https:" || !service) throw new Error();
          add({ type: "embed", data: { service, source, caption: "" } });
        } catch {
          notify("Use an https:// YouTube, Vimeo or GitHub Gist address.");
        }
      },
      "",
    ],
    [
      "Undo",
      () => {
        editor.chain().focus().undo().run();
      },
      "",
    ],
    [
      "Redo",
      () => {
        editor.chain().focus().redo().run();
      },
      "",
    ],
  ];
  return (
    <>
      <div
        className="studio-toolbar"
        role="toolbar"
        aria-label="Text formatting"
      >
        {buttons.map(([label, run, active]) => {
          const ToolIcon = toolIcons[label as keyof typeof toolIcons];
          const pressed = label === "Heading" ? editor.isActive("heading", { level: 2 })
            : label === "Subheading" ? editor.isActive("heading", { level: 3 })
            : active ? editor.isActive(active) : undefined;
          return (
          <button
            key={label}
            type="button"
            disabled={readOnly || uploading}
            aria-label={label}
            aria-pressed={pressed}
            data-tooltip={label}
            onMouseDown={(e) => e.preventDefault()}
            onClick={run}
          >
            <ToolIcon fontSize="small" aria-hidden="true" />
          </button>
          );
        })}
      </div>
      <input
        ref={file}
        type="file"
        className="sr-only"
        aria-label="Upload article image"
        accept="image/jpeg,image/png,image/webp,image/gif"
        onChange={async (e) => {
          const image = e.target.files?.[0];
          if (!image) return;
          setUploading(true);
          try {
            const result = await uploadMedia(image);
            add({
              type: "image",
              data: {
                media_id: result.file.media_id,
                file: result.file,
                alt: "",
                caption: "",
                alignment: "normal",
              },
            });
            notify("Image uploaded. Add a description before publishing.");
          } catch (error) {
            notify((error as Error).message);
          } finally {
            setUploading(false);
            e.target.value = "";
          }
        }}
      />
      {uploading && <p role="status">Uploading image…</p>}
      <EditorContent editor={editor} />
    </>
  );
}
