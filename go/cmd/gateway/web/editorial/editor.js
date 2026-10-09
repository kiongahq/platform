/* The post editor: Editor.js canvas plus title, summary, URL, tags, cover,
 * SEO and the role-appropriate workflow actions. Saves go through
 * EditorialState.createSaveController (autosave with base_revision, 409
 * conflict handling and a local recovery copy). */
(function (global) {
  "use strict";
  const {request, upload, esc, timeAgo, toast, statusBadge, ACTION_LABELS, STATUS_LABELS} = EditorialAPI;
  const S = EditorialState;

  let active = null; // the open editor, so the router can tear it down

  function tools(readOnly) {
    return {
      header: {class: global.Header, inlineToolbar: true, config: {levels: [2, 3, 4], defaultLevel: 2, placeholder: "Heading"}},
      list: {class: global.EditorjsList, inlineToolbar: true, config: {defaultStyle: "unordered"}},
      quote: {class: global.Quote, inlineToolbar: true, config: {quotePlaceholder: "Quote", captionPlaceholder: "Who said it"}},
      code: {class: global.CodeTool, config: {placeholder: "Paste or write code"}},
      delimiter: global.Delimiter,
      image: {class: global.KiongaImage, config: {uploader: (file, onProgress) => upload(file, {onProgress}).then(result => ({media_id: result.file.media_id, url: result.file.url, alt: result.media.alt}))}},
      embed: {class: global.Embed, config: {services: {youtube: true, vimeo: true, github: true}}},
      table: {class: global.Table, inlineToolbar: true, config: {rows: 2, cols: 3, withHeadings: true}},
      warning: {class: global.Warning, inlineToolbar: true, config: {titlePlaceholder: "Callout title", messagePlaceholder: "Callout text"}},
      inlineCode: {class: global.InlineCode, shortcut: "CMD+SHIFT+C"},
      marker: {class: global.Marker, shortcut: "CMD+SHIFT+M"},
    };
  }

  function template(post, member) {
    return `<div class="ed-editor" id="ed-editor">
      <div class="ed-editor-bar" role="toolbar" aria-label="Post tools">
        <a class="button-link quiet btn-sm ed-back" href="#/posts">← Posts</a>
        <span id="ed-post-status">${statusBadge(post.status)}</span>
        <span class="ed-save-status" id="ed-save-status" role="status" aria-live="polite" data-state="saved">${post.id ? `Saved ${esc(new Date(post.updated_at).toLocaleTimeString([], {hour: "2-digit", minute: "2-digit"}))}` : "New post"}</span>
        <span class="app-spacer"></span>
        <button type="button" class="icon-button quiet" id="ed-undo" title="Undo block change" aria-label="Undo" disabled>↶</button>
        <button type="button" class="icon-button quiet" id="ed-redo" title="Redo block change" aria-label="Redo" disabled>↷</button>
        <button type="button" class="btn-sm" id="ed-preview">Preview</button>
        <button type="button" class="btn-sm" id="ed-history" ${post.id ? "" : "disabled"}>History</button>
        <button type="button" class="btn-sm quiet" id="ed-focus" aria-pressed="false" title="Distraction-free writing (Esc to exit)">Focus</button>
        <button type="button" class="btn-sm" id="ed-save" title="Save draft (Ctrl/⌘ S)">Save draft</button>
      </div>
      <div id="ed-recovery" class="view-feedback" hidden></div>
      <div id="ed-conflict" class="view-feedback error" hidden></div>
      <div class="ed-editor-grid">
        <article class="ed-canvas panel">
          <label class="sr-only" for="ed-title">Title</label>
          <textarea id="ed-title" class="ed-title" rows="1" maxlength="200" placeholder="Post title"></textarea>
          <label class="sr-only" for="ed-summary">Summary</label>
          <textarea id="ed-summary" class="ed-summary" rows="2" maxlength="600" placeholder="One or two sentences: what will readers learn?"></textarea>
          <div id="ed-holder" class="ed-holder" aria-label="Article body"></div>
        </article>
        <div class="ed-details" role="complementary" aria-label="Post details">
          <section class="panel"><h2 class="ed-h2">Publishing</h2>
            <p class="ed-post-meta"><span>By ${esc(post.author || member.display_name)}</span><span id="ed-revision">${post.revision ? `Revision ${post.revision}` : "Not saved yet"}</span></p>
            <label>Publish date and time<input type="datetime-local" id="ed-publish-at"><small>Used by “Schedule”. Times are in your local timezone.</small></label>
            <div class="ed-actions" id="ed-actions"></div>
            <p class="form-error" role="alert" id="ed-action-error"></p>
          </section>
          <section class="panel"><h2 class="ed-h2">URL and tags</h2>
            <label>Slug<input id="ed-slug" maxlength="80" autocomplete="off" spellcheck="false"><small id="ed-slug-hint">Follows the title until you edit it.</small></label>
            <label>Tags<input id="ed-tags" placeholder="Go, Kubernetes, Storage" autocomplete="off"><small>Comma separated, up to 12.</small></label>
          </section>
          <section class="panel"><h2 class="ed-h2">Cover image</h2>
            <div class="ed-cover" id="ed-cover"></div>
            <div class="button-row"><label class="button-link btn-sm" for="ed-cover-file">Upload cover</label><input class="sr-only" id="ed-cover-file" type="file" accept="image/jpeg,image/png,image/webp,image/gif"><button type="button" class="btn-sm" id="ed-cover-pick">Choose from library</button><button type="button" class="btn-sm quiet" id="ed-cover-remove">Remove</button></div>
            <label>Cover alt text<input id="ed-cover-alt" maxlength="300" placeholder="Describe the cover image"></label>
            <div class="ed-picker" id="ed-cover-picker" hidden></div>
          </section>
          <section class="panel"><h2 class="ed-h2">Search and social</h2>
            <label>SEO title<input id="ed-seo-title" maxlength="120" placeholder="Defaults to the post title"><small><span id="ed-seo-title-count">0</span>/60 recommended</small></label>
            <label>SEO description<textarea id="ed-seo-description" rows="3" maxlength="320" placeholder="Defaults to the summary"></textarea><small><span id="ed-seo-description-count">0</span>/160 recommended</small></label>
            <div class="ed-social" id="ed-social" aria-label="Social card preview"></div>
          </section>
          <section class="panel"><h2 class="ed-h2">Content</h2>
            <p class="ed-post-meta"><span id="ed-words">0 words</span><span id="ed-reading">1 min read</span></p>
            <div class="button-row"><button type="button" class="btn-sm" id="ed-import">Import Markdown</button>${post.id ? `<a class="button-link btn-sm quiet" href="/api/v1/editorial/posts/${encodeURIComponent(post.id)}/markdown">Export Markdown</a>` : ""}</div>
          </section>
        </div>
      </div>
      <dialog id="ed-preview-dialog" class="ed-dialog ed-preview-dialog" aria-labelledby="ed-preview-title">
        <header class="modal-header ed-dialog-head"><h2 id="ed-preview-title">Preview</h2><div class="ed-seg" role="group" aria-label="Preview width"><button type="button" class="active" data-preview-mode="desktop" aria-pressed="true">Desktop</button><button type="button" data-preview-mode="mobile" aria-pressed="false">Mobile 390px</button></div><button type="button" class="quiet" data-close>Close</button></header>
        <div class="ed-preview-stage"><iframe id="ed-preview-frame" title="Rendered post preview" sandbox="allow-same-origin"></iframe></div>
      </dialog>
      <dialog id="ed-history-dialog" class="ed-dialog" aria-labelledby="ed-history-title">
        <header class="modal-header ed-dialog-head"><h2 id="ed-history-title">Revision history</h2><button type="button" class="quiet" data-close>Close</button></header>
        <div class="ed-dialog-body"><p class="field-help">Every save is an immutable revision. Select two to compare; restoring creates a new revision.</p><div id="ed-history-list"></div><div id="ed-compare"></div></div>
      </dialog>
      <dialog id="ed-markdown-dialog" class="ed-dialog" aria-labelledby="ed-markdown-title">
        <form method="dialog" id="ed-markdown-form"><header class="modal-header ed-dialog-head"><h2 id="ed-markdown-title">Import Markdown</h2><button type="button" class="quiet" data-close>Close</button></header>
        <div class="ed-dialog-body"><label>Markdown<textarea name="markdown" rows="14" placeholder="# Heading&#10;&#10;Paragraph with **bold** and \`code\`."></textarea></label><p class="field-help">Converted to blocks and appended to the post. Markdown is never required.</p></div>
        <footer class="modal-footer"><button class="primary" type="submit" value="import">Append blocks</button></footer></form>
      </dialog>
    </div>`;
  }

  function toLocalInput(value) {
    if (!value) return "";
    const date = new Date(value);
    const pad = number => String(number).padStart(2, "0");
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
  }

  async function editorView(root, member, {postId}) {
    let post = postId === "new" ? {id: "", title: "", summary: "", slug: "", tags: [], blocks: [], cover: null, seo: {}, status: "draft", revision: 0, actions: [], can_edit: true, author: member.display_name} : await request(`/api/v1/editorial/posts/${encodeURIComponent(postId)}`);
    root.innerHTML = template(post, member);
    const $ = selector => root.querySelector(selector);
    const fields = {title: $("#ed-title"), summary: $("#ed-summary"), slug: $("#ed-slug"), tags: $("#ed-tags"), coverAlt: $("#ed-cover-alt"), seoTitle: $("#ed-seo-title"), seoDescription: $("#ed-seo-description"), publishAt: $("#ed-publish-at")};
    let cover = post.cover ? {...post.cover} : null;
    let slugManual = Boolean(post.slug) && post.slug !== S.slugify(post.title);
    const history = S.createHistory(100);

    function fill(source) {
      fields.title.value = source.title || "";
      fields.summary.value = source.summary || "";
      fields.slug.value = source.slug || "";
      fields.tags.value = (source.tags || []).join(", ");
      fields.seoTitle.value = (source.seo || {}).title || "";
      fields.seoDescription.value = (source.seo || {}).description || "";
      cover = source.cover ? {...source.cover} : null;
      fields.coverAlt.value = cover ? cover.alt || "" : "";
      fields.publishAt.value = toLocalInput(post.publish_at);
      renderCover(); renderSocial(); grow(fields.title); grow(fields.summary);
    }
    function grow(node) { node.style.height = "auto"; node.style.height = `${node.scrollHeight}px`; }

    const readOnly = !post.can_edit;
    fill(post);
    for (const field of Object.values(fields)) field.disabled = readOnly && field !== fields.publishAt;

    const editor = new global.EditorJS({
      holder: $("#ed-holder"), data: {blocks: post.blocks || []}, readOnly, tools: tools(readOnly),
      placeholder: "Start writing. Press Tab or “+” for headings, lists, code, tables, callouts and images.",
      onChange: () => scheduleSnapshot(),
    });
    await editor.isReady;

    async function snapshot() {
      const output = await editor.save();
      return {
        title: fields.title.value.trim(), summary: fields.summary.value.trim(), slug: fields.slug.value.trim(),
        tags: fields.tags.value.split(",").map(tag => tag.trim()).filter(Boolean),
        blocks: output.blocks, editor_version: output.version,
        cover: cover && cover.media_id ? {media_id: cover.media_id, alt: fields.coverAlt.value.trim()} : null,
        seo: {title: fields.seoTitle.value.trim(), description: fields.seoDescription.value.trim()},
      };
    }

    const statusNode = $("#ed-save-status");
    const controller = S.createSaveController({
      key: post.id || "new", revision: post.revision, initial: post.id ? post : null, storage: global.localStorage,
      isOnline: () => navigator.onLine !== false,
      save: async (snap, base) => {
        if (!post.id) {
          const created = await request("/api/v1/editorial/posts", {method: "POST", body: snap});
          controller.rekey(created.id, created.revision);
          history_replace(created.id);
          return created;
        }
        return request(`/api/v1/editorial/posts/${encodeURIComponent(post.id)}`, {method: "PUT", body: {...snap, base_revision: base}});
      },
      onStatus: (state, label) => { statusNode.textContent = label; statusNode.dataset.state = state; },
      onSaved: saved => { post = saved; afterServerUpdate(); },
      onConflict: (latest, mine) => showConflict(latest, mine),
    });

    function history_replace(id) {
      post.id = id;
      global.history.replaceState(null, "", `#/edit/${encodeURIComponent(id)}`);
      $("#ed-history").disabled = false;
    }

    let snapshotTimer = null;
    function scheduleSnapshot() {
      if (readOnly) return;
      clearTimeout(snapshotTimer);
      snapshotTimer = setTimeout(async () => {
        const snap = await snapshot();
        if (history.record(snap.blocks)) updateHistoryButtons();
        updateCounts(snap.blocks);
        controller.changed(snap);
      }, 250);
    }

    function updateHistoryButtons() { $("#ed-undo").disabled = !history.canUndo; $("#ed-redo").disabled = !history.canRedo; }
    function updateCounts(blocks) {
      const words = S.wordCount(blocks);
      $("#ed-words").textContent = `${words} word${words === 1 ? "" : "s"}`;
      $("#ed-reading").textContent = `${Math.max(1, Math.ceil(words / 220))} min read`;
    }

    function afterServerUpdate() {
      $("#ed-post-status").innerHTML = statusBadge(post.status);
      $("#ed-revision").textContent = `Revision ${post.revision}`;
      renderActions();
    }

    function renderActions() {
      const container = $("#ed-actions");
      const actions = post.actions || [];
      container.innerHTML = actions.length ? actions.map(action => `<button type="button" class="${action === "publish" ? "primary" : action === "archive" || action === "unpublish" ? "danger" : ""}" data-action="${esc(action)}">${esc(ACTION_LABELS[action] || action)}</button>`).join("") :
        `<p class="field-help">${post.id ? `This post is ${esc(STATUS_LABELS[post.status] || post.status)}. ${post.can_edit ? "" : "Your role cannot change it right now."}` : "Save the draft to unlock workflow actions."}</p>`;
      container.querySelectorAll("[data-action]").forEach(button => button.addEventListener("click", () => runAction(button.dataset.action, button)));
    }

    async function runAction(action, button) {
      const error = $("#ed-action-error");
      error.textContent = "";
      const snap = post.can_edit ? await snapshot() : null;
      if (snap) { controller.changed(snap); await controller.flush(); }
      if (controller.state === "conflict") { error.textContent = "Resolve the version conflict first."; return; }
      if (controller.state === "offline" || controller.state === "error") { error.textContent = "Your latest changes are not saved yet."; return; }
      const body = {action};
      if (action === "publish" || action === "schedule") {
        const blocks = snap ? snap.blocks : post.blocks;
        const missing = S.missingAlt(blocks);
        if (missing.length) {
          error.textContent = `Add alt text to ${missing.length} image${missing.length === 1 ? "" : "s"} before publishing.`;
          root.querySelector(".kimg-missing-alt [data-kimg-alt]")?.focus();
          return;
        }
        if (cover && !fields.coverAlt.value.trim()) { error.textContent = "Add alt text to the cover image before publishing."; fields.coverAlt.focus(); return; }
      }
      if (action === "schedule") {
        if (!fields.publishAt.value) { error.textContent = "Choose a publish date and time first."; fields.publishAt.focus(); return; }
        body.publish_at = new Date(fields.publishAt.value).toISOString();
      }
      if ((action === "unpublish" || action === "archive") && !confirm(action === "archive" ? "Archive this post? It disappears from the blog and the workspace lists." : "Unpublish this post? It returns to draft and leaves the public blog.")) return;
      button.setAttribute("aria-busy", "true");
      try {
        post = await request(`/api/v1/editorial/posts/${encodeURIComponent(post.id)}/transition`, {method: "POST", body});
        afterServerUpdate();
        if (!post.can_edit) await editor.readOnly.toggle(true);
        toast(action === "publish" ? "Published. The post is live on the blog." : action === "schedule" ? `Scheduled for ${new Date(post.publish_at).toLocaleString()}.` : `${ACTION_LABELS[action]}: done.`);
      } catch (failure) {
        error.textContent = failure.message;
      } finally {
        button.removeAttribute("aria-busy");
      }
    }

    function showConflict(latest, mine) {
      const node = $("#ed-conflict");
      node.hidden = false;
      node.innerHTML = `<span><b>Someone saved revision ${esc(latest.revision)} while you were editing</b> (${esc(latest.updated_by || "another session")}, ${esc(timeAgo(latest.updated_at))}). Your changes are kept on this device.</span>
        <span class="button-row"><button type="button" class="btn-sm primary" data-conflict="mine">Keep mine (save on top)</button><button type="button" class="btn-sm" data-conflict="theirs">Load their version</button></span>`;
      node.querySelector('[data-conflict="mine"]').addEventListener("click", async () => { node.hidden = true; controller.resolve(latest, true); await controller.flush(); });
      node.querySelector('[data-conflict="theirs"]').addEventListener("click", async () => {
        node.hidden = true;
        controller.resolve(latest, false);
        post = {...post, ...latest};
        fill(latest);
        await editor.render({blocks: latest.blocks || []});
        afterServerUpdate();
      });
      void mine;
    }

    // Recovery after an interruption (crash, closed tab, offline).
    const offer = S.recoveryFor(global.localStorage, post.id || "new", post.id ? post : null);
    if (offer && !readOnly) {
      const node = $("#ed-recovery");
      node.hidden = false;
      node.innerHTML = `<span><b>Unsaved changes from ${esc(new Date(offer.savedLocallyAt).toLocaleString())} were found on this device.</b> ${offer.stale ? "The post changed on the server since; restoring saves your copy as a new revision on top." : ""}</span>
        <span class="button-row"><button type="button" class="btn-sm primary" data-recover="restore">Restore my changes</button><button type="button" class="btn-sm" data-recover="discard">Discard</button></span>`;
      node.querySelector('[data-recover="restore"]').addEventListener("click", async () => {
        node.hidden = true;
        fill(offer.snapshot);
        await editor.render({blocks: offer.snapshot.blocks || []});
        if (offer.stale) controller.revision = post.revision;
        controller.changed(await snapshot());
        await controller.flush();
      });
      node.querySelector('[data-recover="discard"]').addEventListener("click", () => { node.hidden = true; S.discardRecovery(global.localStorage, post.id || "new"); });
    }

    // Field wiring.
    fields.title.addEventListener("input", () => {
      grow(fields.title);
      if (!slugManual) fields.slug.value = S.slugify(fields.title.value);
      renderSocial(); scheduleSnapshot();
    });
    fields.slug.addEventListener("input", () => { slugManual = fields.slug.value.trim() !== ""; $("#ed-slug-hint").textContent = slugManual ? `URL: /blog.html?slug=${S.slugify(fields.slug.value)}` : "Follows the title until you edit it."; scheduleSnapshot(); });
    fields.slug.addEventListener("blur", () => { fields.slug.value = S.slugify(fields.slug.value); });
    for (const field of [fields.summary, fields.tags, fields.coverAlt, fields.seoTitle, fields.seoDescription]) field.addEventListener("input", () => { if (field === fields.summary) grow(field); renderSocial(); scheduleSnapshot(); });
    $("#ed-holder").addEventListener("kimg:change", scheduleSnapshot);

    function renderCover() {
      const node = $("#ed-cover");
      node.innerHTML = cover ? `<img src="/media/blog/${encodeURIComponent(cover.media_id)}/480" alt="${esc(fields.coverAlt.value)}">` : `<p class="field-help">No cover. Posts with a cover get richer cards on the blog and social sites.</p>`;
      $("#ed-cover-remove").hidden = !cover;
    }
    function renderSocial() {
      const title = fields.seoTitle.value.trim() || fields.title.value.trim() || "Untitled post";
      const description = fields.seoDescription.value.trim() || fields.summary.value.trim() || "Add a summary to describe this post.";
      $("#ed-seo-title-count").textContent = title.length;
      $("#ed-seo-description-count").textContent = description.length;
      $("#ed-social").innerHTML = `${cover ? `<img src="/media/blog/${encodeURIComponent(cover.media_id)}/960" alt="">` : '<div class="ed-social-blank" aria-hidden="true">K</div>'}<div><small>${esc(location.host)}</small><b>${esc(title.slice(0, 70))}</b><p>${esc(description.slice(0, 160))}</p></div>`;
    }

    $("#ed-cover-file").addEventListener("change", async event => {
      const file = event.target.files[0];
      if (!file) return;
      try {
        const result = await upload(file, {alt: fields.coverAlt.value});
        cover = {media_id: result.file.media_id, alt: fields.coverAlt.value};
        renderCover(); renderSocial(); scheduleSnapshot();
        if (!fields.coverAlt.value) fields.coverAlt.focus();
      } catch (error) { toast(error.message, "error"); }
    });
    $("#ed-cover-remove").addEventListener("click", () => { cover = null; fields.coverAlt.value = ""; renderCover(); renderSocial(); scheduleSnapshot(); });
    $("#ed-cover-pick").addEventListener("click", async () => {
      const picker = $("#ed-cover-picker");
      if (!picker.hidden) { picker.hidden = true; return; }
      const media = await request("/api/v1/editorial/media");
      picker.hidden = false;
      picker.innerHTML = media.items.length ? media.items.map(item => `<button type="button" class="ed-pick" data-pick="${esc(item.id)}" data-alt="${esc(item.alt)}"><img src="/media/blog/${encodeURIComponent(item.id)}/480" alt="${esc(item.alt || "Untitled image")}"></button>`).join("") : '<p class="field-help">The library is empty.</p>';
      picker.querySelectorAll("[data-pick]").forEach(button => button.addEventListener("click", () => {
        cover = {media_id: button.dataset.pick, alt: button.dataset.alt};
        if (!fields.coverAlt.value) fields.coverAlt.value = button.dataset.alt;
        picker.hidden = true; renderCover(); renderSocial(); scheduleSnapshot();
      }));
    });

    // Toolbar.
    $("#ed-save").addEventListener("click", async () => { controller.changed(await snapshot()); await controller.flush(); });
    $("#ed-undo").addEventListener("click", async () => { const blocks = history.undo(); if (blocks) { await editor.render({blocks}); updateHistoryButtons(); controller.changed(await snapshot()); } });
    $("#ed-redo").addEventListener("click", async () => { const blocks = history.redo(); if (blocks) { await editor.render({blocks}); updateHistoryButtons(); controller.changed(await snapshot()); } });
    const focusButton = $("#ed-focus");
    const setFocus = on => { document.body.classList.toggle("ed-focus", on); focusButton.setAttribute("aria-pressed", String(on)); focusButton.textContent = on ? "Exit focus" : "Focus"; };
    focusButton.addEventListener("click", () => setFocus(!document.body.classList.contains("ed-focus")));

    root.querySelectorAll("dialog [data-close]").forEach(button => button.addEventListener("click", () => button.closest("dialog").close()));

    // Preview with the server renderer.
    const previewDialog = $("#ed-preview-dialog");
    const frame = $("#ed-preview-frame");
    previewDialog.querySelectorAll("[data-preview-mode]").forEach(button => button.addEventListener("click", () => {
      previewDialog.querySelectorAll("[data-preview-mode]").forEach(other => { other.classList.toggle("active", other === button); other.setAttribute("aria-pressed", String(other === button)); });
      frame.dataset.mode = button.dataset.previewMode;
    }));
    $("#ed-preview").addEventListener("click", async () => {
      const snap = await snapshot();
      let rendered;
      try { rendered = await request("/api/v1/editorial/preview", {method: "POST", body: snap}); } catch (error) { toast(error.message, "error"); return; }
      const coverHTML = rendered.cover ? `<img class="article-cover" src="${esc(rendered.cover.url)}" srcset="${esc(rendered.cover.srcset)}" sizes="(max-width: 860px) 100vw, 860px" alt="${esc(rendered.cover.alt)}">` : "";
      frame.srcdoc = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/landing.css"><link rel="stylesheet" href="/blog.css"><link rel="stylesheet" href="/vendor/highlight/github-dark.min.css"></head><body><main class="article-shell"><header class="article-header"><div class="blog-tags">${snap.tags.map(tag => `<span>${esc(tag)}</span>`).join("")}</div><h1>${esc(snap.title || "Untitled post")}</h1><p>${esc(snap.summary)}</p><div class="article-byline"><span>${esc(post.author || member.display_name)}</span><span>${esc(rendered.reading_minutes)} min read</span></div></header>${coverHTML}<article class="article-content">${rendered.rendered_html}</article></main></body></html>`;
      frame.dataset.mode = previewDialog.querySelector("[data-preview-mode].active").dataset.previewMode;
      previewDialog.showModal();
    });

    // Revision history, compare and restore.
    const historyDialog = $("#ed-history-dialog");
    async function loadHistory() {
      const data = await request(`/api/v1/editorial/posts/${encodeURIComponent(post.id)}/revisions`);
      $("#ed-history-list").innerHTML = `<table class="ed-table ed-history"><thead><tr><th scope="col"><span class="sr-only">Compare</span></th><th scope="col">Revision</th><th scope="col">Saved</th><th scope="col">By</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead><tbody>${data.items.map(item => `<tr><td><input type="checkbox" data-compare="${item.number}" aria-label="Select revision ${item.number} to compare"></td><td><b>#${item.number}</b> ${item.number === data.current ? '<span class="status active">current</span>' : ""}<br><small>${esc(item.reason)}${item.restored_from ? ` of #${item.restored_from}` : ""} · ${esc(item.title || "Untitled")}</small></td><td>${esc(new Date(item.created_at).toLocaleString())}</td><td>${esc(item.created_by)}</td><td>${item.number !== data.current && post.can_edit ? `<button type="button" class="btn-sm" data-restore="${item.number}">Restore</button>` : ""}</td></tr>`).join("")}</tbody></table><div class="button-row"><button type="button" class="btn-sm" id="ed-compare-run">Compare selected</button></div>`;
      $("#ed-compare-run").addEventListener("click", async () => {
        const selected = [...historyDialog.querySelectorAll("[data-compare]:checked")].map(node => Number(node.dataset.compare)).sort((a, b) => a - b);
        if (selected.length !== 2) { $("#ed-compare").innerHTML = '<p class="form-error">Select exactly two revisions.</p>'; return; }
        const diff = await request(`/api/v1/editorial/posts/${encodeURIComponent(post.id)}/compare?from=${selected[0]}&to=${selected[1]}`);
        $("#ed-compare").innerHTML = `<h3>#${diff.from} → #${diff.to}: ${diff.summary.added} added, ${diff.summary.removed} removed, ${diff.summary.changed} changed</h3>
          ${diff.fields.map(field => `<p class="ed-diff-field"><b>${esc(field.field)}</b>: <del>${esc(field.before)}</del> → <ins>${esc(field.after)}</ins></p>`).join("")}
          <ol class="ed-diff">${diff.blocks.filter(block => block.change !== "unchanged").map(block => `<li class="ed-diff-${esc(block.change)}"><small>${esc(block.change)} · ${esc(block.type)}</small>${block.before ? `<del>${esc(block.before)}</del>` : ""}${block.after && block.change !== "removed" ? `<ins>${esc(block.after)}</ins>` : ""}</li>`).join("") || "<li>No block changes.</li>"}</ol>`;
      });
      historyDialog.querySelectorAll("[data-restore]").forEach(button => button.addEventListener("click", async () => {
        await controller.flush();
        try {
          post = await request(`/api/v1/editorial/posts/${encodeURIComponent(post.id)}/revisions/${button.dataset.restore}/restore`, {method: "POST", body: {base_revision: controller.revision}});
          controller.resolve(post, false);
          fill(post);
          await editor.render({blocks: post.blocks || []});
          afterServerUpdate();
          toast(`Restored revision #${button.dataset.restore} as revision #${post.revision}.`);
          await loadHistory();
        } catch (error) { toast(error.message, "error"); }
      }));
    }
    $("#ed-history").addEventListener("click", async () => { $("#ed-compare").innerHTML = ""; await loadHistory(); historyDialog.showModal(); });

    // Optional Markdown import.
    const markdownDialog = $("#ed-markdown-dialog");
    $("#ed-import").addEventListener("click", () => markdownDialog.showModal());
    $("#ed-markdown-form").addEventListener("submit", async event => {
      event.preventDefault();
      const markdown = event.target.elements.markdown.value;
      try {
        const result = await request("/api/v1/editorial/markdown", {method: "POST", body: {markdown}});
        const current = await editor.save();
        await editor.render({blocks: [...current.blocks, ...result.blocks]});
        markdownDialog.close();
        scheduleSnapshot();
      } catch (error) { toast(error.message, "error"); }
    });

    // Keyboard: Cmd/Ctrl+S saves; Esc leaves focus mode.
    const onKey = async event => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s") {
        event.preventDefault();
        if (!readOnly) { controller.changed(await snapshot()); await controller.flush(); }
      } else if (event.key === "Escape" && document.body.classList.contains("ed-focus") && !document.querySelector("dialog[open]")) {
        setFocus(false);
      }
    };
    const onOnline = () => controller.online();
    const onBeforeUnload = event => { if (controller.dirty) { event.preventDefault(); event.returnValue = ""; } };
    document.addEventListener("keydown", onKey);
    global.addEventListener("online", onOnline);
    global.addEventListener("beforeunload", onBeforeUnload);

    history.record((await editor.save()).blocks);
    updateCounts(post.blocks || []);
    renderActions();
    if (!post.id) fields.title.focus();

    active = {
      async destroy() {
        clearTimeout(snapshotTimer);
        document.removeEventListener("keydown", onKey);
        global.removeEventListener("online", onOnline);
        global.removeEventListener("beforeunload", onBeforeUnload);
        setFocus(false);
        try { if (!readOnly && controller.dirty) await controller.flush(); } catch (_) { /* kept locally */ }
        try { editor.destroy(); } catch (_) { /* already gone */ }
      },
      controller,
    };
  }

  async function closeEditor() {
    if (active) { const current = active; active = null; await current.destroy(); }
  }

  global.EditorialEditor = {editorView, closeEditor, get active() { return active; }};
})(window);
