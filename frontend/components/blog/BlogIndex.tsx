"use client";
import { useEffect, useState } from "react";
import { useQuery } from "@/hooks/useQuery";
import type { Post } from "@/lib/api";
import { PostCard } from "./PostCard";
export function BlogIndex() {
  const { data, loading, error } = useQuery<{
    items: Post[];
    site?: { title: string; description: string };
  }>("/api/v1/blogs");
  const [filters, setFilters] = useState({ q: "", tag: "", author: "" });
  useEffect(() => {
    const read = () => {
      const q = new URLSearchParams(location.search);
      setFilters({
        q: q.get("q") || "",
        tag: q.get("tag") || "",
        author: q.get("author") || "",
      });
    };
    read();
    window.addEventListener("popstate", read);
    return () => window.removeEventListener("popstate", read);
  }, []);
  function update(patch: Partial<typeof filters>) {
    const next = { ...filters, ...patch };
    setFilters(next);
    const query = new URLSearchParams(
      Object.entries(next).filter(([, value]) => !!value),
    );
    history.replaceState(null, "", `/blog${query.size ? `?${query}` : ""}`);
  }
  const posts = data?.items || [];
  const tags = [...new Set(posts.flatMap((post) => post.tags || []))].sort();
  const visible = posts.filter(
    (post) =>
      (!filters.tag ||
        post.tags?.some(
          (tag) => tag.toLowerCase() === filters.tag.toLowerCase(),
        )) &&
      (!filters.author || post.author === filters.author) &&
      [post.title, post.summary, post.author, ...(post.tags || [])]
        .join(" ")
        .toLowerCase()
        .includes(filters.q.trim().toLowerCase()),
  );
  return (
    <div className="blog-shell">
      <section className="blog-hero">
        <h1>{data?.site?.title || "Engineering articles"}</h1>
        <p>
          {data?.site?.description ||
            "Technical decisions, practical guides and lessons from the engineering team."}
        </p>
      </section>
      <section className="blog-index-section">
        <label className="blog-search">
          Search articles
          <input
            type="search"
            value={filters.q}
            onChange={(e) => update({ q: e.target.value })}
            placeholder="Search topics, authors, and articles"
          />
        </label>
        {filters.author && (
          <p>
            Posts by <b>{filters.author}</b>{" "}
            <button onClick={() => update({ author: "" })}>All authors</button>
          </p>
        )}
        <div className="blog-filter" aria-label="Filter by topic">
          {["", ...tags].map((tag) => (
            <button
              type="button"
              key={tag}
              className={filters.tag === tag ? "active" : ""}
              aria-pressed={filters.tag === tag}
              onClick={() => update({ tag })}
            >
              {tag || "All topics"}
            </button>
          ))}
        </div>
        <p role="status">
          {loading
            ? "Loading articles…"
            : `${visible.length} article${visible.length === 1 ? "" : "s"}`}
        </p>
        {error ? (
          <p role="alert">
            {error} <a href="">Retry</a>
          </p>
        ) : (
          <div className="blog-index-grid">
            {visible.map((post) => (
              <PostCard key={post.id || post.slug} post={post} />
            ))}
          </div>
        )}
        {!loading && !error && !visible.length && (
          <p className="blog-empty">No articles match these filters.</p>
        )}
      </section>
    </div>
  );
}
