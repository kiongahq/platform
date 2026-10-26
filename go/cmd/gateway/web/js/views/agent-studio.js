/* Agent designer studio: validation, playground runs with a streamed node
 * trace, regression test cases, evaluations, and immutable published
 * versions. Every number shown comes from the gateway; empty states say so. */
const designStudio = {tab: "playground", run: null, source: null, poll: null, sessionID: "", history: [], lastInput: "", lastOutput: "",
  rows: new Map(), fixtures: null, evaluations: [], evaluation: null, compare: [], evalPoll: null, versions: [], validation: null};

function studioDesign() { return agentDesignState.draft; }
function studioSaved() {
  const draft = studioDesign();
  if (!draft?.id) { designMessage("Save the design first; runs, evaluations and versions use saved revisions.", true); return false; }
  if (agentDesignState.dirty) { designMessage("Save your changes first; runs, evaluations and versions use the saved revision.", true); return false; }
  return true;
}
function studioPath(suffix = "") { return `/api/v1/agent-designs/${encodeURIComponent(studioDesign().id)}${suffix}`; }
function studioError(failure) {
  const issues = failure?.body?.issues;
  if (Array.isArray(issues) && issues.length && typeof issues[0] === "object") {
    agentDesignState.issues = issues; designRender(); studioRenderValidation({runnable: false, issues}); studioTab("validation");
  }
  designMessage(failure?.body?.message || failure?.message || "The request failed.", true);
}
function studioNumber(value, digits = 0) { return value === null || value === undefined ? "—" : Number(value).toLocaleString(undefined, {maximumFractionDigits: digits, minimumFractionDigits: digits}); }
function studioPercent(value) { return value === null || value === undefined ? "—" : `${Math.round(value * 1000) / 10}%`; }
function studioCost(value) { return value === null || value === undefined ? "—" : `$${Number(value).toFixed(value && value < 0.01 ? 4 : 2)}`; }
function studioWhen(value) { return value ? new Date(value).toLocaleString() : "—"; }

function studioTab(name) {
  designStudio.tab = name;
  for (const tab of document.querySelectorAll("[data-design-tab]")) tab.setAttribute("aria-selected", String(tab.dataset.designTab === name));
  for (const panel of document.querySelectorAll("[data-design-panel]")) panel.hidden = panel.dataset.designPanel !== name;
  if (name === "tests") studioLoadFixtures();
  if (name === "evaluations") studioLoadEvaluations();
  if (name === "versions") studioLoadVersions();
  if (name === "operations") studioLoadOperations();
}

// Called by the designer whenever a design is opened or reloaded.
function designStudioLoaded() {
  studioStopRun();
  designStudio.fixtures = null; designStudio.evaluations = []; designStudio.evaluation = null; designStudio.compare = []; designStudio.versions = []; designStudio.validation = null;
  designStudio.sessionID = ""; designStudio.history = []; designStudio.rows = new Map();
  document.querySelector("#design-chat-log").innerHTML = `<p class="field-help">Save the design, then send a message to run it. Each run executes in the isolated agent runtime with your project access.</p>`;
  document.querySelector("#design-run-timeline").innerHTML = "";
  document.querySelector("#design-run-summary").textContent = "No run yet.";
  document.querySelector("#design-run-save-case").disabled = true;
  studioRenderStopOptions();
  studioRenderValidation(null);
  studioLoadOverview();
  if (designStudio.tab !== "playground") studioTab(designStudio.tab);
}
function studioRenderStopOptions() {
  const draft = studioDesign(), select = document.querySelector("#design-run-stop");
  if (!draft) return;
  select.innerHTML = `<option value="">Output</option>${draft.nodes.filter(node => !["input", "output"].includes(node.kind)).map(node => `<option value="${escapeHTML(node.id)}">${escapeHTML(node.name)} (${escapeHTML(node.id)})</option>`).join("")}`;
}

async function studioLoadOverview() {
  const target = document.querySelector("#design-kpis"), project = studioDesign()?.project_id || "";
  try {
    const data = await api(`/api/v1/agent-designs/overview${project ? `?project_id=${encodeURIComponent(project)}` : ""}`);
    const cards = [["Designs", studioNumber(data.designs)], ["Published versions", studioNumber(data.versions)], ["Deployments", studioNumber(data.deployments)],
      [`Runs (${data.window_hours}h)`, studioNumber(data.runs)], ["Success rate", studioPercent(data.success_rate)], ["P95 latency", data.p95_latency_ms === null ? "—" : `${studioNumber(data.p95_latency_ms)} ms`], [`Cost (${data.window_hours}h)`, studioCost(data.cost_usd)]];
    target.innerHTML = cards.map(([label, value]) => `<div class="design-kpi"><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong></div>`).join("") + (data.runs ? "" : `<p class="field-help design-kpi-empty">No designer runs in this window yet; metrics appear after the first run.</p>`);
  } catch { target.innerHTML = `<p class="field-help">Agent metrics are unavailable.</p>`; }
}

// --- validation ---
async function studioValidate() {
  const draft = studioDesign();
  if (!draft) return null;
  const body = JSON.stringify({project_id: draft.project_id, nodes: draft.nodes, edges: draft.edges, ...(draft.settings ? {settings: draft.settings} : {})});
  try {
    const result = await api(draft.id ? studioPath("/validate") : "/api/v1/agent-designs/validate", {method: "POST", body});
    agentDesignState.issues = result.issues || [];
    designStudio.validation = result;
    designRender(); studioRenderValidation(result);
    const errors = agentDesignState.issues.filter(issue => issue.severity === "error").length;
    designMessage(result.runnable ? `Valid: the graph can run${agentDesignState.issues.length ? ` (${agentDesignState.issues.length} warning${agentDesignState.issues.length === 1 ? "" : "s"})` : ""}.` : `${errors} error${errors === 1 ? "" : "s"} must be fixed before running or publishing.`, !result.runnable);
    return result;
  } catch (failure) { studioError(failure); return null; }
}
function studioRenderValidation(result) {
  const target = document.querySelector("#design-validation");
  if (!result) { target.innerHTML = `<p class="field-help">Validate checks types, connections, settings and that every model, tool, knowledge base, dataset and API connection is available to this project.</p>`; return; }
  const issues = result.issues || [];
  target.innerHTML = `<p class="design-validation-status ${result.runnable ? "ok" : "error"}">${result.runnable ? "Runnable" : "Not runnable"}${result.content_sha256 ? ` · behavior hash <code>${escapeHTML(result.content_sha256.slice(0, 12))}</code>` : ""}</p>${issues.length ? `<ul class="design-issue-list">${issues.map(issue => `<li class="${escapeHTML(issue.severity)}"><span class="tag">${escapeHTML(issue.severity)}</span> ${escapeHTML(issue.message)}${issue.node_id ? ` <button type="button" data-design-issue-node="${escapeHTML(issue.node_id)}">Show node</button>` : ""}</li>`).join("")}</ul>` : `<p class="field-help">No issues found.</p>`}`;
}
document.querySelector("#design-validation").addEventListener("click", event => {
  const button = event.target.closest("[data-design-issue-node]");
  if (button) designSelect(button.dataset.designIssueNode);
});

