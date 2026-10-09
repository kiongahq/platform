/* Pipeline definition editor (form and YAML over one canonical definition)
 * and the flow detail view (graph, schedule, revisions, YAML).
 *
 * The form builds a definition request; the YAML tab holds text. Both are
 * validated by POST /api/v1/pipelines/validate, which returns the canonical
 * YAML, the normalized request, layers and field-level issues. Switching
 * tabs converts through that response, so the two editors never diverge. */
const definitionEditor = {mode: "form", editingID: "", lastValid: null, timer: null, request: 0};

function blankNode(index) {
  return {name: index === 0 ? "extract" : `step-${index + 1}`, kind: "container", image: "python:3.11-slim", command: [], depends_on: [], resources: {cpu: "500m", memory: "1Gi"}, retries: 0, timeout_seconds: 0};
}

function nodeEditorRow(job, index, all) {
  const others = all.filter((_, other) => other !== index);
  const command = Array.isArray(job.command) ? job.command.map(part => (/\s/.test(part) ? JSON.stringify(part) : part)).join(" ") : "";
  return `<fieldset class="node-editor" data-node-index="${index}">
    <legend class="sr-only">Node ${index + 1}</legend>
    <div class="node-head"><b>Node ${index + 1}</b><button type="button" class="danger btn-sm" data-remove-node="${index}" ${all.length === 1 ? "disabled title=\"A flow needs at least one node\"" : ""}>Remove</button></div>
    <label>Node ID<input data-field="name" value="${escapeHTML(job.name)}" required pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?" aria-describedby="node-id-help-${index}"><small id="node-id-help-${index}">Lowercase letters, digits, hyphens.</small></label>
    <label>Type<select data-field="kind"><option value="container" ${job.kind === "container" ? "selected" : ""}>Container</option><option value="function" ${job.kind === "function" ? "selected" : ""}>Function</option></select></label>
    <label ${job.kind === "function" ? "hidden" : ""}>Image<input data-field="image" value="${escapeHTML(job.image || "")}" placeholder="ghcr.io/team/job:1.0"></label>
    <label ${job.kind === "function" ? "" : "hidden"}>Function<select data-field="function">${functionCache.map(fn => `<option ${fn.name === job.function ? "selected" : ""}>${escapeHTML(fn.name)}</option>`).join("") || `<option value="">No functions deployed</option>`}</select></label>
    <label ${job.kind === "function" ? "hidden" : ""}>Command<input data-field="command" value="${escapeHTML(command)}" placeholder='python train.py --epochs 3'><small>Split on spaces; quote arguments with spaces.</small></label>
    <label>CPU<input data-field="cpu" value="${escapeHTML(job.resources?.cpu || "")}" placeholder="500m"></label>
    <label>Memory<input data-field="memory" value="${escapeHTML(job.resources?.memory || "")}" placeholder="1Gi"></label>
    <label>GPUs<input data-field="gpu" type="number" min="0" value="${Number(job.resources?.gpu || 0)}"></label>
    <label>Retries<input data-field="retries" type="number" min="0" max="10" value="${Number(job.retries || 0)}"></label>
    <label>Timeout (s)<input data-field="timeout_seconds" type="number" min="0" max="86400" value="${Number(job.timeout_seconds || 0)}"><small>0 uses the default (1 hour).</small></label>
    <div class="node-deps"><span class="field-help">Runs after</span><div class="checkbox-grid">${others.length ? others.map(other => `<label class="inline-check"><input type="checkbox" data-dep="${escapeHTML(other.name)}" ${(job.depends_on || []).includes(other.name) ? "checked" : ""}> ${escapeHTML(other.name)}</label>`).join("") : `<span class="field-help">No other nodes yet. Nodes without dependencies start immediately.</span>`}</div></div>
  </fieldset>`;
}

function splitCommand(text) {
  const parts = [];
  const pattern = /"((?:[^"\\]|\\.)*)"|(\S+)/g;
  let match;
  while ((match = pattern.exec(text))) parts.push(match[1] !== undefined ? JSON.parse(`"${match[1]}"`) : match[2]);
  return parts;
}

