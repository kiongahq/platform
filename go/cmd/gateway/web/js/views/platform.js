/* Platform: actively checked connections and production readiness. */
let componentCache = [];
async function loadComponents() {
  const [items, readiness, connections, logExport] = await Promise.all([api("/api/v1/components"), hasService("overview") ? api("/api/v1/onboarding/readiness").catch(() => ({percent: 0, items: []})) : {percent: 0, items: []}, api("/api/v1/connections"), api("/api/v1/observability/log-export").catch(error => ({state: "unavailable", detail: error.message}))]);
  renderLogExport(logExport);
  document.querySelector("#readiness-list").closest(".panel").hidden = !hasService("overview");
  componentCache = items;
  document.querySelector("#component-grid").innerHTML = items.map((item, index) => `<article class="component interactive-card" role="button" tabindex="0" data-component-detail="${index}"><div><span class="category">${escapeHTML(item.category)}</span><h3>${escapeHTML(item.name)}</h3></div>${status(item.status)}<p>${escapeHTML(item.description)}</p></article>`).join("");
  document.querySelector("#readiness-percent").textContent = `${readiness.percent}%`;
  document.querySelector("#readiness-list").innerHTML = readiness.items.map(item => `<li class="${item.status === "ready" ? "done" : ""}"><span aria-hidden="true">${item.status === "ready" ? "✓" : "○"}</span><div><b>${escapeHTML(item.label)}</b><small>${escapeHTML(item.description)}</small></div></li>`).join("");
  const active = Object.fromEntries(["prefect", "openfaas"].map(type => [type, connections.items.filter(item => item.type === type && item.activated_at).sort((a, b) => new Date(b.activated_at) - new Date(a.activated_at))[0]?.id]));
  document.querySelector("#connection-grid").innerHTML = connections.items.length ? connections.items.map(item => `<article class="card connection-card"><span class="kind">${escapeHTML(item.type)} · ${active[item.type] === item.id ? "Active runtime" : ["prefect", "openfaas"].includes(item.type) ? "Available runtime" : "Availability monitor"}</span><h3>${escapeHTML(item.name)}</h3><p class="truncate" title="${escapeHTML(item.endpoint)}">${escapeHTML(item.endpoint)}</p>${item.message ? `<small>${escapeHTML(item.message)}</small>` : ""}<footer>${status(item.status)}${can("connections_write") ? `<span class="button-row"><button type="button" data-connection-test="${escapeHTML(item.id)}">Test</button>${["prefect", "openfaas"].includes(item.type) && active[item.type] !== item.id ? `<button type="button" data-connection-activate="${escapeHTML(item.id)}">Use for new operations</button>` : ""}</span>` : ""}</footer></article>`).join("") : emptyState("No services connected", "Add a connection to check a service's availability and, for Prefect or OpenFaaS, route new operations to it.");
  await loadDataConnections();
}
function renderLogExport(exporter) {
  const labels = {not_deployed: "Not deployed", not_configured: "Not configured", configured: "Configured", healthy: "Healthy", degraded: "Degraded", unavailable: "Unavailable"};
  const facts = exporter.target ? metaList([["Target", escapeHTML(exporter.target)], ["Destination", `<code>${escapeHTML(exporter.destination || "")}</code>`], ["Tenant", escapeHTML(exporter.tenant || "")], ["Exported", String(exporter.exported ?? 0)], ["Dead-lettered", String(exporter.dead_lettered ?? 0)], ["Last success", exporter.last_success && !exporter.last_success.startsWith("0001") ? timeTag(exporter.last_success) : "Never"], exporter.last_error ? ["Last error", escapeHTML(exporter.last_error)] : null]) : "";
  document.querySelector("#log-export-status").innerHTML = `<div class="panel-heading"><div><p class="eyebrow">OBSERVABILITY</p><h3>Log export</h3><p>Ships pipeline logs and platform events to Elasticsearch or OpenSearch.</p></div>${status(String(labels[exporter.state] ? exporter.state : "unavailable"))}</div>${exporter.detail ? `<p class="field-help">${escapeHTML(exporter.detail)}</p>` : ""}${facts}`;
}