// --- playground ---
function studioStopRun() {
  designStudio.source?.close?.(); designStudio.source = null;
  clearTimeout(designStudio.poll); designStudio.poll = null;
  designStudio.run = null;
  document.querySelector("#design-run-start").disabled = !can("agents_write");
  document.querySelector("#design-run-cancel").disabled = true;
}
function studioChat(role, text, extra = "") {
  const log = document.querySelector("#design-chat-log");
  if (log.querySelector(".field-help")) log.innerHTML = "";
  log.insertAdjacentHTML("beforeend", `<div class="design-chat-turn ${role}"><span>${role === "user" ? "You" : role === "agent" ? "Agent" : "Run"}</span><p>${escapeHTML(text)}</p>${extra}</div>`);
  log.scrollTop = log.scrollHeight;
}
// Live model tokens: one streaming bubble per run, replaced by the final
// reply when run.completed arrives (deltas are best effort).
function studioDelta(event) {
  if (!event?.delta) return;
  const log = document.querySelector("#design-chat-log");
  let bubble = log.querySelector(".design-chat-turn.streaming");
  if (!bubble) {
    if (log.querySelector(".field-help")) log.innerHTML = "";
    log.insertAdjacentHTML("beforeend", `<div class="design-chat-turn agent streaming" aria-live="off"><span>Agent · streaming from ${escapeHTML(event.node_id || "model")}</span><p></p></div>`);
    bubble = log.querySelector(".design-chat-turn.streaming");
  }
  bubble.querySelector("p").textContent += event.delta;
  log.scrollTop = log.scrollHeight;
}
// An approval node is waiting: approve or reject from the chat.
function studioApprovalCard(event) {
  const log = document.querySelector("#design-chat-log");
  if (log.querySelector(".field-help")) log.innerHTML = "";
  const writable = can("agents_write");
  log.insertAdjacentHTML("beforeend", `<div class="design-chat-turn approval" data-approval-card="${escapeHTML(event.approval_id)}" role="group" aria-label="Approval request from ${escapeHTML(event.node_id)}">
    <span>Approval needed · ${escapeHTML(event.node_id)}</span><p>${escapeHTML(event.output_preview || "Approve this step?")}</p>
    <strong class="design-approval-state">Waiting for a decision</strong>
    <div class="design-approval-actions"><input aria-label="Comment for this decision" maxlength="1000" placeholder="Comment (optional)"><button type="button" class="primary" data-approval-decision="approve" ${writable ? "" : "disabled"}>Approve</button><button type="button" data-approval-decision="reject" ${writable ? "" : "disabled"}>Reject</button></div></div>`);
  log.scrollTop = log.scrollHeight;
}
document.querySelector("#design-chat-log").addEventListener("click", async event => {
  const button = event.target.closest("[data-approval-decision]");
  if (!button) return;
  const card = button.closest("[data-approval-card]");
  for (const control of card.querySelectorAll("button")) control.disabled = true;
  try {
    const decided = await api(`/api/v1/agent-approvals/${encodeURIComponent(card.dataset.approvalCard)}/decision`, {method: "POST",
      body: JSON.stringify({decision: button.dataset.approvalDecision, comment: card.querySelector("input")?.value || ""})});
    card.querySelector(".design-approval-state").textContent = decided.status === "approved" ? "Approved" : "Rejected";
    card.querySelector(".design-approval-actions")?.remove();
  } catch (failure) {
    designMessage(failure.status === 409 ? "That request was already decided or has expired." : failure.message, true);
    for (const control of card.querySelectorAll("button")) control.disabled = !can("agents_write");
  }
});
function studioRenderTimeline() {
  document.querySelector("#design-run-timeline").innerHTML = [...designStudio.rows.values()].map(row => `<tr class="run-${escapeHTML(row.status)}"><th scope="row">${escapeHTML(row.name)}<small>${escapeHTML(row.kind || "")}${row.port ? ` → ${escapeHTML(row.port)}` : ""}${row.attempt > 1 ? ` · attempt ${row.attempt}` : ""}</small></th><td><span class="status ${escapeHTML(row.status)}">${escapeHTML(row.status)}</span>${row.error ? `<small>${escapeHTML(row.error)}</small>` : ""}</td><td>${row.duration === undefined ? "—" : `${studioNumber(row.duration)} ms`}</td><td>${row.usage ? `${studioNumber(row.usage.input_tokens)} / ${studioNumber(row.usage.output_tokens)}` : "—"}</td><td><details><summary>${escapeHTML((row.output || row.input || "").slice(0, 60) || "—")}</summary>${row.input ? `<p><strong>Input</strong></p><pre>${escapeHTML(row.input)}</pre>` : ""}${row.output ? `<p><strong>Output</strong></p><pre>${escapeHTML(row.output)}</pre>` : ""}</details></td></tr>`).join("");
}
const studioNodeStatus = {"node.started": "running", "node.retrying": "retrying", "node.completed": "succeeded", "node.failed": "failed", "node.skipped": "skipped"};
function studioHandleEvent(event) {
  if (!event || typeof event !== "object") return;
  if (event.node_id) {
    const node = studioDesign()?.nodes.find(item => item.id === event.node_id);
    const row = designStudio.rows.get(event.node_id) || {name: node?.name || event.node_id, kind: event.kind};
    row.status = studioNodeStatus[event.type] || row.status;
    if (event.attempt) row.attempt = event.attempt;
    if (event.duration_ms !== undefined) row.duration = event.duration_ms;
    if (event.input_preview) row.input = event.input_preview;
    if (event.output_preview) row.output = event.output_preview;
    if (event.usage) row.usage = event.usage;
    if (event.port) row.port = event.port;
    if (event.error) row.error = event.error.message;
    designStudio.rows.set(event.node_id, row);
    agentDesignState.nodeStatus[event.node_id] = row.status;
    studioRenderTimeline();
    const card = document.querySelector(`[data-design-node="${CSS.escape(event.node_id)}"]`);
    if (card) { card.className = card.className.replace(/\brun-\S+/g, "").trim() + ` run-${row.status}`; }
  }
  const summary = document.querySelector("#design-run-summary");
  if (event.type === "run.started") summary.textContent = "Running…";
  if (event.type === "approval.requested") studioApprovalCard(event);
  if (event.type === "approval.resolved") {
    const card = document.querySelector(`[data-approval-card="${CSS.escape(event.approval_id || "")}"]`);
    if (card) { card.querySelector(".design-approval-actions")?.remove(); card.querySelector(".design-approval-state").textContent = event.port === "approved" ? "Approved" : "Rejected or expired"; }
  }
  if (event.type === "run.completed") {
    document.querySelector("#design-chat-log .design-chat-turn.streaming")?.remove();
    const usage = event.usage || {};
    summary.textContent = `Succeeded in ${studioNumber(event.duration_ms)} ms · ${studioNumber(usage.input_tokens)} input / ${studioNumber(usage.output_tokens)} output tokens · ${studioCost(usage.cost_usd)}`;
    designStudio.lastOutput = event.output || "";
    designStudio.history.push({role: "user", text: designStudio.lastInput}, {role: "assistant", text: designStudio.lastOutput});
    designStudio.history = designStudio.history.slice(-50);
    studioChat("agent", event.output || "(empty reply)");
    document.querySelector("#design-run-save-case").disabled = !can("agents_write");
  }
  if (event.type === "run.failed" || event.type === "run.cancelled") {
    document.querySelector("#design-chat-log .design-chat-turn.streaming")?.remove();
    const message = event.error?.message || (event.type === "run.cancelled" ? "The run was cancelled." : "The run failed.");
    summary.textContent = message;
    studioChat("system", message);
  }
}
function studioFollow(path, runID) {
  const eventsPath = `${path}/runs/${encodeURIComponent(runID)}/events`;
  let after = -1, finished = false;
  const done = () => { finished = true; studioStopRun(); studioLoadOverview(); };
  const poll = async () => {
    if (finished || designStudio.run !== runID) return;
    try {
      const page = await api(`${eventsPath}?after=${after}`);
      for (const event of page.events || []) { after = Math.max(after, event.seq); studioHandleEvent(event); }
      if (page.done) { done(); return; }
    } catch (failure) { designMessage(failure.message, true); }
    designStudio.poll = setTimeout(poll, 500);
  };
  if (typeof EventSource !== "function" || typeof EventSource.prototype?.addEventListener !== "function") { poll(); return; }
  const source = new EventSource(eventsPath);
  designStudio.source = source;
  source.addEventListener("run", message => { try { const event = JSON.parse(message.data); after = Math.max(after, event.seq); studioHandleEvent(event); } catch { /* ignore malformed frame */ } });
  source.addEventListener("delta", message => { try { studioDelta(JSON.parse(message.data)); } catch { /* ignore malformed frame */ } });
  source.addEventListener("end", () => { source.close(); done(); });
  source.onerror = () => { if (finished) return; source.close(); designStudio.source = null; poll(); };
}
async function studioStartRun({input, stopAfter = "", versionID = ""}) {
  const draft = studioDesign();
  designStudio.rows = new Map(); agentDesignState.nodeStatus = {}; studioRenderTimeline(); designRender();
  designStudio.lastInput = input;
  designStudio.sessionID ||= `play-${Math.random().toString(36).slice(2, 12)}`;
  const path = versionID ? `/api/v1/agent-versions/${encodeURIComponent(versionID)}/runs` : studioPath("/runs");
  const body = {input, session_id: designStudio.sessionID, history: designStudio.history, allow_side_effects: document.querySelector("#design-run-side-effects").checked, stop_after: stopAfter, revision: draft.revision};
  document.querySelector("#design-run-start").disabled = true;
  try {
    const run = await api(path, {method: "POST", body: JSON.stringify(body), headers: {"Idempotency-Key": `ui-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`}});
    designStudio.run = run.id;
    document.querySelector("#design-run-cancel").disabled = false;
    studioChat("user", input, versionID ? `<small>Published version run</small>` : stopAfter ? `<small>Stops after ${escapeHTML(stopAfter)}</small>` : "");
    document.querySelector("#design-run-summary").textContent = "Starting…";
    studioFollow(studioPath(), run.id);
  } catch (failure) { studioStopRun(); studioError(failure); }
}
document.querySelector("#design-run-form").addEventListener("submit", event => {
  event.preventDefault();
  if (!can("agents_write") || designStudio.run || !studioSaved()) return;
  const input = document.querySelector("#design-run-input").value.trim();
  if (!input) { designMessage("Enter a message to run the agent.", true); return; }
  studioStartRun({input, stopAfter: document.querySelector("#design-run-stop").value});
});
document.querySelector("#design-run-cancel").addEventListener("click", async () => {
  if (!designStudio.run) return;
  try { await api(studioPath(`/runs/${encodeURIComponent(designStudio.run)}/cancel`), {method: "POST", body: "{}"}); designMessage("Cancelling the run…"); }
  catch (failure) { studioError(failure); }
});
document.querySelector("#design-run-reset").addEventListener("click", () => {
  designStudio.sessionID = ""; designStudio.history = [];
  document.querySelector("#design-chat-log").innerHTML = `<p class="field-help">New session. Memory nodes start empty.</p>`;
});
document.querySelector("#design-run-save-case").addEventListener("click", async () => {
  if (!studioSaved() || !designStudio.lastInput) return;
  await studioLoadFixtures();
  const cases = designStudio.fixtures.cases;
  const words = designStudio.lastOutput.split(/\s+/).filter(word => word.length > 4).slice(0, 3);
  cases.push({id: `case-${cases.length + 1}-${Math.random().toString(36).slice(2, 6)}`, input: designStudio.lastInput, expected: {contains: words}});
  studioTab("tests");
  designMessage("Test case added from the last run. Review the expected text, then save the test cases.");
});
document.querySelector("#design-test").addEventListener("click", () => { studioTab("playground"); document.querySelector("#design-run-input").focus(); });
document.querySelector("#design-validate").addEventListener("click", async () => { studioTab("validation"); await studioValidate(); });

