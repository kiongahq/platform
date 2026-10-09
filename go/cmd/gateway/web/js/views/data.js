/* Data views: feature store, object storage and endpoints, real-time
 * streams, and the shared catalog. */
let featureCache = [];
let featureStoreCache = [];
const freshnessLabel = freshness => {
  if (!freshness || freshness.state === "never") return "Never materialized";
  const age = freshness.age_seconds || 0;
  const human = age < 120 ? `${age}s` : age < 7200 ? `${Math.round(age / 60)}m` : `${Math.round(age / 3600)}h`;
  return `${freshness.state === "stale" ? "Stale" : "Fresh"} · updated ${human} ago${freshness.ttl_seconds ? ` · TTL ${freshness.ttl_seconds}s` : ""}`;
};
const accessibleProjects = store => store.all_projects ? "All projects" : (store.allowed_projects || []).map(projectName).join(", ") || "No projects";
function storeItem(store, actions = "") {
  const health = store.health || {state: "configured", detail: ""};
  return `<article class="store-item" data-store-id="${escapeHTML(store.id)}"><header><h4>${escapeHTML(store.name)}</h4>${status(health.state)}</header>
    <small><span class="tag">${escapeHTML(store.kind === "internal" ? "Internal" : "External")}</span> ${escapeHTML(store.provider)} · ${escapeHTML(store.location || "")}</small>
    <small>Projects: ${escapeHTML(accessibleProjects(store))}</small>
    <p class="field-help">${escapeHTML(health.detail || "Not checked yet.")}${health.checked_at ? ` · checked ${timeTag(health.checked_at)}` : ""}</p>${actions}</article>`;
}
function renderFeatures(query = "") {
  const lowered = query.toLowerCase();
  const filtered = featureCache.filter(view => !lowered || view.name.toLowerCase().includes(lowered) || (view.tags || []).some(tag => tag.toLowerCase().includes(lowered)));
  document.querySelector("#feature-grid").innerHTML = filtered.length ? filtered.map(view => {
    const store = view.store || {kind: "internal", provider: "internal", location: "Redis online · Parquet offline"};
    const freshness = view.freshness || {state: view.materialized_at ? "fresh" : "never"};
    return `<article class="card feature-card interactive-card" role="button" tabindex="0" data-feature-detail="${escapeHTML(view.id)}" aria-label="Open feature view ${escapeHTML(view.name)}"><span class="kind">${escapeHTML(store.kind === "external" ? `external · ${store.provider || "adapter"}` : "internal store")} · entity ${escapeHTML(view.entity)}${view.version ? ` · v${view.version}` : ""}</span><h3>${escapeHTML(view.name)}</h3><p class="feature-store-line" title="${escapeHTML(store.location || "")}">${escapeHTML(store.location || "")}</p><div class="table-wrap"><table class="schema"><thead><tr><th>Field</th><th>Type</th></tr></thead><tbody>${(view.fields || []).map(field => `<tr><td>${escapeHTML(field.name)}</td><td>${escapeHTML(field.type)}</td></tr>`).join("")}</tbody></table></div><div class="tags">${(view.tags || []).map(tag => `<span class="tag">${escapeHTML(tag)}</span>`).join("")}${view.ttl_seconds ? `<span class="tag">TTL ${view.ttl_seconds}s</span>` : ""}</div>${view.failure_count ? `<p class="form-error">Last ${plural(view.failure_count, "run")} failed: ${escapeHTML(view.latest_failure || "see lineage")}</p>` : ""}<footer>${status(freshness.state)}<span class="tag">${view.online_entity_count || 0} entities online</span><small class="freshness">${escapeHTML(freshnessLabel(freshness))}</small></footer></article>`;
  }).join("") : emptyState(query ? "No feature views match your search" : "No feature views applied yet", query ? "Try another name or tag." : "Apply definitions with the SDK, then run the materializer to fill the online store.");
}
function renderFeatureStores() {
  document.querySelector("#feature-store-list").innerHTML = featureStoreCache.length
    ? featureStoreCache.map(store => storeItem(store)).join("")
    : emptyState("No feature stores visible", "Ask an administrator to share a store with your project.");
}
async function loadFeatures() {
  const [views, stores] = await Promise.all([
    api("/api/v1/features/views").catch(error => error.status === 404 ? api("/api/v1/features") : Promise.reject(error)),
    api("/api/v1/features/stores").catch(error => ({items: [], error: error.message})),
  ]);
  featureCache = views.items || [];
  featureStoreCache = stores.items || [];
  renderFeatureStores();
  if (stores.error) document.querySelector("#feature-store-list").innerHTML = emptyState("Feature stores unavailable", escapeHTML(stores.error));
  renderFeatures(document.querySelector("#feature-search").value);
}
document.querySelector("#feature-search").addEventListener("input", event => renderFeatures(event.target.value));
onClick("[data-feature-detail]", async node => {
  const item = featureCache.find(feature => feature.id === node.dataset.featureDetail);
  if (!item) return;
  const [versions, lineage] = await Promise.all([
    api(`/api/v1/features/${encodeURIComponent(item.name)}/versions`).catch(() => ({items: []})),
    api(`/api/v1/features/${encodeURIComponent(item.name)}/lineage`).catch(() => ({items: []})),
  ]);
  const store = item.store || {};
  const runs = (lineage.items || []).slice(0, 5).map(run => `<li>${status(run.status)} <code>${escapeHTML(run.run_id)}</code> · v${escapeHTML(run.view_version || "?")} · ${escapeHTML(run.source_dataset)}${run.offline_uri ? ` → <code>${escapeHTML(run.offline_uri)}</code>` : ""} · ${run.entity_count} entities · ${timeTag(run.created_at)}${run.error ? ` <span class="form-error">${escapeHTML(run.error)}</span>` : ""}</li>`).join("");
  showMetadata("FEATURE VIEW", item.name, item, `${metaList([
    ["Entity", escapeHTML(item.entity)], ["Store", `${escapeHTML(store.name || "internal")} · ${escapeHTML(store.location || "")}`],
    ["Freshness", `${status((item.freshness || {}).state || "never")} ${escapeHTML(freshnessLabel(item.freshness))}`],
    ["Version", item.version ? `v${item.version} of ${(versions.items || []).length}` : "—"],
    ["Online entities", String(item.online_entity_count || 0)], ["TTL", item.ttl_seconds ? `${item.ttl_seconds}s` : "None"],
  ])}<h3>Lineage</h3>${runs ? `<ul class="file-list">${runs}</ul>` : `<p class="field-help">No materialization runs recorded yet.</p>`}`);
}, {ignoreControls: true});

