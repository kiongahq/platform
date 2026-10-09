/* Engineering blog entry point inside the ML console. Writing happens in the
 * separate editorial workspace; this view only links there and lists posts. */
let blogAdminCache = [];
async function loadAdminBlogs() {
  const data = await api("/api/v1/admin/blogs");
  blogAdminCache = data.items || [];
  document.querySelector("#blog-admin-table").innerHTML = blogAdminCache.length ? blogAdminCache.map(post => `<tr><td><div class="cell-primary"><b>${escapeHTML(post.title)}</b><small class="truncate">/${escapeHTML(post.slug)}</small></div></td><td>${escapeHTML(post.author)}</td><td><div class="tags">${post.tags.map(tag => `<span class="tag">${escapeHTML(tag)}</span>`).join("")}</div></td><td>${status(post.status)}</td><td>${timeTag(post.updated_at)}</td><td class="actions"><a class="button-link btn-sm" href="/blog.html?slug=${encodeURIComponent(post.slug)}" target="_blank" rel="noopener">View ↗</a><button type="button" data-blog-edit="${escapeHTML(post.id)}">Edit</button><button type="button" class="danger" data-blog-delete="${escapeHTML(post.id)}">Delete</button></td></tr>`).join("") : tableEmpty(6, "No blog posts yet");
}
document.querySelector("#new-blog-post").addEventListener("click", () => {
  const form = document.querySelector("#blog-editor-form"); form.reset(); form.elements.id.value = "";
  form.elements.author.value = "Kionga Engineering"; document.querySelector("#blog-editor-title").textContent = "New post";
  document.querySelector("#blog-editor-error").textContent = ""; document.querySelector("#blog-editor-dialog").showModal();
});
document.querySelector("#blog-editor-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.target, error = document.querySelector("#blog-editor-error");
  const payload = {title: form.elements.title.value, slug: form.elements.slug.value, status: form.elements.status.value, author: form.elements.author.value, tags: csv(form.elements.tags.value), summary: form.elements.summary.value, content: form.elements.content.value};
  const id = form.elements.id.value; error.textContent = "";
  try {
    await withBusy(event.submitter, () => api(id ? `/api/v1/admin/blogs/${encodeURIComponent(id)}` : "/api/v1/admin/blogs", {method: id ? "PUT" : "POST", body: JSON.stringify(payload)}), {failure: false});
    document.querySelector("#blog-editor-dialog").close(); toast(id ? "Blog post updated." : "Blog post created."); await loadAdminBlogs();
  } catch (failure) { error.textContent = failure.message; }
});
onClick("[data-blog-edit]", node => {
  const post = blogAdminCache.find(item => item.id === node.dataset.blogEdit); if (!post) return;
  const form = document.querySelector("#blog-editor-form");
  form.elements.id.value = post.id; form.elements.title.value = post.title; form.elements.slug.value = post.slug; form.elements.status.value = post.status === "published" ? "published" : "draft"; form.elements.author.value = post.author; form.elements.tags.value = post.tags.join(", "); form.elements.summary.value = post.summary; form.elements.content.value = post.content;
  document.querySelector("#blog-editor-title").textContent = "Edit post"; document.querySelector("#blog-editor-error").textContent = ""; document.querySelector("#blog-editor-dialog").showModal();
});
onClick("[data-blog-delete]", async node => {
  if (!confirm("Delete this blog post permanently?")) return;
  await withBusy(node, () => api(`/api/v1/admin/blogs/${encodeURIComponent(node.dataset.blogDelete)}`, {method: "DELETE"}), {success: "Blog post deleted."});
  await loadAdminBlogs();
});

registerView("blogs", loadAdminBlogs, {eyebrow: "PUBLICATION", title: "Engineering blog"});
