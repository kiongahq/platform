/* Editorial workspace list views: Posts, Drafts, Reviews, Media, Authors,
 * Settings. Every server value is escaped with esc() before it reaches
 * innerHTML. */
(function (global) {
  "use strict";
  const {request, upload, esc, timeAgo, toast, statusBadge, ROLE_LABELS} = EditorialAPI;

  const isEditor = member => member.role === "editor" || member.role === "editorial_admin";
  const isAdmin = member => member.role === "editorial_admin";

  function heading(title, description, actions = "") {
    return `<header class="section-heading"><div><p class="eyebrow">EDITORIAL</p><h1>${esc(title)}</h1><p>${esc(description)}</p></div><div class="heading-actions">${actions}</div></header>`;
  }

  function postRows(items, empty) {
    if (!items.length) return `<div class="empty-state panel"><b>${esc(empty)}</b><p>Start a new post to begin writing.</p><div class="button-row"><a class="button-link primary" href="#/new">New post</a></div></div>`;
    return `<ul class="ed-post-list" role="list">${items.map(post => `<li class="ed-post-row panel">
      <div class="ed-post-main"><a class="ed-post-title" href="#/edit/${encodeURIComponent(post.id)}">${esc(post.title || "Untitled draft")}</a>
        <p class="ed-post-summary">${esc(post.summary || "No summary yet.")}</p>
        <p class="ed-post-meta"><span>${esc(post.author)}</span><span>/${esc(post.slug)}</span><span>Updated ${esc(timeAgo(post.updated_at))}</span>${post.status === "scheduled" && post.publish_at ? `<span>Publishes ${esc(new Date(post.publish_at).toLocaleString())}</span>` : ""}</p></div>
      <div class="ed-post-side">${statusBadge(post.status)}${post.status === "published" ? `<a class="button-link quiet btn-sm" href="/blog.html?slug=${encodeURIComponent(post.slug)}" target="_blank" rel="noopener">View ↗</a>` : ""}<a class="button-link btn-sm" href="#/edit/${encodeURIComponent(post.id)}">${post.can_edit ? "Edit" : "Open"}</a></div></li>`).join("")}</ul>`;
  }

  async function postsView(root, member, {route}) {
    const filters = {posts: "", drafts: "draft", reviews: "in_review"};
    const titles = {
      posts: ["Posts", "Every post you can see, newest activity first."],
      drafts: ["Drafts", member.role === "author" ? "Your drafts. Submit one for review when it is ready." : "Drafts across the team."],
      reviews: ["Reviews", isEditor(member) ? "Posts waiting for an editor to approve, schedule or send back." : "Your posts waiting for an editor."],
    };
    const status = filters[route];
    const params = new URLSearchParams();
    if (status) params.set("status", status);
    if (route === "drafts" && !isEditor(member)) params.set("mine", "1");
    const data = await request(`/api/v1/editorial/posts?${params}`);
    let items = data.items;
    const chips = route === "posts" ? `<div class="ed-chips" role="group" aria-label="Filter by status">${["all", "draft", "in_review", "scheduled", "published", "archived"].map(value => `<button type="button" class="${value === "all" ? "active" : ""}" data-chip="${value}">${esc(value === "all" ? "All" : EditorialAPI.STATUS_LABELS[value])}</button>`).join("")}</div>` : "";
    root.innerHTML = heading(titles[route][0], titles[route][1], `<a class="button-link primary" href="#/new" id="ed-new-post">＋ New post</a>`) + chips + `<div id="ed-post-rows">${postRows(items, route === "reviews" ? "Nothing is waiting for review." : "No posts yet.")}</div>`;
    root.querySelectorAll("[data-chip]").forEach(chip => chip.addEventListener("click", () => {
      root.querySelectorAll("[data-chip]").forEach(other => other.classList.toggle("active", other === chip));
      const value = chip.dataset.chip;
      root.querySelector("#ed-post-rows").innerHTML = postRows(value === "all" ? items : items.filter(post => post.status === value), "No posts with this status.");
    }));
  }

  function mediaCard(media) {
    const thumb = `/media/blog/${encodeURIComponent(media.id)}/480`;
    return `<li class="ed-media-card panel" data-media="${esc(media.id)}">
      <img src="${esc(thumb)}" alt="${esc(media.alt)}" loading="lazy" width="${media.width}" height="${media.height}">
      <div class="ed-media-body"><p class="ed-post-meta"><span class="status ${media.status === "attached" ? "published" : "pending"}">${media.status === "attached" ? "Attached" : "Pending"}</span><span>${media.width}×${media.height}</span><span>${esc(timeAgo(media.created_at))}</span></p>
        <label>Alt text<input data-media-alt value="${esc(media.alt)}" maxlength="300" aria-invalid="${media.alt ? "false" : "true"}"></label>
        <label>Caption<input data-media-caption value="${esc(media.caption ? media.caption.replace(/<[^>]*>/g, "") : "")}" maxlength="300"></label>
        <label>Attribution<input data-media-attribution value="${esc(media.attribution)}" maxlength="200" placeholder="Photo: name / license"></label>
        <div class="button-row"><button type="button" class="btn-sm" data-media-save>Save details</button><button type="button" class="quiet btn-sm" data-media-copy>Copy URL</button></div></div></li>`;
  }

  async function mediaView(root, member) {
    const data = await request("/api/v1/editorial/media");
    root.innerHTML = heading("Media", "Images uploaded for posts. Unused uploads are removed after 24 hours.",
      `<label class="button-link primary" for="ed-media-upload">Upload image</label><input class="sr-only" id="ed-media-upload" type="file" accept="image/jpeg,image/png,image/webp,image/gif" multiple>`) +
      `<div class="ed-media-drop panel" id="ed-media-drop"><p>Drop images here to upload (JPEG, PNG, WebP, GIF, 10 MB max). EXIF data is removed and 480/960/1600 px sizes are generated.</p><div id="ed-media-progress"></div></div>` +
      (data.items.length ? `<ul class="ed-media-grid" role="list">${data.items.map(item => mediaCard(item)).join("")}</ul>` : `<div class="empty-state panel"><b>No images yet.</b><p>Upload an image or drop one into a post.</p></div>`);
    const progress = root.querySelector("#ed-media-progress");
    const send = async files => {
      for (const file of files) {
        const row = document.createElement("p");
        row.innerHTML = `<span>${esc(file.name)}</span> <progress max="100" value="0"></progress>`;
        progress.append(row);
        try {
          await upload(file, {onProgress: value => { row.querySelector("progress").value = value; }});
          row.append(" uploaded");
        } catch (error) { row.append(` — ${error.message}`); }
      }
      await mediaView(root, member);
      toast("Upload finished.");
    };
    root.querySelector("#ed-media-upload").addEventListener("change", event => send([...event.target.files]));
    const drop = root.querySelector("#ed-media-drop");
    drop.addEventListener("dragover", event => { event.preventDefault(); drop.classList.add("over"); });
    drop.addEventListener("dragleave", () => drop.classList.remove("over"));
    drop.addEventListener("drop", event => { event.preventDefault(); drop.classList.remove("over"); send([...event.dataTransfer.files]); });
    root.querySelectorAll("[data-media]").forEach(card => {
      card.querySelector("[data-media-save]").addEventListener("click", async () => {
        try {
          await request(`/api/v1/editorial/media/${encodeURIComponent(card.dataset.media)}`, {method: "PATCH", body: {alt: card.querySelector("[data-media-alt]").value, caption: esc(card.querySelector("[data-media-caption]").value), attribution: card.querySelector("[data-media-attribution]").value}});
          toast("Image details saved.");
        } catch (error) { toast(error.message, "error"); }
      });
      card.querySelector("[data-media-copy]").addEventListener("click", async () => {
        const url = `${location.origin}/media/blog/${card.dataset.media}/960`;
        try { await navigator.clipboard.writeText(url); toast("Image URL copied."); } catch (_) { toast(url); }
      });
    });
  }

  async function authorsView(root, member) {
    const data = await request("/api/v1/editorial/members");
    const manage = data.can_manage;
    const rows = data.items.map(item => `<tr data-member="${esc(item.subject)}">
      <td><div class="cell-primary"><b>${esc(item.display_name)}</b><small>${esc(item.subject)}${item.email ? ` · ${esc(item.email)}` : ""}</small></div></td>
      <td>${manage ? `<select data-member-role aria-label="Role for ${esc(item.display_name)}">${Object.entries(ROLE_LABELS).map(([value, label]) => `<option value="${value}" ${value === item.role ? "selected" : ""}>${label}</option>`).join("")}</select>` : esc(ROLE_LABELS[item.role] || item.role)}</td>
      <td>${item.posts}</td>
      <td>${item.disabled ? '<span class="status suspended">Suspended</span>' : '<span class="status active">Active</span>'}</td>
      <td class="actions">${manage ? `<button type="button" class="btn-sm" data-member-toggle>${item.disabled ? "Reactivate" : "Suspend"}</button><button type="button" class="btn-sm danger" data-member-remove>Remove</button>` : `<a class="button-link quiet btn-sm" href="/blogs.html?author=${encodeURIComponent(item.display_name)}" target="_blank" rel="noopener">Posts ↗</a>`}</td></tr>`).join("");
    root.innerHTML = heading("Authors", manage ? "Editorial membership is separate from ML platform roles: only people listed here can write or publish." : "People who write and edit the engineering blog.") +
      (manage ? `<form class="panel ed-member-form" id="ed-member-form"><h2 class="ed-h2">Add a member</h2><div class="form-grid">
        <label>Platform identity<input name="subject" required placeholder="username or OIDC subject" autocomplete="off"></label>
        <label>Display name (byline)<input name="display_name" required placeholder="Ada Lovelace"></label>
        <label>Role<select name="role">${Object.entries(ROLE_LABELS).map(([value, label]) => `<option value="${value}">${label}</option>`).join("")}</select></label></div>
        <p class="field-help">Authors write and submit their own drafts. Editors review, publish, schedule and unpublish. Editorial admins also manage members and settings.</p>
        <div class="form-actions"><p class="form-error" role="alert" id="ed-member-error"></p><button class="primary" type="submit">Add member</button></div></form>` : "") +
      `<div class="panel table-wrap"><table class="data-table ed-table"><thead><tr><th scope="col">Member</th><th scope="col">Role</th><th scope="col">Posts</th><th scope="col">Status</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead><tbody>${rows}</tbody></table></div>`;
    if (!manage) return;
    const lookup = subject => data.items.find(item => item.subject === subject);
    const put = async (subject, changes) => {
      const current = lookup(subject);
      try {
        await request(`/api/v1/editorial/members/${encodeURIComponent(subject)}`, {method: "PUT", body: {display_name: current.display_name, email: current.email || "", role: current.role, disabled: current.disabled, ...changes}});
        toast("Membership updated.");
      } catch (error) { toast(error.message, "error"); }
      await authorsView(root, member);
    };
    root.querySelector("#ed-member-form").addEventListener("submit", async event => {
      event.preventDefault();
      const form = event.target;
      try {
        await request(`/api/v1/editorial/members/${encodeURIComponent(form.elements.subject.value.trim())}`, {method: "PUT", body: {display_name: form.elements.display_name.value, role: form.elements.role.value}});
        toast("Member added.");
        await authorsView(root, member);
      } catch (error) { root.querySelector("#ed-member-error").textContent = error.message; }
    });
    root.querySelectorAll("[data-member]").forEach(row => {
      const subject = row.dataset.member;
      row.querySelector("[data-member-role]").addEventListener("change", event => put(subject, {role: event.target.value}));
      row.querySelector("[data-member-toggle]").addEventListener("click", () => put(subject, {disabled: !lookup(subject).disabled}));
      row.querySelector("[data-member-remove]").addEventListener("click", async () => {
        if (!confirm(`Remove ${subject} from the editorial workspace?`)) return;
        try { await request(`/api/v1/editorial/members/${encodeURIComponent(subject)}`, {method: "DELETE"}); toast("Member removed."); } catch (error) { toast(error.message, "error"); }
        await authorsView(root, member);
      });
    });
  }

  async function settingsView(root, member, {session}) {
    const settings = await request("/api/v1/editorial/settings");
    const admin = isAdmin(member);
    const limits = session.limits || {};
    root.innerHTML = heading("Settings", "Blog identity and workspace facts.") +
      `<form class="panel" id="ed-settings-form"><h2 class="ed-h2">Blog</h2>
        <label>Blog headline<input name="title" value="${esc(settings.title)}" maxlength="120" ${admin ? "" : "disabled"}></label>
        <label>Description<textarea name="description" rows="3" maxlength="400" ${admin ? "" : "disabled"}>${esc(settings.description)}</textarea></label>
        ${admin ? '<div class="form-actions"><p class="form-error" role="alert" id="ed-settings-error"></p><button class="primary" type="submit">Save settings</button></div>' : '<p class="field-help">Only editorial admins can change these.</p>'}</form>
      <section class="panel"><h2 class="ed-h2">Workspace</h2><dl class="ed-facts">
        <div><dt>Your role</dt><dd>${esc(ROLE_LABELS[member.role] || member.role)}</dd></div>
        <div><dt>Session</dt><dd>${esc(limits.session_hours || 4)} hours, re-checked on every request</dd></div>
        <div><dt>Media storage</dt><dd>${esc(session.media_backend === "s3" ? "Object storage (S3/RustFS)" : "Local filesystem")}</dd></div>
        <div><dt>Upload limit</dt><dd>${Math.round((limits.max_upload_bytes || 10485760) / 1048576)} MB; JPEG, PNG, WebP, GIF</dd></div>
        <div><dt>Image sizes</dt><dd>${esc((limits.variant_widths || [480, 960, 1600]).join(", "))} px wide</dd></div></dl></section>`;
    if (!admin) return;
    root.querySelector("#ed-settings-form").addEventListener("submit", async event => {
      event.preventDefault();
      const form = event.target;
      try { await request("/api/v1/editorial/settings", {method: "PUT", body: {title: form.elements.title.value, description: form.elements.description.value}}); toast("Settings saved."); } catch (error) { root.querySelector("#ed-settings-error").textContent = error.message; }
    });
  }

  global.EditorialViews = {postsView, mediaView, authorsView, settingsView, isEditor, isAdmin};
})(window);