const storageState = {bucket: "", prefix: ""};
async function loadStorage() {
  const browser = document.querySelector("#storage-browser");
  const title = document.querySelector("#storage-title");
  const up = document.querySelector("#storage-up");
  try {
    if (!storageState.bucket) {
      const data = await api("/api/v1/storage/buckets");
      title.textContent = "Buckets";
      up.hidden = true;
      browser.innerHTML = (data.buckets || []).map(bucket => `<button type="button" class="storage-row" data-bucket="${escapeHTML(bucket.name)}"><span aria-hidden="true">🪣</span><b>${escapeHTML(bucket.name)}</b></button>`).join("") || emptyState("No buckets found", "Buckets you are granted appear here.");
    } else {
      const data = await api(`/api/v1/storage/objects?bucket=${encodeURIComponent(storageState.bucket)}&prefix=${encodeURIComponent(storageState.prefix)}`);
      title.textContent = `${storageState.bucket}/${storageState.prefix}`;
      up.hidden = false;
      const prefixes = (data.prefixes || []).map(prefix => `<button type="button" class="storage-row" data-prefix="${escapeHTML(prefix)}"><span aria-hidden="true">📁</span><b>${escapeHTML(prefix.slice(storageState.prefix.length))}</b></button>`).join("");
      const objects = (data.objects || []).map(object => `<button type="button" class="storage-row" data-object="${escapeHTML(object.key)}"><span aria-hidden="true">📄</span><b>${escapeHTML(object.key.slice(storageState.prefix.length))}</b><small>${bytes(object.size)}</small></button>`).join("");
      browser.innerHTML = prefixes + objects || emptyState("This prefix is empty");
    }
  } catch (error) {
    browser.innerHTML = emptyState("Object store unavailable", escapeHTML(error.message));
  }
  const [models, functions] = await Promise.all([hasService("models") ? api("/api/v1/models").catch(() => ({items: []})) : {items: []}, hasService("functions") ? api("/api/v1/functions").catch(() => ({items: [], configured: false})) : {items: [], configured: false}]);
  document.querySelector("#endpoint-list").closest("article").hidden = !hasService("models");
  document.querySelector("#function-list").closest("article").hidden = !hasService("functions");
  const live = (models.items || []).filter(model => model.endpoint_url && model.endpoint_url.startsWith("http"));
  document.querySelector("#endpoint-list").innerHTML = live.length ? live.map(model => `<article class="endpoint-item"><div><h4>${escapeHTML(model.name)} v${escapeHTML(model.version)}</h4><div class="endpoint-meta">${status(model.deployment_status)}<span class="tag">${escapeHTML(model.stage)}</span></div></div>${can("models_write") ? `<button type="button" data-model-test="${escapeHTML(model.id)}">Test</button>` : ""}<code>${escapeHTML(model.endpoint_url)}</code></article>`).join("") : emptyState("No live endpoints", "Deploy a model that passed its quality gate.");
  document.querySelector("#function-list").innerHTML = functions.configured
    ? ((functions.items || []).length ? functions.items.map(fn => `<article class="endpoint-item"><div><h4>${escapeHTML(fn.name)}</h4><div class="endpoint-meta"><span class="tag">${fn.replicas} replicas</span></div></div>${can("functions_write") ? `<button type="button" data-function-invoke="${escapeHTML(fn.name)}">Invoke</button>` : ""}<code>${escapeHTML(fn.image)}</code></article>`).join("") : emptyState("No functions yet", "OpenFaaS is connected."))
    : emptyState("Serverless not configured", "Set <code>OPENFAAS_URL</code> to connect it.");
}
document.querySelector("#storage-up").addEventListener("click", () => {
  if (storageState.prefix) {
    const parts = storageState.prefix.replace(/\/$/, "").split("/");
    parts.pop();
    storageState.prefix = parts.length ? parts.join("/") + "/" : "";
  } else storageState.bucket = "";
  loadStorage().catch(error => toast(error.message, "error"));
});
onClick("[data-bucket]", async node => { storageState.bucket = node.dataset.bucket; storageState.prefix = ""; await loadStorage(); });
onClick("[data-prefix]", async node => { storageState.prefix = node.dataset.prefix; await loadStorage(); });
onClick("[data-object]", async node => {
  const preview = await api(`/api/v1/storage/object?bucket=${encodeURIComponent(storageState.bucket)}&key=${encodeURIComponent(node.dataset.object)}`);
  document.querySelector("#preview-detail").innerHTML = `<p class="eyebrow">OBJECT PREVIEW</p><h2>${escapeHTML(preview.key)}</h2><div class="detail-meta"><span class="tag">${escapeHTML(preview.content_type || "unknown type")}</span><span class="tag">${bytes(preview.size)}</span>${preview.truncated ? `<span class="tag">Truncated</span>` : ""}</div><pre class="hint object-preview">${escapeHTML(preview.content)}</pre>`;
  document.querySelector("#preview-dialog").showModal();
});