// --- test cases ---
async function studioLoadFixtures(force = false) {
  const target = document.querySelector("#design-tests");
  if (!studioDesign()?.id) { target.innerHTML = `<p class="field-help">Save the design to add regression test cases.</p>`; return; }
  if (!designStudio.fixtures || force) {
    try { designStudio.fixtures = await api(studioPath("/fixtures")); } catch (failure) { target.innerHTML = `<p class="field-help error">${escapeHTML(failure.message)}</p>`; return; }
  }
  studioRenderFixtures();
}
function studioRenderFixtures() {
  const fixtures = designStudio.fixtures, writable = can("agents_write");
  document.querySelector("#design-tests").innerHTML = `<p class="field-help">Cases are stored with the design and copied into every evaluation, so results stay reproducible. Matching ignores case. Judge criteria (groundedness, task_completion, relevance, safety) are scored by the runner's model; a case fails if the judge is unavailable or a score is below the minimum.</p><datalist id="design-judge-criteria"><option value="groundedness"><option value="task_completion"><option value="relevance"><option value="safety"></datalist>
    <div class="table-wrap"><table class="data-table design-case-table"><thead><tr><th scope="col">ID</th><th scope="col">Input</th><th scope="col">Must contain (comma separated)</th><th scope="col">Must not contain</th><th scope="col">Max ms</th><th scope="col">Must call (node IDs)</th><th scope="col">Judge criteria</th><th scope="col">Min score</th><th scope="col">Tags</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead><tbody>
    ${fixtures.cases.map((item, index) => `<tr data-case-index="${index}"><td><input aria-label="Case ID" data-case-field="id" value="${escapeHTML(item.id)}" ${writable ? "" : "disabled"}></td><td><textarea aria-label="Case input" data-case-field="input" rows="2" ${writable ? "" : "disabled"}>${escapeHTML(item.input)}</textarea></td><td><input aria-label="Must contain" data-case-field="contains" value="${escapeHTML((item.expected?.contains || []).join(", "))}" ${writable ? "" : "disabled"}></td><td><input aria-label="Must not contain" data-case-field="not_contains" value="${escapeHTML((item.expected?.not_contains || []).join(", "))}" ${writable ? "" : "disabled"}></td><td><input aria-label="Maximum latency in milliseconds" type="number" min="0" data-case-field="max_latency_ms" value="${escapeHTML(item.expected?.max_latency_ms || "")}" ${writable ? "" : "disabled"}></td><td><input aria-label="Nodes the run must complete" data-case-field="calls" placeholder="lookup, refund" value="${escapeHTML((item.expected?.calls || []).join(", "))}" ${writable ? "" : "disabled"}></td><td><input aria-label="Judge criteria" data-case-field="judge_criteria" list="design-judge-criteria" placeholder="groundedness, safety" value="${escapeHTML((item.expected?.judge?.criteria || []).join(", "))}" ${writable ? "" : "disabled"}></td><td><input aria-label="Minimum judge score" type="number" min="0" max="1" step="0.05" data-case-field="judge_min" placeholder="0.7" value="${escapeHTML(item.expected?.judge?.min_score ?? "")}" ${writable ? "" : "disabled"}></td><td><input aria-label="Tags for slicing results" data-case-field="tags" placeholder="billing" value="${escapeHTML((item.tags || []).join(", "))}" ${writable ? "" : "disabled"}></td><td><button type="button" data-case-remove="${index}" ${writable ? "" : "disabled"}>Remove</button></td></tr>`).join("") || `<tr><td colspan="10">No test cases yet. Add one, or save one from a playground run.</td></tr>`}
    </tbody></table></div><div class="form-actions"><button type="button" id="design-case-add" ${writable ? "" : "disabled"}>Add case</button><button type="button" class="primary" id="design-case-save" ${writable ? "" : "disabled"}>Save test cases</button></div>`;
}
document.querySelector("#design-tests").addEventListener("input", event => {
  const row = event.target.closest("[data-case-index]"), field = event.target.dataset.caseField;
  if (!row || !field) return;
  const item = designStudio.fixtures.cases[Number(row.dataset.caseIndex)];
  item.expected ||= {};
  const list = value => value.split(",").map(part => part.trim()).filter(Boolean);
  if (field === "id" || field === "input") item[field] = event.target.value;
  else if (field === "max_latency_ms") { const value = Number(event.target.value); if (value > 0) item.expected.max_latency_ms = value; else delete item.expected.max_latency_ms; }
  else if (field === "tags") { item.tags = list(event.target.value); if (!item.tags.length) delete item.tags; }
  else if (field === "judge_criteria") {
    const criteria = list(event.target.value);
    if (criteria.length) item.expected.judge = {...(item.expected.judge || {}), criteria}; else delete item.expected.judge;
  } else if (field === "judge_min") {
    if (!item.expected.judge) return;
    const value = event.target.value === "" ? null : Number(event.target.value);
    if (value === null) delete item.expected.judge.min_score; else item.expected.judge.min_score = value;
  } else item.expected[field] = list(event.target.value);
});
document.querySelector("#design-tests").addEventListener("click", async event => {
  if (event.target.closest("[data-case-remove]")) { designStudio.fixtures.cases.splice(Number(event.target.closest("[data-case-remove]").dataset.caseRemove), 1); studioRenderFixtures(); return; }
  if (event.target.id === "design-case-add") { designStudio.fixtures.cases.push({id: `case-${designStudio.fixtures.cases.length + 1}`, input: "", expected: {contains: []}}); studioRenderFixtures(); return; }
  if (event.target.id === "design-case-save") {
    try {
      designStudio.fixtures = await api(studioPath("/fixtures"), {method: "PUT", body: JSON.stringify({revision: designStudio.fixtures.revision, cases: designStudio.fixtures.cases})});
      studioRenderFixtures(); designMessage(`Saved ${designStudio.fixtures.cases.length} test case${designStudio.fixtures.cases.length === 1 ? "" : "s"}.`);
    } catch (failure) {
      if (failure.status === 409) { designMessage("Test cases changed elsewhere. Reloaded the latest cases; reapply your edits.", true); await studioLoadFixtures(true); return; }
      designMessage((failure.body?.issues || [failure.message]).join(" "), true);
    }
  }
});

