"use client";
import React from "react";
import {CodeBlock} from "@/components/ui/CodeBlock";
import { inlineNodes, type Block, type RichNode } from "@/lib/editor-blocks";
function Inline({ nodes }: { nodes: RichNode[] }) {
  return (
    <>
      {nodes.map((node, i) => {
        if (node.type === "hardBreak") return <br key={i} />;
        let content: React.ReactNode = node.text || "";
        for (const mark of node.marks || []) {
          switch (mark.type) {
            case "bold":
              content = <strong>{content}</strong>;
              break;
            case "italic":
              content = <em>{content}</em>;
              break;
            case "underline":
              content = <u>{content}</u>;
              break;
            case "strike":
              content = <s>{content}</s>;
              break;
            case "code":
              content = <code>{content}</code>;
              break;
            case "highlight":
              content = <mark>{content}</mark>;
              break;
            case "link":
              content = (
                <a href={mark.attrs?.href} rel="noopener noreferrer">
                  {content}
                </a>
              );
          }
        }
        return <React.Fragment key={i}>{content}</React.Fragment>;
      })}
    </>
  );
}
export function RichInline({ html }: { html?: string }) {
  return <Inline nodes={inlineNodes(html || "")} />;
}
function ListItems({
  items,
  ordered,
  checklist = false,
}: {
  items: any[];
  ordered: boolean;
  checklist?: boolean;
}) {
  const Tag = ordered ? "ol" : "ul";
  return (
    <Tag>
      {items.map((item, i) => (
        <li key={i}>
          {checklist && (
            <input
              type="checkbox"
              checked={!!item.meta?.checked}
              readOnly
              disabled
              aria-label={
                item.meta?.checked ? "Completed item" : "Incomplete item"
              }
            />
          )}
          <RichInline html={typeof item === "string" ? item : item.content} />
          {item.items?.length > 0 && (
            <ListItems
              items={item.items}
              ordered={ordered}
              checklist={checklist}
            />
          )}
        </li>
      ))}
    </Tag>
  );
}
function mediaSource(data: Record<string, any>) {
  if (data.media_id)
    return `/media/blog/${encodeURIComponent(data.media_id)}/960`;
  const url = data.file?.url || "";
  return /^\/media\/blog\//.test(url) ? url : "";
}
export function ArticleBlocks({ blocks }: { blocks: Block[] }) {
  return (
    <>
      {blocks.map((block, i) => {
        const d = block.data,
          key = block.id || i;
        switch (block.type) {
          case "paragraph":
            return (
              <p key={key}>
                <RichInline html={d.text} />
              </p>
            );
          case "header": {
            const Tag = `h${Math.min(4, Math.max(2, Number(d.level) || 2))}` as
              | "h2"
              | "h3"
              | "h4";
            return (
              <Tag key={key}>
                <RichInline html={d.text} />
              </Tag>
            );
          }
          case "delimiter":
            return <hr key={key} />;
          case "code":
            return (
              <CodeBlock key={key} language={String(d.language || "text").replace(/[^\w-]/g, "")} format={false}>{d.code || ""}</CodeBlock>
            );
          case "list":
            return (
              <ListItems
                key={key}
                items={d.items || []}
                ordered={d.style === "ordered"}
                checklist={d.style === "checklist"}
              />
            );
          case "quote":
            return (
              <blockquote
                key={key}
                style={{
                  textAlign: d.alignment === "center" ? "center" : "left",
                }}
              >
                <p>
                  <RichInline html={d.text} />
                </p>
                {d.caption && (
                  <cite>
                    <RichInline html={d.caption} />
                  </cite>
                )}
              </blockquote>
            );
          case "warning":
            return (
              <aside key={key} className="article-callout">
                <strong>
                  <RichInline html={d.title} />
                </strong>
                <p>
                  <RichInline html={d.message} />
                </p>
              </aside>
            );
          case "image": {
            const source = mediaSource(d);
            return source ? (
              <figure
                key={key}
                className={`article-image image-${["wide", "full"].includes(d.alignment) ? d.alignment : "normal"}`}
              >
                <img
                  src={source}
                  srcSet={
                    d.media_id
                      ? `/media/blog/${encodeURIComponent(d.media_id)}/480 480w, /media/blog/${encodeURIComponent(d.media_id)}/960 960w, /media/blog/${encodeURIComponent(d.media_id)}/1600 1600w`
                      : undefined
                  }
                  alt={d.alt || ""}
                  loading="lazy"
                />
                {(d.caption || d.attribution) && (
                  <figcaption>
                    <RichInline html={d.caption} />
                    {d.attribution && <span>{d.attribution}</span>}
                  </figcaption>
                )}
              </figure>
            ) : null;
          }
          case "table":
            return (
              <div key={key} className="table-wrap">
                <table>
                  <tbody>
                    {(d.content || []).map((row: string[], r: number) => (
                      <tr key={r}>
                        {row.map((cell, c) =>
                          d.withHeadings && r === 0 ? (
                            <th key={c} scope="col">
                              <RichInline html={cell} />
                            </th>
                          ) : (
                            <td key={c}>
                              <RichInline html={cell} />
                            </td>
                          ),
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            );
          case "embed": {
            if (/^https:\/\/gist.github.com\//.test(d.source || ""))
              return (
                <figure key={key}>
                  <a href={d.source} rel="noopener noreferrer">
                    View code on GitHub
                  </a>
                  {d.caption && (
                    <figcaption>
                      <RichInline html={d.caption} />
                    </figcaption>
                  )}
                </figure>
              );
            let url: URL;
            try {
              url = new URL(d.embed || "");
            } catch {
              return null;
            }
            const allowed =
              url.protocol === "https:" &&
              [
                "www.youtube.com",
                "www.youtube-nocookie.com",
                "player.vimeo.com",
              ].includes(url.hostname);
            return (
              <figure key={key}>
                {allowed ? (
                  <iframe
                    src={url.href}
                    title={d.caption || `${d.service} embed`}
                    loading="lazy"
                    sandbox="allow-scripts allow-same-origin allow-presentation"
                    allowFullScreen
                  />
                ) : /^https:\/\/gist.github.com\//.test(d.source || "") ? (
                  <a href={d.source} rel="noopener noreferrer">
                    View code on GitHub
                  </a>
                ) : null}
                {d.caption && (
                  <figcaption>
                    <RichInline html={d.caption} />
                  </figcaption>
                )}
              </figure>
            );
          }
          default:
            return null;
        }
      })}
    </>
  );
}
