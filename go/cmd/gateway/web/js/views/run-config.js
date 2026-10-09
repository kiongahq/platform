/* Manual run configuration: defaults beside one-run overrides, locked fields
 * with reasons, and a server preflight that must pass before submission.
 * The server re-runs the same preflight on submit, so these rules are
 * enforced, not decorative. */
const runConfig = {definition: null, rerunFrom: "", checkedKey: "", checking: 0};

const lockedNote = reason => `<span class="lock" title="${escapeHTML(reason)}">🔒 ${escapeHTML(reason)}</span>`;

function renderRunParameters(definition) {
  const container = document.querySelector("#submit-parameters");
  if (!definition || !Object.keys(definition.parameters || {}).length) {
    const legacy = definition ? "This flow declares no parameters; values are passed through unchecked." : "The built-in training pipeline accepts free-form parameters.";
    container.innerHTML = `<label>Parameters (JSON object)<textarea id="submit-parameters-json" rows="3" spellcheck="false">{}</textarea><small>${escapeHTML(legacy)}</small></label>`;
    return;
  }
  const allowed = new Set(definition.overridable?.parameters || []);
  container.innerHTML = `<div class="table-wrap"><table class="override-table"><thead><tr><th scope="col">Parameter</th><th scope="col">Default</th><th scope="col">This run</th></tr></thead><tbody>${Object.entries(definition.parameters).map(([name, value]) => `<tr><td><code>${escapeHTML(name)}</code></td><td><code>${escapeHTML(JSON.stringify(value))}</code></td><td>${allowed.has(name)
    ? `<label class="sr-only" for="param-${escapeHTML(name)}">${escapeHTML(name)} for this run</label><input id="param-${escapeHTML(name)}" data-param="${escapeHTML(name)}" data-default="${escapeHTML(JSON.stringify(value))}" placeholder="${escapeHTML(String(value))}">`
    : lockedNote("Not overridable in this flow")}</td></tr>`).join("")}</tbody></table></div>`;
}

function renderRunNodes(definition) {
  const section = document.querySelector("#submit-nodes-section");
  const overridable = definition?.overridable || {};
  const anything = overridable.image || overridable.resources || overridable.retries || overridable.timeout || overridable.nodes || overridable.parallelism;
  section.hidden = !definition || !anything;
  document.querySelector("#submit-parallelism-label").hidden = !overridable.parallelism;
  if (section.hidden) return;
  const columns = [overridable.nodes && "Run", "Node", overridable.image && "Image (digest)", overridable.resources && "CPU", overridable.resources && "Memory", (overridable.retries || overridable.timeout) && "Retries", (overridable.retries || overridable.timeout) && "Timeout (s)"].filter(Boolean);
  document.querySelector("#submit-nodes").innerHTML = `<div class="table-wrap"><table class="override-table"><thead><tr>${columns.map(name => `<th scope="col">${escapeHTML(name)}</th>`).join("")}</tr></thead><tbody>${definition.jobs.map(job => `<tr data-node="${escapeHTML(job.name)}">
    ${overridable.nodes ? `<td><input type="checkbox" data-node-run checked aria-label="Run ${escapeHTML(job.name)}"></td>` : ""}
    <td><b>${escapeHTML(job.name)}</b><br><small class="field-help">${escapeHTML(job.kind === "function" ? job.function : job.image)}</small></td>
    ${overridable.image ? `<td>${job.kind === "container" ? `<input data-node-field="image" aria-label="Image for ${escapeHTML(job.name)}" placeholder="registry/name@sha256:…">` : lockedNote("Functions have no image")}</td>` : ""}
    ${overridable.resources ? `<td><input data-node-field="cpu" aria-label="CPU for ${escapeHTML(job.name)}" placeholder="${escapeHTML(job.resources?.cpu || "500m")}"></td><td><input data-node-field="memory" aria-label="Memory for ${escapeHTML(job.name)}" placeholder="${escapeHTML(job.resources?.memory || "1Gi")}"></td>` : ""}
    ${overridable.retries || overridable.timeout ? `<td><input type="number" min="0" max="10" data-node-field="retries" aria-label="Retries for ${escapeHTML(job.name)}" placeholder="${job.retries || 0}"></td><td><input type="number" min="1" max="86400" data-node-field="timeout_seconds" aria-label="Timeout for ${escapeHTML(job.name)}" placeholder="${job.timeout_seconds || 3600}"></td>` : ""}
  </tr>`).join("")}</tbody></table></div>`;
}