// ---- feature stores and external object storage (administrators) ----------
let featureProviderCache = [];
async function loadDataConnections() {
  const section = document.querySelector("#data-connections");
  section.hidden = !isAdmin();
  if (!isAdmin()) return;
  const [stores, storage] = await Promise.all([
    api("/api/v1/admin/feature-stores").catch(error => ({items: [], error: error.message})),
    api("/api/v1/admin/storage-connections").catch(error => ({items: [], error: error.message})),
  ]);
  const testButton = (kind, id) => `<div class="button-row start"><button type="button" data-${kind}-test="${escapeHTML(id)}">Test</button>${id !== "internal" ? `<button type="button" class="danger" data-${kind}-delete="${escapeHTML(id)}">Remove</button>` : ""}</div>`;
  const capabilities = caps => Object.entries(caps || {}).filter(([, on]) => on).map(([name]) => name.replaceAll("_", "-")).join(", ") || "none";
  document.querySelector("#feature-store-connections").innerHTML = stores.error ? emptyState("Feature stores unavailable", escapeHTML(stores.error)) : (stores.items || []).map(item => {
    const summary = {...item, location: item.provider === "internal" ? `online ${item.config?.online_url || "not configured"} · offline ${item.config?.offline_uri || "not configured"}` : (item.config?.url || ""), all_projects: !(item.allowed_projects || []).length};
    return storeItem(summary, `<small>Capabilities: ${escapeHTML(capabilities(item.capabilities))}${item.secret_ref ? ` · secret ${escapeHTML(item.secret_ref)} ${item.secret_present ? "set" : "missing"}` : ""}</small>${testButton("feature-store", item.id)}`);
  }).join("");
  document.querySelector("#storage-connections").innerHTML = storage.error ? emptyState("Storage connections unavailable", escapeHTML(storage.error)) : (storage.items || []).length ? storage.items.map(item => storeItem({...item, kind: "external", provider: "s3", location: `${item.endpoint} · ${item.bucket}${item.region ? ` · ${item.region}` : ""}${item.path_style ? " · path-style" : ""}`, all_projects: !(item.allowed_projects || []).length}, `<small>Secret ${escapeHTML(item.secret_ref)} ${item.secret_present ? "set" : "missing"}${item.ca_bundle ? " · custom CA" : ""}</small>${testButton("storage", item.id)}`)).join("") : emptyState("No external object storage", "Connect an S3-compatible bucket to check it from here.");
}
function connectionHealthToast(health) {
  const ok = health.state === "healthy";
  toast(`${ok ? "Healthy" : health.state.charAt(0).toUpperCase() + health.state.slice(1)}: ${health.detail}`, ok ? "info" : "error");
}
onClick("[data-feature-store-test]", async node => {
  const result = await withBusy(node, () => api(`/api/v1/admin/feature-stores/${encodeURIComponent(node.dataset.featureStoreTest)}/test`, {method: "POST", body: "{}"}));
  connectionHealthToast(result.health);
  await loadDataConnections();
});
onClick("[data-storage-test]", async node => {
  const result = await withBusy(node, () => api(`/api/v1/admin/storage-connections/${encodeURIComponent(node.dataset.storageTest)}/test`, {method: "POST", body: "{}"}));
  connectionHealthToast(result.health);
  await loadDataConnections();
});
onClick("[data-feature-store-delete]", async node => {
  if (!window.confirm("Remove this feature store connection? Projects using it lose access.")) return;
  await withBusy(node, () => api(`/api/v1/admin/feature-stores/${encodeURIComponent(node.dataset.featureStoreDelete)}`, {method: "DELETE"}), {success: "Feature store removed."});
  await loadDataConnections();
});
onClick("[data-storage-delete]", async node => {
  if (!window.confirm("Remove this storage connection?")) return;
  await withBusy(node, () => api(`/api/v1/admin/storage-connections/${encodeURIComponent(node.dataset.storageDelete)}`, {method: "DELETE"}), {success: "Storage connection removed."});
  await loadDataConnections();
});
document.querySelector("#add-feature-store").addEventListener("click", async () => {
  document.querySelector("#feature-store-error").textContent = "";
  try {
    if (!featureProviderCache.length) featureProviderCache = (await api("/api/v1/admin/feature-store-providers")).items || [];
  } catch (error) { document.querySelector("#feature-store-error").textContent = error.message; }
  document.querySelector("#feature-store-provider").innerHTML = featureProviderCache.filter(item => item.kind === "external").map(item => `<option value="${escapeHTML(item.name)}" ${item.adapter ? "" : "disabled"}>${escapeHTML(item.title)}${item.adapter ? "" : " · contract available — no adapter"}</option>`).join("");
  document.querySelector("#feature-store-provider-help").textContent = "Providers marked “no adapter” have a published contract but cannot be connected yet.";
  document.querySelector("#feature-store-dialog").showModal();
});
document.querySelector("#add-storage-connection").addEventListener("click", () => {
  document.querySelector("#storage-connection-error").textContent = "";
  document.querySelector("#storage-connection-dialog").showModal();
});
async function saveAndTest(form, error, create, test, submitter) {
  error.textContent = "";
  try {
    const result = await withBusy(submitter, async () => {
      const saved = await api(create.path, {method: "POST", body: JSON.stringify(create.body)});
      return api(test(saved.id), {method: "POST", body: "{}"});
    }, {failure: false});
    form.reset();
    form.closest("dialog").close();
    connectionHealthToast(result.health);
    await loadDataConnections();
  } catch (failure) { error.textContent = failure.message; }
}
document.querySelector("#feature-store-form").addEventListener("submit", event => {
  event.preventDefault();
  const form = event.target, data = Object.fromEntries(new FormData(form));
  const config = {url: data.url};
  if (data.feature_services.trim()) config.feature_services = data.feature_services.trim();
  saveAndTest(form, document.querySelector("#feature-store-error"), {path: "/api/v1/admin/feature-stores", body: {provider: data.provider, name: data.name, config, secret_ref: data.secret_ref.trim(), allowed_projects: csv(data.allowed_projects)}},
    id => `/api/v1/admin/feature-stores/${encodeURIComponent(id)}/test`, event.submitter);
});
document.querySelector("#storage-connection-form").addEventListener("submit", event => {
  event.preventDefault();
  const form = event.target, data = Object.fromEntries(new FormData(form));
  saveAndTest(form, document.querySelector("#storage-connection-error"), {path: "/api/v1/admin/storage-connections", body: {name: data.name, endpoint: data.endpoint, region: data.region, bucket: data.bucket, path_style: form.elements.path_style.checked, ca_bundle: data.ca_bundle, secret_ref: data.secret_ref.trim(), allowed_projects: csv(data.allowed_projects)}},
    id => `/api/v1/admin/storage-connections/${encodeURIComponent(id)}/test`, event.submitter);
});

