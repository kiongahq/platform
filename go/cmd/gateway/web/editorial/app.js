/* Editorial workspace shell: opens the editorial session (members only),
 * then routes #/posts, #/drafts, #/reviews, #/media, #/authors, #/settings,
 * #/new and #/edit/<id>. */
(function (global) {
  "use strict";
  const {request, esc, toast, ROLE_LABELS} = EditorialAPI;
  const main = document.getElementById("ed-main");
  let session = null;

  function gate(html) {
    document.getElementById("ed-nav").hidden = true;
    main.innerHTML = `<section class="ed-gate panel">${html}</section>`;
  }

  async function openSession() {
    try {
      return await request("/api/v1/editorial/session");
    } catch (error) {
      if (error.status !== 403) throw error;
    }
    try {
      await request("/api/v1/editorial/session", {method: "POST"});
      return await request("/api/v1/editorial/session");
    } catch (error) {
      if (error.status !== 403) throw error;
      const body = error.body || {};
      gate(`<p class="eyebrow">EDITORIAL WORKSPACE</p><h1 id="ed-denied">You don't have editorial access</h1>
        <p>Writing and publishing on the engineering blog needs an editorial membership. ML platform roles, including administrator, do not grant it.</p>
        <p>Ask an editorial admin to add your account as an author or editor.</p>
        ${body.bootstrap_available && body.can_bootstrap ? `<div class="ed-bootstrap"><p><b>No editorial admin exists yet.</b> As a platform administrator you can claim the first editorial admin role. This is recorded in the audit log.</p>
          <label>Your byline<input id="ed-bootstrap-name" placeholder="Display name shown on posts"></label>
          <button type="button" class="primary" id="ed-bootstrap">Become the first editorial admin</button><p class="form-error" role="alert" id="ed-bootstrap-error"></p></div>` : ""}
        <div class="button-row"><a class="button-link" href="/blogs.html">Read the blog</a><a class="button-link quiet" href="/console.html">Back to the ML console</a></div>`);
      document.getElementById("ed-bootstrap")?.addEventListener("click", async () => {
        try {
          await request("/api/v1/editorial/bootstrap", {method: "POST", body: {display_name: document.getElementById("ed-bootstrap-name").value}});
          start();
        } catch (failure) { document.getElementById("ed-bootstrap-error").textContent = failure.message; }
      });
      return null;
    }
  }

  function parseRoute() {
    const hash = location.hash.replace(/^#\/?/, "");
    const [name, id] = hash.split("/");
    if (name === "edit" && id) return {name: "edit", id: decodeURIComponent(id)};
    if (name === "new") return {name: "edit", id: "new"};
    return {name: ["posts", "drafts", "reviews", "media", "authors", "settings"].includes(name) ? name : "posts"};
  }

  let routing = Promise.resolve();
  function route() {
    routing = routing.then(render).catch(error => {
      main.innerHTML = `<div class="view-feedback error" role="alert"><span>${esc(error.message || "Something went wrong.")}</span><button type="button" onclick="location.reload()">Reload</button></div>`;
      if (error.status === 403) start();
    });
    return routing;
  }

  async function render() {
    const current = parseRoute();
    const editorOpen = EditorialEditor.active;
    // Keep the open editor when the hash only changed because a new post got its id.
    if (editorOpen && current.name === "edit" && main.querySelector("#ed-editor") && current.id !== "new" && main.dataset.editing === "new") {
      main.dataset.editing = current.id;
      return;
    }
    await EditorialEditor.closeEditor();
    document.querySelectorAll("#ed-nav [data-route]").forEach(link => {
      const on = link.dataset.route === current.name;
      link.classList.toggle("active", on);
      if (on) link.setAttribute("aria-current", "page"); else link.removeAttribute("aria-current");
    });
    main.dataset.editing = current.name === "edit" ? current.id : "";
    main.innerHTML = '<p class="ed-loading">Loading…</p>';
    const member = session.member;
    switch (current.name) {
      case "edit": await EditorialEditor.editorView(main, member, {postId: current.id}); break;
      case "media": await EditorialViews.mediaView(main, member); break;
      case "authors": await EditorialViews.authorsView(main, member); break;
      case "settings": await EditorialViews.settingsView(main, member, {session}); break;
      default: await EditorialViews.postsView(main, member, {route: current.name});
    }
    document.title = `${current.name === "edit" ? "Editor" : current.name[0].toUpperCase() + current.name.slice(1)} · Kionga Editorial`;
  }

  async function start() {
    try {
      const saved = sessionStorage.getItem("kionga.editorial.return");
      sessionStorage.removeItem("kionga.editorial.return");
      if (saved && !location.hash) history.replaceState(null, "", saved);
    } catch (_) { /* storage unavailable */ }
    session = await openSession();
    if (!session) return;
    const member = session.member;
    document.getElementById("ed-nav").hidden = false;
    document.getElementById("ed-member").hidden = false;
    document.getElementById("ed-signout").hidden = false;
    document.getElementById("ed-member-name").textContent = member.display_name;
    document.getElementById("ed-member-role").textContent = ROLE_LABELS[member.role] || member.role;
    document.getElementById("ed-avatar").textContent = (member.display_name || "E").trim()[0].toUpperCase();
    await route();
  }

  document.getElementById("ed-signout").addEventListener("click", async () => {
    await EditorialEditor.closeEditor();
    try { await request("/api/v1/editorial/session", {method: "DELETE"}); } catch (_) { /* already gone */ }
    toast("You left the editorial workspace.");
    location.assign("/blogs.html");
  });
  global.addEventListener("hashchange", route);
  start().catch(error => gate(`<h1>Editorial workspace unavailable</h1><p>${esc(error.message)}</p><button type="button" onclick="location.reload()">Try again</button>`));
})(window);
