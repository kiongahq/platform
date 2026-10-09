 /* Kionga console core: API client, identity, routing, shared components and
 * the click-handler registry. View modules (js/views/*.js) register loaders
 * with registerView() and delegated click handlers with onClick(); boot.js
 * starts the console once every module has loaded. All files are classic
 * scripts that share one global scope, loaded in order by console.html. */

const api = async (path, options = {}) => {
  const response = await fetch(path, {signal: AbortSignal.timeout(20000), ...options, headers: {"Accept": "application/json", "Content-Type": "application/json", ...options.headers}});
  const contentType = response.headers.get("content-type") || "";
  const body = response.status === 204 ? {} : contentType.includes("application/json") ? await response.json() : {message: await response.text()};
  if (response.status === 401) {
    if (eventSource) eventSource.close();
    const returnTo = encodeURIComponent(location.pathname + location.search);
    location.assign(`/auth/login?return_to=${returnTo}`);
    throw new Error("Your session has expired. Please sign in again.");
  }
  if (!response.ok) {
    const error = new Error(body.message || body.error || "Unable to complete this request. Please retry.");
    error.status = response.status;
    error.details = body.details || body.errors || null;
    error.decision = body.decision || null;
    throw error;
  }
  return body;
};

const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, char => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[char]));
const relativeFormat = new Intl.RelativeTimeFormat("en", {numeric: "auto"});
function when(value) {
  if (!value) return "—";
  const seconds = (new Date(value) - Date.now()) / 1000;
  const abs = Math.abs(seconds);
  if (abs < 60) return relativeFormat.format(Math.round(seconds), "second");
  if (abs < 3600) return relativeFormat.format(Math.round(seconds / 60), "minute");
  if (abs < 86400) return relativeFormat.format(Math.round(seconds / 3600), "hour");
  return relativeFormat.format(Math.round(seconds / 86400), "day");
}
const dateTime = value => value ? new Date(value).toLocaleString() : "—";
/* A relative time whose tooltip and machine-readable value are absolute. */
const timeTag = value => value ? `<time datetime="${escapeHTML(value)}" title="${escapeHTML(dateTime(value))}">${escapeHTML(when(value))}</time>` : "—";
const statusIcons = {succeeded:"✓", ready:"✓", healthy:"✓", failed:"✕", cancelled:"⊘", skipped:"↷", queued:"…", running:"▶", pending:"…"};
const status = value => `<span class="status ${escapeHTML(String(value || "unknown").replace(/\s+/g, "_"))}">${escapeHTML(String(value || "unknown").replaceAll("_", " "))}</span>`;
const bytes = size => size > 1048576 ? `${(size/1048576).toFixed(1)} MB` : size > 1024 ? `${(size/1024).toFixed(1)} KB` : `${size} B`;
const csv = value => String(value || "").split(",").map(item => item.trim()).filter(Boolean);
const plural = (count, word) => `${count} ${word}${count === 1 ? "" : "s"}`;

let toastTimer = null;
/* A failure carrying an access decision (403 from the policy engine) gets a
 * "Why?" button that explains the denial for the current user. */
function toast(message, kind = "info", failure = null) {
  const node = document.querySelector("#toast");
  node.textContent = message;
  const decision = failure?.decision;
  node.classList.toggle("has-action", Boolean(decision));
  if (decision) {
    const why = document.createElement("button");
    why.type = "button";
    why.className = "toast-action";
    why.textContent = "Why?";
    why.setAttribute("aria-label", "Why was this denied?");
    why.addEventListener("click", () => explainDenial(decision).catch(error => toast(error.message, "error")));
    node.append(" ", why);
  }
  node.classList.toggle("error", kind === "error");
  node.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => node.classList.remove("show"), decision ? 9000 : kind === "error" ? 5000 : 2600);
}

