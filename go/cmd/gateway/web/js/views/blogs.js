/* Engineering blog entry point inside the ML console. Writing happens in the
 * separate editorial workspace (/editorial.html); this view links there and
 * lists published posts read-only from the public API. */
async function loadPublishedBlogs() {
  const response = await fetch("/api/v1/blogs", {credentials: "same-origin"});
  if (!response.ok) throw new Error("The blog is unavailable right now.");
  const posts = (await response.json()).items || [];
  document.querySelector("#blog-published-table").innerHTML = posts.length ? posts.map(post => `<tr><td><div class="cell-primary"><b>${escapeHTML(post.title)}</b><small class="truncate">/${escapeHTML(post.slug)}</small></div></td><td>${escapeHTML(post.author)}</td><td><div class="tags">${(post.tags || []).map(tag => `<span class="tag">${escapeHTML(tag)}</span>`).join("")}</div></td><td>${timeTag(post.published_at || post.created_at)}</td><td class="actions"><a class="button-link btn-sm" href="/blog.html?slug=${encodeURIComponent(post.slug)}" target="_blank" rel="noopener">View ↗</a></td></tr>`).join("") : tableEmpty(5, "No published posts yet");
}

registerView("blogs", loadPublishedBlogs, {eyebrow: "PUBLICATION", title: "Engineering blog"});
