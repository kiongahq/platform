import {Select} from "@/components/ui/Select";
import React, { useState } from "react";
import {CodeEditor} from "@/components/ui/CodeEditor";
import { NodeViewWrapper, type NodeViewProps } from "@tiptap/react";
import type { Block } from "../../lib/editor-blocks";
const plain = (html: string) =>
  new DOMParser().parseFromString(html || "", "text/html").body.textContent ||
  "";
export function PreservedBlockView({
  node,
  updateAttributes,
  editor,
  deleteNode,
}: NodeViewProps) {
  const block = node.attrs.block as Block;
  const d = block.data;
  const [editing, setEditing] = useState(false);
  function update(patch: Record<string, unknown>) {
    updateAttributes({ block: { ...block, data: { ...d, ...patch } } });
  }
  function field(label: string, name: string, multiline = false) {
    if (name === "code") return <div className="field" key={name}><span>{label}</span><CodeEditor label={label} language={d.language || "text"} value={d[name] || ""} onChange={event=>update({[name]:event.target.value})}/></div>;
    return (
      <label key={name}>
        {label}
        {multiline ? (
          <textarea
            value={d[name] || ""}
            onChange={(e) => update({ [name]: e.target.value })}
          />
        ) : (
          <input
            value={d[name] || ""}
            onChange={(e) => update({ [name]: e.target.value })}
          />
        )}
      </label>
    );
  }
  function items(
    values: any[],
    onChange?: (next: any[]) => void,
  ): React.ReactNode {
    return (
      <ul>
        {values.map((item, index) => (
          <li key={index}>
            {d.style === "checklist" && (
              <input
                type="checkbox"
                aria-label={`Complete list item ${index + 1}`}
                checked={!!item.meta?.checked}
                disabled={!onChange}
                onChange={(event) =>
                  onChange?.(
                    values.map((old, i) =>
                      i !== index
                        ? old
                        : {
                            ...(typeof old === "string"
                              ? { content: old, items: [] }
                              : old),
                            meta: {
                              ...(old.meta || {}),
                              checked: event.target.checked,
                            },
                          },
                    ),
                  )
                }
              />
            )}
            {onChange ? (
              <input
                aria-label={`List item ${index + 1}`}
                value={typeof item === "string" ? item : item.content}
                onChange={(e) =>
                  onChange(
                    values.map((old, i) =>
                      i !== index
                        ? old
                        : typeof old === "string"
                          ? e.target.value
                          : { ...old, content: e.target.value },
                    ),
                  )
                }
              />
            ) : (
              plain(typeof item === "string" ? item : item.content)
            )}
            {onChange && (
              <span className="button-row">
                <button
                  type="button"
                  aria-label={`Remove list item ${index + 1}`}
                  disabled={values.length === 1}
                  onClick={() => onChange(values.filter((_, i) => i !== index))}
                >
                  Remove item
                </button>
                <button
                  type="button"
                  aria-label={`Add child to list item ${index + 1}`}
                  onClick={() =>
                    onChange(
                      values.map((old, i) =>
                        i !== index
                          ? old
                          : {
                              ...(typeof old === "string"
                                ? { content: old, meta: {} }
                                : old),
                              items: [
                                ...(old.items || []),
                                { content: "", meta: {}, items: [] },
                              ],
                            },
                      ),
                    )
                  }
                >
                  Add nested item
                </button>
              </span>
            )}
            {item.items?.length > 0 &&
              items(
                item.items,
                onChange
                  ? (next) =>
                      onChange(
                        values.map((old, i) =>
                          i === index ? { ...old, items: next } : old,
                        ),
                      )
                  : undefined,
              )}
          </li>
        ))}
      </ul>
    );
  }
  const imageURL = d.media_id
    ? `/media/blog/${encodeURIComponent(d.media_id)}/960`
    : /^\/(?!\/)/.test(d.file?.url || "")
      ? d.file.url
      : "";
  return (
    <NodeViewWrapper className="studio-preserved" contentEditable={false}>
      <div className="studio-block-heading" data-drag-handle>
        <strong>{block.type === "warning" ? "Callout" : block.type}</strong>
        {editor.isEditable && (
          <span>
            <button type="button" onClick={() => setEditing(!editing)}>
              {editing ? "Done" : "Edit"}
            </button>
            <button type="button" onClick={deleteNode}>
              Remove
            </button>
          </span>
        )}
      </div>
      {block.type === "image" && (
        <>
          {imageURL && <img src={imageURL} alt={d.alt || ""} />}
          {editing ? (
            <>
              {field("Image description", "alt")}
              {field("Caption", "caption")}
              <label>
                Width
                <Select
                  value={d.alignment || "normal"}
                  onChange={(e) => update({ alignment: e.target.value })}
                >
                  <option value="normal">Normal</option>
                  <option value="wide">Wide</option>
                  <option value="full">Full</option>
                </Select>
              </label>
            </>
          ) : (
            <p>{plain(d.caption)}</p>
          )}
        </>
      )}
      {block.type === "quote" &&
        (editing ? (
          <>
            {field("Quote", "text", true)}
            {field("Attribution", "caption")}
          </>
        ) : (
          <blockquote>
            {plain(d.text)}
            <cite>{plain(d.caption)}</cite>
          </blockquote>
        ))}
      {block.type === "warning" &&
        (editing ? (
          <>
            {field("Title", "title")}
            {field("Message", "message", true)}
          </>
        ) : (
          <aside>
            <b>{plain(d.title)}</b>
            <p>{plain(d.message)}</p>
          </aside>
        ))}
      {block.type === "list" && (
        <>
          {editing && (
            <label>
              List style
              <Select
                value={d.style || "unordered"}
                onChange={(event) => update({ style: event.target.value })}
              >
                <option value="unordered">Bullets</option>
                <option value="ordered">Numbered</option>
                <option value="checklist">Checklist</option>
              </Select>
            </label>
          )}
          {items(
            d.items || [],
            editing ? (next) => update({ items: next }) : undefined,
          )}
          {editing && (
            <button
              type="button"
              onClick={() =>
                update({
                  items: [
                    ...(d.items || []),
                    { content: "", items: [], meta: {} },
                  ],
                })
              }
            >
              Add item
            </button>
          )}
        </>
      )}
      {block.type === "table" && (
        <>
          <table>
            <tbody>
              {(d.content || []).map((row: string[], i: number) => (
                <tr key={i}>
                  {row.map((cell, j) => (
                    <td key={j}>
                      {editing ? (
                        <input
                          aria-label={`Row ${i + 1}, column ${j + 1}`}
                          value={cell}
                          onChange={(e) =>
                            update({
                              content: d.content.map(
                                (r: string[], ri: number) =>
                                  ri === i
                                    ? r.map((c, ci) =>
                                        ci === j ? e.target.value : c,
                                      )
                                    : r,
                              ),
                            })
                          }
                        />
                      ) : (
                        plain(cell)
                      )}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          {editing && (
            <>
              <button
                type="button"
                onClick={() =>
                  update({
                    content: [
                      ...d.content,
                      Array(d.content[0]?.length || 2).fill(""),
                    ],
                  })
                }
              >
                Add row
              </button>
              <button
                type="button"
                onClick={() =>
                  update({
                    content: d.content.map((row: string[]) => [...row, ""]),
                  })
                }
              >
                Add column
              </button>
              <label>
                <input
                  type="checkbox"
                  checked={!!d.withHeadings}
                  onChange={(e) => update({ withHeadings: e.target.checked })}
                />
                Header row
              </label>
            </>
          )}
        </>
      )}
      {block.type === "embed" && (
        <>
          {editing ? (
            <>
              {field("Source address", "source")}
              {field("Caption", "caption")}
            </>
          ) : (
            <p>
              {d.source}
              <br />
              {plain(d.caption)}
            </p>
          )}
        </>
      )}
    </NodeViewWrapper>
  );
}
