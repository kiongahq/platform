/* Public engineering blog: index (search, tag and author filters) and article
 * pages. Article bodies arrive as rendered_html that the server produced from
 * blocks and sanitized; every other field is escaped here. */
const escapeBlog = value => String(value == null ? "" : value).replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[character]));
const blogDate = value => new Date(value).toLocaleDateString(undefined, {year: "numeric", month: "long", day: "numeric"});
const postURL = post => `/blog.html?slug=${encodeURIComponent(post.slug)}`;
const authorURL = name => `/blogs.html?author=${encodeURIComponent(name)}`;

function coverHTML(cover, sizes, className) {
  if (!cover || !cover.url) return "";
  return `<img class="${className}" src="${escapeBlog(cover.url)}" srcset="${escapeBlog(cover.srcset)}" sizes="${sizes}" width="${Number(cover.width) || ""}" height="${Number(cover.height) || ""}" alt="${escapeBlog(cover.alt)}" loading="lazy" decoding="async">`;
}

function cardHTML(post) {
  return `<article class="blog-index-card${post.cover ? " has-cover" : ""}">${post.cover ? `<a class="blog-card-cover" href="${postURL(post)}" tabindex="-1" aria-hidden="true">${coverHTML(post.cover, "(max-width: 760px) 100vw, 540px", "")}</a>` : ""}
    <div class="blog-card-meta"><span>${escapeBlog((post.tags || [])[0] || "Engineering")}</span><time datetime="${escapeBlog(post.published_at || post.created_at)}">${blogDate(post.published_at || post.created_at)}</time></div>
    <h2><a href="${postURL(post)}">${escapeBlog(post.title)}</a></h2><p>${escapeBlog(post.summary)}</p>
    <div class="blog-card-foot"><a class="blog-author" href="${authorURL(post.author)}">${escapeBlog(post.author)}</a><span>${Number(post.reading_minutes) || 1} min read</span></div>
    <div class="blog-tags">${(post.tags || []).map(tag => `<a href="/blogs.html?tag=${encodeURIComponent(tag)}">${escapeBlog(tag)}</a>`).join("")}</div></article>`;
}

/* Pure filter shared by the index page and its tests. */
function filterPosts(posts, {query = "", tag = "", author = ""} = {}) {
  const needle = query.trim().toLowerCase();
  return posts.filter(post => (!tag || (post.tags || []).some(value => value.toLowerCase() === tag.toLowerCase())) &&
    (!author || post.author === author) &&
    (!needle || [post.title, post.summary, post.author, ...(post.tags || [])].join(" ").toLowerCase().includes(needle)));
}

async function loadBlogIndex() {
  const params = new URLSearchParams(location.search);
  const state = {query: params.get("q") || "", tag: params.get("tag") || "", author: params.get("author") || ""};
  const data = await fetch("/api/v1/blogs").then(response => response.json());
  const posts = data.items || [];
  if (data.site) {
    document.querySelector("#blog-headline").textContent = data.site.title;
    document.querySelector("#blog-description").textContent = data.site.description;
  }
  const tags = [...new Set(posts.flatMap(post => post.tags || []))].sort((a, b) => a.localeCompare(b));
  const search = document.querySelector("#blog-search");
  search.value = state.query;
  const sync = () => {
    const next = new URLSearchParams();
    for (const [key, value] of Object.entries({q: state.query, tag: state.tag, author: state.author})) if (value) next.set(key, value);
    history.replaceState(null, "", `${location.pathname}${next.toString() ? `?${next}` : ""}`);
  };
  const render = () => {
    const visible = filterPosts(posts, state);
    document.querySelector("#blog-author-banner").innerHTML = state.author ? `<p>Posts by <b>${escapeBlog(state.author)}</b> · <a href="/blogs.html" data-clear-author>All authors</a></p>` : "";
    document.querySelector("#blog-filter").innerHTML = `<button type="button" class="${state.tag ? "" : "active"}" data-blog-tag="" aria-pressed="${!state.tag}">All</button>${tags.map(tag => `<button type="button" class="${tag === state.tag ? "active" : ""}" data-blog-tag="${escapeBlog(tag)}" aria-pressed="${tag === state.tag}">${escapeBlog(tag)}</button>`).join("")}`;
    document.querySelector("#blog-count").textContent = `${visible.length} post${visible.length === 1 ? "" : "s"}`;
    document.querySelector("#blog-index").innerHTML = visible.map(cardHTML).join("") || '<p class="blog-empty">No posts match. Try another search or tag.</p>';
  };
  document.querySelector("#blog-filter").addEventListener("click", event => {
    const button = event.target.closest("[data-blog-tag]");
    if (!button) return;
    state.tag = button.dataset.blogTag; sync(); render();
  });
  document.querySelector("#blog-author-banner").addEventListener("click", event => {
    if (!event.target.closest("[data-clear-author]")) return;
    event.preventDefault(); state.author = ""; sync(); render();
  });
  let timer;
  search.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(() => { state.query = search.value; sync(); render(); }, 150); });
  render();
}

