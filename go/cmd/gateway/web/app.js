const api = async (path, options = {}) => {
  const response = await fetch(path, {headers: {"Accept": "application/json", "Content-Type": "application/json"}, ...options});
  const contentType = response.headers.get("content-type") || "";
  const body = contentType.includes("application/json") ? await response.json() : {message: await response.text()};
  if (!response.ok) throw new Error(body.message || body.error || "Something went wrong");
  return body;
};

const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, char => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[char]));
const when = value => new Intl.RelativeTimeFormat("en", {numeric: "auto"}).format(Math.round((new Date(value) - Date.now()) / 60000), "minute");
const status = value => `<span class="status ${escapeHTML(value)}">${escapeHTML(String(value).replace("_", " "))}</span>`;
const toast = message => { const node = document.querySelector("#toast"); node.textContent = message; node.classList.add("show"); setTimeout(() => node.classList.remove("show"), 2400); };
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
const bytes = size => size > 1048576 ? `${(size/1048576).toFixed(1)} MB` : size > 1024 ? `${(size/1024).toFixed(1)} KB` : `${size} B`;
const dateTime = value => value ? new Date(value).toLocaleString() : "—";
const metadataValue = value => {
  if (value === undefined || value === null || value === "") return "—";
  if (typeof value === "object") return `<pre class="hint metadata-json">${escapeHTML(JSON.stringify(value, null, 2))}</pre>`;
  return escapeHTML(value);
};
function showMetadata(kind, title, item, actions = "") {
  const rows = Object.entries(item).map(([key, value]) =>
    `<tr><th>${escapeHTML(key.replaceAll("_", " "))}</th><td>${metadataValue(value)}</td></tr>`
  ).join("");
  document.querySelector("#metadata-detail").innerHTML = `<p class="eyebrow">${escapeHTML(kind.toUpperCase())}</p><h2>${escapeHTML(title)}</h2><table class="metadata-table"><tbody>${rows}</tbody></table>${actions}`;
  document.querySelector("#metadata-dialog").showModal();
}

// ---- identity & permissions -------------------------------------------------
// The gateway's /api/v1/me is the single source of truth: buttons the caller's
// role cannot use are disabled here, and the API enforces the same table.
let me = {subject: "", email: "", roles: [], services: [], mode: "local", permissions: {}, entitlements: null};
const can = key => me.permissions[key] !== false;
const isAdmin = () => me.roles.includes("admin") || me.roles.includes("operator");
const hasService = service => isAdmin() || !me.roles.includes("user") || me.services.includes(service);

function applyPermissions() {
  document.querySelectorAll("[data-perm]").forEach(button => {
    const allowed = can(button.dataset.perm);
    button.disabled = !allowed;
    button.title = allowed ? "" : `Not available to role: ${me.roles.join(", ") || "unknown"}`;
  });
  const name = me.email || me.subject || "anonymous";
  document.querySelector("#user-name").textContent = name;
  document.querySelector("#user-role").textContent = `${me.roles.join(", ") || "no role"} · ${me.mode} mode`;
  document.querySelector("#menu-user").textContent = name;
  document.querySelector("#account-avatar").textContent = name.trim().charAt(0).toUpperCase() || "U";
  document.querySelectorAll(".logout-action").forEach(link => { link.hidden = false; });
  document.querySelectorAll("[data-admin-only]").forEach(node => { node.hidden = !isAdmin(); });
  document.querySelectorAll(".nav-item[data-view]").forEach(node => {
    if (!["access", "profile", "settings"].includes(node.dataset.view)) node.hidden = !hasService(node.dataset.view);
  });
  document.querySelector("#workbench-link").hidden = !hasService("workbench");
  document.querySelector("#ide-link").hidden = !hasService("ide");
}

async function loadMe() {
  try { me = {...me, ...await api("/api/v1/me")}; }
  catch { me.permissions = {}; } // gateway still enforces; keep controls visible
  applyPermissions();
}

let activeView = "overview";
const viewLoaders = {};

function showView(id) {
  if (!["access", "profile", "settings"].includes(id) && !hasService(id)) { toast("This service has not been assigned to you."); return; }
  if (["access","blogs"].includes(id) && !isAdmin()) { toast("Administrator access is required."); return; }
  activeView = id;
  document.querySelectorAll(".view").forEach(node => node.classList.toggle("active", node.id === id));
  document.querySelectorAll(".nav-item").forEach(node => node.classList.toggle("active", node.dataset.view === id));
  const labels = {overview:"Good morning, builder.", projects:"Build with a clear starting point.", pipelines:"Every run, made legible.", functions:"Small jobs, composed into serious systems.", models:"Promote with confidence.", agents:"Understand every agent turn.", features:"One source of truth for features.", storage:"Artifacts and live endpoints.", realtime:"Score events as they happen.", catalog:"Reuse what your team knows.", platform:"Connect the production pieces.", profile:"Know exactly what you can use.", settings:"Your account, tools, and preferences.", blogs:"Write what the engineering team learns.", access:"Provision only what people need."};
  document.querySelector("#page-title").textContent = labels[id] || "Workspace";
  if (viewLoaders[id]) viewLoaders[id]().catch(error => toast(error.message));
}

async function loadDashboard() {
  const data = await api("/api/v1/dashboard");
  document.querySelector("#stat-projects").textContent = data.projects;
  document.querySelector("#stat-runs").textContent = data.active_runs;
  document.querySelector("#stat-health").textContent = `${data.healthy_components}/${data.total_components}`;
  document.querySelector("#progress-ring").style.background = `conic-gradient(#0071e3 ${data.onboarding_percent}%,rgba(118,118,128,.15) 0)`;
  document.querySelector("#progress-ring strong").textContent = `${data.onboarding_percent}%`;
  document.querySelector("#recent-runs").innerHTML = data.recent_runs.length ? data.recent_runs.map(run => `<div class="run-row"><span class="run-icon">↯</span><div><b>${escapeHTML(run.name)}</b><small>${when(run.created_at)}</small></div>${status(run.status)}</div>`).join("") : `<p class="empty">No runs yet.</p>`;
}

let projectCache = [];
async function loadProjects() {
  const projects = await api("/api/v1/projects");
  projectCache = projects;
  document.querySelector("#project-grid").innerHTML = projects.length ? projects.map(project => `<article class="card interactive-card" role="button" tabindex="0" data-project-detail="${escapeHTML(project.id)}"><span class="kind">${escapeHTML(project.template)}${project.template_version ? ` · v${escapeHTML(project.template_version)}` : ""}</span><h3>${escapeHTML(project.name)}</h3><p>${escapeHTML(project.description || "No description yet.")}</p><div class="tags">${project.framework ? `<span class="tag">${escapeHTML(project.framework)}</span>` : ""}${project.accelerator ? `<span class="tag">${escapeHTML(project.accelerator)}</span>` : ""}${project.requested_profile ? `<span class="tag">${escapeHTML(project.requested_profile)} profile</span>` : ""}</div>${project.repository ? `<div class="repository-badge"><span>⌘</span><b>${escapeHTML(project.repository.provider)}</b><small>${escapeHTML(project.repository.default_branch)}</small></div>` : `<div class="repository-badge unbound"><span>＋</span><b>Connect Git</b></div>`}<footer><span class="tag">${escapeHTML(project.namespace)}</span>${status(project.status)}</footer></article>`).join("") : `<p class="empty">No projects yet — create one to begin.</p>`;
  const select = document.querySelector("#submit-project");
  select.innerHTML = projects.map(project => `<option value="${escapeHTML(project.id)}">${escapeHTML(project.name)}</option>`).join("");
  document.querySelector("#function-project").innerHTML = select.innerHTML;
  document.querySelector("#definition-project").innerHTML = select.innerHTML;
  document.querySelector("#agent-project").innerHTML = select.innerHTML;
  return projects;
}

let pipelineDefinitionCache = [];
const pipelineGraph = (jobs, options = {}) => window.KiongaPipelineGraph.render(jobs, options);
async function loadRuns() {
  const [runs, definitions] = await Promise.all([api("/api/v1/pipelines/runs"), api("/api/v1/pipelines/definitions")]);
  pipelineDefinitionCache = definitions.items || [];
  document.querySelector("#pipeline-definition-grid").innerHTML = pipelineDefinitionCache.length ? pipelineDefinitionCache.map(definition => `<article class="panel pipeline-definition-card" role="button" tabindex="0" data-definition-detail="${escapeHTML(definition.id)}"><div><span class="kind">${escapeHTML(definition.execution_mode)} · v${escapeHTML(definition.version)}</span><h3>${escapeHTML(definition.name)}</h3><p>${definition.jobs.length} jobs · ${escapeHTML(definition.project_id)}</p></div><button data-run-definition="${escapeHTML(definition.id)}" data-project-id="${escapeHTML(definition.project_id)}">Run</button><div class="definition-graph">${pipelineGraph(definition.jobs, {compact:true, ariaLabel:`${definition.name} dependency graph`})}</div></article>`).join("") : `<article class="panel empty-state"><b>No reusable flows yet</b><span>Define a flow from container jobs or deployed functions.</span></article>`;
  document.querySelector("#submit-definition").innerHTML = `<option value="">Built-in training pipeline</option>${pipelineDefinitionCache.map(definition => `<option value="${escapeHTML(definition.id)}" data-project="${escapeHTML(definition.project_id)}">${escapeHTML(definition.name)} · v${escapeHTML(definition.version)} · ${escapeHTML(definition.execution_mode)}</option>`).join("")}`;
  document.querySelector("#run-table").innerHTML = runs.length ? runs.map(run => `<tr class="clickable" data-run-id="${escapeHTML(run.id)}"><td><b>${escapeHTML(run.name)}</b><br><small>${escapeHTML(run.id)}</small></td><td>${escapeHTML(run.project_id)}</td><td>${status(run.status)}</td><td><div class="bar"><i style="width:${Number(run.progress)}%"></i></div></td><td>${when(run.created_at)}</td></tr>`).join("") : `<tr><td colspan="5" class="empty">No runs yet — submit one.</td></tr>`;
}

let openRunID = "";
let runDetailRequest = 0;
function renderRunDetail(run) {
  const logs = (run.logs || []).map(log => `<div class="log-line"><time>${new Date(log.timestamp).toLocaleTimeString()}</time><b>${escapeHTML(log.step || "system")}</b><span>${escapeHTML(log.message)}</span></div>`).join("") || `<p class="empty">No logs have arrived yet.</p>`;
  const engine = run.engine_run_id ? `<span class="tag">engine ${escapeHTML(run.engine_run_id)}</span>` : "";
  const active = run.status === "queued" || run.status === "running";
  const runActions = can("pipelines_write") ? `<div class="sheet-actions">${active ? `<button data-run-action="cancel" data-run-id="${escapeHTML(run.id)}">Cancel</button>` : ""}<button class="primary" data-run-action="retry" data-run-id="${escapeHTML(run.id)}">Retry run</button></div>` : "";
  document.querySelector("#run-detail").innerHTML = `<p class="eyebrow">PIPELINE RUN</p><h2>${escapeHTML(run.name)}</h2><div class="detail-meta">${status(run.status)}<span>${escapeHTML(run.id)}</span><span>${when(run.created_at)}</span>${engine}<span>${Number(run.progress) || 0}% complete</span></div><h3>Execution graph</h3>${pipelineGraph(run.steps, {ariaLabel:`${run.name} execution graph`})}<h3>Logs</h3><div class="logs">${logs}</div>${runActions}`;
}
async function showRun(runId, options = {}) {
  const dialog = document.querySelector("#run-dialog");
  const shouldOpen = options.open !== false;
  if (shouldOpen) {
    openRunID = runId;
    document.querySelector("#run-detail").innerHTML = `<p class="empty">Loading pipeline run…</p>`;
    if (!dialog.open) dialog.showModal();
  }
  const request = ++runDetailRequest;
  const run = await api(`/api/v1/pipelines/runs/${encodeURIComponent(runId)}`);
  if (request !== runDetailRequest || openRunID !== runId || !dialog.open) return;
  renderRunDetail(run);
}
document.querySelector("#run-dialog").addEventListener("close", () => { openRunID = ""; runDetailRequest++; });

