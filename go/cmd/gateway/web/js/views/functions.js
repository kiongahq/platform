/* Functions: OCI functions on OpenFaaS, their triggers and invocation. */
let functionCache = [];
let functionsConfigured = false;
function functionTrigger(fn) {
  const annotations = fn.annotations || {};
  if (annotations.schedule) return `Cron · ${annotations.schedule}`;
  if (annotations.topic) return `Kafka · ${annotations.topic}`;
  if (annotations["io.kionga.invocation"] === "async") return "Async queue";
  return "HTTP / webhook";
}
async function loadFunctionsCache() {
  const data = await api("/api/v1/functions");
  functionsConfigured = Boolean(data.configured);
  functionCache = scoped(data.items || []);
  return data;
}
async function loadFunctions() {
  const [data] = await Promise.all([loadFunctionsCache(), loadProjectOptions()]);
  const serving = functionCache.filter(item => item.status === "deployed").length;
  const replicas = functionCache.reduce((total, item) => total + Number(item.replicas || 0), 0);
  document.querySelector("#function-summary").innerHTML = `<article><span>Runtime</span><strong>${data.configured ? "Connected" : "Not configured"}</strong></article><article><span>Functions</span><strong>${functionCache.length}</strong></article><article><span>Serving</span><strong>${serving}</strong></article><article><span>Replicas</span><strong>${replicas}</strong></article>`;
  const deploy = document.querySelector("#deploy-function");
  if (!data.configured && can("functions_write")) { deploy.disabled = true; deploy.title = "OpenFaaS is not configured. Set OPENFAAS_URL on the gateway or add an OpenFaaS connection in Platform."; }
  document.querySelector("#functions-grid").innerHTML = functionCache.length ? functionCache.map(fn => `<article class="card function-card"><span class="kind">${escapeHTML(projectName(fn.project_id) || "unmanaged")} · ${escapeHTML(fn.status)}</span><h3>${escapeHTML(fn.name)}</h3><code title="${escapeHTML(fn.image)}">${escapeHTML(fn.image)}</code><div class="function-resources"><span>${escapeHTML(fn.cpu || "default CPU")}</span><span>${escapeHTML(fn.memory || "default memory")}</span><span>${Number(fn.replicas || 0)} replicas</span><span>${escapeHTML(functionTrigger(fn))}</span></div><footer>${can("functions_write") ? `<button type="button" data-function-invoke="${escapeHTML(fn.name)}" data-function-async="${fn.annotations?.["io.kionga.invocation"] === "async"}">Invoke</button>` : ""}${can("functions_write") && fn.project_id ? `<button type="button" class="danger" data-function-delete="${escapeHTML(fn.name)}">Remove</button>` : ""}</footer></article>`).join("")
    : emptyState(data.configured ? "No functions deployed" : "Serverless runtime not configured", data.configured ? "Deploy an OCI image to run it on demand, on a schedule, from Kafka, or as a pipeline step." : "Functions run on OpenFaaS. An administrator sets <code>OPENFAAS_URL</code> or adds an OpenFaaS connection on the Platform page.", !data.configured && hasService("platform") ? `<button type="button" data-navigate="platform">Open Platform</button>` : "");
}