/* Renders a policy decision and its explain trace. */
function decisionHTML(decision) {
  const allowed = decision.allowed === true;
  const matched = decision.matched_statements || [];
  const evaluated = decision.evaluated_policies || [];
  return `<div class="decision ${allowed ? "allow" : "deny"}" data-decision="${allowed ? "allow" : "deny"}">
    <p class="decision-verdict"><strong>${allowed ? "Allowed" : "Denied"}</strong> <code>${escapeHTML(decision.action)}</code> on <code>${escapeHTML(decision.resource)}</code></p>
    <p>${escapeHTML(decision.reason)}</p>
    ${metaList([["Decided by", `<code>${escapeHTML(decision.decided_by)}</code>`], ["Policies evaluated", String(evaluated.length)]])}
    ${matched.length ? `<div class="table-wrap decision-trace"><table class="data-table compact-table"><thead><tr><th scope="col">Statement</th><th scope="col">Effect</th><th scope="col">Policy</th><th scope="col">Applies through</th></tr></thead><tbody>${matched.map(item => `<tr><td><code>${escapeHTML(item.sid)}</code></td><td>${status(item.effect === "deny" ? "denied" : "allowed")}</td><td>${escapeHTML(item.policy_name)} <small>v${escapeHTML(item.version)}</small></td><td>${escapeHTML(item.source)}</td></tr>`).join("")}</tbody></table></div>` : `<p class="field-help">No statement matched this action and resource.</p>`}
    ${evaluated.length ? `<div class="tags">${evaluated.map(item => `<span class="tag" title="${escapeHTML(item.source)}">${escapeHTML(item.id)}@v${escapeHTML(item.version)}</span>`).join("")}</div>` : ""}
  </div>`;
}

/* Re-evaluates a denial for the signed-in user and shows the trace. The
 * decision from the failed request is the fallback (and is authoritative
 * for the coarse role/service gate, which explain does not model). */
async function explainDenial(decision) {
  let shown = decision;
  if (decision.action && decision.resource && decision.decided_by !== "coarse-gate") {
    try {
      shown = (await api(`/api/v1/iam/explain?${new URLSearchParams({action: decision.action, resource: decision.resource})}`)).decision || decision;
    } catch { /* keep the original decision */ }
  }
  document.querySelector("#decision-detail").innerHTML = decisionHTML(shown);
  document.querySelector("#decision-dialog").showModal();
}

async function copyText(value) {
  const text = String(value || "");
  if (!text) throw new Error("There is nothing to copy.");
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const input = document.createElement("textarea");
  input.value = text;
  input.setAttribute("readonly", "");
  input.style.cssText = "position:fixed;left:-9999px;top:0;opacity:0";
  document.body.appendChild(input);
  input.select();
  const copied = document.execCommand("copy");
  input.remove();
  if (!copied) throw new Error("Copy is not available in this browser.");
}

/* Show a transient success/failure state on a button without changing its
 * accessible name permanently. */
function flashButton(button, state, label) {
  if (!button) return;
  const original = button.dataset.originalLabel || button.innerHTML;
  button.dataset.originalLabel = original;
  button.dataset.state = state;
  if (label) button.innerHTML = label;
  setTimeout(() => {
    delete button.dataset.state;
    button.innerHTML = button.dataset.originalLabel;
    delete button.dataset.originalLabel;
  }, 1600);
}

/* Run an async action with a busy button. Errors surface as a toast and the
 * button's failure state; the promise still rejects for callers that care. */
async function withBusy(button, action, {success = "", failure = true} = {}) {
  if (button) { button.setAttribute("aria-busy", "true"); button.disabled = true; }
  try {
    const result = await action();
    if (button) flashButton(button, "success");
    if (success) toast(success);
    return result;
  } catch (error) {
    if (button) flashButton(button, "error");
    if (failure) toast(error.message, "error", error);
    throw error;
  } finally {
    if (button) { button.removeAttribute("aria-busy"); button.disabled = false; }
  }
}