// metricChart draws a per-version bar chart for the selected metric.
function metricChart(models, metric) {
  const canvas = document.querySelector("#metric-chart");
  const context = canvas.getContext("2d");
  const points = models.filter(model => model.metrics && model.metrics[metric] !== undefined)
    .map(model => ({label: `${model.name} v${model.version}`, value: Number(model.metrics[metric])}));
  canvas.width = canvas.clientWidth * devicePixelRatio;
  canvas.height = 160 * devicePixelRatio;
  context.scale(devicePixelRatio, devicePixelRatio);
  context.clearRect(0, 0, canvas.clientWidth, 160);
  if (!points.length) { context.fillStyle = "#8e8e93"; context.font = "13px -apple-system, sans-serif"; context.fillText("No metric data yet — run a training pipeline.", 12, 80); return; }
  const max = Math.max(...points.map(point => point.value), 1);
  const barWidth = Math.min(72, (canvas.clientWidth - 24) / points.length - 16);
  points.forEach((point, index) => {
    const x = 12 + index * (barWidth + 16);
    const height = (point.value / max) * 110;
    context.fillStyle = "#0071e3";
    context.beginPath(); context.roundRect(x, 128 - height, barWidth, height, 6); context.fill();
    context.fillStyle = "#1d1d1f"; context.font = "11px -apple-system, sans-serif";
    context.fillText(point.value.toFixed(3), x, 122 - height);
    context.fillStyle = "#8e8e93";
    context.fillText(point.label.slice(0, Math.ceil(barWidth / 6)), x, 148);
  });
}

let cachedModels = [];
async function loadModels() {
  const data = await api("/api/v1/models");
  data.items = data.items || [];
  cachedModels = data.items;
  const metricNames = [...new Set(data.items.flatMap(model => Object.keys(model.metrics || {})))];
  const select = document.querySelector("#metric-select");
  const chosen = select.value || metricNames[0] || "";
  select.innerHTML = metricNames.map(name => `<option ${name === chosen ? "selected" : ""}>${escapeHTML(name)}</option>`).join("");
  metricChart(data.items, chosen);
  document.querySelector("#model-grid").innerHTML = data.items.length ? data.items.map(model => {
    const live = model.endpoint_url && model.endpoint_url.startsWith("http");
    const actions = can("models_write")
      ? `<button data-model-action="promote" data-model-id="${escapeHTML(model.id)}">Promote</button><button data-model-action="deploy" data-model-id="${escapeHTML(model.id)}">Deploy</button><button data-model-action="rollback" data-model-id="${escapeHTML(model.id)}">Rollback</button>${live ? `<button class="primary" data-model-test="${escapeHTML(model.id)}">Test</button>` : ""}`
      : `<span class="tag">read-only</span>`;
    return `<article class="card model-card interactive-card" role="button" tabindex="0" data-model-detail="${escapeHTML(model.id)}"><span class="kind">${escapeHTML(model.stage)} · v${escapeHTML(model.version)}</span><h3>${escapeHTML(model.name)}</h3><p>${escapeHTML(model.artifact_uri)}</p><div class="metric-row"><span>Quality gate <b class="${model.gate_status === "passed" ? "good" : "bad"}">${escapeHTML(model.gate_status || "pending")}</b></span><span>Deployment <b>${escapeHTML(model.deployment_status || "not deployed")}</b></span></div><div class="tags">${Object.entries(model.metrics || {}).map(([key,value]) => `<span class="tag">${escapeHTML(key)} ${Number(value).toFixed(3)}</span>`).join("")}${live ? `<span class="tag live">● live</span>` : ""}</div><footer>${actions}</footer></article>`;
  }).join("") : `<p class="empty">No models registered yet — run the training pipeline.</p>`;
}

let agentCache = [];
async function loadAgents() {
  const [data, prompts] = await Promise.all([api("/api/v1/agents"), api("/api/v1/prompts")]);
  agentCache = data.items || [];
  const sessionGroups = await Promise.all(data.items.map(agent => api(`/api/v1/agents/${encodeURIComponent(agent.id)}/sessions`)));
  const sessions = sessionGroups.flatMap(group => group.items);
  const tokens = sessions.reduce((sum, item) => sum + item.input_tokens + item.output_tokens, 0);
  const cost = sessions.reduce((sum, item) => sum + item.cost_usd, 0);
  const readyAgents = data.items.filter(agent => agent.status === "ready").length;
  document.querySelector("#agent-summary").innerHTML = `<article><span>Agent versions</span><strong>${data.total}</strong><small>${readyAgents} runtime${readyAgents === 1 ? "" : "s"} ready</small></article><article><span>Active sessions</span><strong>${sessions.filter(item => item.status === "running").length}</strong><small>${sessions.length} total sessions</small></article><article><span>LLM cost</span><strong>$${cost.toFixed(4)}</strong><small>${tokens.toLocaleString()} tokens</small></article>`;
  document.querySelector("#agent-grid").innerHTML = data.items.length ? data.items.map(agent => {
    const ready = agent.status === "ready";
    const actions = can("agents_write") ? `<button data-agent-traffic="${escapeHTML(agent.id)}">Traffic</button><button class="primary" data-agent-chat="${escapeHTML(agent.id)}" data-agent-name="${escapeHTML(agent.name)}" ${ready ? "" : "disabled title=\"Runtime health check has not passed\""}>Chat</button>` : `<span class="tag">read-only</span>`;
    const resources = agent.resources || {};
    const scaling = agent.autoscaling || {min_replicas:agent.replicas || 1,max_replicas:agent.replicas || 1};
    return `<article class="card interactive-card" role="button" tabindex="0" data-agent-detail="${escapeHTML(agent.id)}"><span class="kind">${escapeHTML(agent.llm_backend)} · v${escapeHTML(agent.version)}</span><h3>${escapeHTML(agent.name)}</h3><p>${escapeHTML(agent.graph_module)}</p><div class="tags"><span class="tag">${escapeHTML(resources.cpu || "500m")} CPU</span><span class="tag">${escapeHTML(resources.memory || "1Gi")} RAM</span><span class="tag">${scaling.min_replicas}–${scaling.max_replicas} replicas</span>${resources.gpu ? `<span class="tag">${resources.gpu} × ${escapeHTML(resources.gpu_type)}</span>` : ""}${(agent.tools || []).map(tool => `<span class="tag">${escapeHTML(tool)}</span>`).join("")}</div><footer>${status(agent.status)}<span class="tag">${agent.canary_weight}% canary</span>${actions}</footer></article>`;
  }).join("") : `<p class="empty">No agents deployed yet.</p>`;
  document.querySelector("#session-table").innerHTML = sessions.length ? sessions.map(session => `<tr><td>${escapeHTML(session.id)}</td><td>${escapeHTML(session.agent_id)}</td><td>${escapeHTML(session.current_node)}</td><td>${status(session.status)}</td><td>${session.turns}</td><td>${(session.input_tokens + session.output_tokens).toLocaleString()}</td><td>$${session.cost_usd.toFixed(4)}</td></tr>`).join("") : `<tr><td colspan="7" class="empty">No sessions yet — chat with an agent.</td></tr>`;
  document.querySelector("#prompt-list").innerHTML = prompts.configured
    ? (prompts.items.length ? prompts.items.map(prompt => `<div class="prompt-row"><b>${escapeHTML(prompt.name)}</b><span class="tag">v${escapeHTML(prompt.version ?? "?")}</span>${(prompt.labels || []).map(label => `<span class="tag">${escapeHTML(label)}</span>`).join("")}</div>`).join("") : `<p class="empty">Langfuse is connected — no prompts stored yet.</p>`)
    : `<p class="empty">Langfuse not configured. Prompts appear here once connected.</p>`;
}

let featureCache = [];
function renderFeatures(query = "") {
  const lowered = query.toLowerCase();
  const filtered = featureCache.filter(view => !lowered || view.name.toLowerCase().includes(lowered) || (view.tags || []).some(tag => tag.toLowerCase().includes(lowered)));
  document.querySelector("#feature-grid").innerHTML = filtered.length ? filtered.map(view => `<article class="card feature-card interactive-card" role="button" tabindex="0" data-feature-detail="${escapeHTML(view.id)}"><span class="kind">entity: ${escapeHTML(view.entity)}</span><h3>${escapeHTML(view.name)}</h3><table class="schema"><thead><tr><th>Field</th><th>Type</th></tr></thead><tbody>${(view.fields || []).map(field => `<tr><td>${escapeHTML(field.name)}</td><td>${escapeHTML(field.type)}</td></tr>`).join("")}</tbody></table><div class="tags">${(view.tags || []).map(tag => `<span class="tag">${escapeHTML(tag)}</span>`).join("")}${view.ttl_seconds ? `<span class="tag">TTL ${view.ttl_seconds}s</span>` : ""}</div><footer>${status(view.status)}<span class="tag">${view.online_entity_count || 0} entities online</span>${view.materialized_at ? `<small>${when(view.materialized_at)}</small>` : ""}</footer></article>`).join("") : `<p class="empty">No feature views${query ? " match your search" : " applied yet — run the materializer"}.</p>`;
}
async function loadFeatures() {
  const data = await api("/api/v1/features");
  featureCache = data.items || [];
  renderFeatures(document.querySelector("#feature-search").value);
}

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
      browser.innerHTML = (data.buckets || []).map(bucket => `<button class="storage-row" data-bucket="${escapeHTML(bucket.name)}"><span>🪣</span><b>${escapeHTML(bucket.name)}</b></button>`).join("") || `<p class="empty">No buckets found.</p>`;
    } else {
      const data = await api(`/api/v1/storage/objects?bucket=${encodeURIComponent(storageState.bucket)}&prefix=${encodeURIComponent(storageState.prefix)}`);
      title.textContent = `${storageState.bucket}/${storageState.prefix}`;
      up.hidden = false;
      const prefixes = (data.prefixes || []).map(prefix => `<button class="storage-row" data-prefix="${escapeHTML(prefix)}"><span>📁</span><b>${escapeHTML(prefix.slice(storageState.prefix.length))}</b></button>`).join("");
      const objects = (data.objects || []).map(object => `<button class="storage-row" data-object="${escapeHTML(object.key)}"><span>📄</span><b>${escapeHTML(object.key.slice(storageState.prefix.length))}</b><small>${bytes(object.size)}</small></button>`).join("");
      browser.innerHTML = prefixes + objects || `<p class="empty">Empty prefix.</p>`;
    }
  } catch (error) {
    browser.innerHTML = `<p class="empty">Object store unavailable: ${escapeHTML(error.message)}</p>`;
  }
  const [models, functions] = await Promise.all([api("/api/v1/models"), api("/api/v1/functions")]);
  const live = models.items.filter(model => model.endpoint_url && model.endpoint_url.startsWith("http"));
  document.querySelector("#endpoint-list").innerHTML = live.length ? live.map(model => `<article class="endpoint-item"><div><h4>${escapeHTML(model.name)} v${escapeHTML(model.version)}</h4><div class="endpoint-meta">${status(model.deployment_status)}<span class="tag">${escapeHTML(model.stage)}</span></div></div>${can("models_write") ? `<button data-model-test="${escapeHTML(model.id)}">Test</button>` : ""}<code>${escapeHTML(model.endpoint_url)}</code></article>`).join("") : `<p class="empty">No live endpoints — deploy a gated model.</p>`;
  document.querySelector("#function-list").innerHTML = functions.configured
    ? ((functions.items || []).length ? functions.items.map(fn => `<article class="endpoint-item"><div><h4>${escapeHTML(fn.name)}</h4><div class="endpoint-meta"><span class="tag">${fn.replicas} replicas</span></div></div>${can("functions_write") ? `<button data-function-invoke="${escapeHTML(fn.name)}">Invoke</button>` : ""}<code>${escapeHTML(fn.image)}</code></article>`).join("") : `<p class="empty">OpenFaaS connected — no functions yet.</p>`)
    : `<p class="empty">Serverless not configured. Set <code>OPENFAAS_URL</code> to connect it.</p>`;
}

