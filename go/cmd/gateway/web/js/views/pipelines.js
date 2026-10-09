/* Pipelines: reusable flow definitions, their triggers, and run history. */
let pipelineDefinitionCache = [];
let runCache = [];
const pipelineGraph = (jobs, options = {}) => window.KiongaPipelineGraph.render(jobs, options);

const triggerLabels = {manual: "Manual", schedule: "Scheduled", api: "API", event: "Event", retry: "Retry"};
function triggerLabel(run) { return triggerLabels[run.trigger] || (run.parent_run_id ? "Retry" : "Manual"); }

/* Honest schedule summary for a definition: only what is persisted. */
function scheduleSummary(definition) {
  const schedules = (definition.triggers || []).filter(trigger => trigger.type === "schedule");
  if (!schedules.length) return `<span class="tag">Manual / API only</span><span>No schedule configured</span>`;
  return schedules.map(schedule => `<span class="status ${schedule.paused ? "paused" : "active"}">${schedule.paused ? "Paused" : "Active"}</span><code>${escapeHTML(schedule.cron)}</code><span>${escapeHTML(schedule.timezone || "UTC")}</span>${schedule.next_run_at && !schedule.paused ? `<span>Next ${timeTag(schedule.next_run_at)}</span>` : ""}${schedule.last_run_at ? `<span>Last ${timeTag(schedule.last_run_at)}</span>` : ""}`).join("");
}

function definitionCard(definition) {
  const jobs = definition.jobs || definition.nodes || [];
  return `<article class="panel pipeline-definition-card interactive-card" role="button" tabindex="0" data-definition-detail="${escapeHTML(definition.id)}" aria-label="Open flow ${escapeHTML(definition.name)}">
    <div><span class="kind">${escapeHTML(definition.execution_mode)} · v${escapeHTML(definition.version)}</span><h3>${escapeHTML(definition.name)}</h3><p>${plural(jobs.length, "job")} · ${escapeHTML(projectName(definition.project_id))}</p></div>
    ${can("pipelines_write") ? `<button type="button" class="btn-sm" data-run-definition="${escapeHTML(definition.id)}" data-project-id="${escapeHTML(definition.project_id)}">▶ Run</button>` : ""}
    <div class="schedule-line">${scheduleSummary(definition)}</div>
    <div class="definition-graph">${pipelineGraph(jobs, {compact: true, ariaLabel: `${definition.name} dependency graph`})}</div>
  </article>`;
}

function runRow(run) {
  const progress = Math.max(0, Math.min(100, Number(run.progress) || 0));
  return `<tr class="clickable" tabindex="0" data-run-id="${escapeHTML(run.id)}" aria-label="Open run ${escapeHTML(run.name)}">
    <td><div class="cell-primary"><b>${escapeHTML(run.name)}</b><small class="truncate" title="${escapeHTML(run.id)}">${escapeHTML(run.id)}</small></div></td>
    <td data-label="Project"><span class="truncate" title="${escapeHTML(run.project_id)}">${escapeHTML(projectName(run.project_id))}</span></td>
    <td data-label="Trigger"><span class="tag">${escapeHTML(triggerLabel(run))}</span></td>
    <td data-label="Status">${status(run.status)}</td>
    <td data-label="Progress"><div class="progress"><div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${progress}" aria-label="Run progress"><i style="width:${progress}%"></i></div><span>${progress}%</span></div></td>
    <td data-label="Started">${timeTag(run.created_at)}</td>
  </tr>`;
}

async function loadRuns() {
  const [allRuns, definitions] = await Promise.all([api("/api/v1/pipelines/runs"), api("/api/v1/pipelines/definitions")]);
  runCache = scoped(allRuns);
  pipelineDefinitionCache = scoped(definitions.items || []);
  document.querySelector("#pipeline-definition-grid").innerHTML = pipelineDefinitionCache.length
    ? pipelineDefinitionCache.map(definitionCard).join("")
    : emptyState("No reusable flows yet", "A flow is a DAG of container jobs or deployed functions. Define one, then run it on demand or on a schedule.", can("pipelines_write") ? `<button class="primary" type="button" data-open-define-flow>＋ Define flow</button>` : "");
  document.querySelector("#submit-definition").innerHTML = `<option value="">Built-in training pipeline</option>${pipelineDefinitionCache.map(definition => `<option value="${escapeHTML(definition.id)}" data-project="${escapeHTML(definition.project_id)}">${escapeHTML(definition.name)} · v${escapeHTML(definition.version)} · ${escapeHTML(definition.execution_mode)}</option>`).join("")}`;
  document.querySelector("#run-count").textContent = plural(runCache.length, "run");
  document.querySelector("#run-table").innerHTML = runCache.length
    ? runCache.map(runRow).join("")
    : tableEmpty(6, "No runs yet", selectedProject ? "Nothing has run in this project. Use Run pipeline to start one." : "Use Run pipeline to start one, or run a flow above.");
}