const copyButton = (value, label = "Copy") => `<button type="button" class="copy-button" data-copy="${escapeHTML(value)}" aria-label="${escapeHTML(`${label}: ${value}`)}" title="Copy to clipboard">${escapeHTML(label)}</button>`;
const copyable = value => `<span class="copyable"><code title="${escapeHTML(value)}">${escapeHTML(value)}</code>${copyButton(value)}</span>`;
const metaList = rows => `<dl class="meta-list">${rows.filter(Boolean).map(([label, value]) => `<div><dt>${escapeHTML(label)}</dt><dd>${value ?? "—"}</dd></div>`).join("")}</dl>`;
const emptyState = (title, detail = "", actions = "") => `<div class="empty-state"><b>${escapeHTML(title)}</b>${detail ? `<span>${detail}</span>` : ""}${actions ? `<div class="button-row start">${actions}</div>` : ""}</div>`;
const tableEmpty = (columns, title, detail = "") => `<tr><td colspan="${columns}" class="empty"><b>${escapeHTML(title)}</b>${detail ? `<br><small>${detail}</small>` : ""}</td></tr>`;
const metadataValue = value => {
  if (value === undefined || value === null || value === "") return "—";
  if (typeof value === "object") return `<pre class="hint metadata-json">${escapeHTML(JSON.stringify(value, null, 2))}</pre>`;
  return escapeHTML(value);
};
function showMetadata(kind, title, item, actions = "") {
  const rows = Object.entries(item).map(([key, value]) =>
    `<tr><th>${escapeHTML(key.replaceAll("_", " "))}</th><td>${metadataValue(value)}</td></tr>`
  ).join("");
  document.querySelector("#metadata-detail").innerHTML = `<p class="eyebrow">${escapeHTML(kind)}</p><h2>${escapeHTML(title)}</h2>${actions}<details class="raw-json"><summary>All fields</summary><table class="metadata-table"><tbody>${rows}</tbody></table></details>`;
  document.querySelector("#metadata-dialog").showModal();
}

// ---- identity & permissions -------------------------------------------------
// /api/v1/me is the single source of truth: controls the caller's role cannot
// use are disabled with the reason, and the API enforces the same table.
let me = {subject: "", email: "", roles: [], services: [], mode: "local", permissions: {}, entitlements: null};
const can = key => me.permissions[key] === true;
const isAdmin = () => me.roles.includes("admin") || me.roles.includes("operator");
const hasService = service => isAdmin() || (me.roles.length > 0 && !me.roles.includes("user")) || (me.services || []).includes(service);
const denialReason = () => me.roles.includes("viewer") ? "Your viewer role is read-only." : "Your administrator has not granted this action. Request access from My access.";

function applyPermissions() {
  document.querySelectorAll("[data-perm]").forEach(button => {
    const allowed = can(button.dataset.perm);
    button.disabled = !allowed;
    button.title = allowed ? (button.dataset.title || "") : denialReason();
  });
  const name = me.email || me.subject || "anonymous";
  document.querySelector("#user-name").textContent = name;
  document.querySelector("#user-role").textContent = `${me.roles.join(", ") || "no role"} · ${me.mode} sign-in`;
  document.querySelector("#menu-user").textContent = name;
  document.querySelector("#account-avatar").textContent = name.trim().charAt(0).toUpperCase() || "U";
  document.querySelectorAll(".logout-action").forEach(link => { link.hidden = false; });
  document.querySelectorAll("[data-admin-only]").forEach(node => { node.hidden = !isAdmin(); });
  document.querySelectorAll(".nav-item[data-view]").forEach(node => {
    if (!["access", "profile", "settings"].includes(node.dataset.view)) node.hidden = !hasService(serviceForView(node.dataset.view));
  });
  document.querySelector("#workbench-link").hidden = !hasService("workbench");
  document.querySelector("#ide-link").hidden = !hasService("ide");
}

async function loadMe() {
  me = {...me, ...await api("/api/v1/me")};
  applyPermissions();
}

// ---- views and routing ------------------------------------------------------
let activeView = "overview";
const viewLoaders = {};
const viewMeta = {};
/* Views whose data the project selector filters. Others hide the selector so
 * it never appears to do something it does not. */
