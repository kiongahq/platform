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
}
function renderLogExport(exporter) {
  const labels = {not_deployed: "Not deployed", not_configured: "Not configured", configured: "Configured", healthy: "Healthy", degraded: "Degraded", unavailable: "Unavailable"};
  const facts = exporter.target ? metaList([["Target", escapeHTML(exporter.target)], ["Destination", `<code>${escapeHTML(exporter.destination || "")}</code>`], ["Tenant", escapeHTML(exporter.tenant || "")], ["Exported", String(exporter.exported ?? 0)], ["Dead-lettered", String(exporter.dead_lettered ?? 0)], ["Last success", exporter.last_success && !exporter.last_success.startsWith("0001") ? timeTag(exporter.last_success) : "Never"], exporter.last_error ? ["Last error", escapeHTML(exporter.last_error)] : null]) : "";
  document.querySelector("#log-export-status").innerHTML = `<div class="panel-heading"><div><p class="eyebrow">OBSERVABILITY</p><h3>Log export</h3><p>Ships pipeline logs and platform events to Elasticsearch or OpenSearch.</p></div>${status(String(labels[exporter.state] ? exporter.state : "unavailable"))}</div>${exporter.detail ? `<p class="field-help">${escapeHTML(exporter.detail)}</p>` : ""}${facts}`;
}

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
    "Object Store": {type: "s3", endpoint: "http://minio:9000/minio/health/live"},
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