function parseLoose(text) {
  try { return JSON.parse(text); } catch { return text; }
}

function runRequest() {
  const form = document.querySelector("#submit-form");
  const request = {project_id: form.elements.project_id.value, name: form.elements.name.value.trim()};
  const definitionID = form.elements.definition_id.value;
  if (definitionID) request.definition_id = definitionID;
  const json = document.querySelector("#submit-parameters-json");
  if (json) {
    try { request.parameters = JSON.parse(json.value || "{}"); }
    catch { throw new Error("Parameters must be a JSON object, for example {\"epochs\": 3}."); }
  }
  const overrides = {};
  const parameters = {};
  document.querySelectorAll("[data-param]").forEach(input => {
    if (input.value.trim() !== "") parameters[input.dataset.param] = parseLoose(input.value.trim());
  });
  if (Object.keys(parameters).length) overrides.parameters = parameters;
  const nodes = {};
  const selected = [];
  let anyUnselected = false;
  document.querySelectorAll("#submit-nodes tr[data-node]").forEach(row => {
    const name = row.dataset.node;
    const override = {};
    const value = field => row.querySelector(`[data-node-field="${field}"]`)?.value.trim();
    if (value("image")) override.image = value("image");
    if (value("cpu") || value("memory")) override.resources = {cpu: value("cpu") || undefined, memory: value("memory") || undefined};
    if (value("retries")) override.retries = Number(value("retries"));
    if (value("timeout_seconds")) override.timeout_seconds = Number(value("timeout_seconds"));
    if (Object.keys(override).length) nodes[name] = override;
    const run = row.querySelector("[data-node-run]");
    if (run) { if (run.checked) selected.push(name); else anyUnselected = true; }
  });
  if (Object.keys(nodes).length) overrides.nodes = nodes;
  if (anyUnselected) overrides.selected_nodes = selected;
  const parallelism = Number(document.querySelector("#submit-parallelism").value);
  if (parallelism) overrides.max_parallelism = parallelism;
  if (runConfig.rerunFrom) overrides.rerun_from_run_id = runConfig.rerunFrom;
  if (Object.keys(overrides).length) request.overrides = overrides;
  return request;
}

function markChanged() {
  document.querySelectorAll("#submit-dialog .override-table td").forEach(cell => {
    const input = cell.querySelector("input:not([type=checkbox])");
    cell.classList.toggle("changed", Boolean(input && input.value.trim()));
  });
}

function invalidateCheck() {
  runConfig.checkedKey = "";
  const button = document.querySelector("#submit-run");
  button.disabled = true;
  button.title = "Check the run first";
}

async function checkRun() {
  const list = document.querySelector("#submit-checks");
  const error = document.querySelector("#submit-error");
  error.textContent = "";
  let request;
  try { request = runRequest(); }
  catch (failure) { error.textContent = failure.message; return null; }
  const ticket = ++runConfig.checking;
  const result = await withBusy(document.querySelector("#submit-check"), () => api("/api/v1/pipelines/preflight", {method: "POST", body: JSON.stringify(request)}), {failure: false});
  if (ticket !== runConfig.checking) return null;
  const changed = (result.fields || []).filter(field => field.overridden);
  list.innerHTML = result.checks.map(check => `<li class="${escapeHTML(check.status)}"><span>${check.node ? `<b>${escapeHTML(check.node)}</b> · ` : ""}${escapeHTML(check.message)}</span></li>`).join("")
    + (changed.length ? `<li class="pass"><span>This run changes: ${changed.map(field => `<code>${escapeHTML(field.node ? `${field.node}.${field.field}` : field.field)}</code>`).join(", ")}. Changes are recorded on the run.</span></li>` : "")
    + (result.reused_nodes?.length ? `<li class="pass"><span>Reused without re-running: ${result.reused_nodes.map(escapeHTML).join(", ")}.</span></li>` : "");
  const button = document.querySelector("#submit-run");
  button.disabled = !result.allowed;
  button.title = result.allowed ? "" : "Resolve the failed checks first";
  runConfig.checkedKey = result.allowed ? JSON.stringify(request) : "";
  return result;
}