let functionCache = [];
function functionTrigger(fn) {
  const annotations = fn.annotations || {};
  if (annotations.schedule) return `Cron · ${annotations.schedule}`;
  if (annotations.topic) return `Kafka · ${annotations.topic}`;
  if (annotations["io.kionga.invocation"] === "async") return "Async queue";
  return "HTTP / webhook";
}
async function loadFunctions() {
  const [data] = await Promise.all([api("/api/v1/functions"), projectCache.length ? Promise.resolve(projectCache) : loadProjects()]);
  functionCache = data.items || [];
  const serving = functionCache.filter(item => item.status === "deployed").length;
  const replicas = functionCache.reduce((total, item) => total + Number(item.replicas || 0), 0);
  document.querySelector("#function-summary").innerHTML = `<article><span>Runtime</span><strong>${data.configured ? "Connected" : "Not configured"}</strong></article><article><span>Functions</span><strong>${functionCache.length}</strong></article><article><span>Serving</span><strong>${serving}</strong></article><article><span>Replicas</span><strong>${replicas}</strong></article>`;
  document.querySelector("#functions-grid").innerHTML = functionCache.length ? functionCache.map(fn => `<article class="card function-card"><span class="kind">${escapeHTML(fn.project_id || "unmanaged")} · ${escapeHTML(fn.status)}</span><h3>${escapeHTML(fn.name)}</h3><code>${escapeHTML(fn.image)}</code><div class="function-resources"><span>${escapeHTML(fn.cpu || "default CPU")}</span><span>${escapeHTML(fn.memory || "default memory")}</span><span>${Number(fn.replicas || 0)} replicas</span><span>${escapeHTML(functionTrigger(fn))}</span></div><footer><button data-function-invoke="${escapeHTML(fn.name)}" data-function-async="${fn.annotations?.["io.kionga.invocation"] === "async"}">Invoke</button>${can("functions_write") && fn.project_id ? `<button class="danger" data-function-delete="${escapeHTML(fn.name)}">Remove</button>` : ""}</footer></article>`).join("") : `<article class="panel empty-state"><b>No functions deployed</b><span>Connect OpenFaaS and deploy an OCI image to run it independently or inside a flow.</span></article>`;
}

let realtimeCache = [];
async function loadRealtime() {
  const data = await api("/api/v1/realtime");
  const demos = [
    {key: "fraud", title: "Fraud detection", detail: "transaction → features → model score → alert"},
    {key: "callcenter", title: "Call-center analysis", detail: "transcript → support agent → sentiment + intent"},
    {key: "recommendations", title: "Recommendations", detail: "activity → profile features → ranked items"},
  ];
  realtimeCache = demos.map(demo => ({...demo, stats: (data.demos || {})[demo.key] || null}));
  document.querySelector("#realtime-grid").innerHTML = demos.map(demo => {
    const stats = (data.demos || {})[demo.key];
    const body = stats
      ? `<div class="metric-row"><span>Events <b>${stats.events ?? 0}</b></span><span>Avg latency <b>${stats.avg_latency_ms ?? 0} ms</b></span>${demo.key === "fraud" ? `<span>Flagged <b class="bad">${stats.flagged ?? 0}</b></span>` : ""}</div><small>updated ${when(stats.updated_at)}</small>`
      : `<p class="empty">No events processed yet.</p>`;
    return `<article class="card interactive-card" role="button" tabindex="0" data-stream-detail="${demo.key}"><span class="kind">stream</span><h3>${demo.title}</h3><p>${demo.detail}</p>${body}</article>`;
  }).join("");
}

let catalogCache = [];
async function loadCatalog(kind = "") {
  const items = await api(`/api/v1/catalog${kind ? `?kind=${encodeURIComponent(kind)}` : ""}`);
  catalogCache = items;
  document.querySelector("#catalog-grid").innerHTML = items.length ? items.map((item, index) => `<article class="card interactive-card" role="button" tabindex="0" data-catalog-detail="${index}"><span class="kind">${escapeHTML(item.kind)} · ${escapeHTML(item.version)}</span><h3>${escapeHTML(item.name)}</h3><div class="tags">${item.metadata.map(meta => `<span class="tag">${escapeHTML(meta)}</span>`).join("")}</div><footer><span></span>${status(item.status)}</footer></article>`).join("") : `<p class="empty">The catalog fills up as you register models, features, agents and tools.</p>`;
}

let componentCache = [];
async function loadComponents() {
  const [items, readiness, connections] = await Promise.all([api("/api/v1/components"), api("/api/v1/onboarding/readiness"), api("/api/v1/connections")]);
  componentCache = items;
  document.querySelector("#component-grid").innerHTML = items.map((item, index) => `<article class="component interactive-card" role="button" tabindex="0" data-component-detail="${index}"><div><span class="category">${escapeHTML(item.category)}</span><h3>${escapeHTML(item.name)}</h3></div>${status(item.status)}<p>${escapeHTML(item.description)}</p></article>`).join("");
  document.querySelector("#readiness-percent").textContent = `${readiness.percent}%`;
  document.querySelector("#readiness-list").innerHTML = readiness.items.map(item => `<li class="${item.status === "ready" ? "done" : ""}"><span>${item.status === "ready" ? "✓" : "○"}</span><div><b>${escapeHTML(item.label)}</b><small>${escapeHTML(item.description)}</small></div></li>`).join("");
  document.querySelector("#connection-grid").innerHTML = connections.items.length ? connections.items.map(item => `<article class="card connection-card"><span class="kind">${escapeHTML(item.type)}</span><h3>${escapeHTML(item.name)}</h3><p>${escapeHTML(item.endpoint)}</p><footer>${status(item.status)}${can("connections_write") ? `<button data-connection-test="${escapeHTML(item.id)}">Test</button>` : ""}</footer>${item.message ? `<small>${escapeHTML(item.message)}</small>` : ""}</article>`).join("") : `<div class="empty-state"><b>No services connected</b><span>Add Kubernetes, MLflow, storage and Kafka to complete onboarding.</span></div>`;
}

const accessServices = ["overview","projects","pipelines","functions","models","agents","features","storage","realtime","catalog","platform","git","workbench","ide"];
let accessCache = [];
let resourceProfiles = [];
const csv = value => String(value || "").split(",").map(item => item.trim()).filter(Boolean);
function accessPayload(form) {
  const value = Object.fromEntries(new FormData(form));
  return {
    email:value.email, role:value.role,
    services:[...form.querySelectorAll("[name='services']:checked")].map(input => input.value),
    project_ids:csv(value.project_ids),
    storage:{size_gb:Number(value.storage_gb), buckets:csv(value.buckets)},
    compute:{profile:value.profile, vcpus:Number(value.vcpus), memory_gb:Number(value.memory_gb), gpus:Number(value.gpus), gpu_type:value.gpu_type, max_vms:Number(value.max_vms), max_projects:Number(value.max_projects), max_concurrent_runs:Number(value.max_runs), max_functions:Number(value.max_functions)},
    disabled:form.elements.disabled.checked,
  };
}

function setResourceProfile(name) {
  const form = document.querySelector("#access-form");
  const profile = resourceProfiles.find(item => item.name === name);
  const custom = name === "custom" || !profile;
  if (profile && !custom) {
    const values = {...profile.compute, storage_gb:profile.storage_gb};
    Object.entries(values).forEach(([key, value]) => { if (form.elements[key] && key !== "profile") form.elements[key].value = value ?? ""; });
  }
  document.querySelector("#custom-resource-fields").classList.toggle("preset-locked", !custom);
  document.querySelectorAll("#custom-resource-fields input").forEach(input => { input.readOnly = !custom; });
  document.querySelector("#resource-profile-description").textContent = profile?.description || "Set every boundary explicitly.";
  const compute = profile?.compute || {};
  document.querySelector("#resource-profile-summary").innerHTML = custom ? `<span>Custom allocation</span>` : `<span><b>${compute.vcpus}</b> vCPU</span><span><b>${compute.memory_gb}</b> GB memory</span><span><b>${profile.storage_gb}</b> GB storage</span><span><b>${compute.max_concurrent_runs}</b> runs</span><span><b>${compute.max_functions}</b> functions</span>${compute.gpus ? `<span><b>${compute.gpus}</b> GPU</span>` : ""}`;
}
async function loadAccess() {
  const [data, requests, profiles] = await Promise.all([api("/api/v1/admin/users"), api("/api/v1/admin/access-requests"), api("/api/v1/admin/resource-profiles")]);
  resourceProfiles = profiles.items || [];
  accessCache = data.items || [];
  document.querySelector("#access-table").innerHTML = accessCache.length ? accessCache.map(item => `<tr>
    <td><b>${escapeHTML(item.email || item.subject)}</b><br><small>${escapeHTML(item.subject)}</small></td>
    <td><span class="tag">${escapeHTML(item.role)}</span></td><td>${item.services.map(service => `<span class="tag">${escapeHTML(service)}</span>`).join(" ") || "None"}</td>
    <td><span class="tag">${escapeHTML(item.compute.profile || "custom")}</span> ${item.compute.vcpus} vCPU · ${item.compute.memory_gb} GB${item.compute.gpus ? ` · ${item.compute.gpus} GPU` : ""}<br><small>${item.compute.max_vms} VM · ${item.compute.max_projects} projects · ${item.compute.max_concurrent_runs} runs · ${item.compute.max_functions || 0} functions</small></td>
    <td>${item.storage.size_gb} GB<br><small>${(item.storage.buckets || []).map(escapeHTML).join(", ") || "No buckets"}</small></td>
    <td>${status(item.disabled ? "suspended" : "active")}</td>
    <td><button data-access-edit="${escapeHTML(item.subject)}">Edit</button><button class="danger" data-access-delete="${escapeHTML(item.subject)}">Revoke</button></td>
  </tr>`).join("") : `<tr><td colspan="7" class="empty">No users have been provisioned.</td></tr>`;
  const pending = (requests.items || []).filter(item => item.status === "pending");
  document.querySelector("#access-request-count").textContent = `${pending.length} pending`;
  document.querySelector("#access-request-table").innerHTML = (requests.items || []).length ? requests.items.map(item => `<tr>
    <td><b>${escapeHTML(item.email || item.subject)}</b><br><small>${escapeHTML(item.subject)}</small></td>
    <td>${item.requested_services.map(service => `<span class="tag">${escapeHTML(service)}</span>`).join(" ")}</td>
    <td class="request-reason">${escapeHTML(item.reason)}</td><td>${dateTime(item.created_at)}</td><td>${status(item.status)}</td>
    <td>${item.status === "pending" ? `<button data-request-review="${escapeHTML(item.id)}" data-request-subject="${escapeHTML(item.subject)}">Provision</button><button class="danger" data-request-reject="${escapeHTML(item.id)}">Reject</button>` : `<small>${escapeHTML(item.reviewer || "reviewed")}</small>`}</td>
  </tr>`).join("") : `<tr><td colspan="6" class="empty">No access requests yet.</td></tr>`;
}

async function loadMyAccess() {
  const [identity, requests] = await Promise.all([api("/api/v1/me"), api("/api/v1/access-requests")]);
  me = {...me, ...identity};
  const grant = me.entitlements;
  const services = isAdmin() ? accessServices : (me.services || []);
  const roleState = grant?.disabled ? "suspended" : (me.provisioned || isAdmin() ? "active" : "not_provisioned");
  const allocation = grant ? `
    <article class="panel access-allocation">
      <div class="panel-heading"><div><p class="eyebrow">COMPUTE ALLOCATION</p><h3>Workspace capacity</h3></div>${status(roleState)}</div>
      <div class="allocation-grid">
        <div><strong>${grant.compute.vcpus}</strong><span>vCPUs</span></div>
        <div><strong>${grant.compute.memory_gb}</strong><span>GB memory</span></div>
        <div><strong>${grant.compute.gpus || 0}</strong><span>GPUs</span></div>
        <div><strong>${grant.compute.max_vms}</strong><span>VMs</span></div>
        <div><strong>${grant.compute.max_projects}</strong><span>Projects</span></div>
        <div><strong>${grant.compute.max_concurrent_runs}</strong><span>Concurrent runs</span></div>
        <div><strong>${grant.compute.max_functions || 0}</strong><span>Functions</span></div>
        <div><strong>${grant.storage.size_gb}</strong><span>GB storage</span></div>
      </div>
    </article>` : `
    <article class="panel access-allocation"><p class="eyebrow">COMPUTE ALLOCATION</p><h3>Administrative access</h3><p>Administrators are not constrained by user workspace quotas.</p></article>`;
  const latestRequest = (requests.items || [])[0];
  document.querySelector("#request-access").disabled = latestRequest?.status === "pending";
  document.querySelector("#request-access").textContent = latestRequest?.status === "pending" ? "Request pending" : "Request access";
  document.querySelector("#my-access-summary").innerHTML = `
    <div class="access-identity panel">
      <div><span class="role-badge ${escapeHTML(me.roles[0] || "unknown")}">${escapeHTML(me.roles.join(", ") || "no role")}</span><h3>${escapeHTML(me.email || me.subject || "Unknown identity")}</h3><p><code>${escapeHTML(me.subject)}</code> · ${escapeHTML(me.mode)} authentication</p></div>
      <div>${status(roleState)}<small>${me.provisioned ? "Administrator-provisioned profile" : isAdmin() ? "Full platform administrator" : "Contact an administrator for access"}</small></div>
    </div>
    <div class="split">
      <article class="panel"><p class="eyebrow">ASSIGNED SERVICES</p><h3>${services.length} services available</h3><div class="service-grant-grid">${services.map(service => `<button data-view-target="${escapeHTML(service === "git" ? "projects" : service)}" ${["workbench","ide"].includes(service) ? "disabled" : ""}><span>✓</span>${escapeHTML(service)}</button>`).join("") || `<p class="empty">No services assigned.</p>`}</div></article>
      <article class="panel"><p class="eyebrow">PROJECT SCOPE</p><h3>${grant?.project_ids?.length || 0} explicitly assigned</h3><div class="tags">${(grant?.project_ids || []).map(id => `<span class="tag">${escapeHTML(id)}</span>`).join("") || `<span class="tag">${isAdmin() ? "All projects" : "Owned projects only"}</span>`}</div><p class="eyebrow access-subhead">STORAGE BUCKETS</p><div class="tags">${(grant?.storage?.buckets || []).map(name => `<span class="tag">${escapeHTML(name)}</span>`).join("") || `<span class="tag">${isAdmin() ? "All buckets" : "None assigned"}</span>`}</div></article>
    </div>${latestRequest ? `<article class="panel access-request-state"><div><p class="eyebrow">LATEST ACCESS REQUEST</p><h3>${escapeHTML(latestRequest.requested_services.join(", "))}</h3><p>${escapeHTML(latestRequest.reason)}</p></div><div>${status(latestRequest.status)}<small>${dateTime(latestRequest.updated_at)}</small></div></article>` : ""}${allocation}`;
}