async function highlightCode(root) {
  const blocks = root.querySelectorAll("pre code");
  if (!blocks.length) return;
  try {
    const {default: hljs} = await import("/vendor/highlight/core.min.js");
    const names = ["bash", "dockerfile", "go", "ini", "javascript", "json", "plaintext", "python", "shell", "sql", "typescript", "xml", "yaml"];
    const grammars = await Promise.all(names.map(name => import(`/vendor/highlight/languages/${name}.min.js`)));
    names.forEach((name, index) => hljs.registerLanguage(name, grammars[index].default));
    for (const [alias, languageName] of [["sh", "bash"], ["yml", "yaml"], ["js", "javascript"], ["ts", "typescript"], ["py", "python"], ["text", "plaintext"], ["toml", "ini"], ["html", "xml"]]) hljs.registerAliases(alias, {languageName});
    blocks.forEach(block => {
      const language = (block.className.match(/language-([\w+#-]+)/) || [])[1];
      if (language && !hljs.getLanguage(language)) block.classList.replace(`language-${language}`, "language-plaintext");
      hljs.highlightElement(block);
    });
  } catch (_) {
    /* Highlighting is decoration; plain code stays readable. */
  }
}

function setMeta(name, content, attribute = "name") {
  let node = document.head.querySelector(`meta[${attribute}="${name}"]`);
  if (!node) { node = document.createElement("meta"); node.setAttribute(attribute, name); document.head.append(node); }
  node.setAttribute("content", content);
}

async function loadArticle() {
  const slug = new URLSearchParams(location.search).get("slug") || "";
  const response = await fetch(`/api/v1/blogs/${encodeURIComponent(slug)}`);
  if (!response.ok) { document.querySelector("#article-header").innerHTML = '<h1>Article not found.</h1><p>It may have been unpublished. <a href="/blogs.html">Browse all posts</a>.</p>'; return; }
  const post = await response.json();
  const title = (post.seo && post.seo.title) || post.title;
  const description = (post.seo && post.seo.description) || post.summary;
  document.title = `${title} — Kionga Engineering`;
  setMeta("description", description);
  setMeta("og:title", title, "property");
  setMeta("og:description", description, "property");
  if (post.cover) setMeta("og:image", new URL(post.cover.url, location.origin).href, "property");
  document.querySelector("#article-header").innerHTML = `<div class="blog-tags">${(post.tags || []).map(tag => `<a href="/blogs.html?tag=${encodeURIComponent(tag)}">${escapeBlog(tag)}</a>`).join("")}</div><h1>${escapeBlog(post.title)}</h1><p>${escapeBlog(post.summary)}</p><div class="article-byline"><a href="${authorURL(post.author)}">${escapeBlog(post.author)}</a><time datetime="${escapeBlog(post.published_at || post.created_at)}">${blogDate(post.published_at || post.created_at)}</time><span>${Number(post.reading_minutes) || 1} min read</span></div>`;
  document.querySelector("#article-cover").innerHTML = coverHTML(post.cover, "(max-width: 860px) 100vw, 860px", "article-cover");
  const content = document.querySelector("#article-content");
  content.innerHTML = post.rendered_html || ""; // produced by the server renderer and sanitizer
  const related = post.related || [];
  document.querySelector("#article-related").innerHTML = related.length ? `<h2>Related posts</h2><div class="blog-index-grid">${related.map(cardHTML).join("")}</div>` : "";
  highlightCode(content);
}

if (typeof module !== "undefined") module.exports = {filterPosts, escapeBlog};
if (typeof document !== "undefined" && document.body) {
  if (document.body.dataset.blogPage === "index") loadBlogIndex().catch(() => { document.querySelector("#blog-index").innerHTML = "<p>Posts are temporarily unavailable.</p>"; });
  if (document.body.dataset.blogPage === "article") loadArticle().catch(() => { document.querySelector("#article-header").innerHTML = "<h1>Article unavailable.</h1>"; });
}