// --- evaluations ---
async function studioLoadEvaluations() {
  const target = document.querySelector("#design-evaluations");
  if (!studioDesign()?.id) { target.innerHTML = `<p class="field-help">Save the design to evaluate it.</p>`; return; }
  try {
    const [evaluations, versions] = await Promise.all([api(studioPath("/evaluations")), api(studioPath("/versions"))]);
    designStudio.evaluations = evaluations.items || []; designStudio.versions = versions.items || [];
  } catch (failure) { target.innerHTML = `<p class="field-help error">${escapeHTML(failure.message)}</p>`; return; }
  studioRenderEvaluations();
}
function studioEvaluationTarget(item) { return item.version_id ? `v${designStudio.versions.find(version => version.id === item.version_id)?.version ?? "?"}` : `r${item.revision}`; }
function studioRenderEvaluations() {
  const writable = can("agents_write"), list = designStudio.evaluations;
  const reasons = item => Object.entries(item.failure_reasons || {}).map(([reason, count]) => `${reason.replace(/_/g, " ")} ×${count}`).join(", ") || "—";
  const completed = list.filter(item => item.status === "completed").slice().reverse();
  const trend = completed.length ? `<figure class="design-trend" aria-label="Pass rate by evaluation, oldest first"><figcaption>Pass rate over time</figcaption><div>${completed.map(item => `<span style="height:${Math.max(4, Math.round(item.pass_rate * 100))}%" class="${item.passed ? "pass" : "fail"}" title="${escapeHTML(studioEvaluationTarget(item))}: ${studioPercent(item.pass_rate)}"><i>${escapeHTML(studioEvaluationTarget(item))}</i></span>`).join("")}</div></figure>` : "";
  const detail = designStudio.evaluation ? studioRenderEvaluationDetail(designStudio.evaluation) : "";
  document.querySelector("#design-evaluations").innerHTML = `<form id="design-eval-form" class="design-eval-form"><label>Evaluate <select id="design-eval-target"><option value="">Saved revision r${studioDesign().revision}</option>${designStudio.versions.map(version => `<option value="${escapeHTML(version.id)}">Published v${version.version}</option>`).join("")}</select></label><label>Quality gate (% of cases) <input id="design-eval-gate" type="number" min="0" max="100" value="80"></label><label class="checkbox"><input type="checkbox" id="design-eval-side-effects"> Allow side effects</label><button class="primary" type="submit" ${writable ? "" : "disabled"}>Run evaluation</button></form>
    ${trend}
    <div class="table-wrap"><table class="data-table"><thead><tr><th scope="col"><span class="sr-only">Compare</span></th><th scope="col">Started</th><th scope="col">Target</th><th scope="col">Status</th><th scope="col">Pass rate</th><th scope="col">Gate</th><th scope="col">P95</th><th scope="col">Cost</th><th scope="col">Judge (mean)</th><th scope="col">Tool accuracy</th><th scope="col">Failures</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead><tbody>
    ${list.map(item => `<tr><td><input type="checkbox" aria-label="Compare evaluation ${escapeHTML(item.id)}" data-eval-compare="${escapeHTML(item.id)}" ${designStudio.compare.includes(item.id) ? "checked" : ""} ${item.status === "completed" ? "" : "disabled"}></td><td>${escapeHTML(studioWhen(item.created_at))}</td><td>${escapeHTML(studioEvaluationTarget(item))}</td><td><span class="status ${escapeHTML(item.status)}">${escapeHTML(item.status)}</span> ${item.completed}/${item.case_count}</td><td>${studioPercent(item.pass_rate)}</td><td>${studioPercent(item.gate)} ${item.status === "completed" ? `<span class="tag ${item.passed ? "ok" : "error"}">${item.passed ? "passed" : "failed"}</span>` : ""}</td><td>${studioNumber(item.p95_latency_ms)} ms</td><td>${studioCost(item.cost_usd)}</td><td>${escapeHTML(studioJudgeSummary(item.judge_scores))}</td><td>${item.tool_accuracy === undefined || item.tool_accuracy === null ? "—" : studioPercent(item.tool_accuracy)}</td><td>${escapeHTML(reasons(item))}</td><td><button type="button" data-eval-view="${escapeHTML(item.id)}">View</button></td></tr>`).join("") || `<tr><td colspan="12">No evaluations yet. Add test cases, then run an evaluation.</td></tr>`}
    </tbody></table></div>${designStudio.compare.length === 2 ? `<button type="button" id="design-eval-compare">Compare selected</button>` : `<p class="field-help">Select two completed evaluations to compare them case by case.</p>`}<div id="design-eval-detail">${detail}</div>`;
}
function studioRenderEvaluationDetail(item) {
  return `<h3>Evaluation of ${escapeHTML(studioEvaluationTarget(item))} · ${studioPercent(item.pass_rate)} passed · gate ${studioPercent(item.gate)}</h3><p class="field-help">Behavior hash <code>${escapeHTML((item.content_sha256 || "").slice(0, 12))}</code> · ${studioNumber(item.input_tokens)} input / ${studioNumber(item.output_tokens)} output tokens · ${studioCost(item.cost_usd)}${item.judge ? ` · judged by ${escapeHTML(item.judge.backend)}/${escapeHTML(item.judge.model)}, rubric v${escapeHTML(item.judge.rubric_version)}` : ""}</p>
    ${studioSlices(item.slices)}
    <div class="table-wrap"><table class="data-table"><thead><tr><th scope="col">Case</th><th scope="col">Input</th><th scope="col">Expected</th><th scope="col">Reply</th><th scope="col">Result</th><th scope="col">Judge</th><th scope="col">Time</th><th scope="col">Review</th></tr></thead><tbody>${(item.cases || []).map(result => `<tr class="${result.passed ? "pass" : "fail"}"><th scope="row">${escapeHTML(result.id)}</th><td>${escapeHTML(result.input.slice(0, 160))}</td><td>${escapeHTML([...(result.expected?.contains || []).map(text => `contains “${text}”`), ...(result.expected?.not_contains || []).map(text => `not “${text}”`), result.expected?.equals ? `equals “${result.expected.equals}”` : "", result.expected?.matches ? `matches /${result.expected.matches}/` : "", result.expected?.max_latency_ms ? `≤ ${result.expected.max_latency_ms} ms` : "", ...(result.expected?.calls || []).map(call => `calls ${call}`), result.expected?.judge ? `judge ≥ ${result.expected.judge.min_score ?? 0.7}` : ""].filter(Boolean).join("; ") || "completes")}</td><td><details><summary>${escapeHTML((result.output || "—").slice(0, 60))}</summary><pre>${escapeHTML(result.output || "")}</pre></details></td><td><span class="status ${result.passed ? "succeeded" : result.status === "pending" ? "pending" : "failed"}">${result.passed ? "pass" : result.status === "pending" ? "pending" : "fail"}</span>${(result.failures || []).map(failure => `<small>${escapeHTML(failure)}</small>`).join("")}</td><td>${Object.entries(result.judge || {}).map(([criterion, score]) => `<span class="design-score" title="${escapeHTML(score.reason || "")}" style="--score:${Number(score.score)}">${escapeHTML(criterion.replace("_", " "))} ${Number(score.score).toFixed(2)}</span>`).join(" ") || "—"}</td><td>${studioNumber(result.duration_ms)} ms</td><td>${studioReviewCell(item.id, result)}</td></tr>`).join("")}</tbody></table></div>`;
}
function studioJudgeSummary(scores) {
  const entries = Object.entries(scores || {});
  return entries.length ? entries.map(([criterion, score]) => `${criterion.replace("_", " ")} ${Number(score).toFixed(2)}`).join(", ") : "—";
}
// Per-tag slices as a heat table: pass rate and mean judge scores by tag.
function studioSlices(slices) {
  if (!slices?.length) return "";
  const criteria = [...new Set(slices.flatMap(slice => Object.keys(slice.judge_scores || {})))].sort();
  const cell = value => value === undefined ? `<td>—</td>` : `<td class="design-heat" style="--score:${value}">${studioPercent(value)}</td>`;
  return `<h4>Results by tag</h4><div class="table-wrap"><table class="data-table design-slices"><thead><tr><th scope="col">Tag</th><th scope="col">Cases</th><th scope="col">Pass rate</th>${criteria.map(name => `<th scope="col">${escapeHTML(name.replace("_", " "))}</th>`).join("")}</tr></thead><tbody>
    ${slices.map(slice => `<tr><th scope="row">${escapeHTML(slice.tag)}</th><td>${slice.passed}/${slice.cases}</td>${cell(slice.pass_rate)}${criteria.map(name => cell(slice.judge_scores?.[name])).join("")}</tr>`).join("")}</tbody></table></div>`;
}
function studioReviewCell(evaluationID, result) {
  if (result.status === "pending") return "—";
  const current = result.review ? `<small>${escapeHTML(result.review.status.replace("_", " "))} by ${escapeHTML(result.review.reviewer)}${result.review.note ? `: ${escapeHTML(result.review.note)}` : ""}</small>` : "";
  return `<select aria-label="Review case ${escapeHTML(result.id)}" data-review-case="${escapeHTML(result.id)}" data-review-evaluation="${escapeHTML(evaluationID)}" ${can("agents_write") ? "" : "disabled"}><option value="">Review…</option><option value="accepted">Accept</option><option value="needs_changes">Needs changes</option><option value="rejected">Reject</option></select>${current}`;
}
document.querySelector("#design-evaluations").addEventListener("change", async event => {
  const select = event.target.closest("[data-review-case]");
  if (!select || !select.value) return;
  const note = prompt(`Note for ${select.dataset.reviewCase} (optional)`) || "";
  try {
    designStudio.evaluation = await api(`/api/v1/agent-evaluations/${encodeURIComponent(select.dataset.reviewEvaluation)}/cases/${encodeURIComponent(select.dataset.reviewCase)}/review`,
      {method: "POST", body: JSON.stringify({status: select.value, note})});
    studioRenderEvaluations();
    designMessage(`Reviewed ${select.dataset.reviewCase}.`);
  } catch (failure) { designMessage(failure.message, true); }
});
function studioPollEvaluation(id) {
  clearTimeout(designStudio.evalPoll);
  const tick = async () => {
    try {
      const item = await api(`/api/v1/agent-evaluations/${encodeURIComponent(id)}`);
      designStudio.evaluation = item;
      designStudio.evaluations = [item, ...designStudio.evaluations.filter(other => other.id !== id)].sort((a, b) => b.created_at.localeCompare(a.created_at));
      studioRenderEvaluations();
      if (item.status === "running" || item.status === "cancelling") designStudio.evalPoll = setTimeout(tick, 1000);
      else { designMessage(`Evaluation ${item.status}: ${studioPercent(item.pass_rate)} of cases passed; the gate ${item.passed ? "passed" : "did not pass"}.`, !item.passed); studioLoadOverview(); }
    } catch (failure) { designMessage(failure.message, true); }
  };
  tick();
}
document.querySelector("#design-evaluations").addEventListener("submit", async event => {
  if (event.target.id !== "design-eval-form") return;
  event.preventDefault();
  if (!studioSaved()) return;
  const gate = Number(document.querySelector("#design-eval-gate").value) / 100;
  try {
    const item = await api(studioPath("/evaluations"), {method: "POST", body: JSON.stringify({version_id: document.querySelector("#design-eval-target").value, gate, allow_side_effects: document.querySelector("#design-eval-side-effects").checked, revision: studioDesign().revision})});
    designMessage("Evaluation started.");
    studioPollEvaluation(item.id);
  } catch (failure) { studioError(failure); }
});
document.querySelector("#design-evaluations").addEventListener("change", event => {
  const box = event.target.closest("[data-eval-compare]");
  if (!box) return;
  designStudio.compare = box.checked ? [...designStudio.compare.filter(id => id !== box.dataset.evalCompare), box.dataset.evalCompare].slice(-2) : designStudio.compare.filter(id => id !== box.dataset.evalCompare);
  studioRenderEvaluations();
});
document.querySelector("#design-evaluations").addEventListener("click", async event => {
  const view = event.target.closest("[data-eval-view]");
  if (view) { studioPollEvaluation(view.dataset.evalView); return; }
  if (event.target.id === "design-eval-compare") {
    try {
      const [a, b] = await Promise.all(designStudio.compare.map(id => api(`/api/v1/agent-evaluations/${encodeURIComponent(id)}`)));
      const [older, newer] = a.created_at < b.created_at ? [a, b] : [b, a];
      const byID = new Map(older.cases.map(item => [item.id, item]));
      const rows = newer.cases.map(item => { const before = byID.get(item.id); const change = !before ? "new case" : before.passed === item.passed ? (item.passed ? "still passing" : "still failing") : item.passed ? "fixed" : "regressed"; return `<tr class="${change === "regressed" ? "fail" : change === "fixed" ? "pass" : ""}"><th scope="row">${escapeHTML(item.id)}</th><td>${before ? (before.passed ? "pass" : "fail") : "—"}</td><td>${item.passed ? "pass" : "fail"}</td><td>${escapeHTML(change)}</td></tr>`; }).join("");
      const delta = newer.pass_rate - older.pass_rate;
      document.querySelector("#design-eval-detail").innerHTML = `<h3>${escapeHTML(studioEvaluationTarget(older))} → ${escapeHTML(studioEvaluationTarget(newer))}: pass rate ${studioPercent(older.pass_rate)} → ${studioPercent(newer.pass_rate)} (${delta >= 0 ? "+" : ""}${Math.round(delta * 1000) / 10} points)</h3><p class="field-help">P95 ${studioNumber(older.p95_latency_ms)} → ${studioNumber(newer.p95_latency_ms)} ms · cost ${studioCost(older.cost_usd)} → ${studioCost(newer.cost_usd)}</p><div class="table-wrap"><table class="data-table"><thead><tr><th scope="col">Case</th><th scope="col">${escapeHTML(studioEvaluationTarget(older))}</th><th scope="col">${escapeHTML(studioEvaluationTarget(newer))}</th><th scope="col">Change</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    } catch (failure) { designMessage(failure.message, true); }
  }
});
document.querySelector("#design-evaluate").addEventListener("click", () => studioTab("evaluations"));

// --- versions ---
async function studioLoadVersions() {
  const target = document.querySelector("#design-versions");
  if (!studioDesign()?.id) { target.innerHTML = `<p class="field-help">Save the design to publish versions.</p>`; return; }
  try {
    const [versions, evaluations] = await Promise.all([api(studioPath("/versions")), api(studioPath("/evaluations"))]);
    designStudio.versions = versions.items || []; designStudio.evaluations = evaluations.items || [];
  } catch (failure) { target.innerHTML = `<p class="field-help error">${escapeHTML(failure.message)}</p>`; return; }
  studioRenderVersions();
}
function studioRenderVersions() {
  const writable = can("agents_write"), passing = designStudio.evaluations.filter(item => item.status === "completed" && item.passed && !item.version_id && item.revision === studioDesign().revision);
  document.querySelector("#design-versions").innerHTML = `<form id="design-publish-form" class="design-eval-form"><label>Release notes <input id="design-publish-notes" maxlength="1000" placeholder="What changed"></label><label>Quality gate <select id="design-publish-evaluation"><option value="">Publish without an evaluation</option>${passing.map(item => `<option value="${escapeHTML(item.id)}">Passing evaluation ${studioPercent(item.pass_rate)} (${escapeHTML(studioWhen(item.created_at))})</option>`).join("")}</select></label><button class="primary" type="submit" ${writable ? "" : "disabled"}>Publish r${studioDesign().revision}</button></form>
    <p class="field-help">A version is an immutable snapshot identified by its behavior hash. Publishing unchanged behavior returns the existing version. Deploy an earlier version to roll back; set canary traffic from the Agents view.</p>
    <div class="table-wrap"><table class="data-table"><thead><tr><th scope="col">Version</th><th scope="col">Published</th><th scope="col">From</th><th scope="col">Behavior hash</th><th scope="col">Evaluation</th><th scope="col">Deployments</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead><tbody>
    ${designStudio.versions.map(item => `<tr><th scope="row">v${item.version}${item.notes ? `<small>${escapeHTML(item.notes)}</small>` : ""}</th><td>${escapeHTML(studioWhen(item.created_at))}<small>${escapeHTML(item.author)}</small></td><td>r${item.source_revision}</td><td><code>${escapeHTML(item.content_sha256.slice(0, 12))}</code></td><td>${item.evaluation ? `${studioPercent(item.evaluation.pass_rate)} <span class="tag ok">gate ${studioPercent(item.evaluation.gate)}</span>` : "—"}</td><td>${(item.deployments || []).map(agent => `<span class="tag">${escapeHTML(agent.name)} ${escapeHTML(agent.version)} · ${escapeHTML(agent.status)}</span>`).join(" ") || "—"}</td><td><button type="button" data-version-run="${escapeHTML(item.id)}" ${writable ? "" : "disabled"}>Run</button> <button type="button" data-version-deploy="${escapeHTML(item.id)}" ${writable ? "" : "disabled"}>Deploy</button></td></tr>`).join("") || `<tr><td colspan="7">No published versions yet.</td></tr>`}
    </tbody></table></div><label class="checkbox"><input type="checkbox" id="design-deploy-gate"> Only deploy versions published with a passing evaluation</label>`;
}
document.querySelector("#design-versions").addEventListener("submit", async event => {
  if (event.target.id !== "design-publish-form") return;
  event.preventDefault();
  if (!studioSaved()) return;
  try {
    const version = await api(studioPath("/publish"), {method: "POST", body: JSON.stringify({revision: studioDesign().revision, notes: document.querySelector("#design-publish-notes").value, evaluation_id: document.querySelector("#design-publish-evaluation").value})});
    designMessage(designStudio.versions.some(item => item.id === version.id) ? `Behavior is unchanged; v${version.version} is already published.` : `Published v${version.version}.`);
    await studioLoadVersions(); studioLoadOverview();
  } catch (failure) { studioError(failure); }
});
document.querySelector("#design-versions").addEventListener("click", async event => {
  const run = event.target.closest("[data-version-run]"), deploy = event.target.closest("[data-version-deploy]");
  if (run) {
    const input = document.querySelector("#design-run-input").value.trim();
    if (!input) { studioTab("playground"); designMessage("Enter a message in the playground, then choose Run on a version.", true); document.querySelector("#design-run-input").focus(); return; }
    studioTab("playground"); studioStartRun({input, versionID: run.dataset.versionRun});
  }
  if (deploy) {
    try {
      const agent = await api(`/api/v1/agent-versions/${encodeURIComponent(deploy.dataset.versionDeploy)}/deploy`, {method: "POST", body: JSON.stringify({require_evaluation: document.querySelector("#design-deploy-gate").checked})});
      designMessage(`Deploying ${agent.name} ${agent.version}. Follow its rollout in the Agents view.`);
      await studioLoadVersions(); studioLoadOverview();
    } catch (failure) { studioError(failure); }
  }
});
document.querySelector("#design-publish").addEventListener("click", () => { studioTab("versions"); });
document.querySelector("#design-drawer").addEventListener("click", event => {
  const tab = event.target.closest("[data-design-tab]");
  if (tab) studioTab(tab.dataset.designTab);
});
document.querySelector("#design-drawer .design-tabs").addEventListener("keydown", event => {
  const tabs = [...document.querySelectorAll("[data-design-tab]")], index = tabs.indexOf(event.target);
  if (index < 0 || !["ArrowLeft", "ArrowRight"].includes(event.key)) return;
  const next = tabs[(index + (event.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
  next.focus(); studioTab(next.dataset.designTab);
});
document.querySelector("#design-inspector-content").addEventListener("click", event => {
  if (event.target.id !== "design-test-node" || !studioSaved()) return;
  studioTab("playground");
  document.querySelector("#design-run-stop").value = agentDesignState.selected;
  const input = document.querySelector("#design-run-input");
  if (input.value.trim()) studioStartRun({input: input.value.trim(), stopAfter: agentDesignState.selected});
  else { input.focus(); designMessage(`Enter a message to test up to ${agentDesignState.selected}.`); }
});

// --- operations: one deployed version's traffic, latency, cost and rollout ---
designStudio.ops = {agent: "", hours: 24, data: null};
async function studioLoadOperations() {
  const target = document.querySelector("#design-operations");
  if (!studioDesign()?.id) { target.innerHTML = `<p class="field-help">Save and publish the design, then deploy a version to see its operations.</p>`; return; }
  try { designStudio.versions = (await api(studioPath("/versions"))).items || []; }
  catch (failure) { target.innerHTML = `<p class="field-help error">${escapeHTML(failure.message)}</p>`; return; }
  const deployments = designStudio.versions.flatMap(version => (version.deployments || []).map(agent => ({...agent, versionNumber: version.version})));
  if (!deployments.length) { target.innerHTML = `<p class="field-help">No deployments yet. Deploy a version from the Versions tab on a Kubernetes managed runtime.</p>`; return; }
  if (!deployments.some(agent => agent.id === designStudio.ops.agent)) designStudio.ops.agent = deployments[0].id;
  try { designStudio.ops.data = await api(`/api/v1/agents/${encodeURIComponent(designStudio.ops.agent)}/operations?hours=${designStudio.ops.hours}`); }
  catch (failure) { target.innerHTML = `<p class="field-help error">${escapeHTML(failure.message)}</p>`; return; }
  studioRenderOperations(deployments);
}
function studioBars(series, value, label, colorClass, format) {
  const values = series.map(value), max = Math.max(1e-9, ...values), width = 100 / series.length;
  return `<figure class="design-ops-chart"><figcaption>${escapeHTML(label)}</figcaption><svg viewBox="0 0 100 40" preserveAspectRatio="none" role="img" aria-label="${escapeHTML(label)}: ${values.map(format).join(", ")}">
    ${values.map((item, index) => `<rect class="${colorClass}" x="${index * width + 0.3}" y="${40 - (item / max) * 38}" width="${width - 0.6}" height="${(item / max) * 38}"><title>${escapeHTML(new Date(series[index].start).toLocaleString())}: ${escapeHTML(format(item))}</title></rect>`).join("")}
  </svg><div class="design-ops-axis"><span>${escapeHTML(new Date(series[0].start).toLocaleTimeString([], {hour: "2-digit", minute: "2-digit"}))}</span><span>max ${escapeHTML(format(max === 1e-9 ? 0 : max))}</span><span>now</span></div></figure>`;
}
function studioLatencyChart(series) {
  const keys = [["p50_ms", "P50"], ["p95_ms", "P95"], ["p99_ms", "P99"]], max = Math.max(1, ...series.flatMap(bucket => keys.map(([key]) => bucket[key] || 0)));
  const step = 100 / series.length;
  const line = key => series.map((bucket, index) => bucket[key] === null ? null : `${index * step + step / 2},${40 - (bucket[key] / max) * 38}`).filter(Boolean).join(" ");
  return `<figure class="design-ops-chart"><figcaption>Latency (ms) <span class="design-ops-legend"><i class="p50"></i>P50 <i class="p95"></i>P95 <i class="p99"></i>P99</span></figcaption><svg viewBox="0 0 100 40" preserveAspectRatio="none" role="img" aria-label="Latency percentiles per bucket, max ${max} ms">
    ${keys.map(([key, name]) => `<polyline class="${name.toLowerCase()}" fill="none" vector-effect="non-scaling-stroke" points="${line(key)}"><title>${name}</title></polyline>`).join("")}
  </svg><div class="design-ops-axis"><span>${designStudio.ops.hours} h ago</span><span>max ${studioNumber(max)} ms</span><span>now</span></div></figure>`;
}
function studioRenderOperations(deployments) {
  const ops = designStudio.ops.data, totals = ops.totals, agent = ops.agent, writable = can("agents_write");
  const cards = [["Status", agent.status], ["Version", agent.version], ["Canary weight", `${agent.canary_weight}%`], [`Requests (${ops.window_hours} h)`, studioNumber(totals.requests)],
    ["Success rate", studioPercent(ops.success_rate)], ["P95 latency", totals.p95_ms === null ? "—" : `${studioNumber(totals.p95_ms)} ms`],
    ["Tokens", `${studioNumber(totals.input_tokens)} / ${studioNumber(totals.output_tokens)}`], ["Cost", studioCost(totals.cost_usd)]];
  const series = ops.series || [];
  document.querySelector("#design-operations").innerHTML = `<div class="design-eval-form"><label>Deployment <select id="design-ops-agent">${deployments.map(item => `<option value="${escapeHTML(item.id)}" ${item.id === designStudio.ops.agent ? "selected" : ""}>${escapeHTML(item.name)} · v${item.versionNumber}</option>`).join("")}</select></label>
    <label>Window <select id="design-ops-hours">${[1, 6, 24, 168].map(hours => `<option value="${hours}" ${hours === designStudio.ops.hours ? "selected" : ""}>${hours === 168 ? "7 days" : `${hours} h`}</option>`).join("")}</select></label><button type="button" id="design-ops-refresh">Refresh</button></div>
    <div class="design-kpis">${cards.map(([label, value]) => `<div class="design-kpi"><span>${escapeHTML(label)}</span><strong>${escapeHTML(String(value))}</strong></div>`).join("")}</div>
    ${totals.requests ? `<div class="design-ops-charts">${studioBars(series, bucket => bucket.requests, "Requests", "requests", value => studioNumber(value))}${studioBars(series, bucket => bucket.errors, "Errors", "errors", value => studioNumber(value))}${studioLatencyChart(series)}${studioBars(series, bucket => bucket.cost_usd, "Cost", "cost", value => studioCost(value))}</div>`
      : `<p class="field-help">No traffic recorded in this window. Invoke the agent from Agents or the API to see requests, latency and cost here.</p>`}
    <div class="design-ops-columns"><section><h3>Rollout</h3>${(ops.timeline || []).length ? `<ol class="design-ops-timeline">${ops.timeline.map(event => `<li><time datetime="${escapeHTML(event.at)}">${escapeHTML(studioWhen(event.at))}</time> ${escapeHTML(event.action === "agent.deployed" ? "Deployed" : event.action === "agent.traffic_updated" ? `Canary weight ${event.details?.canary_weight ?? "?"}%` : event.action)} · ${escapeHTML(event.version || event.agent_id)} <small>${escapeHTML(event.actor)}</small></li>`).join("")}</ol>` : `<p class="field-help">No rollout events.</p>`}</section>
    <section><h3>Traffic</h3><label>Canary weight for ${escapeHTML(agent.version)} (%)<input id="design-ops-weight" type="number" min="0" max="100" value="${escapeHTML(agent.canary_weight)}" ${writable ? "" : "disabled"}></label><button type="button" id="design-ops-set-weight" ${writable ? "" : "disabled"}>Set canary weight</button>
      ${(ops.siblings || []).length ? `<p class="field-help">Other deployments of this design: ${ops.siblings.map(item => `${escapeHTML(item.name)} ${escapeHTML(item.version)} (${item.canary_weight}%)`).join(", ")}. To roll back, deploy an earlier version from Versions.</p>` : `<p class="field-help">This is the only deployment of the design. To roll back, deploy an earlier version from Versions.</p>`}</section></div>
    <h3>Recent turns</h3><div class="table-wrap"><table class="data-table"><thead><tr><th scope="col">Time</th><th scope="col">Session</th><th scope="col">Status</th><th scope="col">Duration</th><th scope="col">Tokens</th><th scope="col">Cost</th></tr></thead><tbody>
    ${(ops.recent_traces || []).map(trace => `<tr><td>${escapeHTML(studioWhen(trace.created_at))}</td><td><code>${escapeHTML(trace.session_id)}</code></td><td><span class="status ${escapeHTML(trace.status)}">${escapeHTML(trace.status)}</span></td><td>${studioNumber(trace.duration_ms)} ms</td><td>${studioNumber(trace.tokens)}</td><td>${studioCost(trace.cost_usd || 0)}</td></tr>`).join("") || `<tr><td colspan="6">No turns in this window.</td></tr>`}
    </tbody></table></div>`;
}
document.querySelector("#design-operations").addEventListener("change", event => {
  if (event.target.id === "design-ops-agent") { designStudio.ops.agent = event.target.value; studioLoadOperations(); }
  if (event.target.id === "design-ops-hours") { designStudio.ops.hours = Number(event.target.value); studioLoadOperations(); }
});
document.querySelector("#design-operations").addEventListener("click", async event => {
  if (event.target.id === "design-ops-refresh") studioLoadOperations();
  if (event.target.id === "design-ops-set-weight") {
    const weight = Number(document.querySelector("#design-ops-weight").value);
    if (!Number.isInteger(weight) || weight < 0 || weight > 100) { designMessage("Canary weight must be a whole number from 0 to 100.", true); return; }
    try {
      await api(`/api/v1/agents/${encodeURIComponent(designStudio.ops.agent)}/traffic`, {method: "PUT", body: JSON.stringify({canary_weight: weight})});
      designMessage(`Canary weight set to ${weight}%.`);
      studioLoadOperations();
    } catch (failure) { designMessage(failure.message, true); }
  }
});