const preferenceKey = "kionga.console.preferences";
function readPreferences() {
  try { return JSON.parse(localStorage.getItem(preferenceKey) || "{}"); } catch { return {}; }
}
function applyPreferences() {
  const prefs = readPreferences();
  document.body.classList.toggle("compact-ui", Boolean(prefs.compact));
  document.body.classList.toggle("reduce-motion", Boolean(prefs.reduceMotion));
}
function writePreferences() {
  const prefs = {
    compact:document.querySelector("#setting-compact").checked,
    reduceMotion:document.querySelector("#setting-motion").checked,
    live:document.querySelector("#setting-live").checked,
    startView:document.querySelector("#setting-start-view").value,
  };
  localStorage.setItem(preferenceKey, JSON.stringify(prefs)); applyPreferences();
  if (prefs.live && hasService("overview")) connectEvents();
  if (!prefs.live && eventSource) { eventSource.close(); eventSource = null; }
}
async function loadSettings() {
  const data = await api("/api/v1/settings/tokens");
  const prefs = readPreferences();
  document.querySelector("#setting-compact").checked = Boolean(prefs.compact);
  document.querySelector("#setting-motion").checked = Boolean(prefs.reduceMotion);
  document.querySelector("#setting-live").checked = prefs.live !== false;
  document.querySelector("#setting-start-view").value = prefs.startView || "overview";
  document.querySelector("#settings-profile").innerHTML = `<dl class="settings-definition"><div><dt>Name</dt><dd>${escapeHTML(me.email || me.subject)}</dd></div><div><dt>Subject</dt><dd><code>${escapeHTML(me.subject)}</code></dd></div><div><dt>Role</dt><dd>${me.roles.map(role => `<span class="tag">${escapeHTML(role)}</span>`).join(" ")}</dd></div><div><dt>Authentication</dt><dd>${escapeHTML(me.mode)}</dd></div></dl>`;
  document.querySelector("#settings-auth-mode").textContent = `${me.mode.toUpperCase()} session`;
  document.querySelector("#settings-session-identity").textContent = me.email || me.subject;
  document.querySelector("#settings-jupyter").href = `http://${location.hostname}:8888`;
  document.querySelector("#settings-ide").href = `http://${location.hostname}:13337`;
  document.querySelector("#settings-jupyter").hidden = !hasService("workbench");
  document.querySelector("#settings-ide").hidden = !hasService("ide");
  const items = data.items || [];
  document.querySelector("#api-token-list").innerHTML = items.length ? items.map(token => `<div class="token-row"><div class="token-mark">⌁</div><div><b>${escapeHTML(token.name)}</b><small><code>${escapeHTML(token.prefix)}…</code> · ${token.services.map(escapeHTML).join(", ")}</small><small>Expires ${dateTime(token.expires_at)} · ${token.last_used_at ? `Last used ${dateTime(token.last_used_at)}` : "Never used"}</small></div>${token.revoked_at ? status("revoked") : `<button class="danger" data-token-revoke="${escapeHTML(token.id)}">Revoke</button>`}</div>`).join("") : `<div class="empty-state"><b>No personal API keys</b><span>Create a scoped key for the CLI, SDK, or local scripts.</span></div>`;
}

let blogAdminCache = [];
async function loadAdminBlogs() {
  const data = await api("/api/v1/admin/blogs");
  blogAdminCache = data.items || [];
  document.querySelector("#blog-admin-table").innerHTML = blogAdminCache.length ? blogAdminCache.map(post => `<tr><td><b>${escapeHTML(post.title)}</b><br><small>/${escapeHTML(post.slug)}</small></td><td>${escapeHTML(post.author)}</td><td>${post.tags.map(tag => `<span class="tag">${escapeHTML(tag)}</span>`).join(" ")}</td><td>${status(post.status)}</td><td>${dateTime(post.updated_at)}</td><td><button data-blog-open="${escapeHTML(post.slug)}">View</button><button data-blog-edit="${escapeHTML(post.id)}">Edit</button><button class="danger" data-blog-delete="${escapeHTML(post.id)}">Delete</button></td></tr>`).join("") : `<tr><td colspan="6" class="empty">No blog posts yet.</td></tr>`;
}

Object.assign(viewLoaders, {
  overview: loadDashboard, projects: loadProjects, pipelines: loadRuns, functions: loadFunctions, models: loadModels,
  agents: loadAgents, features: loadFeatures, storage: loadStorage, realtime: loadRealtime,
  catalog: () => loadCatalog(document.querySelector("[data-kind].active")?.dataset.kind || ""),
  platform: loadComponents,
  profile: loadMyAccess,
  settings: loadSettings,
  blogs: loadAdminBlogs,
  access: loadAccess,
});

// ---- live updates over SSE --------------------------------------------------
let lastDigest = "";
let eventSource = null;
function connectEvents() {
  if (eventSource) eventSource.close();
  const source = new EventSource("/api/v1/events");
  eventSource = source;
  const indicator = document.querySelector("#live-indicator");
  source.onmessage = event => {
    indicator.textContent = "●  Local Cluster  live";
    if (event.data === lastDigest) return;
    lastDigest = event.data;
    loadDashboard().catch(() => {});
    if (viewLoaders[activeView] && activeView !== "overview") viewLoaders[activeView]().catch(() => {});
    if (openRunID && document.querySelector("#run-dialog").open) showRun(openRunID, {open:false}).catch(() => {});
  };
  source.onerror = () => { indicator.textContent = "○  Local Cluster  reconnecting"; };
}

// ---- application navigation ------------------------------------------------
const openSubmitDialog = async () => { await Promise.all([loadProjects(), loadRuns()]); document.querySelector("#submit-dialog").showModal(); };
const shell = document.querySelector(".shell");
const sidebarToggle = document.querySelector("#sidebar-toggle");
const isMobile = () => window.matchMedia("(max-width: 650px)").matches;
function toggleSidebar() {
  if (isMobile()) {
    const open = shell.classList.toggle("mobile-nav-open");
    sidebarToggle.setAttribute("aria-expanded", String(open));
    sidebarToggle.title = open ? "Close navigation" : "Open navigation";
    return;
  }
  const collapsed = shell.classList.toggle("sidebar-collapsed");
  localStorage.setItem("kionga.sidebar.collapsed", String(collapsed));
  sidebarToggle.setAttribute("aria-expanded", String(!collapsed));
  sidebarToggle.title = collapsed ? "Expand navigation" : "Collapse navigation";
}

if (!isMobile() && localStorage.getItem("kionga.sidebar.collapsed") === "true") {
  shell.classList.add("sidebar-collapsed");
  sidebarToggle.setAttribute("aria-expanded", "false");
  sidebarToggle.title = "Expand navigation";
}

async function showAbout() {
  let health = {status: "unreachable", version: "?"};
  try { health = await api("/api/v1/health"); } catch { /* shown as unreachable */ }
  document.querySelector("#about-detail").innerHTML = `
    <div class="detail-meta">${status(health.status === "ok" ? "healthy" : "failed")}<span class="tag">${escapeHTML(health.service || "gateway")}</span><span class="tag">v${escapeHTML(health.version)}</span></div>
    <p>Self-hosted MLOps, data-centric and agentic AI platform — pipelines, model serving, feature store, agents and real-time streams behind one control plane.</p>
    <table class="schema"><tbody>
      <tr><td>Signed in as</td><td>${escapeHTML(me.email || me.subject || "anonymous")}</td></tr>
      <tr><td>Roles</td><td>${escapeHTML(me.roles.join(", ") || "none")}</td></tr>
      <tr><td>Auth mode</td><td>${escapeHTML(me.mode)}</td></tr>
      <tr><td>API</td><td><code>${escapeHTML(location.origin)}/api/v1</code></td></tr>
    </tbody></table>`;
  document.querySelector("#about-dialog").showModal();
}

document.querySelector("#workbench-link").href = `http://${location.hostname}:8888`;
document.querySelector("#ide-link").href = `http://${location.hostname}:13337`;
document.querySelector("#account-button").addEventListener("click", () => showView("settings"));
document.querySelector("#service-grants").innerHTML = accessServices.map(service => `<label class="inline-check"><input type="checkbox" name="services" value="${service}"> ${service}</label>`).join("");
document.querySelector("#request-service-grants").innerHTML = accessServices.map(service => `<label class="inline-check"><input type="checkbox" name="services" value="${service}"> ${service}</label>`).join("");
document.querySelector("#resource-profile").addEventListener("change", event => setResourceProfile(event.target.value));
sidebarToggle.addEventListener("click", toggleSidebar);
document.querySelector("#refresh-view").addEventListener("click", () => (viewLoaders[activeView] || loadDashboard)().then(() => toast("View refreshed.")).catch(error => toast(error.message)));
document.querySelector("#open-help").addEventListener("click", () => showAbout().catch(error => toast(error.message)));
document.querySelectorAll(".brand, .app-brand").forEach(link => link.addEventListener("click", event => {
  event.preventDefault();
  showView(hasService("overview") ? "overview" : (me.services[0] || "access"));
}));

// ---- interactions -----------------------------------------------------------
document.querySelectorAll(".nav-item").forEach(button => button.addEventListener("click", () => {
  showView(button.dataset.view);
  if (isMobile()) {
    shell.classList.remove("mobile-nav-open");
    sidebarToggle.setAttribute("aria-expanded", "false");
  }
}));
document.querySelectorAll("[data-view-target]").forEach(button => button.addEventListener("click", () => showView(button.dataset.viewTarget)));
document.addEventListener("keydown", event => {
  if ((event.key === "Enter" || event.key === " ") && event.target.matches("[role='button']")) {
    event.preventDefault();
    event.target.click();
  }
});
document.querySelectorAll("[data-kind]").forEach(button => button.addEventListener("click", () => { document.querySelectorAll("[data-kind]").forEach(n => n.classList.remove("active")); button.classList.add("active"); loadCatalog(button.dataset.kind); }));
document.querySelector("#feature-search").addEventListener("input", event => renderFeatures(event.target.value));
document.querySelector("#storage-up").addEventListener("click", () => {
  if (storageState.prefix) {
    const parts = storageState.prefix.replace(/\/$/, "").split("/");
    parts.pop();
    storageState.prefix = parts.length ? parts.join("/") + "/" : "";
  } else storageState.bucket = "";
  loadStorage().catch(error => toast(error.message));
});