document.querySelector("#deploy-function").addEventListener("click", async () => {
  await loadProjectOptions(); const form = document.querySelector("#deploy-function-form"); form.reset();
  form.elements.cpu.value = "500m"; form.elements.memory.value = "512Mi"; form.elements.env_vars.value = "{}";
  updateFunctionTrigger("http");
  document.querySelector("#deploy-function-error").textContent = ""; document.querySelector("#deploy-function-dialog").showModal();
});
function updateFunctionTrigger(type) {
  const settings = {
    http: [false, "Source", "", "Invoke synchronously from HTTP, a webhook, the SDK, or a pipeline."],
    async: [false, "Source", "", "Queue work immediately and receive an OpenFaaS call ID."],
    cron: [true, "Cron schedule", "*/5 * * * *", "Five-field cron expression; the OpenFaaS cron connector invokes this function."],
    kafka: [true, "Kafka topic", "events.created", "The OpenFaaS Kafka connector forwards every message on this topic."],
  }[type] || [false, "Source", "", ""];
  const source = document.querySelector("#function-trigger-source");
  document.querySelector("#function-trigger-source-label").hidden = !settings[0];
  document.querySelector("#function-trigger-source-title").textContent = settings[1];
  source.placeholder = settings[2]; source.required = settings[0];
  document.querySelector("#function-trigger-help").textContent = settings[3];
}
document.querySelector("#function-trigger-type").addEventListener("change", event => updateFunctionTrigger(event.target.value));
document.querySelector("#deploy-function-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.target, error = document.querySelector("#deploy-function-error"); error.textContent = "";
  try {
    const payload = Object.fromEntries(new FormData(form));
    try { payload.env_vars = JSON.parse(payload.env_vars || "{}"); } catch { throw new Error("Environment must be a JSON object."); }
    payload.annotations = {"io.kionga.invocation": payload.trigger_type === "async" ? "async" : "sync"};
    if (payload.trigger_type === "cron") Object.assign(payload.annotations, {topic: "cron-function", schedule: payload.trigger_source});
    if (payload.trigger_type === "kafka") payload.annotations.topic = payload.trigger_source;
    delete payload.trigger_type; delete payload.trigger_source;
    await withBusy(event.submitter, () => api("/api/v1/functions", {method: "POST", body: JSON.stringify(payload)}), {failure: false});
    document.querySelector("#deploy-function-dialog").close(); toast("Function deployed."); await loadFunctions();
  } catch (failure) { error.textContent = failure.message; }
});

const functionState = {name: "", async: false};
document.querySelector("#function-form").addEventListener("submit", async event => {
  event.preventDefault();
  const error = document.querySelector("#function-error"), output = document.querySelector("#function-output");
  error.textContent = ""; output.textContent = "";
  try {
    const payload = document.querySelector("#function-payload").value;
    try { JSON.parse(payload); } catch { throw new Error("Payload must be valid JSON."); }
    const route = functionState.async ? "invoke-async" : "invoke";
    const result = await withBusy(event.submitter, () => api(`/api/v1/functions/${encodeURIComponent(functionState.name)}/${route}`, {method: "POST", body: payload}), {failure: false});
    output.textContent = JSON.stringify(result, null, 2);
    toast(functionState.async ? "Function invocation queued." : "Function invocation completed.");
  } catch (failure) { error.textContent = failure.message; }
});
onClick("[data-function-invoke]", node => {
  functionState.name = node.dataset.functionInvoke;
  functionState.async = node.dataset.functionAsync === "true";
  document.querySelector("#function-name").textContent = `${functionState.async ? "Queue" : "Invoke"} ${functionState.name}`;
  document.querySelector("#function-payload").value = "{}";
  document.querySelector("#function-error").textContent = "";
  document.querySelector("#function-output").textContent = "";
  document.querySelector("#function-dialog").showModal();
});
onClick("[data-function-delete]", async node => {
  if (!confirm(`Remove ${node.dataset.functionDelete}? Pipelines referencing it will no longer run.`)) return;
  await withBusy(node, () => api(`/api/v1/functions/${encodeURIComponent(node.dataset.functionDelete)}`, {method: "DELETE"}), {success: "Function removed."});
  await loadFunctions();
});

registerView("functions", loadFunctions, {
  eyebrow: "EVENT-DRIVEN COMPUTE", title: "Functions",
  intro: {
    what: "Small OCI-packaged jobs deployed to OpenFaaS with explicit CPU and memory.",
    why: "Run work on demand, on a schedule or from Kafka events, and reuse it as a pipeline step.",
    needs: "A configured OpenFaaS runtime, the functions service, and function quota.",
    action: "Deploy function registers the image with OpenFaaS. Invoke calls it and shows the response.",
    results: "Invocation output appears in the invoke dialog; deployment errors come back from OpenFaaS verbatim.",
  },
});