let openRunID = "";
let runDetailRequest = 0;
function renderRunDetail(run) {
  const logs = (run.logs || []).map(log => `<div class="log-line"><time datetime="${escapeHTML(log.timestamp)}">${escapeHTML(new Date(log.timestamp).toLocaleTimeString())}</time><b title="${escapeHTML(log.step || "system")}">${escapeHTML(log.step || "system")}</b><span class="sev ${escapeHTML(log.level || "info")}">${escapeHTML(log.level || "info")}</span><span>${escapeHTML(log.message)}</span></div>`).join("") || `<p class="field-help" style="color:var(--code-text)">No logs have arrived yet.</p>`;
  const active = run.status === "queued" || run.status === "running";
  const resumable = (run.status === "failed" || run.status === "cancelled") && run.definition_id;
  const runActions = can("pipelines_write") ? `<div class="sheet-actions">${active ? `<button type="button" class="danger" data-run-action="cancel" data-run-id="${escapeHTML(run.id)}">Cancel run</button>` : ""}${resumable ? `<button type="button" data-rerun-from="${escapeHTML(run.id)}" data-definition-id="${escapeHTML(run.definition_id)}" data-project-id="${escapeHTML(run.project_id)}" title="Reuse succeeded nodes and run the rest again">Rerun from failure</button>` : ""}<button class="primary" type="button" data-run-action="retry" data-run-id="${escapeHTML(run.id)}">Retry whole run</button></div>` : "";
  const provenance = run.provenance || {};
  const overrides = provenance.overrides ? Object.entries({...(provenance.overrides.parameters ? {parameters: provenance.overrides.parameters} : {}), ...(provenance.overrides.nodes ? {nodes: provenance.overrides.nodes} : {}), ...(provenance.overrides.selected_nodes ? {selected_nodes: provenance.overrides.selected_nodes} : {}), ...(provenance.overrides.rerun_from_run_id ? {rerun_from: provenance.overrides.rerun_from_run_id} : {}), ...(provenance.overrides.max_parallelism ? {max_parallelism: provenance.overrides.max_parallelism} : {})}) : [];
  document.querySelector("#run-detail").innerHTML = `<p class="eyebrow">PIPELINE RUN</p><h2>${escapeHTML(run.name)}</h2>
    <div class="detail-meta">${status(run.status)}<span class="tag">${escapeHTML(triggerLabel(run))}</span><span>${Number(run.progress) || 0}% complete</span><span>Started ${timeTag(run.created_at)}</span></div>
    ${metaList([["Run ID", copyable(run.id)], ["Project", escapeHTML(projectName(run.project_id))], ["Definition", escapeHTML(run.definition_id || "Built-in training pipeline")], ["Engine run", run.engine_run_id ? copyable(run.engine_run_id) : "Not yet accepted by an engine"], ["Execution mode", escapeHTML(run.execution_mode || "prefect")], ["Definition revision", provenance.definition_revision ? `r${provenance.definition_revision} · <code title="${escapeHTML(provenance.definition_sha256)}">${escapeHTML((provenance.definition_sha256 || "").slice(0, 12))}</code>` : "Built-in or legacy"], ["Started by", `${escapeHTML(run.owner_subject || "—")} · ${escapeHTML(triggerLabel(run))}${run.scheduled_for ? ` for ${escapeHTML(dateTime(run.scheduled_for))}` : ""}`], ["Policy decision", escapeHTML(provenance.policy_decision || "—")], ["Overrides", overrides.length ? `<pre class="hint metadata-json">${escapeHTML(JSON.stringify(Object.fromEntries(overrides), null, 2))}</pre>` : "None (definition defaults)"]])}
    <h3>Execution graph</h3>${pipelineGraph(run.steps, {ariaLabel: `${run.name} execution graph`})}<div id="run-node-detail"><p class="field-help">Select a node for its timing, attempts and workload.</p></div>
    <h3>Logs</h3><div class="logs" role="log" aria-live="polite">${logs}</div>${runActions}`;
}
async function showRun(runId, options = {}) {
  const dialog = document.querySelector("#run-dialog");
  if (options.open !== false) {
    openRunID = runId;
    document.querySelector("#run-detail").innerHTML = `<div class="skeleton" aria-label="Loading run"></div>`;
    if (!dialog.open) dialog.showModal();
  }
  const request = ++runDetailRequest;
  const run = await api(`/api/v1/pipelines/runs/${encodeURIComponent(runId)}`);
  if (request !== runDetailRequest || openRunID !== runId || !dialog.open) return;
  renderRunDetail(run);
  window.KiongaPipelineGraph.enhance(document.querySelector("#run-detail"), {onSelect: name => showRunNode(run, name)});
}

