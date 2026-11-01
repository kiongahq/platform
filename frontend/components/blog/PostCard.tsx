import type { Post } from "@/lib/api";
export const postURL = (post: Post) =>
  `/blog/read?slug=${encodeURIComponent(post.slug)}`;
export const blogDate = (date: string) =>
  new Date(date).toLocaleDateString("en", {
    year: "numeric",
    month: "long",
    day: "numeric",
    timeZone: "UTC",
  });
export function PostCard({ post }: { post: Post }) {
  return (
    <article className={`blog-index-card${post.cover ? " has-cover" : ""}`}>
      {post.cover && (
        <a
          className="blog-card-cover"
          href={postURL(post)}
          tabIndex={-1}
          aria-hidden="true"
        >
          <img
            src={post.cover.url}
            alt={post.cover.alt}
            srcSet={post.cover.srcset}
            loading="lazy"
          />
        </a>
      )}
      <div className="blog-card-meta">
        <span>{post.tags?.[0] || "Engineering"}</span>
        <time dateTime={post.published_at || post.created_at}>
          {blogDate(post.published_at || post.created_at)}
        </time>
      </div>
      <h2>
        <a href={postURL(post)}>{post.title}</a>
      </h2>
      <p>{post.summary}</p>
      <div className="blog-card-foot">
        <a
          className="blog-author"
          href={`/blog?author=${encodeURIComponent(post.author)}`}
        >
          {post.author}
        </a>
        <span>{post.reading_minutes || 1} min read</span>
      </div>
      <div className="blog-tags">
        {post.tags?.map((tag) => (
          <a key={tag} href={`/blog?tag=${encodeURIComponent(tag)}`}>
            {tag}
          </a>
        ))}
      </div>
    </article>
  );
}