const projectScopedViews = new Set(["overview", "projects", "pipelines", "functions", "models", "agents", "logs"]);
const adminViews = new Set(["access", "blogs"]);
const ungatedViews = new Set(["access", "profile", "settings"]);

function registerView(id, loader, meta = {}) {
  viewLoaders[id] = loader;
  viewMeta[id] = meta;
}

let selectedProject = new URLSearchParams(location.search).get("project") || "";
let selectedResource = new URLSearchParams(location.search).get("resource") || "";
let projectOptions = [];
let navigationRequest = 0;
function scoped(items) { return selectedProject ? items.filter(item => item.project_id === selectedProject || item.id === selectedProject) : items; }
const projectName = id => projectOptions.find(item => item.id === id)?.name || id || "—";

function feedback(message, error = false) {
  const node = document.querySelector("#view-feedback");
  node.hidden = !message;
  node.classList.toggle("error", error);
  document.querySelector("#view-feedback-message").textContent = message;
  document.querySelector("#retry-view").hidden = !error;
}

async function loadCurrentView() {
  const request = ++navigationRequest;
  const id = activeView;
  const view = document.getElementById(id);
  view?.setAttribute("aria-busy", "true");
  feedback("");
  try {
    await viewLoaders[id]?.();
    if (request === navigationRequest) feedback("");
    return true;
  } catch (error) {
    if (request === navigationRequest) feedback(error.message, true);
    return false;
  } finally { view?.setAttribute("aria-busy", "false"); }
}

function routeURL(id, resource = selectedResource) {
  const url = new URL(location.href);
  url.searchParams.set("view", id);
  if (selectedProject) url.searchParams.set("project", selectedProject); else url.searchParams.delete("project");
  if (resource) url.searchParams.set("resource", resource); else url.searchParams.delete("resource");
  return url;
}

/* Views backed by another service's API (logs belong to pipelines). */
const serviceForView = id => ({logs: "pipelines"})[id] || id;
function canOpenView(id) {
  if (!ungatedViews.has(id) && !hasService(serviceForView(id))) return "This service has not been assigned to you.";
  if (adminViews.has(id) && !isAdmin()) return "Administrator access is required.";
  return "";
}

function syncProjectContext() {
  const select = document.querySelector("#project-context");
  if (selectedProject && !projectOptions.some(item => item.id === selectedProject)) {
    // Keep a handed-off project visible even before options refresh, so the
    // selector never silently shows "All projects" while filtering.
    select.insertAdjacentHTML("beforeend", `<option value="${escapeHTML(selectedProject)}">${escapeHTML(selectedProject)}</option>`);
  }
  select.value = selectedProject;
  document.querySelector("#project-context-wrap").hidden = !projectScopedViews.has(activeView) || (!projectOptions.length && !selectedProject);
}

async function showView(id, options = {}) {
  if (!viewLoaders[id]) id = "profile";
  const blocked = canOpenView(id);
  if (blocked) { toast(blocked, "error"); return; }
  if (options.project !== undefined) selectedProject = options.project;
  selectedResource = options.resource || "";
  activeView = id;
  document.querySelectorAll("dialog[open]").forEach(dialog => dialog.close());
  if (options.history !== false) history.pushState({}, "", routeURL(id));
  document.querySelectorAll(".view").forEach(node => node.classList.toggle("active", node.id === id));
  document.querySelectorAll(".nav-item").forEach(node => {
    node.classList.toggle("active", node.dataset.view === id);
    node.setAttribute("aria-current", node.dataset.view === id ? "page" : "false");
  });
  const meta = viewMeta[id] || {};
  document.querySelector("#page-eyebrow").textContent = meta.eyebrow || "ML WORKSPACE";
  document.querySelector("#page-title").textContent = meta.title || "Workspace";
  document.title = `${meta.title || "Console"} · Kionga`;
  syncProjectContext();
  const title = document.querySelector("#page-title");
  title.setAttribute("tabindex", "-1");
  if (options.focus !== false) title.focus({preventScroll: true});
  const loaded = await loadCurrentView();
  if (loaded && selectedResource && meta.openResource) {
    try { await meta.openResource(selectedResource); }
    catch (error) { feedback(error.message || "This item is no longer available in the selected project.", true); }
  }
}