const projectProfiles = {
  starter:{label:"Starter",detail:"2 vCPU · 4 GB RAM · 25 GB storage",description:"Notebook exploration and small development runs.",vcpus:2,memory:4,gpus:0,storage:25},
  team:{label:"Team",detail:"4 vCPU · 8 GB RAM · 100 GB storage",description:"Daily model development and shared pipelines.",vcpus:4,memory:8,gpus:0,storage:100},
  power:{label:"Power",detail:"8 vCPU · 16 GB RAM · 250 GB storage",description:"Large training runs and production delivery.",vcpus:8,memory:16,gpus:0,storage:250},
  gpu:{label:"GPU",detail:"8 vCPU · 32 GB RAM · 1 GPU · 500 GB storage",description:"Accelerated model and generative AI development.",vcpus:8,memory:32,gpus:1,storage:500},
  custom:{label:"Current allocation",detail:"Use exactly what your administrator assigned",description:"The backend still enforces your effective resource grant."},
};
let projectTemplateCache = [];
let selectedProjectTemplate = "";

const effectiveGrant = () => (me.roles.includes("user") && !isAdmin() ? me.entitlements : null);
function profileFitsGrant(profileName) {
  const grant = effectiveGrant();
  const profile = projectProfiles[profileName];
  if (!grant || profileName === "custom") return true;
  return Number(grant.compute?.vcpus || 0) >= profile.vcpus
    && Number(grant.compute?.memory_gb || 0) >= profile.memory
    && Number(grant.compute?.gpus || 0) >= profile.gpus
    && Number(grant.storage?.size_gb || 0) >= profile.storage;
}
const acceleratorGPUs = value => value === "multi-gpu" ? 2 : value === "single-gpu" ? 1 : 0;
function acceleratorFitsGrant(value) {
  const grant = effectiveGrant();
  return !grant || Number(grant.compute?.gpus || 0) >= acceleratorGPUs(value);
}

function acceleratorCompatible(template, framework, accelerator) {
  return !(template?.id === "production-ml" && framework === "scikit-learn" && accelerator !== "cpu");
}

function configureProjectAccelerators(template) {
  const accelerator = document.querySelector("#project-accelerator");
  const framework = document.querySelector("#project-framework").value;
  const previous = accelerator.value;
  accelerator.innerHTML = (template.accelerators || []).map(value => {
    const provisioned = acceleratorFitsGrant(value);
    const compatible = acceleratorCompatible(template, framework, value);
    const reason = !compatible ? " · unsupported by framework" : !provisioned ? " · not provisioned" : "";
    return `<option value="${escapeHTML(value)}" ${provisioned && compatible ? "" : "disabled"}>${escapeHTML(value)}${reason}</option>`;
  }).join("");
  const available = [...accelerator.options].filter(option => !option.disabled);
  if (available.some(option => option.value === previous)) accelerator.value = previous;
  else accelerator.value = available[0]?.value || "";
  return available.length > 0;
}

function configureProjectProfileOptions() {
  const select = document.querySelector("#project-requested-profile");
  [...select.options].forEach(option => {
    const allowed = profileFitsGrant(option.value);
    option.disabled = !allowed;
    option.title = allowed ? "" : "This profile exceeds your assigned compute or storage grant.";
  });
}

function renderProjectTemplatePreview() {
  const template = projectTemplateCache.find(item => item.id === selectedProjectTemplate);
  if (!template) return;
  const framework = document.querySelector("#project-framework").value;
  const accelerator = document.querySelector("#project-accelerator").value;
  const profileName = document.querySelector("#project-requested-profile").value;
  const profile = projectProfiles[profileName] || projectProfiles.custom;
  const grant = effectiveGrant();
  const grantDetail = grant ? `${grant.compute?.vcpus || 0} vCPU · ${grant.compute?.memory_gb || 0} GB RAM · ${grant.compute?.gpus || 0} GPU · ${grant.storage?.size_gb || 0} GB storage assigned` : "Capacity is enforced when the project is created.";
  document.querySelector("#project-template-preview").innerHTML = `<div class="template-preview-heading"><div><span class="kind">${escapeHTML(template.category)} · v${escapeHTML(template.version)}</span><h4>${escapeHTML(template.name)}</h4></div><span class="profile-badge">${escapeHTML(profile.label)}</span></div><p>${escapeHTML(template.description)}</p><div class="template-runtime"><span><b>Framework</b>${escapeHTML(framework)}</span><span><b>Accelerator</b>${escapeHTML(accelerator)}</span><span><b>Requested capacity</b>${escapeHTML(profile.detail)}</span></div><small>${escapeHTML(profile.description)} ${escapeHTML(grantDetail)}</small><div class="tags">${(template.capabilities || []).map(value => `<span class="tag">${escapeHTML(value)}</span>`).join("")}</div><div class="template-services"><b>Required services</b>${(template.required_services || []).map(value => `<span>${escapeHTML(value)}</span>`).join("")}</div>`;
}

function selectProjectTemplate(templateID, resetProfile = true) {
  const template = projectTemplateCache.find(item => item.id === templateID);
  if (!template) return;
  selectedProjectTemplate = template.id;
  document.querySelector("#project-template-id").value = template.id;
  document.querySelector("#project-template-version").value = template.version;
  document.querySelectorAll("[data-project-template]").forEach(button => {
    const selected = button.dataset.projectTemplate === template.id;
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-checked", String(selected));
  });
  const framework = document.querySelector("#project-framework");
  framework.innerHTML = (template.frameworks || []).map(value => `<option value="${escapeHTML(value)}">${escapeHTML(value)}</option>`).join("");
  configureProjectProfileOptions();
  if (!configureProjectAccelerators(template)) {
    document.querySelector("#project-template-preview").innerHTML = `<p class="empty">This template needs accelerator capacity that has not been assigned.</p>`;
    document.querySelector("#project-template-id").value = "";
    return;
  }
  if (resetProfile) {
    const recommended = template.recommended_profile || "starter";
    document.querySelector("#project-requested-profile").value = profileFitsGrant(recommended) ? recommended : "custom";
  }
  renderProjectTemplatePreview();
}

function renderProjectTemplateCatalog() {
  const catalog = document.querySelector("#project-template-catalog");
  const unavailable = template => {
    if (!me.roles.includes("user") || isAdmin()) return [];
    const blockers = (template.required_services || []).filter(service => !me.services.includes(service));
    if (!(template.accelerators || []).some(acceleratorFitsGrant)) blockers.push("required accelerator capacity");
    return blockers;
  };
  catalog.innerHTML = projectTemplateCache.map(template => {
    const missing = unavailable(template);
    const reason = missing.length ? `Not provisioned: ${missing.join(", ")}.` : "";
    return `<button type="button" class="project-template-option${missing.length ? " unavailable" : ""}" role="radio" aria-checked="false" aria-disabled="${missing.length}" data-project-template="${escapeHTML(template.id)}" ${missing.length ? "disabled" : ""}><span class="template-option-heading"><b>${escapeHTML(template.name)}</b><small>${escapeHTML(template.category)} · v${escapeHTML(template.version)}</small></span><span>${escapeHTML(template.description)}</span><span class="template-option-meta">${(template.frameworks || []).map(escapeHTML).join(" · ")}<i>${missing.length ? escapeHTML(reason) : `${escapeHTML(template.recommended_profile)} profile`}</i></span></button>`;
  }).join("");
  const available = projectTemplateCache.find(template => !unavailable(template).length);
  const selected = projectTemplateCache.find(template => template.id === selectedProjectTemplate && !unavailable(template).length);
  selectedProjectTemplate = "";
  document.querySelector("#project-template-id").value = "";
  document.querySelector("#project-template-version").value = "";
  if (selected || available) selectProjectTemplate((selected || available).id);
  else document.querySelector("#project-template-preview").innerHTML = `<p class="empty">Your administrator must assign the services required by a project template.</p>`;
}

async function loadProjectTemplates() {
  if (!projectTemplateCache.length) {
    const response = await api("/api/v1/project-templates");
    projectTemplateCache = response.items || [];
  }
  if (!projectTemplateCache.length) throw new Error("No project templates are available.");
  renderProjectTemplateCatalog();
}

document.querySelector("#project-template-catalog").addEventListener("click", event => {
  const option = event.target.closest("[data-project-template]");
  if (option) selectProjectTemplate(option.dataset.projectTemplate);
});
document.querySelector("#project-framework").addEventListener("change", () => {
  const template = projectTemplateCache.find(item => item.id === selectedProjectTemplate);
  if (template) configureProjectAccelerators(template);
  renderProjectTemplatePreview();
});
document.querySelector("#project-requested-profile").addEventListener("change", renderProjectTemplatePreview);
document.querySelector("#project-accelerator").addEventListener("change", event => {
  const profile = document.querySelector("#project-requested-profile");
  if (event.target.value !== "cpu" && profile.value !== "custom") profile.value = profileFitsGrant("gpu") ? "gpu" : "custom";
  renderProjectTemplatePreview();
});

const dialog = document.querySelector("#project-dialog");
document.querySelector("#new-project").addEventListener("click", async () => {
  const fields = document.querySelector("#new-project-git-fields"), allowed = can("git_write");
  fields.hidden = !allowed; fields.querySelectorAll("input").forEach(input => { input.disabled = !allowed; });
  document.querySelector("#form-error").textContent = "";
  if (!dialog.open) dialog.showModal();
  try { await loadProjectTemplates(); }
  catch (failure) {
    document.querySelector("#project-template-catalog").innerHTML = `<p class="empty">Template catalog unavailable.</p>`;
    document.querySelector("#form-error").textContent = failure.message;
  }
});
function closeDialog(node) {
  const modal = node.closest("dialog");
  modal.querySelectorAll(".form-error").forEach(error => { error.textContent = ""; });
  modal.close();
}
document.querySelectorAll("dialog .close, dialog [data-dialog-close]").forEach(button => button.addEventListener("click", () => closeDialog(button)));
document.querySelectorAll("dialog").forEach(modal => modal.addEventListener("click", event => {
  if (event.target === modal) closeDialog(modal);
}));
document.querySelector("#project-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = new FormData(event.target);
  const error = document.querySelector("#form-error");
  error.textContent = "";
  try {
    if (!event.target.elements.template.value) throw new Error("Choose a project template.");
    await api("/api/v1/projects", {method:"POST", body:JSON.stringify(Object.fromEntries(form))});
    event.target.reset(); selectedProjectTemplate = ""; dialog.close(); toast("Project saved. Open its details and copy the scaffold command to generate the workspace.");
    await Promise.all([loadDashboard(), loadProjects()]); showView("projects");
  } catch (failure) { error.textContent = failure.message; }
});

document.querySelector("#run-pipeline").addEventListener("click", openSubmitDialog);
document.querySelector("#submit-form").addEventListener("submit", async event => {
  event.preventDefault(); const error = document.querySelector("#submit-error"); error.textContent = "";
  try {
    const payload = Object.fromEntries(new FormData(event.target));
    payload.parameters = JSON.parse(payload.parameters || "{}");
    if (!payload.definition_id) delete payload.definition_id;
    await api("/api/v1/pipelines/submit", {method:"POST", body:JSON.stringify(payload)});
    document.querySelector("#submit-dialog").close(); toast("Run submitted to the engine."); await loadRuns();
  } catch (failure) { error.textContent = failure.message; }
});

let pipelinePreviewTimer = null;
function updatePipelineDefinitionPreview() {
  const form = document.querySelector("#pipeline-definition-form");
  const preview = document.querySelector("#pipeline-definition-preview");
  const previewStatus = document.querySelector("#pipeline-preview-status");
  try {
    const jobs = JSON.parse(form.elements.jobs.value || "[]");
    if (!Array.isArray(jobs)) throw new Error("Jobs must be a JSON array.");
    const analysis = window.KiongaPipelineGraph.analyze(jobs);
    preview.innerHTML = pipelineGraph(jobs, {ariaLabel:"Pipeline definition preview"});
    previewStatus.textContent = analysis.errors.length ? `${analysis.errors.length} issue${analysis.errors.length === 1 ? "" : "s"}` : `${jobs.length} jobs · valid DAG`;
    previewStatus.classList.toggle("invalid", analysis.errors.length > 0);
  } catch (failure) {
    preview.innerHTML = `<div class="dag-empty">${escapeHTML(failure.message)}</div>`;
    previewStatus.textContent = "Invalid JSON";
    previewStatus.classList.add("invalid");
  }
}
document.querySelector("#pipeline-definition-form").elements.jobs.addEventListener("input", () => {
  clearTimeout(pipelinePreviewTimer);
  pipelinePreviewTimer = setTimeout(updatePipelineDefinitionPreview, 120);
});