function formJobs() {
  return [...document.querySelectorAll("#definition-nodes .node-editor")].map(row => {
    const value = field => row.querySelector(`[data-field="${field}"]`)?.value ?? "";
    const job = {name: value("name").trim(), kind: value("kind"), depends_on: [...row.querySelectorAll("[data-dep]:checked")].map(input => input.dataset.dep),
      resources: {cpu: value("cpu").trim(), memory: value("memory").trim(), gpu: Number(value("gpu")) || 0},
      retries: Number(value("retries")) || 0, timeout_seconds: Number(value("timeout_seconds")) || 0};
    if (job.kind === "function") job.function = value("function");
    else { job.image = value("image").trim(); job.command = splitCommand(value("command")); }
    return job;
  });
}

function formRequest() {
  const form = document.querySelector("#pipeline-definition-form");
  const request = {
    project_id: form.elements.project_id.value, name: form.elements.name.value.trim(), version: form.elements.version.value.trim(),
    execution_mode: form.elements.execution_mode.value, description: form.elements.description.value.trim(), jobs: formJobs(),
  };
  let parameters = {};
  try { parameters = JSON.parse(document.querySelector("#definition-parameters").value || "{}"); }
  catch { throw new Error("Default parameters must be a JSON object."); }
  if (parameters && typeof parameters === "object" && !Array.isArray(parameters) && Object.keys(parameters).length) request.parameters = parameters;
  if (document.querySelector("#definition-schedule-enabled").checked) {
    request.triggers = [{type: "schedule", cron: document.querySelector("#definition-cron").value.trim(), timezone: document.querySelector("#definition-timezone").value.trim() || "UTC"}];
  }
  const overridable = {
    parameters: csv(document.querySelector("#definition-overridable").value),
    resources: document.querySelector("#definition-override-resources").checked,
    image: document.querySelector("#definition-override-image").checked,
    nodes: document.querySelector("#definition-override-nodes").checked,
    retries: document.querySelector("#definition-override-retries").checked,
    timeout: document.querySelector("#definition-override-retries").checked,
  };
  if (overridable.parameters.length || overridable.resources || overridable.image || overridable.nodes || overridable.retries) request.overridable = overridable;
  return request;
}

function fillForm(request) {
  const form = document.querySelector("#pipeline-definition-form");
  if (request.project_id && [...form.elements.project_id.options].some(option => option.value === request.project_id)) form.elements.project_id.value = request.project_id;
  form.elements.name.value = request.name || "";
  form.elements.version.value = request.version || "";
  form.elements.execution_mode.value = request.execution_mode || "prefect";
  form.elements.description.value = request.description || "";
  const schedule = (request.triggers || []).find(trigger => trigger.type === "schedule");
  document.querySelector("#definition-schedule-enabled").checked = Boolean(schedule);
  document.querySelector("#definition-cron").value = schedule?.cron || "";
  document.querySelector("#definition-timezone").value = schedule?.timezone || "UTC";
  document.querySelector("#definition-parameters").value = JSON.stringify(request.parameters || {}, null, 2);
  const overridable = request.overridable || {};
  document.querySelector("#definition-overridable").value = (overridable.parameters || []).join(", ");
  document.querySelector("#definition-override-resources").checked = Boolean(overridable.resources);
  document.querySelector("#definition-override-image").checked = Boolean(overridable.image);
  document.querySelector("#definition-override-nodes").checked = Boolean(overridable.nodes);
  document.querySelector("#definition-override-retries").checked = Boolean(overridable.retries || overridable.timeout);
  renderNodeRows(request.jobs?.length ? request.jobs : [blankNode(0)]);
}

function renderNodeRows(jobs) {
  document.querySelector("#definition-nodes").innerHTML = jobs.map((job, index) => nodeEditorRow(job, index, jobs)).join("");
}

function showIssues(issues) {
  const list = document.querySelector("#definition-issues");
  document.querySelectorAll(".node-editor.has-issue").forEach(row => row.classList.remove("has-issue"));
  list.innerHTML = issues.map(issue => {
    const where = [issue.node && `node ${issue.node}`, issue.line && `line ${issue.line}`, !issue.node && !issue.line && issue.path].filter(Boolean).join(" · ");
    if (issue.node && definitionEditor.mode === "form") {
      document.querySelectorAll("#definition-nodes .node-editor").forEach(row => {
        if (row.querySelector('[data-field="name"]').value.trim() === issue.node) row.classList.add("has-issue");
      });
    }
    const jump = issue.line && definitionEditor.mode === "yaml" ? `<button type="button" data-jump-line="${issue.line}">Go to line</button>` : "";
    return `<li><span class="where">${escapeHTML(where)}</span><span>${escapeHTML(issue.message)} ${jump}</span></li>`;
  }).join("");
}