function duration(step) {
  if (!step.started_at) return "—";
  const end = step.ended_at ? new Date(step.ended_at) : new Date();
  const seconds = Math.max(0, Math.round((end - new Date(step.started_at)) / 1000));
  return seconds < 90 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

let runNodeLogs = null;
function showRunNode(run, name) {
  const step = run.steps.find(item => item.name === name);
  if (!step) return;
  const workload = step.workload_kind ? `${escapeHTML({"docker-container": "Docker container", "k8s-job": "Kubernetes Job", "k8s-pod": "Kubernetes Pod", "openfaas-call": "OpenFaaS function call"}[step.workload_kind] || step.workload_kind)} ${step.workload_id ? `<code>${escapeHTML(step.workload_id)}</code>` : ""}` : "Not started";
  document.querySelector("#run-node-detail").innerHTML = `<div class="panel"><h3 style="margin-top:0">${escapeHTML(step.name)} ${status(step.status)}</h3>${metaList([
    ["Started", step.started_at ? timeTag(step.started_at) : "Not started"],
    ["Ended", step.ended_at ? timeTag(step.ended_at) : "—"],
    ["Duration", duration(step)],
    ["Attempt", step.attempt ? String(step.attempt) : "—"],
    ["Exit code", step.exit_code ?? "—"],
    ["Workload", workload],
    ["Image", step.image_digest ? `<code>${escapeHTML(step.image_digest)}</code>` : `<code>${escapeHTML(step.image || "—")}</code>`],
    ["Depends on", (step.depends_on || []).map(dep => `<span class="tag">${escapeHTML(dep)}</span>`).join("") || "Nothing"],
    ["Last message", step.message ? `<span class="mono">${escapeHTML(step.message.slice(-400))}</span>` : "—"],
  ])}<h4 style="margin-top:var(--space-4)">Logs and events</h4><div id="run-node-logs"></div></div>`;
  runNodeLogs?.close();
  runNodeLogs = createLogPanel(document.querySelector("#run-node-logs"), {run_id: run.id, node: step.name}, {emptyText: "No output recorded for this node yet."});
}
document.querySelector("#run-dialog").addEventListener("close", () => {
  openRunID = ""; runDetailRequest++;
  runNodeLogs?.close(); runNodeLogs = null;
  if (activeView === "pipelines" && selectedResource) { selectedResource = ""; history.replaceState({}, "", routeURL("pipelines", "")); }
});
onLiveUpdate(() => { if (openRunID && document.querySelector("#run-dialog").open) showRun(openRunID, {open: false}).catch(() => {}); });

document.querySelector("#new-pipeline-definition").addEventListener("click", () => openDefinitionEditor().catch(error => toast(error.message, "error")));
onClick("[data-open-define-flow]", () => openDefinitionEditor());

onClick("[data-run-definition]", async node => {
  document.querySelector("#definition-detail-dialog").close();
  await openRunConfig({definitionID: node.dataset.runDefinition, projectID: node.dataset.projectId});
});
onClick("[data-definition-detail]", node => openDefinitionDetail(node.dataset.definitionDetail), {ignoreControls: true});
onClick("[data-run-action]", async node => {
  const action = node.dataset.runAction;
  if (action === "cancel" && !confirm("Cancel this run? Running jobs are stopped and the run is marked cancelled.")) return;
  await withBusy(node, () => api(`/api/v1/pipelines/runs/${encodeURIComponent(node.dataset.runId)}/${action}`, {method: "POST", body: "{}"}), {success: action === "cancel" ? "Cancellation requested." : "Retry submitted."});
  document.querySelector("#run-dialog").close();
  await loadRuns();
});
onClick("tr[data-run-id]", async node => {
  selectedResource = node.dataset.runId;
  history.replaceState({}, "", routeURL("pipelines", selectedResource));
  await showRun(node.dataset.runId);
});

registerView("pipelines", loadRuns, {
  eyebrow: "ORCHESTRATION", title: "Pipelines",
  openResource: id => showRun(id),
  intro: {
    what: "Flows are directed acyclic graphs of container jobs or deployed functions. A run is one execution of a flow.",
    why: "Reproducible training, batch scoring and data jobs with retries, timeouts and logs in one place.",
    needs: "The pipelines service and a project. Container flows need the pipeline runner; function flows need OpenFaaS.",
    action: "Run pipeline submits a run to the execution engine. The run is recorded first, then accepted by the engine.",
    results: "Open a run to see its graph, per-step status and logs. Engine rejections appear as a failed status with the reason.",
  },
});