let realtimeCache = [];
async function loadRealtime() {
  const data = await api("/api/v1/realtime");
  const demos = [
    {key: "fraud", title: "Fraud detection", detail: "transaction → features → model score → alert"},
    {key: "callcenter", title: "Call-center analysis", detail: "transcript → support agent → sentiment + intent"},
    {key: "recommendations", title: "Recommendations", detail: "activity → profile features → ranked items"},
  ];
  realtimeCache = demos.map(demo => ({...demo, stats: (data.demos || {})[demo.key] || null}));
  document.querySelector("#realtime-grid").innerHTML = realtimeCache.map(demo => {
    const stats = demo.stats;
    const body = stats
      ? `<div class="metric-row"><span>Events <b>${stats.events ?? 0}</b></span><span>Avg latency <b>${stats.avg_latency_ms ?? 0} ms</b></span>${demo.key === "fraud" ? `<span>Flagged <b class="bad">${stats.flagged ?? 0}</b></span>` : ""}</div><small class="field-help">Updated ${timeTag(stats.updated_at)}</small>`
      : `<p class="field-help">No events processed yet. Produce demo events below.</p>`;
    return `<article class="card interactive-card" role="button" tabindex="0" data-stream-detail="${demo.key}"><span class="kind">stream</span><h3>${demo.title}</h3><p>${demo.detail}</p>${body}</article>`;
  }).join("");
}
onClick("[data-stream-detail]", node => {
  const item = realtimeCache.find(stream => stream.key === node.dataset.streamDetail);
  if (item) showMetadata("REAL-TIME STREAM", item.title, {key: item.key, flow: item.detail, statistics: item.stats || "No events processed yet"});
});