async function validateDefinition() {
  const request = ++definitionEditor.request;
  const status = document.querySelector("#pipeline-preview-status");
  let body;
  try {
    body = definitionEditor.mode === "yaml" ? {yaml: document.querySelector("#definition-yaml").value} : {request: formRequest()};
  } catch (error) {
    showIssues([{message: error.message}]);
    status.textContent = "Fix the form"; status.classList.add("invalid");
    return null;
  }
  const result = await api("/api/v1/pipelines/validate", {method: "POST", body: JSON.stringify(body)});
  if (request !== definitionEditor.request) return null;
  showIssues(result.issues || []);
  status.textContent = result.valid ? `${plural(result.request.jobs.length, "node")} · ${plural(result.layers.length, "stage")} · valid` : plural(result.issues.length, "issue");
  status.classList.toggle("invalid", !result.valid);
  const preview = document.querySelector("#pipeline-definition-preview");
  preview.innerHTML = pipelineGraph(result.request.jobs || [], {ariaLabel: "Definition preview"});
  window.KiongaPipelineGraph.enhance(preview);
  if (result.valid) definitionEditor.lastValid = result;
  return result;
}

function scheduleValidation() {
  clearTimeout(definitionEditor.timer);
  definitionEditor.timer = setTimeout(() => validateDefinition().catch(error => showIssues([{message: error.message}])), 250);
}

async function switchEditorMode(mode) {
  if (mode === definitionEditor.mode) return;
  const result = await validateDefinition().catch(() => null);
  if (mode === "yaml") {
    if (result?.yaml) document.querySelector("#definition-yaml").value = result.yaml;
  } else {
    if (!result || (result.issues || []).some(issue => issue.path === "" || issue.path === "apiVersion" || issue.path === "kind")) {
      toast("Fix the YAML syntax before switching to the form.", "error");
      return;
    }
    fillForm(result.request);
  }
  definitionEditor.mode = mode;
  document.querySelector("#definition-form-panel").hidden = mode !== "form";
  document.querySelector("#definition-yaml-panel").hidden = mode !== "yaml";
  document.querySelector("#definition-tab-form").setAttribute("aria-selected", String(mode === "form"));
  document.querySelector("#definition-tab-yaml").setAttribute("aria-selected", String(mode === "yaml"));
  scheduleValidation();
}

async function openDefinitionEditor(definitionID = "") {
  await Promise.all([loadProjectOptions(), hasService("functions") ? loadFunctionsCache().catch(() => {}) : Promise.resolve()]);
  definitionEditor.editingID = definitionID;
  definitionEditor.mode = "form";
  document.querySelector("#definition-form-panel").hidden = false;
  document.querySelector("#definition-yaml-panel").hidden = true;
  document.querySelector("#definition-tab-form").setAttribute("aria-selected", "true");
  document.querySelector("#definition-tab-yaml").setAttribute("aria-selected", "false");
  document.querySelector("#pipeline-definition-error").textContent = "";
  document.querySelector("#definition-message").value = "";
  document.querySelector("#definition-message-label").hidden = !definitionID;
  document.querySelector("#pipeline-definition-title").textContent = definitionID ? "Edit flow" : "Compose a reusable flow";
  if (definitionID) {
    const definition = await api(`/api/v1/pipelines/definitions/${encodeURIComponent(definitionID)}`);
    fillForm({...definition, jobs: definition.jobs});
  } else {
    fillForm({project_id: selectedProject, name: "event-pipeline", version: "1", execution_mode: "prefect", jobs: [
      {...blankNode(0), command: ["python", "-c", "print('extract')"]},
      {...blankNode(1), name: "train", command: ["python", "-c", "print('train')"], depends_on: ["extract"]},
    ]});
  }
  document.querySelector("#pipeline-definition-dialog").showModal();
  scheduleValidation();
}