document.querySelector("#new-pipeline-definition").addEventListener("click", async () => {
  await Promise.all([loadProjects(), loadFunctions()]);
  document.querySelector("#pipeline-definition-error").textContent = "";
  updatePipelineDefinitionPreview();
  document.querySelector("#pipeline-definition-dialog").showModal();
});
document.querySelector("#pipeline-definition-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.target, error = document.querySelector("#pipeline-definition-error"); error.textContent = "";
  try {
    const payload = Object.fromEntries(new FormData(form)); payload.jobs = JSON.parse(payload.jobs);
    const analysis = window.KiongaPipelineGraph.analyze(payload.jobs);
    if (analysis.errors.length) throw new Error(analysis.errors[0]);
    await api("/api/v1/pipelines/definitions", {method:"POST", body:JSON.stringify(payload)});
    document.querySelector("#pipeline-definition-dialog").close(); toast("Pipeline flow saved."); await loadRuns();
  } catch (failure) { error.textContent = failure.message; }
});

document.querySelector("#deploy-function").addEventListener("click", async () => {
  await loadProjects(); const form = document.querySelector("#deploy-function-form"); form.reset();
  form.elements.cpu.value = "500m"; form.elements.memory.value = "512Mi"; form.elements.env_vars.value = "{}";
  updateFunctionTrigger("http");
  document.querySelector("#deploy-function-error").textContent = ""; document.querySelector("#deploy-function-dialog").showModal();
});
function updateFunctionTrigger(type) {
  const label = document.querySelector("#function-trigger-source-label");
  const source = document.querySelector("#function-trigger-source");
  const title = document.querySelector("#function-trigger-source-title");
  const help = document.querySelector("#function-trigger-help");
  const settings = {
    http:[false,"Source","","Invoke synchronously from HTTP, a webhook, the SDK, or a pipeline."],
    async:[false,"Source","","Queue work immediately and receive an OpenFaaS call ID."],
    cron:[true,"Cron schedule","*/5 * * * *","Five-field cron expression; the cron connector invokes this function."],
    kafka:[true,"Kafka topic","events.created","The Kafka connector forwards every message on this topic."],
  }[type] || [false,"Source","",""];
  label.hidden = !settings[0]; title.textContent = settings[1]; source.placeholder = settings[2]; source.required = settings[0]; help.textContent = settings[3];
}
document.querySelector("#function-trigger-type").addEventListener("change", event => updateFunctionTrigger(event.target.value));
document.querySelector("#deploy-function-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.target, error = document.querySelector("#deploy-function-error"); error.textContent = "";
  try {
    const payload = Object.fromEntries(new FormData(form)); payload.env_vars = JSON.parse(payload.env_vars || "{}");
    payload.annotations = {"io.kionga.invocation": payload.trigger_type === "async" ? "async" : "sync"};
    if (payload.trigger_type === "cron") Object.assign(payload.annotations, {topic:"cron-function", schedule:payload.trigger_source});
    if (payload.trigger_type === "kafka") payload.annotations.topic = payload.trigger_source;
    delete payload.trigger_type; delete payload.trigger_source;
    await api("/api/v1/functions", {method:"POST", body:JSON.stringify(payload)});
    document.querySelector("#deploy-function-dialog").close(); toast("Function deployed."); await loadFunctions();
  } catch (failure) { error.textContent = failure.message; }
});

document.querySelector("#deploy-agent").addEventListener("click", async () => {
  const modal = document.querySelector("#deploy-agent-dialog");
  const form = document.querySelector("#deploy-agent-form");
  form.reset();
  document.querySelector("#deploy-agent-error").textContent = "";
  document.querySelector("#agent-tool-options").innerHTML = `<p class="empty">Loading tools…</p>`;
  if (!modal.open) modal.showModal();
  try {
    const [, tools] = await Promise.all([
      loadProjects(),
      api("/api/v1/tools").catch(() => ({items: [], unavailable: true})),
    ]);
    if (!projectCache.length) throw new Error("Create a project before deploying an agent.");
    const options = tools.items || [];
    document.querySelector("#agent-tool-options").innerHTML = options.length
      ? options.map(tool => `<label class="check-card"><input type="checkbox" name="agent_tools" value="${escapeHTML(tool.name)}"><span><b>${escapeHTML(tool.name)}</b><small>v${escapeHTML(tool.version)} · ${escapeHTML(tool.description || "Registered platform tool")}</small></span></label>`).join("")
      : `<p class="empty">${tools.unavailable ? "Tool catalog access is not assigned. " : "No tools are registered. "}The agent can be deployed without tools.</p>`;
    configureAgentCapacityFields(form);
  } catch (failure) { document.querySelector("#deploy-agent-error").textContent = failure.message; }
});

function configureAgentCapacityFields(form) {
  const grant = effectiveGrant();
  if (!grant) {
    document.querySelector("#agent-capacity-hint").textContent = "Capacity is checked against the administrator's grant.";
    return;
  }
  const maximum = Math.max(1, Number(grant.compute?.max_vms || 0));
  form.elements.min_replicas.max = maximum;
  form.elements.max_replicas.max = maximum;
  if (Number(form.elements.min_replicas.value) > maximum) form.elements.min_replicas.value = maximum;
  if (Number(form.elements.max_replicas.value) > maximum) form.elements.max_replicas.value = maximum;
  const gpus = Math.max(0, Number(grant.compute?.gpus || 0));
  form.elements.agent_gpu.max = gpus;
  if (Number(form.elements.agent_gpu.value) > gpus) form.elements.agent_gpu.value = gpus;
  form.elements.agent_gpu_type.value = grant.compute?.gpu_type || "nvidia.com/gpu";
  const owned = agentCache.filter(agent => !agent.owner_subject || agent.owner_subject === me.subject);
  const reserved = owned.reduce((sum, agent) => sum + Number(agent.autoscaling?.max_replicas || agent.replicas || 1), 0);
  document.querySelector("#agent-capacity-hint").textContent = `Assigned: ${grant.compute?.vcpus || 0} vCPU, ${grant.compute?.memory_gb || 0} GB RAM, ${gpus} GPU, ${maximum} maximum agent replicas. ${reserved} replicas are currently reserved; admission includes 100m CPU and 128Mi RAM per trace sidecar.`;
}

function agentContractPayload(documentValue) {
  if (documentValue?.kind !== "KiongaAgent") return documentValue;
  const spec = documentValue.spec || {};
  const requests = spec.resources?.requests || {};
  const gpuEntry = Object.entries(requests).find(([key]) => !["cpu", "memory"].includes(key));
  return {
    name: documentValue.metadata?.name || "",
    version: spec.version || "",
    image: spec.image || "",
    graph_module: spec.graphModule || "",
    llm_backend: spec.llm?.backend || "mock",
    autoscaling: {min_replicas: spec.replicas?.min || 1, max_replicas: spec.replicas?.max || 1},
    resources: {cpu: requests.cpu || "500m", memory: requests.memory || "1Gi", gpu: Number(gpuEntry?.[1] || 0), gpu_type: gpuEntry?.[0] || "nvidia.com/gpu"},
    tools: (spec.tools || []).map(tool => typeof tool === "string" ? tool : tool.name).filter(Boolean),
  };
}

document.querySelector("#load-agent-contract").addEventListener("click", () => {
  const form = document.querySelector("#deploy-agent-form");
  const error = document.querySelector("#deploy-agent-error");
  error.textContent = "";
  try {
    const raw = document.querySelector("#agent-contract-json").value.trim();
    if (!raw) throw new Error("Paste the generated platform/agent.json contract first.");
    const payload = agentContractPayload(JSON.parse(raw));
    if (!payload || typeof payload !== "object") throw new Error("The agent contract must be a JSON object.");
    const values = {
      project_id: payload.project_id, name: payload.name, version: payload.version,
      image: payload.image, graph_module: payload.graph_module, llm_backend: payload.llm_backend,
      min_replicas: payload.autoscaling?.min_replicas ?? payload.replicas,
      max_replicas: payload.autoscaling?.max_replicas ?? payload.replicas,
      agent_cpu: payload.resources?.cpu, agent_memory: payload.resources?.memory,
      agent_gpu: payload.resources?.gpu, agent_gpu_type: payload.resources?.gpu_type,
    };
    Object.entries(values).forEach(([name, value]) => {
      if (value === undefined || value === null || value === "") return;
      const field = form.elements[name];
      if (!field) return;
      if (field.tagName === "SELECT" && ![...field.options].some(option => option.value === String(value))) return;
      field.value = value;
    });
    const tools = new Set(payload.tools || []);
    form.querySelectorAll("[name='agent_tools']").forEach(input => { input.checked = tools.has(input.value); });
    configureAgentCapacityFields(form);
    toast("Agent contract loaded. Review the project and capacity before deploying.");
  } catch (failure) { error.textContent = failure.message; }
});

document.querySelector("#deploy-agent-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target;
  const error = document.querySelector("#deploy-agent-error");
  error.textContent = "";
  try {
    const payload = Object.fromEntries(new FormData(form));
    payload.autoscaling = {min_replicas:Number(payload.min_replicas), max_replicas:Number(payload.max_replicas)};
    payload.resources = {cpu:payload.agent_cpu, memory:payload.agent_memory, gpu:Number(payload.agent_gpu), gpu_type:payload.agent_gpu_type};
    payload.replicas = payload.autoscaling.min_replicas;
    payload.tools = [...form.querySelectorAll("[name='agent_tools']:checked")].map(input => input.value);
    if (payload.autoscaling.max_replicas < payload.autoscaling.min_replicas) throw new Error("Maximum replicas cannot be below minimum replicas.");
    const grant = effectiveGrant();
    if (grant && payload.autoscaling.max_replicas > Number(grant.compute?.max_vms || 0)) throw new Error("Maximum replicas exceed your assigned workload capacity.");
    if (grant && payload.resources.gpu * payload.autoscaling.max_replicas > Number(grant.compute?.gpus || 0)) throw new Error("GPU capacity at maximum replicas exceeds your assignment.");
    ["agent_tools", "min_replicas", "max_replicas", "agent_cpu", "agent_memory", "agent_gpu", "agent_gpu_type"].forEach(key => delete payload[key]);
    await api("/api/v1/agents", {method:"POST", body:JSON.stringify(payload)});
    document.querySelector("#deploy-agent-dialog").close();
    toast("Agent deployment accepted.");
    await loadAgents();
  } catch (failure) { error.textContent = failure.message; }
});

document.querySelector("#project-repository-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.target, error = document.querySelector("#project-repository-error"); error.textContent = "";
  try {
    const projectId = form.elements.project_id.value;
    await api(`/api/v1/projects/${encodeURIComponent(projectId)}/repository`, {method:"PUT", body:JSON.stringify({url:form.elements.url.value, default_branch:form.elements.default_branch.value})});
    document.querySelector("#project-repository-dialog").close(); document.querySelector("#metadata-dialog").close(); toast("Git repository connected."); await loadProjects();
  } catch (failure) { error.textContent = failure.message; }
});

document.querySelector("#add-connection").addEventListener("click", () => document.querySelector("#connection-dialog").showModal());
document.querySelector("#add-user-access").addEventListener("click", () => {
  const form = document.querySelector("#access-form");
  form.reset(); form.elements.original_subject.value = ""; form.elements.request_id.value = ""; form.elements.subject.disabled = false;
  form.elements.profile.value = "starter"; setResourceProfile("starter");
  document.querySelector("#access-error").textContent = "";
  document.querySelector("#access-dialog").showModal();
});
document.querySelector("#access-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target, error = document.querySelector("#access-error");
  const subject = form.elements.original_subject.value || form.elements.subject.value.trim();
  error.textContent = "";
  try {
    await api(`/api/v1/admin/users/${encodeURIComponent(subject)}`, {method:"PUT", body:JSON.stringify(accessPayload(form))});
    if (form.elements.request_id.value) {
      await api(`/api/v1/admin/access-requests/${encodeURIComponent(form.elements.request_id.value)}`, {method:"PATCH", body:JSON.stringify({status:"approved", note:"Provisioned through the admin console"})});
    }
    form.reset(); document.querySelector("#access-dialog").close(); toast("User access saved."); await loadAccess();
  } catch (failure) { error.textContent = failure.message; }
});
document.querySelector("#request-access").addEventListener("click", () => {
  const form = document.querySelector("#access-request-form"); form.reset();
  document.querySelector("#access-request-error").textContent = "";
  document.querySelector("#access-request-dialog").showModal();
});
document.querySelector("#access-request-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target, error = document.querySelector("#access-request-error");
  const requested_services = [...form.querySelectorAll("[name='services']:checked")].map(input => input.value);
  error.textContent = "";
  try {
    await api("/api/v1/access-requests", {method:"POST", body:JSON.stringify({reason:form.elements.reason.value, requested_services})});
    form.reset(); document.querySelector("#access-request-dialog").close(); toast("Access request submitted."); await loadMyAccess();
  } catch (failure) { error.textContent = failure.message; }
});
document.querySelector("#connection-form").addEventListener("submit", async event => {
  event.preventDefault(); const error = document.querySelector("#connection-error"); error.textContent = "";
  try {
    const connection = await api("/api/v1/connections", {method:"POST", body:JSON.stringify(Object.fromEntries(new FormData(event.target)))});
    await api(`/api/v1/connections/${encodeURIComponent(connection.id)}/test`, {method:"POST", body:"{}"});
    event.target.reset(); document.querySelector("#connection-dialog").close(); toast("Connection saved and checked."); await Promise.all([loadDashboard(), loadComponents()]);
  } catch (failure) { error.textContent = failure.message; }
});

