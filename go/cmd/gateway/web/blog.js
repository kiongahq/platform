const escapeBlog = value => String(value || "").replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[character]));
const blogDate = value => new Date(value).toLocaleDateString(undefined,{year:"numeric",month:"long",day:"numeric"});

function inlineMarkdown(value) {
  return escapeBlog(value)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
}

function renderMarkdown(source) {
  const lines = String(source || "").split("\n");
  let html = "", paragraph = [], list = "", code = false, codeLines = [];
  const flushParagraph = () => { if (paragraph.length) { html += `<p>${paragraph.map(inlineMarkdown).join(" ")}</p>`; paragraph = []; } };
  const closeList = () => { if (list) { html += `</${list}>`; list = ""; } };
  for (const line of lines) {
    if (line.startsWith("~~~")) {
      flushParagraph(); closeList();
      if (code) { html += `<pre><code>${escapeBlog(codeLines.join("\n"))}</code></pre>`; codeLines = []; }
      code = !code; continue;
    }
    if (code) { codeLines.push(line); continue; }
    const heading = line.match(/^(#{1,3})\s+(.+)$/);
    if (heading) { flushParagraph(); closeList(); const level = heading[1].length + 1; html += `<h${level}>${inlineMarkdown(heading[2])}</h${level}>`; continue; }
    const unordered = line.match(/^-\s+(.+)$/), ordered = line.match(/^\d+\.\s+(.+)$/);
    if (unordered || ordered) { flushParagraph(); const wanted = unordered ? "ul" : "ol"; if (list !== wanted) { closeList(); html += `<${wanted}>`; list = wanted; } html += `<li>${inlineMarkdown((unordered || ordered)[1])}</li>`; continue; }
    if (line.startsWith("> ")) { flushParagraph(); closeList(); html += `<blockquote>${inlineMarkdown(line.slice(2))}</blockquote>`; continue; }
    if (!line.trim()) { flushParagraph(); closeList(); continue; }
    paragraph.push(line.trim());
  }
  flushParagraph(); closeList();
  if (codeLines.length) html += `<pre><code>${escapeBlog(codeLines.join("\n"))}</code></pre>`;
  return html;
}

async function loadBlogIndex() {
  const data = await fetch("/api/v1/blogs").then(response => response.json());
  const posts = data.items || [];
  const tags = [...new Set(posts.flatMap(post => post.tags || []))];
  const render = selected => {
    const visible = selected ? posts.filter(post => (post.tags || []).includes(selected)) : posts;
    document.querySelector("#blog-index").innerHTML = visible.map(post => `<article class="blog-index-card"><div class="blog-card-meta"><span>${escapeBlog((post.tags || [])[0] || "Engineering")}</span><time>${blogDate(post.published_at || post.created_at)}</time></div><h2><a href="/blog.html?slug=${encodeURIComponent(post.slug)}">${escapeBlog(post.title)}</a></h2><p>${escapeBlog(post.summary)}</p><div class="blog-tags">${(post.tags || []).map(tag => `<span>${escapeBlog(tag)}</span>`).join("")}</div><a class="read-link" href="/blog.html?slug=${encodeURIComponent(post.slug)}">Read article →</a></article>`).join("") || "<p>No posts match this filter.</p>";
  };
  document.querySelector("#blog-filter").innerHTML = `<button class="active" data-blog-tag="">All</button>${tags.map(tag => `<button data-blog-tag="${escapeBlog(tag)}">${escapeBlog(tag)}</button>`).join("")}`;
  document.querySelector("#blog-filter").addEventListener("click", event => { const button = event.target.closest("[data-blog-tag]"); if (!button) return; document.querySelectorAll("[data-blog-tag]").forEach(item => item.classList.toggle("active", item === button)); render(button.dataset.blogTag); });
  render("");
}

async function loadArticle() {
  const slug = new URLSearchParams(location.search).get("slug") || "";
  const response = await fetch(`/api/v1/blogs/${encodeURIComponent(slug)}`);
  if (!response.ok) { document.querySelector("#article-header").innerHTML = "<h1>Article not found.</h1>"; return; }
  const post = await response.json();
  document.title = `${post.title} — Kionga Engineering`;
  document.querySelector("#article-header").innerHTML = `<div class="blog-tags">${post.tags.map(tag => `<span>${escapeBlog(tag)}</span>`).join("")}</div><h1>${escapeBlog(post.title)}</h1><p>${escapeBlog(post.summary)}</p><div class="article-byline"><span>${escapeBlog(post.author)}</span><time>${blogDate(post.published_at || post.created_at)}</time><span>${Math.max(1,Math.ceil(post.content.split(/\s+/).length/220))} min read</span></div>`;
  document.querySelector("#article-content").innerHTML = renderMarkdown(post.content);
}

if (document.body.dataset.blogPage === "index") loadBlogIndex().catch(() => { document.querySelector("#blog-index").innerHTML = "<p>Posts are temporarily unavailable.</p>"; });
if (document.body.dataset.blogPage === "article") loadArticle().catch(() => { document.querySelector("#article-header").innerHTML = "<h1>Article unavailable.</h1>"; });