document.querySelector("#add-connection").addEventListener("click", () => { document.querySelector("#connection-error").textContent = ""; document.querySelector("#connection-dialog").showModal(); });
document.querySelector("#connection-form").addEventListener("submit", async event => {
  event.preventDefault(); const error = document.querySelector("#connection-error"); error.textContent = "";
  try {
    const checked = await withBusy(event.submitter, async () => {
      const connection = await api("/api/v1/connections", {method: "POST", body: JSON.stringify(Object.fromEntries(new FormData(event.target)))});
      return api(`/api/v1/connections/${encodeURIComponent(connection.id)}/test`, {method: "POST", body: "{}"});
    }, {failure: false});
    event.target.reset(); document.querySelector("#connection-dialog").close(); await loadComponents();
    feedback(checked.status === "healthy" ? "Connection verified. Choose Use for new operations to activate a runtime." : `Connection saved, but its check failed: ${checked.message}`, checked.status !== "healthy");
  } catch (failure) { error.textContent = failure.message; }
});
onClick("[data-connection-activate]", async node => {
  await withBusy(node, () => api(`/api/v1/connections/${encodeURIComponent(node.dataset.connectionActivate)}/activate`, {method: "POST", body: "{}"}), {success: "Runtime activated for new operations."});
  await loadComponents();
});
onClick("[data-connection-test]", async node => {
  const result = await withBusy(node, () => api(`/api/v1/connections/${encodeURIComponent(node.dataset.connectionTest)}/test`, {method: "POST", body: "{}"}));
  toast(result.status === "healthy" ? "Connection is healthy." : `Check failed: ${result.message}`, result.status === "healthy" ? "info" : "error");
  await loadComponents();
});
onClick("[data-component-detail]", node => {
  const item = componentCache[Number(node.dataset.componentDetail)];
  if (!item) return;
  const actions = can("connections_write") ? `<div class="form-actions"><button class="primary" type="button" data-configure-component="${escapeHTML(item.name)}">Configure connection</button></div>` : "";
  showMetadata("PLATFORM COMPONENT", item.name, item, `${metaList([["Category", escapeHTML(item.category)], ["Status", status(item.status)], ["Description", escapeHTML(item.description)]])}${actions}`);
});
onClick("[data-configure-component]", node => {
  const presets = {
    "API Gateway": {type: "kubernetes", endpoint: `${location.origin}/api/v1/health`},
    "Pipeline Engine": {type: "prefect", endpoint: "http://prefect-server:4200/api"},
    "Experiment Tracker": {type: "mlflow", endpoint: "http://mlflow:5000/health"},
    "Feature Store": {type: "redis", endpoint: "http://feature-gateway:8083/healthz"},
    "Object Store": {type: "rustfs", endpoint: "http://objectstore:9000/health"},
    "Inference Engine": {type: "kubernetes", endpoint: "http://serving-manager:8085/healthz"},
    "Agent Observability": {type: "langfuse", endpoint: "http://langfuse:3000/api/public/health"},
    "Streaming Broker": {type: "kafka", endpoint: "http://kafka-rest:8082"},
    Kubernetes: {type: "kubernetes", endpoint: "https://kubernetes.default.svc/version"},
  };
  const name = node.dataset.configureComponent;
  const preset = presets[name] || {type: "kubernetes", endpoint: ""};
  const form = document.querySelector("#connection-form");
  form.elements.type.value = preset.type;
  form.elements.name.value = name.toLowerCase().replaceAll(" ", "-");
  form.elements.endpoint.value = preset.endpoint;
  form.elements.secret_ref.value = "none";
  document.querySelector("#metadata-dialog").close();
  document.querySelector("#connection-dialog").showModal();
});

registerView("platform", loadComponents, {
  eyebrow: "HONEST INFRASTRUCTURE", title: "Platform",
  intro: {
    what: "Every engine Kionga depends on, with an active health check rather than a static badge.",
    why: "Know exactly which capabilities work right now and what to configure next.",
    needs: "The platform service. Adding connections needs administrator or operator access.",
    action: "Add connection saves an endpoint and tests it immediately. Runtimes can then be activated for new operations.",
    results: "Each card shows the last check result and its error message.",
  },
});