document.querySelector("#definition-tab-form").addEventListener("click", () => switchEditorMode("form"));
document.querySelector("#definition-tab-yaml").addEventListener("click", () => switchEditorMode("yaml"));
document.querySelector("#pipeline-definition-form").addEventListener("input", scheduleValidation);
document.querySelector("#pipeline-definition-form").addEventListener("change", event => {
  if (event.target.matches('[data-field="kind"], [data-field="name"]')) renderNodeRows(formJobs());
  scheduleValidation();
});
document.querySelector("#definition-add-node").addEventListener("click", () => {
  const jobs = formJobs();
  const node = blankNode(jobs.length);
  if (jobs.length) node.depends_on = [jobs[jobs.length - 1].name];
  renderNodeRows([...jobs, node]);
  scheduleValidation();
  document.querySelector(`#definition-nodes .node-editor:last-child [data-field="name"]`).focus();
});
onClick("[data-remove-node]", node => {
  const jobs = formJobs();
  const [removed] = jobs.splice(Number(node.dataset.removeNode), 1);
  jobs.forEach(job => { job.depends_on = job.depends_on.filter(name => name !== removed.name); });
  renderNodeRows(jobs);
  scheduleValidation();
});
onClick("[data-jump-line]", node => {
  const textarea = document.querySelector("#definition-yaml");
  const lines = textarea.value.split("\n");
  const line = Number(node.dataset.jumpLine) - 1;
  const start = lines.slice(0, line).reduce((total, text) => total + text.length + 1, 0);
  textarea.focus();
  textarea.setSelectionRange(start, start + (lines[line] || "").length);
});

document.querySelector("#pipeline-definition-form").addEventListener("submit", async event => {
  event.preventDefault();
  const error = document.querySelector("#pipeline-definition-error");
  error.textContent = "";
  try {
    const result = await validateDefinition();
    if (!result) throw new Error("Fix the highlighted fields first.");
    if (!result.valid) throw new Error(`Fix ${plural(result.issues.length, "issue")} before saving.`);
    const yaml = definitionEditor.mode === "yaml" ? document.querySelector("#definition-yaml").value : result.yaml;
    const id = definitionEditor.editingID;
    const saved = await withBusy(event.submitter, () => api(id ? `/api/v1/pipelines/definitions/${encodeURIComponent(id)}/yaml` : "/api/v1/pipelines/definitions/yaml", {method: id ? "PUT" : "POST", body: JSON.stringify({yaml, message: document.querySelector("#definition-message").value})}), {failure: false});
    document.querySelector("#pipeline-definition-dialog").close();
    toast(id ? `Saved as revision ${saved.revision}.` : "Flow saved as revision 1.");
    await loadRuns();
  } catch (failure) {
    error.textContent = failure.message;
    if (failure.details) showIssues(failure.details);
  }
});

// ---- flow detail: graph, schedule, revisions, YAML --------------------------

/* Minimal line diff (longest common subsequence). Definitions are small,
 * so the O(n·m) table is fine. */
function lineDiff(before, after) {
  const a = before.split("\n"), b = after.split("\n");
  const table = Array.from({length: a.length + 1}, () => new Array(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) for (let j = b.length - 1; j >= 0; j--) table[i][j] = a[i] === b[j] ? table[i + 1][j + 1] + 1 : Math.max(table[i + 1][j], table[i][j + 1]);
  const out = [];
  let i = 0, j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) { out.push({type: "same", text: a[i]}); i++; j++; }
    else if (table[i + 1][j] >= table[i][j + 1]) out.push({type: "del", text: a[i++]});
    else out.push({type: "add", text: b[j++]});
  }
  while (i < a.length) out.push({type: "del", text: a[i++]});
  while (j < b.length) out.push({type: "add", text: b[j++]});
  return out;
}
const renderDiff = parts => `<div class="diff" role="region" aria-label="Line differences">${parts.map(part => `<div class="${part.type === "same" ? "" : part.type}">${part.type === "add" ? "+ " : part.type === "del" ? "- " : "  "}${escapeHTML(part.text)}</div>`).join("")}</div>`;

