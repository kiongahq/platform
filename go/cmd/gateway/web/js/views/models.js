/* Models: registry, promotion, serving and the inference console. */
let cachedModels = [];
function metricChart(models, metric) {
  const canvas = document.querySelector("#metric-chart");
  const context = canvas.getContext("2d");
  const styles = getComputedStyle(document.documentElement);
  const accent = styles.getPropertyValue("--accent").trim() || "#0066d6";
  const text = styles.getPropertyValue("--text").trim() || "#1d1d1f";
  const muted = styles.getPropertyValue("--text-tertiary").trim() || "#7c7c84";
  const points = models.filter(model => model.metrics && model.metrics[metric] !== undefined)
    .map(model => ({label: `${model.name} v${model.version}`, value: Number(model.metrics[metric])}));
  canvas.width = canvas.clientWidth * devicePixelRatio;
  canvas.height = 160 * devicePixelRatio;
  context.scale(devicePixelRatio, devicePixelRatio);
  context.clearRect(0, 0, canvas.clientWidth, 160);
  canvas.setAttribute("aria-label", points.length ? `${metric} by model version: ${points.map(point => `${point.label} ${point.value.toFixed(3)}`).join(", ")}` : "No metric data yet");
  if (!points.length) { context.fillStyle = muted; context.font = "13px -apple-system, sans-serif"; context.fillText("No metric data yet. Run a training pipeline to register a model.", 12, 80); return; }
  const max = Math.max(...points.map(point => point.value), 1);
  const barWidth = Math.max(18, Math.min(72, (canvas.clientWidth - 24) / points.length - 16));
  points.forEach((point, index) => {
    const x = 12 + index * (barWidth + 16);
    const height = (point.value / max) * 110;
    context.fillStyle = accent;
    context.beginPath(); context.roundRect(x, 128 - height, barWidth, height, 6); context.fill();
    context.fillStyle = text; context.font = "11px -apple-system, sans-serif";
    context.fillText(point.value.toFixed(3), x, 122 - height);
    context.fillStyle = muted;
    context.fillText(point.label.slice(0, Math.ceil(barWidth / 6)), x, 148);
  });
}

async function loadModels() {
  if (typeof refreshHubProjects === "function") refreshHubProjects();
  const data = await api("/api/v1/models");
  data.items = scoped(data.items || []);
  cachedModels = data.items;
  const metricNames = [...new Set(data.items.flatMap(model => Object.keys(model.metrics || {})))];
  const select = document.querySelector("#metric-select");
  const chosen = select.value || metricNames[0] || "";
  select.innerHTML = metricNames.length ? metricNames.map(name => `<option ${name === chosen ? "selected" : ""}>${escapeHTML(name)}</option>`).join("") : `<option value="">No metrics</option>`;
  select.disabled = !metricNames.length;
  metricChart(data.items, chosen);
  document.querySelector("#model-grid").innerHTML = data.items.length ? data.items.map(model => {
    const live = model.endpoint_url && model.endpoint_url.startsWith("http");
    const actions = model.artifact_uri?.startsWith("hf://")
      ? `<button type="button" data-hf-download="${escapeHTML(model.id)}">Download / notebook</button><span class="tag">Evaluate before serving</span>`
      : can("models_write")
      ? `<button type="button" data-model-action="promote" data-model-id="${escapeHTML(model.id)}">Promote</button><button type="button" data-model-action="deploy" data-model-id="${escapeHTML(model.id)}">Deploy</button><button type="button" data-model-action="rollback" data-model-id="${escapeHTML(model.id)}">Rollback</button>${live ? `<button type="button" class="primary" data-model-test="${escapeHTML(model.id)}">Test</button>` : ""}`
      : `<span class="tag">Read-only</span>`;
    return `<article class="card model-card interactive-card" data-model-detail="${escapeHTML(model.id)}"><span class="kind">${escapeHTML(model.stage)} · v${escapeHTML(model.version)}</span><h3><button type="button" class="card-title-button" data-model-detail="${escapeHTML(model.id)}">${escapeHTML(model.name)}</button></h3><p class="truncate" title="${escapeHTML(model.artifact_uri)}">${escapeHTML(model.artifact_uri)}</p><div class="metric-row"><span>Quality gate <b class="${model.gate_status === "passed" ? "good" : "bad"}">${escapeHTML(model.gate_status || "pending")}</b></span><span>Deployment <b>${escapeHTML(model.deployment_status || "not deployed")}</b></span></div><div class="tags">${Object.entries(model.metrics || {}).map(([key, value]) => `<span class="tag">${escapeHTML(key)} ${Number(value).toFixed(3)}</span>`).join("")}${live ? `<span class="tag live">● live</span>` : ""}</div><footer>${actions}</footer></article>`;
  }).join("") : emptyState("No models registered yet", "Run the training pipeline or register a Hugging Face model above.");
}

