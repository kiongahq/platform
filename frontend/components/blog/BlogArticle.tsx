"use client";
import { useEffect, useState, useRef } from "react";
import { request, APIError } from "@/lib/api";
import { highlightCode } from "@/lib/highlight";
import type { Post } from "@/lib/api";
import { PostCard, blogDate } from "./PostCard";
import { ArticleBlocks } from "./ArticleBlocks";
import type { Block } from "@/lib/editor-blocks";
export function BlogArticle() {
  const [slug, setSlug] = useState<string | null>(null);
  const body = useRef<HTMLDivElement>(null);
  useEffect(() => {
    setSlug(new URLSearchParams(location.search).get("slug") || "");
  }, []);
  const [post, setPost] = useState<(Post & { blocks?: Block[] }) | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!slug) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    request<Post & { blocks?: Block[] }>(
      `/api/v1/blogs/${encodeURIComponent(slug)}`,
      { signal: controller.signal },
    )
      .then((value) => {
        if (!controller.signal.aborted) setPost(value);
      })
      .catch((failure) => {
        if (!controller.signal.aborted)
          setError(
            failure instanceof APIError && failure.status === 404
              ? "Article not found."
              : failure.message,
          );
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [slug]);
  useEffect(() => {
    if (!post) return;
    document.title = `${post.seo?.title || post.title} — Kionga Engineering`;
    for (const [name, content, attribute] of [
      ["description", post.seo?.description || post.summary, "name"],
      ["og:title", post.seo?.title || post.title, "property"],
      ["og:description", post.seo?.description || post.summary, "property"],
      [
        "og:image",
        post.cover ? new URL(post.cover.url, location.origin).href : "",
        "property",
      ],
    ]) {
      let meta = document.head.querySelector(`meta[${attribute}="${name}"]`);
      if (!meta) {
        meta = document.createElement("meta");
        meta.setAttribute(attribute, name);
        document.head.append(meta);
      }
      meta.setAttribute("content", content);
    }
    if (body.current) void highlightCode(body.current);
  }, [post]);
  if (slug === "" || error)
    return (
      <section className="blog-index-shell">
        <h1>{error || "Article not found."}</h1>
        <a href="/blog">Browse all articles</a>
      </section>
    );
  if (loading || !post)
    return (
      <p className="blog-index-shell" role="status">
        Loading article…
      </p>
    );
  return (
    <article className="article-shell">
      <a href="/blog">All articles</a>
      <header id="article-header" className="article-header">
        <div className="blog-tags">
          {post.tags?.map((tag) => (
            <a key={tag} href={`/blog?tag=${encodeURIComponent(tag)}`}>
              {tag}
            </a>
          ))}
        </div>
        <h1>{post.title}</h1>
        <p>{post.summary}</p>
        <div className="article-byline">
          <a href={`/blog?author=${encodeURIComponent(post.author)}`}>
            {post.author}
          </a>
          <time dateTime={post.published_at || post.created_at}>
            {blogDate(post.published_at || post.created_at)}
          </time>
          <span>{post.reading_minutes || 1} min read</span>
        </div>
      </header>
      {post.cover && (
        <img
          className="article-cover"
          src={post.cover.url}
          alt={post.cover.alt}
          srcSet={post.cover.srcset}
        />
      )}
      <div ref={body} id="article-content" className="article-content">
        <ArticleBlocks blocks={post.blocks || []} />
      </div>
      {!!post.related?.length && (
        <section id="article-related">
          <h2>Related articles</h2>
          <div className="blog-index-grid">
            {post.related.map((item) => (
              <PostCard key={item.slug} post={item} />
            ))}
          </div>
        </section>
      )}
    </article>
  );
}