const definitionDetail = {id: "", tab: "graph"};
async function openDefinitionDetail(id, tab = "graph") {
  definitionDetail.id = id;
  definitionDetail.tab = tab;
  const dialog = document.querySelector("#definition-detail-dialog");
  const container = document.querySelector("#definition-detail");
  container.innerHTML = `<div class="skeleton" aria-label="Loading flow"></div>`;
  if (!dialog.open) dialog.showModal();
  const [definition, revisions] = await Promise.all([
    api(`/api/v1/pipelines/definitions/${encodeURIComponent(id)}`),
    api(`/api/v1/pipelines/definitions/${encodeURIComponent(id)}/revisions`).catch(() => ({items: []})),
  ]);
  const writable = can("pipelines_write");
  const schedules = (definition.triggers || []).map((trigger, index) => ({...trigger, index})).filter(trigger => trigger.type === "schedule");
  const scheduleBlock = schedules.length ? schedules.map(trigger => `<div class="schedule-line">${status(trigger.paused ? "paused" : "active")}<code>${escapeHTML(trigger.cron)}</code><span>${escapeHTML(trigger.timezone || "UTC")}</span><span>Next: ${trigger.paused ? "paused" : timeTag(trigger.next_run_at)}</span><span>Last: ${trigger.last_run_at ? timeTag(trigger.last_run_at) : "never"}</span>${writable ? `<button type="button" class="btn-sm" data-trigger-action="${trigger.paused ? "resume" : "pause"}" data-trigger-index="${trigger.index}">${trigger.paused ? "Resume" : "Pause"}</button>` : ""}</div>`).join("") : `<div class="schedule-line"><span class="tag">Manual / API only</span><span>No schedule. Edit the flow to add one.</span></div>`;
  const tabs = [["graph", "Graph"], ["revisions", `Revisions (${revisions.items.length})`], ["yaml", "YAML"]];
  let body = "";
  if (tab === "graph") {
    body = `${pipelineGraph(definition.jobs, {ariaLabel: `${definition.name} dependency graph`})}<p class="field-help" id="definition-node-detail">Select a node to see its configuration.</p>`;
  } else if (tab === "revisions") {
    body = revisions.items.length ? revisions.items.map(revision => `<div class="revision-row"><span class="tag">r${revision.revision}</span><div><b>${escapeHTML(revision.message || (revision.revision === 1 ? "Created" : "Updated"))}</b><br><small>${escapeHTML(revision.author)} · ${timeTag(revision.created_at)} · <code title="${escapeHTML(revision.sha256)}">${escapeHTML(revision.sha256.slice(0, 12))}</code></small></div><span class="button-row">${revision.revision !== definition.revision ? `<button type="button" class="btn-sm" data-revision-diff="${revision.revision}">Compare with current</button>${writable ? `<button type="button" class="btn-sm" data-revision-rollback="${revision.revision}">Roll back</button>` : ""}` : `<span class="tag">Current</span>`}</span></div>`).join("") + `<div id="revision-diff"></div>`
      : emptyState("No revision history", "This flow was saved before revisions existed. Its next save creates revision 1.");
  } else {
    const yaml = await api(`/api/v1/pipelines/definitions/${encodeURIComponent(id)}/yaml`);
    body = `<div class="button-row start">${copyButton(yaml.yaml, "Copy YAML")}<a class="button-link btn-sm" download="${escapeHTML(definition.name)}.kionga.yaml" href="data:application/yaml;charset=utf-8,${encodeURIComponent(yaml.yaml)}">Download</a><span class="field-help">Revision ${yaml.revision} · sha256 <code>${escapeHTML(yaml.sha256.slice(0, 16))}…</code></span></div><pre class="yaml-view" tabindex="0">${escapeHTML(yaml.yaml)}</pre><p class="field-help">Commit this file to the project repository (for example <code>pipelines/${escapeHTML(definition.name)}.kionga.yaml</code>) to review changes in Git.</p>`;
  }
  container.innerHTML = `<p class="eyebrow">PIPELINE FLOW</p><h2 id="definition-detail-title">${escapeHTML(definition.name)} <span class="tag">v${escapeHTML(definition.version)} · r${definition.revision || 0}</span></h2>
    ${definition.description ? `<p>${escapeHTML(definition.description)}</p>` : ""}
    <div class="button-row start">${writable ? `<button type="button" class="primary" data-run-definition="${escapeHTML(definition.id)}" data-project-id="${escapeHTML(definition.project_id)}">▶ Run</button><button type="button" data-edit-definition="${escapeHTML(definition.id)}">Edit</button>` : ""}</div>
    ${metaList([["Flow ID", copyable(definition.id)], ["Project", escapeHTML(projectName(definition.project_id))], ["Execution", escapeHTML(definition.execution_mode === "functions" ? "Functions · OpenFaaS" : "Containers · pipeline runner")], ["Nodes", String(definition.jobs.length)], ["Owner", escapeHTML(definition.owner_subject || "—")], ["Updated", timeTag(definition.updated_at)]])}
    <h3>Schedule</h3>${scheduleBlock}
    <div class="tabs" role="tablist">${tabs.map(([key, label]) => `<button type="button" role="tab" aria-selected="${key === tab}" data-definition-tab="${key}">${escapeHTML(label)}</button>`).join("")}</div>
    <div role="tabpanel">${body}</div>`;
  window.KiongaPipelineGraph.enhance(container, {onSelect: name => {
    const job = definition.jobs.find(item => item.name === name);
    if (!job) return;
    document.querySelector("#definition-node-detail").outerHTML = `<div id="definition-node-detail" class="panel">${nodeSummary(job)}</div>`;
  }});
}