const predictState = {modelId: ""};
document.querySelector("#predict-form").addEventListener("submit", async event => {
  event.preventDefault();
  const output = document.querySelector("#predict-output");
  output.textContent = "";
  try {
    const body = document.querySelector("#predict-input").value;
    try { JSON.parse(body); } catch { throw new Error("Request payload must be valid JSON."); }
    const result = await withBusy(event.submitter, () => api(`/api/v1/models/${encodeURIComponent(predictState.modelId)}/predict`, {method: "POST", body}), {failure: false});
    output.textContent = JSON.stringify(result, null, 2);
  } catch (failure) { output.textContent = `Error: ${failure.message}`; }
});
document.querySelector("#metric-select").addEventListener("change", event => metricChart(cachedModels, event.target.value));
onClick("[data-model-test]", node => {
  predictState.modelId = node.dataset.modelTest;
  const model = cachedModels.find(item => item.id === node.dataset.modelTest);
  document.querySelector("#predict-model-name").textContent = model ? `${model.name} v${model.version}` : "Model";
  document.querySelector("#predict-output").textContent = "";
  document.querySelector("#predict-dialog").showModal();
});
onClick("[data-model-action]", async node => {
  const action = node.dataset.modelAction, id = encodeURIComponent(node.dataset.modelId);
  const requests = {
    promote: () => api(`/api/v1/models/${id}/promote`, {method: "POST", body: JSON.stringify({stage: "production"})}),
    deploy: () => api(`/api/v1/models/${id}/deploy`, {method: "POST", body: JSON.stringify({canary_weight: 0})}),
    rollback: () => api(`/api/v1/models/${id}/rollback`, {method: "POST", body: "{}"}),
  };
  await withBusy(node, requests[action], {success: `Model ${action} requested.`}).catch(() => {});
  await loadModels();
});
onClick("[data-model-detail]", node => {
  const item = cachedModels.find(model => model.id === node.dataset.modelDetail);
  if (!item) return;
  selectedResource = item.id; history.replaceState({}, "", routeURL("models", item.id));
  showMetadata("MODEL", `${item.name} v${item.version}`, item, metaList([["Model ID", copyable(item.id)], ["Project", escapeHTML(projectName(item.project_id))], ["Stage", status(item.stage)], ["Quality gate", status(item.gate_status || "pending")], ["Artifact", `<code>${escapeHTML(item.artifact_uri)}</code>`], ["Endpoint", item.endpoint_url ? `<code>${escapeHTML(item.endpoint_url)}</code>` : "Not deployed"], ["Registered", timeTag(item.created_at)]]));
}, {ignoreControls: true});

registerView("models", loadModels, {
  eyebrow: "MODEL LIFECYCLE", title: "Models",
  openResource: id => document.querySelector(`[data-model-detail="${CSS.escape(id)}"]`)?.click(),
  intro: {
    what: "The model registry backed by MLflow, with quality gates, promotion, serving and rollback.",
    why: "Promote only models that pass their gate, serve them behind one API, and roll back in one step.",
    needs: "The models service. Serving needs the serving manager; Hugging Face private models need a connected account.",
    action: "Deploy starts a live serving container; Test sends a request to it.",
    results: "Deployment status and endpoint appear on each card; prediction errors show in the test console.",
  },
});