let catalogCache = [];
async function loadCatalog(kind = "") {
  const items = await api(`/api/v1/catalog${kind ? `?kind=${encodeURIComponent(kind)}` : ""}`);
  catalogCache = items;
  document.querySelector("#catalog-grid").innerHTML = items.length ? items.map((item, index) => `<article class="card interactive-card" role="button" tabindex="0" data-catalog-detail="${index}"><span class="kind">${escapeHTML(item.kind)} · ${escapeHTML(item.version)}</span><h3>${escapeHTML(item.name)}</h3><div class="tags">${item.metadata.map(meta => `<span class="tag">${escapeHTML(meta)}</span>`).join("")}</div><footer><span></span>${status(item.status)}</footer></article>`).join("") : emptyState("Nothing in the catalog yet", "It fills up as you register models, features, agents and tools.");
}
document.querySelectorAll("[data-kind]").forEach(button => button.addEventListener("click", () => {
  document.querySelectorAll("[data-kind]").forEach(n => { n.classList.remove("active"); n.setAttribute("aria-pressed", "false"); });
  button.classList.add("active"); button.setAttribute("aria-pressed", "true");
  loadCatalog(button.dataset.kind).catch(error => feedback(error.message, true));
}));
onClick("[data-catalog-detail]", node => {
  const item = catalogCache[Number(node.dataset.catalogDetail)];
  if (item) showMetadata("CATALOG ENTRY", item.name, item);
});

registerView("features", loadFeatures, {
  eyebrow: "FEATURE STORE", title: "Features",
  intro: {
    what: "Versioned feature definitions with an online store for serving and offline snapshots for training.",
    why: "Train and serve on the same feature logic, and see how fresh each view is.",
    needs: "The features service. External stores need a configured connection.",
    action: "Feature views are applied from code with the SDK; the materializer fills the online store.",
    results: "Freshness, online entity counts and failures appear on each card.",
  },
});
registerView("storage", loadStorage, {
  eyebrow: "ARTIFACTS & ENDPOINTS", title: "Storage",
  intro: {
    what: "A browser for granted object-store buckets, plus live model endpoints and serverless functions.",
    why: "Inspect artifacts and endpoints without leaving the console.",
    needs: "The storage service and bucket grants from your administrator.",
    action: "Select a bucket, folder or object to browse and preview it.",
    results: "Previews are read-only and truncated for large objects.",
  },
});
registerView("realtime", loadRealtime, {
  eyebrow: "STREAM PROCESSING", title: "Real-time",
  intro: {
    what: "Kafka-driven demo flows that score events with features, models and agents.",
    why: "See how streaming inference fits together before building your own consumer.",
    needs: "The realtime service and a running Kafka broker.",
    action: "Produce demo events from a workspace terminal with the commands below.",
    results: "Event counts and latency update on each card.",
  },
});
registerView("catalog", () => loadCatalog(document.querySelector("[data-kind].active")?.dataset.kind || ""), {
  eyebrow: "DISCOVERABILITY", title: "Catalog",
  intro: {
    what: "Every registered model, feature view, agent and tool in one searchable list.",
    why: "Reuse what your team already built instead of rebuilding it.",
    action: "Filter by kind, then open an entry for its details.",
  },
});
