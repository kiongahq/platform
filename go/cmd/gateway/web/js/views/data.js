/* Data views: feature store, object storage and endpoints, real-time
 * streams, and the shared catalog. */
let featureCache = [];
function renderFeatures(query = "") {
  const lowered = query.toLowerCase();
  const filtered = featureCache.filter(view => !lowered || view.name.toLowerCase().includes(lowered) || (view.tags || []).some(tag => tag.toLowerCase().includes(lowered)));
  document.querySelector("#feature-grid").innerHTML = filtered.length ? filtered.map(view => {
    const store = view.store || {kind: "internal", location: "Redis online · Parquet offline"};
    return `<article class="card feature-card interactive-card" role="button" tabindex="0" data-feature-detail="${escapeHTML(view.id)}" aria-label="Open feature view ${escapeHTML(view.name)}"><span class="kind">${escapeHTML(store.kind === "external" ? `external · ${store.provider || "adapter"}` : "internal store")} · entity ${escapeHTML(view.entity)}</span><h3>${escapeHTML(view.name)}</h3><div class="table-wrap"><table class="schema"><thead><tr><th>Field</th><th>Type</th></tr></thead><tbody>${(view.fields || []).map(field => `<tr><td>${escapeHTML(field.name)}</td><td>${escapeHTML(field.type)}</td></tr>`).join("")}</tbody></table></div><div class="tags">${(view.tags || []).map(tag => `<span class="tag">${escapeHTML(tag)}</span>`).join("")}${view.ttl_seconds ? `<span class="tag">TTL ${view.ttl_seconds}s</span>` : ""}</div><footer>${status(view.status)}<span class="tag">${view.online_entity_count || 0} entities online</span>${view.materialized_at ? `<small>Fresh ${timeTag(view.materialized_at)}</small>` : `<small>Never materialized</small>`}</footer></article>`;
  }).join("") : emptyState(query ? "No feature views match your search" : "No feature views applied yet", query ? "Try another name or tag." : "Apply definitions with the SDK, then run the materializer to fill the online store.");
}
async function loadFeatures() {
  const data = await api("/api/v1/features");
  featureCache = data.items || [];
  renderFeatures(document.querySelector("#feature-search").value);
}
document.querySelector("#feature-search").addEventListener("input", event => renderFeatures(event.target.value));
onClick("[data-feature-detail]", node => {
  const item = featureCache.find(feature => feature.id === node.dataset.featureDetail);
  if (!item) return;
  showMetadata("FEATURE VIEW", item.name, item, metaList([["Entity", escapeHTML(item.entity)], ["Status", status(item.status)], ["Online entities", String(item.online_entity_count || 0)], ["Last materialized", timeTag(item.materialized_at)], ["TTL", item.ttl_seconds ? `${item.ttl_seconds}s` : "None"]]));
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