function nodeSummary(job) {
  return `<h3 style="margin-top:0">${escapeHTML(job.name)}</h3>${metaList([
    ["Type", escapeHTML(job.kind)],
    [job.kind === "function" ? "Function" : "Image", `<code>${escapeHTML(job.function || job.image)}</code>`],
    ["Command", job.command?.length ? `<code>${escapeHTML(job.command.join(" "))}</code>` : "Image default"],
    ["Depends on", (job.depends_on || []).map(name => `<span class="tag">${escapeHTML(name)}</span>`).join("") || "Nothing (starts immediately)"],
    ["Resources", `${escapeHTML(job.resources?.cpu || "default")} CPU · ${escapeHTML(job.resources?.memory || "default")} memory${job.resources?.gpu ? ` · ${job.resources.gpu} GPU` : ""}`],
    ["Retries", `${job.retries || 0}${job.retry_backoff_seconds ? ` (${job.retry_backoff_seconds}s backoff)` : ""}`],
    ["Timeout", job.timeout_seconds ? `${job.timeout_seconds}s` : "Default (1 hour)"],
    job.when ? ["Runs when", `<code>${escapeHTML(job.when.param)} = ${escapeHTML(job.when.equals)}</code>`] : null,
  ])}`;
}

onClick("[data-definition-tab]", node => openDefinitionDetail(definitionDetail.id, node.dataset.definitionTab));
onClick("[data-edit-definition]", async node => {
  document.querySelector("#definition-detail-dialog").close();
  await openDefinitionEditor(node.dataset.editDefinition);
});
onClick("[data-trigger-action]", async node => {
  const action = node.dataset.triggerAction;
  await withBusy(node, () => api(`/api/v1/pipelines/definitions/${encodeURIComponent(definitionDetail.id)}/triggers/${node.dataset.triggerIndex}/${action}`, {method: "POST", body: "{}"}), {success: action === "pause" ? "Schedule paused." : "Schedule resumed."});
  await Promise.all([openDefinitionDetail(definitionDetail.id, definitionDetail.tab), loadRuns()]);
});
onClick("[data-revision-diff]", async node => {
  const [old, current] = await Promise.all([
    api(`/api/v1/pipelines/definitions/${encodeURIComponent(definitionDetail.id)}/revisions/${node.dataset.revisionDiff}`),
    api(`/api/v1/pipelines/definitions/${encodeURIComponent(definitionDetail.id)}/yaml`),
  ]);
  document.querySelector("#revision-diff").innerHTML = `<h3>r${old.revision} → current</h3>${renderDiff(lineDiff(old.yaml, current.yaml))}`;
});
onClick("[data-revision-rollback]", async node => {
  const revision = node.dataset.revisionRollback;
  if (!confirm(`Roll back to revision ${revision}? This saves revision ${revision}'s content as a new revision; history is kept.`)) return;
  await withBusy(node, () => api(`/api/v1/pipelines/definitions/${encodeURIComponent(definitionDetail.id)}/revisions/${revision}/rollback`, {method: "POST", body: "{}"}), {success: `Rolled back to revision ${revision}.`});
  await Promise.all([openDefinitionDetail(definitionDetail.id, "revisions"), loadRuns()]);
});