document.querySelector("#metric-select").addEventListener("change", event => metricChart(cachedModels, event.target.value));
document.querySelectorAll("#setting-compact,#setting-motion,#setting-live,#setting-start-view").forEach(control => control.addEventListener("change", writePreferences));
document.querySelector("#create-api-token").addEventListener("click", () => {
  const form = document.querySelector("#api-token-form"); form.reset();
  const scopes = accessServices.filter(service => !["workbench","ide"].includes(service) && hasService(service));
  document.querySelector("#token-service-scopes").innerHTML = scopes.map(service => `<label class="inline-check"><input type="checkbox" name="services" value="${escapeHTML(service)}"> ${escapeHTML(service)}</label>`).join("");
  document.querySelector("#api-token-error").textContent = "";
  document.querySelector("#api-token-dialog").showModal();
});
document.querySelector("#api-token-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.target, error = document.querySelector("#api-token-error");
  error.textContent = "";
  const payload = {name:form.elements.name.value, expires_in_days:Number(form.elements.expires_in_days.value), project_ids:csv(form.elements.project_ids.value), services:[...form.querySelectorAll("[name='services']:checked")].map(input => input.value)};
  try {
    const created = await api("/api/v1/settings/tokens", {method:"POST", body:JSON.stringify(payload)});
    document.querySelector("#api-token-dialog").close();
    document.querySelector("#api-token-secret").textContent = created.secret;
    document.querySelector("#api-token-secret-dialog").showModal();
    await loadSettings();
  } catch (failure) { error.textContent = failure.message; }
});
document.querySelector("#copy-api-token").addEventListener("click", async () => {
  try {
    await copyText(document.querySelector("#api-token-secret").textContent);
    toast("API key copied.");
  } catch (failure) { toast(`${failure.message} Select the key and copy it manually.`); }
});
document.querySelector("#new-blog-post").addEventListener("click", () => {
  const form = document.querySelector("#blog-editor-form"); form.reset(); form.elements.id.value = "";
  form.elements.author.value = "Kionga Engineering"; document.querySelector("#blog-editor-title").textContent = "New post";
  document.querySelector("#blog-editor-error").textContent = ""; document.querySelector("#blog-editor-dialog").showModal();
});
document.querySelector("#blog-editor-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.target, error = document.querySelector("#blog-editor-error");
  const payload = {title:form.elements.title.value, slug:form.elements.slug.value, status:form.elements.status.value, author:form.elements.author.value, tags:csv(form.elements.tags.value), summary:form.elements.summary.value, content:form.elements.content.value};
  const id = form.elements.id.value; error.textContent = "";
  try {
    await api(id ? `/api/v1/admin/blogs/${encodeURIComponent(id)}` : "/api/v1/admin/blogs", {method:id ? "PUT" : "POST", body:JSON.stringify(payload)});
    document.querySelector("#blog-editor-dialog").close(); toast(id ? "Blog post updated." : "Blog post created."); await loadAdminBlogs();
  } catch (failure) { error.textContent = failure.message; }
});

// Agent chat console
const chatState = {agentId: "", sessionId: ""};
document.querySelector("#chat-form").addEventListener("submit", async event => {
  event.preventDefault();
  const input = document.querySelector("#chat-input");
  const log = document.querySelector("#chat-log");
  const message = input.value.trim();
  if (!message) return;
  input.value = "";
  if (log.querySelector(".empty")) log.innerHTML = "";
  log.insertAdjacentHTML("beforeend", `<div class="chat-turn user"><span>You</span><p>${escapeHTML(message)}</p></div>`);
  log.insertAdjacentHTML("beforeend", `<div class="chat-turn agent pending"><span>Agent</span><p>…</p></div>`);
  log.scrollTop = log.scrollHeight;
  try {
    const reply = await api(`/api/v1/agents/${encodeURIComponent(chatState.agentId)}/invoke`, {method:"POST", body:JSON.stringify({message, session_id: chatState.sessionId, user_id: "console"})});
    chatState.sessionId = reply.session_id;
    log.lastElementChild.outerHTML = `<div class="chat-turn agent"><span>Agent · ${reply.input_tokens + reply.output_tokens} tokens · ${reply.duration_ms}ms</span><p>${escapeHTML(reply.reply)}</p></div>`;
  } catch (failure) {
    log.lastElementChild.outerHTML = `<div class="chat-turn agent failed"><span>Agent</span><p>${escapeHTML(failure.message)}</p></div>`;
  }
  log.scrollTop = log.scrollHeight;
});

// Model predict console
const predictState = {modelId: ""};
document.querySelector("#predict-form").addEventListener("submit", async event => {
  event.preventDefault();
  const output = document.querySelector("#predict-output");
  output.textContent = "…";
  try {
    const body = document.querySelector("#predict-input").value;
    JSON.parse(body);
    const result = await api(`/api/v1/models/${encodeURIComponent(predictState.modelId)}/predict`, {method:"POST", body});
    output.textContent = JSON.stringify(result, null, 2);
  } catch (failure) { output.textContent = `Error: ${failure.message}`; }
});

const trafficState = {agentId: ""};
document.querySelector("#traffic-weight").addEventListener("input", event => {
  document.querySelector("#traffic-value").textContent = `${event.target.value}%`;
});
document.querySelector("#traffic-form").addEventListener("submit", async event => {
  event.preventDefault();
  const error = document.querySelector("#traffic-error");
  const weight = Number(document.querySelector("#traffic-weight").value);
  error.textContent = "";
  try {
    await api(`/api/v1/agents/${encodeURIComponent(trafficState.agentId)}/traffic`, {method:"PUT", body:JSON.stringify({canary_weight: weight})});
    document.querySelector("#traffic-dialog").close();
    toast(`Canary weight set to ${weight}%.`);
    await loadAgents();
  } catch (failure) { error.textContent = failure.message; }
});

const functionState = {name: "", async: false};
document.querySelector("#function-form").addEventListener("submit", async event => {
  event.preventDefault();
  const error = document.querySelector("#function-error");
  const output = document.querySelector("#function-output");
  error.textContent = "";
  output.textContent = "";
  try {
    const payload = document.querySelector("#function-payload").value;
    JSON.parse(payload);
    const route = functionState.async ? "invoke-async" : "invoke";
    const result = await api(`/api/v1/functions/${encodeURIComponent(functionState.name)}/${route}`, {method:"POST", body:payload});
    output.textContent = JSON.stringify(result, null, 2);
    toast(functionState.async ? "Function invocation queued." : "Function invocation completed.");
  } catch (failure) { error.textContent = failure.message; }
});