/* navigateTo is the only way views hand off to each other. It carries the
 * project context and keeps the selector, URL and loaded data in agreement. */
async function navigateTo(view, {project = selectedProject, resource = ""} = {}) {
  selectedProject = project || "";
  await showView(view, {project: selectedProject, resource});
  loadWorkspaces().catch(() => {});
}

async function loadProjectOptions() {
  projectOptions = (await api("/api/v1/project-options")).items || [];
  const options = projectOptions.map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)}</option>`).join("");
  document.querySelector("#project-context").innerHTML = `<option value="">All assigned projects</option>${options}`;
  syncProjectContext();
  for (const id of ["submit-project", "function-project", "definition-project", "agent-project"]) {
    const select = document.getElementById(id);
    if (!select) continue;
    const previous = select.value;
    select.innerHTML = options;
    if (selectedProject || projectOptions.some(item => item.id === previous)) select.value = selectedProject || previous;
  }
}

const workspaceState = {};
async function loadWorkspaces() {
  const data = await api("/api/v1/workspaces");
  for (const item of data.items) {
    workspaceState[item.id] = item;
    const ids = item.id === "workbench" ? ["workbench-link", "settings-jupyter"] : ["ide-link", "settings-ide"];
    for (const id of ids) {
      const link = document.getElementById(id);
      if (!link) continue;
      link.href = item.available ? workspaceURL(item.id, selectedProject) : "/console.html?view=profile";
      link.dataset.available = String(item.available);
      link.dataset.message = item.message;
      link.title = `${item.name}: ${item.message}`;
      link.setAttribute("aria-disabled", String(!item.available));
      const detail = link.querySelector("small");
      if (detail) detail.textContent = item.message;
    }
  }
  document.querySelector("#workspace-status").textContent = data.items.filter(item => hasService(item.id)).map(item => `${item.name}: ${item.available ? "ready" : item.state || "unavailable"}`).join(" · ");
}
const workspaceURL = (tool, project = "") => `/workspace.html?tool=${encodeURIComponent(tool)}${project ? `&project=${encodeURIComponent(project)}` : ""}`;

// ---- feature intros ---------------------------------------------------------
/* Each major view answers: what is this, why use it, what you need first,
 * what the main action does, and where results and errors appear. */
const introKey = "kionga.console.intros";
function introState() { try { return JSON.parse(localStorage.getItem(introKey) || "{}"); } catch { return {}; } }
function renderIntros() {
  const state = introState();
  document.querySelectorAll("[data-intro]").forEach(node => {
    const intro = viewMeta[node.dataset.intro]?.intro;
    if (!intro) { node.hidden = true; return; }
    const open = state[node.dataset.intro] !== false;
    node.innerHTML = `<details class="feature-intro" ${open ? "open" : ""}><summary>About ${escapeHTML(viewMeta[node.dataset.intro].title || node.dataset.intro)}</summary><dl>${[
      ["What is this?", intro.what], ["Why use it?", intro.why], ["Before you start", intro.needs],
      ["Main action", intro.action], ["Results and errors", intro.results],
    ].filter(([, value]) => value).map(([label, value]) => `<div><dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value)}</dd></div>`).join("")}</dl></details>`;
    node.querySelector("details").addEventListener("toggle", event => {
      const next = introState(); next[node.dataset.intro] = event.target.open;
      try { localStorage.setItem(introKey, JSON.stringify(next)); } catch { /* per-browser convenience only */ }
    });
  });
}

// ---- refresh ----------------------------------------------------------------
async function refreshView(button) {
  await withBusy(button, async () => {
    const ok = await loadCurrentView();
    if (!ok) throw new Error(document.querySelector("#view-feedback-message").textContent || "Refresh failed.");
  }, {failure: false}).then(() => {
    document.querySelector("#refresh-status").textContent = `Updated ${new Date().toLocaleTimeString()}`;
  }).catch(error => {
    document.querySelector("#refresh-status").textContent = `Refresh failed: ${error.message}`;
  });
}

// ---- delegated clicks -------------------------------------------------------
/* Handlers run in registration order; the first whose selector matches the
 * click target handles it. Returning false lets later handlers also run. */
const clickHandlers = [];
function onClick(selector, handler, {ignoreControls = false} = {}) { clickHandlers.push({selector, handler, ignoreControls}); }
async function handleDynamicClick(event) {
  for (const {selector, handler, ignoreControls} of clickHandlers) {
    const node = event.target.closest(selector);
    if (!node) continue;
    if (ignoreControls && event.target.closest("button,a,input,select,textarea") && event.target.closest("button,a,input,select,textarea") !== node) continue;
    const result = await handler(node, event);
    if (result !== false) return;
  }
}
document.addEventListener("click", event => {
  handleDynamicClick(event).catch(failure => { feedback(failure.message, true); toast(failure.message, "error", failure); });
});
document.addEventListener("keydown", event => {
  if ((event.key === "Enter" || event.key === " ") && event.target.matches("[role='button']")) {
    event.preventDefault();
    event.target.click();
  }
});
onClick("[data-copy]", async button => {
  try { await copyText(button.dataset.copy); flashButton(button, "success", "Copied ✓"); }
  catch (failure) { flashButton(button, "error", "Copy failed"); toast(`${failure.message} Select the text and copy it manually.`, "error"); }
});
onClick("[data-navigate]", async node => navigateTo(node.dataset.navigate, {project: node.dataset.project ?? selectedProject, resource: node.dataset.resource || ""}));

// ---- dialogs ----------------------------------------------------------------
function closeDialog(node) {
  const modal = node.closest("dialog");
  modal.querySelectorAll(".form-error").forEach(error => { error.textContent = ""; });
  modal.close();
}
document.querySelectorAll("dialog .close, dialog [data-dialog-close]").forEach(button => button.addEventListener("click", () => closeDialog(button)));
document.querySelectorAll("dialog").forEach(modal => modal.addEventListener("click", event => {
  if (event.target === modal) closeDialog(modal);
}));

// ---- preferences --------------------------------------------------------------
const preferenceKey = "kionga.console.preferences";
function readPreferences() {
  try { return JSON.parse(localStorage.getItem(preferenceKey) || "{}"); } catch { return {}; }
}
function applyPreferences() {
  const prefs = readPreferences();
  document.body.classList.toggle("compact-ui", Boolean(prefs.compact));
  document.body.classList.toggle("reduce-motion", Boolean(prefs.reduceMotion));
  if (prefs.theme === "light" || prefs.theme === "dark") document.documentElement.dataset.theme = prefs.theme;
  else delete document.documentElement.dataset.theme;
}

// ---- live updates over SSE ----------------------------------------------------
let lastDigest = "";
let eventSource = null;
const liveListeners = [];
function onLiveUpdate(listener) { liveListeners.push(listener); }
function setLiveState(state, label) {
  const indicator = document.querySelector("#live-indicator");
  indicator.dataset.state = state;
  indicator.querySelector(".label").textContent = label;
  indicator.title = label;
}
function connectEvents() {
  if (eventSource) eventSource.close();
  const source = new EventSource("/api/v1/events");
  eventSource = source;
  source.onmessage = event => {
    setLiveState("connected", "Live");
    if (event.data === lastDigest) return;
    lastDigest = event.data;
    if (!document.querySelector("dialog[open]") && !document.querySelector(".view.active :focus")) loadCurrentView();
    liveListeners.forEach(listener => { try { listener(); } catch { /* listeners own their errors */ } });
  };
  source.onerror = () => { setLiveState("reconnecting", "Reconnecting"); api("/api/v1/me").catch(() => {}); };
}

// ---- shell navigation ---------------------------------------------------------
const shell = document.querySelector(".shell");
const sidebarToggle = document.querySelector("#sidebar-toggle");
const isMobile = () => window.matchMedia("(max-width: 640px)").matches;
function toggleSidebar() {
  if (isMobile()) {
    const open = shell.classList.toggle("mobile-nav-open");
    sidebarToggle.setAttribute("aria-expanded", String(open));
    sidebarToggle.setAttribute("aria-label", open ? "Close navigation" : "Open navigation");
    return;
  }
  const collapsed = shell.classList.toggle("sidebar-collapsed");
  try { localStorage.setItem("kionga.sidebar.collapsed", String(collapsed)); } catch { /* optional */ }
  sidebarToggle.setAttribute("aria-expanded", String(!collapsed));
  sidebarToggle.setAttribute("aria-label", collapsed ? "Expand navigation" : "Collapse navigation");
}
try {
  if (!isMobile() && localStorage.getItem("kionga.sidebar.collapsed") === "true") {
    shell.classList.add("sidebar-collapsed");
    sidebarToggle.setAttribute("aria-expanded", "false");
    sidebarToggle.setAttribute("aria-label", "Expand navigation");
  }
} catch { /* storage unavailable */ }

async function showAbout() {
  let health = {status: "unreachable", version: "?"};
  try { health = await api("/api/v1/health"); } catch { /* shown as unreachable */ }
  document.querySelector("#about-detail").innerHTML = `
    <div class="detail-meta">${status(health.status === "ok" ? "healthy" : "failed")}<span class="tag">${escapeHTML(health.service || "gateway")}</span><span class="tag">v${escapeHTML(health.version)}</span></div>
    <p>Self-hosted MLOps, data and agentic AI platform: pipelines, model serving, feature store, agents and real-time streams behind one control plane.</p>
    ${metaList([["Signed in as", escapeHTML(me.email || me.subject || "anonymous")], ["Roles", escapeHTML(me.roles.join(", ") || "none")], ["Sign-in mode", escapeHTML(me.mode)], ["API", `<code>${escapeHTML(location.origin)}/api/v1</code>`]])}
    <p><a href="/api-docs.html" target="_blank" rel="noopener">API reference ↗</a> · <a href="/blogs.html" target="_blank" rel="noopener">Engineering blog ↗</a></p>`;
  document.querySelector("#about-dialog").showModal();
}

for (const id of ["workbench-link", "ide-link", "settings-jupyter", "settings-ide"]) {
  document.getElementById(id)?.addEventListener("click", event => {
    if (event.currentTarget.dataset.available !== "true") {
      event.preventDefault();
      feedback(event.currentTarget.dataset.message || "Checking workspace availability…", true);
      loadWorkspaces().catch(error => feedback(error.message, true));
    }
  });
}
document.querySelector("#project-context").addEventListener("change", event => navigateTo(activeView, {project: event.target.value}));
document.querySelector("#retry-view").addEventListener("click", () => initialized ? loadCurrentView() : initializeConsole());
window.addEventListener("popstate", () => {
  const query = new URLSearchParams(location.search);
  selectedProject = query.get("project") || "";
  showView(query.get("view") || "overview", {history: false, resource: query.get("resource") || ""});
});
document.querySelector("#account-button").addEventListener("click", () => showView("settings"));
sidebarToggle.addEventListener("click", toggleSidebar);
document.querySelector("#refresh-view").addEventListener("click", event => refreshView(event.currentTarget));
document.querySelector("#open-help").addEventListener("click", () => showAbout().catch(error => toast(error.message, "error")));
document.querySelectorAll(".app-brand").forEach(link => link.addEventListener("click", event => {
  event.preventDefault();
  showView(hasService("overview") ? "overview" : "profile");
}));
document.querySelectorAll(".nav-item").forEach(button => button.addEventListener("click", () => {
  showView(button.dataset.view, {resource: ""});
  if (isMobile()) {
    shell.classList.remove("mobile-nav-open");
    sidebarToggle.setAttribute("aria-expanded", "false");
  }
}));
document.querySelectorAll("[data-view-target]:not(.app-brand)").forEach(button => button.addEventListener("click", () => showView(button.dataset.viewTarget, {resource: ""})));