async function selectRunDefinition() {
  const id = document.querySelector("#submit-definition").value;
  runConfig.definition = id ? pipelineDefinitionCache.find(item => item.id === id) || await api(`/api/v1/pipelines/definitions/${encodeURIComponent(id)}`) : null;
  if (runConfig.definition) document.querySelector("#submit-project").value = runConfig.definition.project_id;
  renderRunParameters(runConfig.definition);
  renderRunNodes(runConfig.definition);
  document.querySelector("#submit-checks").innerHTML = `<li class="field-help">Run the checks to see quotas, capacity, image rules and what will change.</li>`;
  invalidateCheck();
}

async function openRunConfig({definitionID = "", projectID = "", rerunFrom = ""} = {}) {
  await Promise.all([loadProjectOptions(), loadRuns()]);
  const form = document.querySelector("#submit-form");
  form.reset();
  runConfig.rerunFrom = rerunFrom;
  document.querySelector("#submit-error").textContent = "";
  if (projectID || selectedProject) form.elements.project_id.value = projectID || selectedProject;
  form.elements.definition_id.value = definitionID;
  const note = document.querySelector("#submit-rerun-note");
  note.hidden = !rerunFrom;
  note.textContent = rerunFrom ? `Rerun from failure of ${rerunFrom}: nodes that succeeded there are reused; failed and downstream nodes run again.` : "";
  await selectRunDefinition();
  document.querySelector("#submit-dialog").showModal();
}

document.querySelector("#run-pipeline").addEventListener("click", () => openRunConfig().catch(error => toast(error.message, "error")));
document.querySelector("#submit-definition").addEventListener("change", () => selectRunDefinition().catch(error => toast(error.message, "error")));
document.querySelector("#submit-form").addEventListener("input", () => { markChanged(); invalidateCheck(); });
document.querySelector("#submit-form").addEventListener("change", event => { if (event.target.id !== "submit-definition") invalidateCheck(); });
document.querySelector("#submit-check").addEventListener("click", () => checkRun().catch(failure => { document.querySelector("#submit-error").textContent = failure.message; }));
document.querySelector("#submit-form").addEventListener("submit", async event => {
  event.preventDefault();
  const error = document.querySelector("#submit-error");
  error.textContent = "";
  try {
    const request = runRequest();
    if (runConfig.checkedKey !== JSON.stringify(request)) {
      const result = await checkRun();
      if (!result?.allowed) return;
    }
    const run = await withBusy(event.submitter, () => api("/api/v1/pipelines/submit", {method: "POST", body: JSON.stringify(request)}), {failure: false});
    document.querySelector("#submit-dialog").close();
    toast(run.engine_run_id ? "Run accepted by the engine." : run.status === "failed" ? "The engine rejected the run; open it for the reason." : "Run recorded.");
    await loadRuns();
  } catch (failure) {
    error.textContent = failure.message;
    if (failure.details) document.querySelector("#submit-checks").innerHTML = failure.details.map(check => `<li class="${escapeHTML(check.status)}"><span>${escapeHTML(check.message)}</span></li>`).join("");
  }
});
onClick("[data-rerun-from]", async node => {
  document.querySelector("#run-dialog").close();
  await openRunConfig({definitionID: node.dataset.definitionId, projectID: node.dataset.projectId, rerunFrom: node.dataset.rerunFrom});
});