async function handleDynamicClick(event) {
  const blogOpen = event.target.closest("[data-blog-open]");
  if (blogOpen) { window.open(`/blog.html?slug=${encodeURIComponent(blogOpen.dataset.blogOpen)}`, "_blank", "noopener"); return; }
  const blogEdit = event.target.closest("[data-blog-edit]");
  if (blogEdit) {
    const post = blogAdminCache.find(item => item.id === blogEdit.dataset.blogEdit); if (!post) return;
    const form = document.querySelector("#blog-editor-form"); form.elements.id.value = post.id; form.elements.title.value = post.title; form.elements.slug.value = post.slug; form.elements.status.value = post.status; form.elements.author.value = post.author; form.elements.tags.value = post.tags.join(", "); form.elements.summary.value = post.summary; form.elements.content.value = post.content;
    document.querySelector("#blog-editor-title").textContent = "Edit post"; document.querySelector("#blog-editor-error").textContent = ""; document.querySelector("#blog-editor-dialog").showModal(); return;
  }
  const blogDelete = event.target.closest("[data-blog-delete]");
  if (blogDelete) {
    if (!confirm("Delete this blog post permanently?")) return;
    await api(`/api/v1/admin/blogs/${encodeURIComponent(blogDelete.dataset.blogDelete)}`, {method:"DELETE"}); toast("Blog post deleted."); await loadAdminBlogs(); return;
  }
  const tokenRevoke = event.target.closest("[data-token-revoke]");
  if (tokenRevoke) {
    if (!confirm("Revoke this API key? Any tool using it will immediately lose access.")) return;
    await api(`/api/v1/settings/tokens/${encodeURIComponent(tokenRevoke.dataset.tokenRevoke)}`, {method:"DELETE"});
    toast("API key revoked."); await loadSettings(); return;
  }
  const viewTarget = event.target.closest("#my-access-summary [data-view-target]");
  if (viewTarget) { showView(viewTarget.dataset.viewTarget); return; }
  const accessEdit = event.target.closest("[data-access-edit]");
  if (accessEdit) {
    const item = accessCache.find(value => value.subject === accessEdit.dataset.accessEdit);
    if (!item) return;
    const form = document.querySelector("#access-form");
    form.reset();
    form.elements.original_subject.value = item.subject;
    form.elements.request_id.value = "";
    form.elements.subject.value = item.subject;
    form.elements.subject.disabled = true;
    form.elements.email.value = item.email || "";
    form.elements.role.value = item.role;
    form.elements.project_ids.value = (item.project_ids || []).join(", ");
    form.elements.profile.value = item.compute.profile || "custom";
    form.elements.vcpus.value = item.compute.vcpus;
    form.elements.memory_gb.value = item.compute.memory_gb;
    form.elements.gpus.value = item.compute.gpus || 0;
    form.elements.gpu_type.value = item.compute.gpu_type || "nvidia.com/gpu";
    form.elements.max_vms.value = item.compute.max_vms;
    form.elements.max_projects.value = item.compute.max_projects;
    form.elements.max_runs.value = item.compute.max_concurrent_runs;
    form.elements.max_functions.value = item.compute.max_functions || 0;
    form.elements.storage_gb.value = item.storage.size_gb;
    form.elements.buckets.value = (item.storage.buckets || []).join(", ");
    form.elements.disabled.checked = item.disabled;
    form.querySelectorAll("[name='services']").forEach(input => { input.checked = item.services.includes(input.value); });
    setResourceProfile(form.elements.profile.value);
    document.querySelector("#access-dialog").showModal();
    return;
  }
  const requestReview = event.target.closest("[data-request-review]");
  if (requestReview) {
    const requestData = (await api("/api/v1/admin/access-requests")).items.find(item => item.id === requestReview.dataset.requestReview);
    if (!requestData) return;
    const form = document.querySelector("#access-form"); form.reset();
    form.elements.original_subject.value = requestData.subject; form.elements.request_id.value = requestData.id;
    form.elements.subject.value = requestData.subject; form.elements.subject.disabled = true;
    form.elements.email.value = requestData.email || "";
    form.elements.profile.value = "starter"; setResourceProfile("starter");
    form.querySelectorAll("[name='services']").forEach(input => { input.checked = requestData.requested_services.includes(input.value); });
    document.querySelector("#access-dialog").showModal(); return;
  }
  const requestReject = event.target.closest("[data-request-reject]");
  if (requestReject) {
    const note = prompt("Why is this request being rejected?") || "";
    await api(`/api/v1/admin/access-requests/${encodeURIComponent(requestReject.dataset.requestReject)}`, {method:"PATCH", body:JSON.stringify({status:"rejected", note})});
    toast("Access request rejected."); await loadAccess(); return;
  }
  const accessDelete = event.target.closest("[data-access-delete]");
  if (accessDelete) {
    if (!confirm(`Revoke all access for ${accessDelete.dataset.accessDelete}?`)) return;
    await api(`/api/v1/admin/users/${encodeURIComponent(accessDelete.dataset.accessDelete)}`, {method:"DELETE"});
    toast("User access revoked."); await loadAccess(); return;
  }
  const bucket = event.target.closest("[data-bucket]");
  if (bucket) { storageState.bucket = bucket.dataset.bucket; storageState.prefix = ""; await loadStorage(); return; }
  const prefix = event.target.closest("[data-prefix]");
  if (prefix) { storageState.prefix = prefix.dataset.prefix; await loadStorage(); return; }
  const object = event.target.closest("[data-object]");
  if (object) {
    const preview = await api(`/api/v1/storage/object?bucket=${encodeURIComponent(storageState.bucket)}&key=${encodeURIComponent(object.dataset.object)}`);
    document.querySelector("#preview-detail").innerHTML = `<p class="eyebrow">OBJECT PREVIEW</p><h2>${escapeHTML(preview.key)}</h2><div class="detail-meta"><span class="tag">${escapeHTML(preview.content_type || "unknown type")}</span><span class="tag">${bytes(preview.size)}</span>${preview.truncated ? `<span class="tag">truncated</span>` : ""}</div><pre class="hint object-preview">${escapeHTML(preview.content)}</pre>`;
    document.querySelector("#preview-dialog").showModal();
    return;
  }
  const chat = event.target.closest("[data-agent-chat]");
  if (chat) { chatState.agentId = chat.dataset.agentChat; chatState.sessionId = ""; document.querySelector("#chat-agent-name").textContent = chat.dataset.agentName; document.querySelector("#chat-log").innerHTML = `<p class="empty">Ask something — the turn runs through the real agent runtime.</p>`; document.querySelector("#chat-dialog").showModal(); return; }
  const traffic = event.target.closest("[data-agent-traffic]");
  if (traffic) {
    trafficState.agentId = traffic.dataset.agentTraffic;
    document.querySelector("#traffic-weight").value = "10";
    document.querySelector("#traffic-value").textContent = "10%";
    document.querySelector("#traffic-error").textContent = "";
    document.querySelector("#traffic-dialog").showModal();
    return;
  }
  const modelTest = event.target.closest("[data-model-test]");
  if (modelTest) { predictState.modelId = modelTest.dataset.modelTest; const model = cachedModels.find(item => item.id === modelTest.dataset.modelTest); document.querySelector("#predict-model-name").textContent = model ? `${model.name} v${model.version}` : "Model"; document.querySelector("#predict-output").textContent = ""; document.querySelector("#predict-dialog").showModal(); return; }
  const fnInvoke = event.target.closest("[data-function-invoke]");
  if (fnInvoke) {
    functionState.name = fnInvoke.dataset.functionInvoke;
    functionState.async = fnInvoke.dataset.functionAsync === "true";
    document.querySelector("#function-name").textContent = `${functionState.async ? "Queue" : "Invoke"} ${functionState.name}`;
    document.querySelector("#function-payload").value = "{}";
    document.querySelector("#function-error").textContent = "";
    document.querySelector("#function-output").textContent = "";
    document.querySelector("#function-dialog").showModal();
    return;
  }
  const fnDelete = event.target.closest("[data-function-delete]");
  if (fnDelete) {
    if (!confirm(`Remove ${fnDelete.dataset.functionDelete}? Pipelines referencing it will no longer run.`)) return;
    await api(`/api/v1/functions/${encodeURIComponent(fnDelete.dataset.functionDelete)}`, {method:"DELETE"});
    toast("Function removed."); await loadFunctions(); return;
  }
  const runDefinition = event.target.closest("[data-run-definition]");
  if (runDefinition) {
    await openSubmitDialog();
    document.querySelector("#submit-project").value = runDefinition.dataset.projectId;
    document.querySelector("#submit-definition").value = runDefinition.dataset.runDefinition;
    return;
  }
  const definitionDetail = event.target.closest("[data-definition-detail]");
  if (definitionDetail && !event.target.closest("button")) {
    const item = pipelineDefinitionCache.find(definition => definition.id === definitionDetail.dataset.definitionDetail);
    if (item) showMetadata("Pipeline flow", `${item.name} v${item.version}`, {...item, created_at:dateTime(item.created_at), updated_at:dateTime(item.updated_at)}, `<h3>Dependency graph</h3>${pipelineGraph(item.jobs, {ariaLabel:`${item.name} dependency graph`})}`);
    return;
  }
  const runRow = event.target.closest("[data-run-id]");
  if (runRow && !runRow.dataset.runAction) { await showRun(runRow.dataset.runId); return; }
  const runAction = event.target.closest("[data-run-action]");
  if (runAction) { await api(`/api/v1/pipelines/runs/${encodeURIComponent(runAction.dataset.runId)}/${runAction.dataset.runAction}`, {method:"POST", body:"{}"}); document.querySelector("#run-dialog").close(); toast(`Run ${runAction.dataset.runAction} requested.`); await Promise.all([loadRuns(),loadDashboard()]); return; }
  const modelAction = event.target.closest("[data-model-action]");
  if (modelAction) {
    const action = modelAction.dataset.modelAction; const id = encodeURIComponent(modelAction.dataset.modelId);
    try {
      if (action === "promote") await api(`/api/v1/models/${id}/promote`, {method:"POST", body:JSON.stringify({stage:"production"})});
      if (action === "deploy") await api(`/api/v1/models/${id}/deploy`, {method:"POST", body:JSON.stringify({canary_weight:0})});
      if (action === "rollback") await api(`/api/v1/models/${id}/rollback`, {method:"POST", body:"{}"});
      toast(`Model ${action} requested.`);
    } catch (failure) { toast(failure.message); }
    await loadModels(); return;
  }
  const connectionTest = event.target.closest("[data-connection-test]");
  if (connectionTest) { await api(`/api/v1/connections/${encodeURIComponent(connectionTest.dataset.connectionTest)}/test`, {method:"POST", body:"{}"}); toast("Connection check completed."); await loadComponents(); return; }

  const configure = event.target.closest("[data-configure-component]");
  if (configure) {
    const presets = {
      "API Gateway": {type:"kubernetes", endpoint:`${location.origin}/api/v1/health`},
      "Pipeline Engine": {type:"prefect", endpoint:"http://prefect-server:4200/api/health"},
      "Experiment Tracker": {type:"mlflow", endpoint:"http://mlflow:5000/health"},
      "Feature Store": {type:"redis", endpoint:"http://feature-gateway:8083/healthz"},
      "Object Store": {type:"s3", endpoint:"http://minio:9000/minio/health/live"},
      "Inference Engine": {type:"kubernetes", endpoint:"http://serving-manager:8085/healthz"},
      "Agent Observability": {type:"langfuse", endpoint:"http://langfuse:3000/api/public/health"},
      "Streaming Broker": {type:"kafka", endpoint:"http://kafka-rest:8082"},
      Kubernetes: {type:"kubernetes", endpoint:"https://kubernetes.default.svc/version"},
    };
    const name = configure.dataset.configureComponent;
    const preset = presets[name] || {type:"kubernetes", endpoint:""};
    const form = document.querySelector("#connection-form");
    form.elements.type.value = preset.type;
    form.elements.name.value = name.toLowerCase().replaceAll(" ", "-");
    form.elements.endpoint.value = preset.endpoint;
    form.elements.secret_ref.value = `${form.elements.name.value}-credentials`;
    document.querySelector("#metadata-dialog").close();
    document.querySelector("#connection-dialog").showModal();
    return;
  }

  const projectDetail = event.target.closest("[data-project-detail]");
  if (projectDetail) {
    const item = projectCache.find(project => project.id === projectDetail.dataset.projectDetail);
    if (item) {
      const clone = item.repository ? `<code>kionga project sync ${escapeHTML(item.id)}</code>` : "";
      const repositoryAction = can("git_write") ? `<button data-project-repository="${escapeHTML(item.id)}">${item.repository ? "Update repository" : "Connect Git repository"}</button>` : "";
      const scaffoldAction = item.scaffold_command ? `<button class="primary" data-copy-project-scaffold="${escapeHTML(item.id)}">Copy scaffold command</button>` : "";
      const scaffoldHelp = item.scaffold_command ? `<div class="scaffold-handoff"><small>Run this command locally to generate the starter workspace.</small><code>${escapeHTML(item.scaffold_command)}</code></div>` : "";
      const actions = `<div class="sheet-actions">${scaffoldAction}${repositoryAction}${item.repository ? `<a class="button-like" href="${escapeHTML(item.repository.url.startsWith("git@") ? "#" : item.repository.url.replace(/\.git$/, ""))}" ${item.repository.url.startsWith("git@") ? "aria-disabled=\"true\"" : "target=\"_blank\" rel=\"noreferrer\""}>Open repository ↗</a>` : ""}</div>${scaffoldHelp}${clone}`;
      showMetadata("Project", item.name, {...item, created_at:dateTime(item.created_at)}, actions);
    }
    return;
  }
  const copyScaffold = event.target.closest("[data-copy-project-scaffold]");
  if (copyScaffold) {
    const item = projectCache.find(project => project.id === copyScaffold.dataset.copyProjectScaffold);
    try {
      await copyText(item?.scaffold_command);
      toast("Scaffold command copied.");
    } catch (failure) { toast(`${failure.message} Select the command and copy it manually.`); }
    return;
  }
  const projectRepository = event.target.closest("[data-project-repository]");
  if (projectRepository) {
    const item = projectCache.find(project => project.id === projectRepository.dataset.projectRepository); if (!item) return;
    const form = document.querySelector("#project-repository-form"); form.elements.project_id.value = item.id; form.elements.url.value = item.repository?.url || ""; form.elements.default_branch.value = item.repository?.default_branch || "main";
    document.querySelector("#project-repository-error").textContent = ""; document.querySelector("#project-repository-dialog").showModal(); return;
  }
  const modelDetail = event.target.closest("[data-model-detail]");
  if (modelDetail) {
    const item = cachedModels.find(model => model.id === modelDetail.dataset.modelDetail);
    if (item) showMetadata("Model", `${item.name} v${item.version}`, {...item, created_at:dateTime(item.created_at)});
    return;
  }
  const agentDetail = event.target.closest("[data-agent-detail]");
  if (agentDetail) {
    const item = agentCache.find(agent => agent.id === agentDetail.dataset.agentDetail);
    if (item) showMetadata("Agent", `${item.name} v${item.version}`, {...item, created_at:dateTime(item.created_at)});
    return;
  }
  const featureDetail = event.target.closest("[data-feature-detail]");
  if (featureDetail) {
    const item = featureCache.find(feature => feature.id === featureDetail.dataset.featureDetail);
    if (item) showMetadata("Feature view", item.name, {...item, created_at:dateTime(item.created_at), materialized_at:dateTime(item.materialized_at)});
    return;
  }
  const streamDetail = event.target.closest("[data-stream-detail]");
  if (streamDetail) {
    const item = realtimeCache.find(stream => stream.key === streamDetail.dataset.streamDetail);
    if (item) showMetadata("Real-time stream", item.title, {key:item.key, flow:item.detail, statistics:item.stats || "No events processed yet"});
    return;
  }
  const catalogDetail = event.target.closest("[data-catalog-detail]");
  if (catalogDetail) {
    const item = catalogCache[Number(catalogDetail.dataset.catalogDetail)];
    if (item) showMetadata("Catalog entry", item.name, item);
    return;
  }
  const componentDetail = event.target.closest("[data-component-detail]");
  if (componentDetail) {
    const item = componentCache[Number(componentDetail.dataset.componentDetail)];
    if (item) {
      const actions = can("connections_write") ? `<div class="form-actions"><button class="primary" data-configure-component="${escapeHTML(item.name)}">Configure connection</button></div>` : "";
      showMetadata("Platform component", item.name, item, actions);
    }
  }
}
document.addEventListener("click", event => {
  handleDynamicClick(event).catch(failure => toast(failure.message));
});

applyPreferences();
loadMe().then(async () => {
  const initial = ["overview","projects","pipelines"].filter(hasService);
  await Promise.all(initial.map(service => viewLoaders[service]()));
  const preferred = readPreferences().startView;
  const first = preferred && (["profile","settings"].includes(preferred) || hasService(preferred)) ? preferred : (initial[0] || (isAdmin() ? "access" : me.services[0]));
  if (first) showView(first);
  if (hasService("overview") && readPreferences().live !== false) connectEvents();
}).catch(error => toast(error.message));
