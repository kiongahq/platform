/* Native Kionga agent graph drafts. Connections and positions are stored by
 * the platform API; the browser never stores credentials in the graph. */
const agentDesignState = {draft: null, list: [], selected: "", connecting: "", connectingPort: "", dirty: false, saving: false, models: [], tools: [], knowledge: [], connections: [], datasets: [],
  registry: null, templates: [], zoom: 1, multi: new Set(), clipboard: null, undo: [], redo: [], baseline: "", lastMark: 0, issues: [], nodeStatus: {}};
const designKinds = {input: "Input", prompt: "Prompt", model: "Model", tool: "Tool", knowledge: "Knowledge base", api: "API call", condition: "Condition", memory: "Memory", storage: "Dataset object", output: "Output"};
function designComponent(kind) { return agentDesignState.registry?.components?.find(item => item.kind === kind) || null; }
function designOutPorts(node) { return node.kind === "condition" ? ["true", "false"] : node.kind === "output" ? [] : ["out"]; }
function designEdgePort(edge) { return edge.from_port || (agentDesignState.draft?.nodes.find(node => node.id === edge.from)?.kind === "condition" ? "true" : "out"); }
const designNodeWidth = 190, designNodeHeight = 88;
function blankAgentDesign() {
  return {id: "", project_id: selectedProject || projectOptions[0]?.id || "", name: "New agent", description: "", revision: 0,
    nodes: [{id: "input", kind: "input", name: "Input", x: 40, y: 100, config: {}}, {id: "output", kind: "output", name: "Output", x: 460, y: 100, config: {}}], edges: [{from: "input", to: "output"}]};
}
function designMessage(message, error = false) {
  const target = document.querySelector("#design-message");
  target.textContent = message;
  target.classList.toggle("error", error);
}
function designSnapshot() {
  const draft = agentDesignState.draft;
  return {project_id: draft.project_id, name: (draft.name || "").trim(), description: (draft.description || "").trim(), revision: draft.revision, nodes: draft.nodes, edges: draft.edges, ...(draft.settings ? {settings: draft.settings} : {})};
}
function designReachable(start, target, edges) {
  const seen = new Set(), pending = [start];
  while (pending.length) {
    const id = pending.pop();
    if (id === target) return true;
    if (seen.has(id)) continue;
    seen.add(id);
    for (const edge of edges) if (edge.from === id) pending.push(edge.to);
  }
  return false;
}
function designConnectionIssue(from, to, draft = agentDesignState.draft, port = "") {
  const source = draft.nodes.find(node => node.id === from), target = draft.nodes.find(node => node.id === to);
  if (!source || !target || from === to || source.kind === "output" || target.kind === "input") return "Choose two different compatible ports.";
  if (draft.edges.some(edge => edge.from === from && edge.to === to && (!port || designEdgePort(edge) === port))) return "Those nodes are already connected.";
  if (draft.edges.length >= 300) return "A design can have at most 300 connections.";
  if (designReachable(to, from, draft.edges)) return "That connection would create a cycle.";
  return "";
}
function designValidation(draft = agentDesignState.draft) {
  if (!draft) return ["No design is open."];
  const issues = [];
  if (!draft.project_id) issues.push("Choose a project.");
  if (!draft.name.trim()) issues.push("Enter a design name.");
  if (draft.nodes.some(node => !node.name.trim())) issues.push("Give every node a label.");
  if (draft.nodes.length > 100) issues.push("A design can have at most 100 nodes.");
  if (draft.edges.length > 300) issues.push("A design can have at most 300 connections.");
  return issues;
}
function designUpdateStatus() {
  const draft = agentDesignState.draft;
  if (!draft) return;
  const issues = designValidation(draft);
  const connected = new Set(draft.edges.flatMap(edge => [edge.from, edge.to]));
  const loose = draft.nodes.filter(node => !connected.has(node.id)).length;
  const summary = document.querySelector("#design-graph-summary");
  summary.classList.toggle("error", issues.length > 0);
  summary.textContent = `${draft.nodes.length} nodes · ${draft.edges.length} connections · ${loose} unconnected${issues.length ? ` · ${issues.join(" ")}` : " · ready to save"}`;
  const save = document.querySelector("#design-save");
  save.disabled = !can("agents_write") || agentDesignState.saving || issues.length > 0;
  save.textContent = agentDesignState.saving ? "Saving…" : "Save revision";
}
// Undo keeps up to 100 graph snapshots. Typing into one field within a
// second coalesces into a single step.
function designResetHistory() {
  agentDesignState.undo = []; agentDesignState.redo = [];
  agentDesignState.baseline = agentDesignState.draft ? JSON.stringify(designSnapshot()) : "";
  designUpdateHistoryButtons();
}
function designUpdateHistoryButtons() {
  const undo = document.querySelector("#design-undo"), redo = document.querySelector("#design-redo");
  if (undo) undo.disabled = !can("agents_write") || !agentDesignState.undo.length;
  if (redo) redo.disabled = !can("agents_write") || !agentDesignState.redo.length;
}
function designRecordHistory(coalesce = false) {
  const current = JSON.stringify(designSnapshot());
  if (current === agentDesignState.baseline) return;
  const now = Date.now();
  if (!(coalesce && now - agentDesignState.lastMark < 1000 && agentDesignState.undo.length)) {
    agentDesignState.undo.push(agentDesignState.baseline);
    if (agentDesignState.undo.length > 100) agentDesignState.undo.shift();
  }
  agentDesignState.lastMark = coalesce ? now : 0;
  agentDesignState.redo = [];
  agentDesignState.baseline = current;
  designUpdateHistoryButtons();
}
function designApplyHistory(from, to) {
  const target = agentDesignState[from].pop();
  if (!target) return;
  agentDesignState[to].push(agentDesignState.baseline);
  const snapshot = JSON.parse(target);
  Object.assign(agentDesignState.draft, {name: snapshot.name, description: snapshot.description, nodes: snapshot.nodes, edges: snapshot.edges, settings: snapshot.settings});
  agentDesignState.baseline = target;
  if (!agentDesignState.draft.nodes.some(node => node.id === agentDesignState.selected)) agentDesignState.selected = "";
  agentDesignState.dirty = true;
  designRender(); designUpdateHistoryButtons();
  designMessage(from === "undo" ? "Undone." : "Redone.");
}
function designMarkDirty(coalesce = false) {
  designRecordHistory(coalesce);
  agentDesignState.dirty = true;
  document.querySelector("#design-revision").textContent = agentDesignState.draft.revision ? `Revision ${agentDesignState.draft.revision} · unsaved changes` : "Unsaved";
  designUpdateStatus();
}
function designFocusSelected() {
  if (agentDesignState.selected) document.querySelector(`[data-design-select="${CSS.escape(agentDesignState.selected)}"]`)?.focus();
}
function designSelect(id) { agentDesignState.selected = id; agentDesignState.multi.clear(); designRender(); designFocusSelected(); }
// The selection is the focused node plus any shift-clicked nodes.
function designSelection() {
  const ids = new Set(agentDesignState.multi);
  if (agentDesignState.selected) ids.add(agentDesignState.selected);
  return ids;
}
function designCopySelection() {
  const ids = designSelection(), draft = agentDesignState.draft;
  const nodes = draft.nodes.filter(node => ids.has(node.id) && !["input", "output"].includes(node.kind));
  if (!nodes.length) { designMessage("Select nodes other than input and output to copy.", true); return; }
  const kept = new Set(nodes.map(node => node.id));
  agentDesignState.clipboard = {nodes: structuredClone(nodes), edges: structuredClone(draft.edges.filter(edge => kept.has(edge.from) && kept.has(edge.to)))};
  designMessage(`Copied ${nodes.length} node${nodes.length === 1 ? "" : "s"}.`);
}
function designPaste() {
  const clip = agentDesignState.clipboard, draft = agentDesignState.draft;
  if (!clip) return;
  if (draft.nodes.length + clip.nodes.length > 100) { designMessage("A design can have at most 100 nodes.", true); return; }
  const ids = new Map();
  for (const node of clip.nodes) {
    const id = `${node.kind}-${Math.random().toString(36).slice(2, 9)}`;
    ids.set(node.id, id);
    draft.nodes.push({...structuredClone(node), id, name: node.name.slice(0, 100), x: Math.min(node.x + 40, 9800), y: Math.min(node.y + 40, 9800)});
  }
  for (const edge of clip.edges) draft.edges.push({...edge, from: ids.get(edge.from), to: ids.get(edge.to)});
  agentDesignState.multi = new Set(ids.values()); agentDesignState.selected = [...ids.values()][0];
  clip.nodes = clip.nodes.map(node => ({...node, x: Math.min(node.x + 40, 9800), y: Math.min(node.y + 40, 9800)}));
  designMarkDirty(); designRender();
  designMessage(`Pasted ${ids.size} node${ids.size === 1 ? "" : "s"} with their internal connections.`);
}
function designDeleteSelection() {
  const draft = agentDesignState.draft, ids = new Set([...designSelection()].filter(id => !["input", "output"].includes(draft.nodes.find(node => node.id === id)?.kind)));
  if (!ids.size) return;
  draft.nodes = draft.nodes.filter(node => !ids.has(node.id));
  draft.edges = draft.edges.filter(edge => !ids.has(edge.from) && !ids.has(edge.to));
  agentDesignState.selected = ""; agentDesignState.multi.clear();
  designMarkDirty(); designRender();
  designMessage(`Removed ${ids.size} node${ids.size === 1 ? "" : "s"}.`);
}
function designRenderCanvasBounds() {
  const nodes = agentDesignState.draft?.nodes || [];
  const width = Math.max(2000, ...nodes.map(node => Number(node.x) + designNodeWidth + 100));
  const height = Math.max(1300, ...nodes.map(node => Number(node.y) + designNodeHeight + 100));
  const canvas = document.querySelector("#design-canvas"), edges = document.querySelector("#design-edges");
  canvas.style.width = `${width}px`; canvas.style.height = `${height}px`;
  edges.setAttribute("width", String(width)); edges.setAttribute("height", String(height));
}
function designRender() {
  const draft = agentDesignState.draft;
  if (!draft) return;
  document.querySelector("#design-project").value = draft.project_id;
  document.querySelector("#design-name").value = draft.name;
  document.querySelector("#design-description").value = draft.description || "";
  document.querySelector("#design-knowledge-options").innerHTML = agentDesignState.knowledge.filter(item => item.project_id === draft.project_id).map(item => `<option value="${escapeHTML(item.id)}" label="${escapeHTML(item.name)}"></option>`).join("");
  document.querySelector("#design-api-options").innerHTML = agentDesignState.connections.filter(item => item.project_id === draft.project_id).map(item => `<option value="${escapeHTML(item.id)}" label="${escapeHTML(item.name)}"></option>`).join("");
  for (const control of document.querySelectorAll("#agent-designer input, #agent-designer textarea, #agent-designer [data-design-add], #design-remove-node, #design-remove-edges")) control.disabled = !can("agents_write");
  document.querySelector("#design-project").disabled = !can("agents_write") || Boolean(draft.id);
  document.querySelector("#design-revision").textContent = draft.revision ? `Revision ${draft.revision}${agentDesignState.dirty ? " · unsaved changes" : ""}` : "Unsaved";
  const options = agentDesignState.list.filter(item => !selectedProject || item.project_id === selectedProject);
  document.querySelector("#design-list").innerHTML = `<option value="">New design</option>${options.map(item => `<option value="${escapeHTML(item.id)}" ${item.id === draft.id ? "selected" : ""}>${escapeHTML(item.name)} · r${item.revision}</option>`).join("")}`;
  designRenderCanvasBounds();
  const nodes = new Map(draft.nodes.map(node => [node.id, node]));
  document.querySelector("#design-edges").innerHTML = `<defs><marker id="design-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z"/></marker></defs>` + draft.edges.map(edge => {
    const from = nodes.get(edge.from), to = nodes.get(edge.to);
    if (!from || !to) return "";
    const port = designEdgePort(edge), label = from.kind === "condition" ? port : "";
    return `<path class="design-edge ${label ? `branch-${label}` : ""}" data-design-edge="${escapeHTML(edge.from + ">" + edge.to)}" data-design-port="${escapeHTML(port)}" d="${designEdgePath(from, to, port)}" marker-end="url(#design-arrow)"><title>${escapeHTML(from.name)}${label ? ` (${escapeHTML(label)})` : ""} to ${escapeHTML(to.name)}</title></path>`;
  }).join("");
  const issuesByNode = {};
  for (const issue of agentDesignState.issues) if (issue.node_id) (issuesByNode[issue.node_id] ||= []).push(issue);
  document.querySelector("#design-nodes").innerHTML = draft.nodes.map(node => {
    const issues = issuesByNode[node.id] || [], errors = issues.filter(item => item.severity === "error").length, status = agentDesignState.nodeStatus[node.id];
    const ports = designOutPorts(node);
    return `<div class="design-node ${node.id === agentDesignState.selected ? "selected" : ""} ${agentDesignState.multi.has(node.id) ? "multi" : ""} ${errors ? "has-error" : issues.length ? "has-warning" : ""} ${status ? `run-${escapeHTML(status)}` : ""}" style="left:${Number(node.x)}px;top:${Number(node.y)}px" data-design-node="${escapeHTML(node.id)}">
    ${node.kind !== "input" ? `<button class="design-port in" type="button" data-design-in="${escapeHTML(node.id)}" aria-label="Connect to ${escapeHTML(node.name)}" title="Connect here"></button>` : ""}
    <button type="button" class="design-node-body" data-design-select="${escapeHTML(node.id)}" aria-label="${escapeHTML(node.name)} ${escapeHTML(designKinds[node.kind] || node.kind)} node${errors ? `, ${errors} validation error${errors === 1 ? "" : "s"}` : ""}${status ? `, last run ${escapeHTML(status)}` : ""}"><span class="design-kind">${escapeHTML(designKinds[node.kind] || node.kind)}${node.group ? ` · ${escapeHTML(node.group)}` : ""}</span><strong>${escapeHTML(node.name)}</strong><small>${escapeHTML(node.id)}</small>${errors || issues.length ? `<span class="design-badge ${errors ? "error" : "warning"}" aria-hidden="true">${errors || issues.length}</span>` : ""}${status ? `<span class="design-run-dot" aria-hidden="true"></span>` : ""}</button>
    ${ports.map((port, index) => `<button class="design-port out ${ports.length > 1 ? `branch branch-${port}` : ""} ${agentDesignState.connecting === node.id && (agentDesignState.connectingPort || ports[0]) === port ? "pending" : ""}" style="${ports.length > 1 ? `top:${index ? 64 : 18}px` : ""}" type="button" data-design-out="${escapeHTML(node.id)}" data-design-port="${port}" aria-label="Connect from ${escapeHTML(node.name)}${ports.length > 1 ? ` when ${port}` : ""}" title="${ports.length > 1 ? `Connect the ${port} branch` : "Connect from here"}">${ports.length > 1 ? (port === "true" ? "T" : "F") : ""}</button>`).join("")}
  </div>`;
  }).join("");
  designRenderInspector();
  const history = [...(draft.history || [])].reverse();
  document.querySelector("#design-revisions").innerHTML = draft.id ? `<h3>Earlier revisions</h3>${history.length ? history.map(item => `<button type="button" data-design-restore="${item.revision}" ${can("agents_write") ? "" : "disabled"}>Restore r${item.revision} <small>${escapeHTML(new Date(item.saved_at).toLocaleString())}</small></button>`).join("") : `<p class="field-help">No earlier revisions yet.</p>`}` : "";
  for (const control of document.querySelectorAll("#design-inspector-content input, #design-inspector-content textarea, #design-inspector-content select, #design-inspector-content button")) control.disabled = !can("agents_write");
  designUpdateStatus();
}
function designEdgePath(from, to, port) {
  const offset = from.kind === "condition" ? (port === "false" ? 73 : 27) : designNodeHeight / 2;
  const x1 = from.x + designNodeWidth, y1 = from.y + offset, x2 = to.x, y2 = to.y + designNodeHeight / 2;
  return `M ${x1} ${y1} C ${x1 + 75} ${y1}, ${x2 - 75} ${y2}, ${x2} ${y2}`;
}
// Resource choices come from the project's permitted resources only; the
// gateway re-checks every reference when the graph is validated or run.
function designResourceOptions(resource, projectID) {
  const own = items => items.filter(item => !item.project_id || item.project_id === projectID);
  if (resource === "model") return [{value: "llm", label: "Platform chat model"}, ...own(agentDesignState.models).map(item => ({value: item.id, label: `${item.name} ${item.version || ""}`.trim()}))];
  if (resource === "tool") return agentDesignState.tools.map(item => ({value: item.name, label: item.description ? `${item.name} — ${item.description}` : item.name}));
  if (resource === "knowledge_base") return own(agentDesignState.knowledge).map(item => ({value: item.id, label: `${item.name} (${item.backend}, ${item.dimensions}d)`}));
  if (resource === "dataset") return own(agentDesignState.datasets).map(item => ({value: item.id, label: item.name || item.id}));
  if (resource === "connection") return own(agentDesignState.connections).map(item => ({value: item.id, label: item.name}));
  return [];
}
function designRegistryField(node, field) {
  const value = node.config?.[field.key] ?? "", id = `design-field-${field.key}`, help = field.help ? `<small>${escapeHTML(field.help)}</small>` : "";
  const required = field.required ? " *" : "";
  if (field.type === "template") return `<label for="${id}">${escapeHTML(field.label)}${required}<textarea id="${id}" data-design-config="${escapeHTML(field.key)}" rows="6" spellcheck="false" maxlength="${field.max_length || 16000}" placeholder="${escapeHTML(field.default || "")}">${escapeHTML(value)}</textarea>${help}</label>`;
  if (field.type === "integer") return `<label for="${id}">${escapeHTML(field.label)}${required}<input id="${id}" type="number" inputmode="numeric" data-design-config="${escapeHTML(field.key)}" min="${field.minimum ?? ""}" max="${field.maximum ?? ""}" placeholder="${escapeHTML(field.default || "")}" value="${escapeHTML(value)}">${help}</label>`;
  if (field.type === "enum") return `<label for="${id}">${escapeHTML(field.label)}${required}<select id="${id}" data-design-config="${escapeHTML(field.key)}">${field.options.map(option => `<option value="${escapeHTML(option)}" ${(value || field.default) === option ? "selected" : ""}>${escapeHTML(option)}</option>`).join("")}${value && !field.options.includes(value) ? `<option value="${escapeHTML(value)}" selected>${escapeHTML(value)} (runs as ${escapeHTML(field.default || field.options[0])})</option>` : ""}</select>${help}</label>`;
  if (field.type === "resource") {
    const options = designResourceOptions(field.resource, agentDesignState.draft.project_id);
    const known = options.some(option => option.value === value) || (!value && field.default);
    return `<label for="${id}">${escapeHTML(field.label)}${required}<select id="${id}" data-design-config="${escapeHTML(field.key)}"><option value="">${options.length ? "Choose…" : "None available in this project"}</option>${options.map(option => `<option value="${escapeHTML(option.value)}" ${(value || field.default) === option.value ? "selected" : ""}>${escapeHTML(option.label)}</option>`).join("")}${value && !known ? `<option value="${escapeHTML(value)}" selected>${escapeHTML(value)} (not available)</option>` : ""}</select>${help}</label>`;
  }
  return `<label for="${id}">${escapeHTML(field.label)}${required}<input id="${id}" data-design-config="${escapeHTML(field.key)}" maxlength="${field.max_length || 16000}" spellcheck="false" placeholder="${escapeHTML(field.default || "")}" value="${escapeHTML(value)}">${help}</label>`;
}
function designRenderInspector() {
  const node = agentDesignState.draft?.nodes.find(item => item.id === agentDesignState.selected);
  const panel = document.querySelector("#design-inspector-content");
  if (!node) { panel.innerHTML = `<p>Select a node to edit it.</p><p class="field-help">Drag nodes to arrange the canvas. Select an output port, then an input port, to connect nodes. With a node selected, use arrow keys to move it. Ctrl+Z undoes a change.</p>`; return; }
  const component = designComponent(node.kind);
  if (component) {
    const issues = agentDesignState.issues.filter(issue => issue.node_id === node.id);
    panel.innerHTML = `<span class="tag">${escapeHTML(component.label)}</span><p class="field-help">${escapeHTML(component.description)}${component.side_effects ? " <strong>Can change external systems.</strong>" : ""}</p><label>Node label<input id="design-node-name" maxlength="100" value="${escapeHTML(node.name)}"></label><label>Group<input id="design-node-group" maxlength="60" value="${escapeHTML(node.group || "")}" placeholder="Optional visual group"></label><p class="field-help">ID: <code>${escapeHTML(node.id)}</code> · v${escapeHTML(component.version)} · timeout ${component.timeout_seconds}s${component.retries ? ` · ${component.retries} retr${component.retries === 1 ? "y" : "ies"}` : ""}</p>
      ${issues.length ? `<ul class="design-node-issues">${issues.map(issue => `<li class="${escapeHTML(issue.severity)}">${escapeHTML(issue.message)}</li>`).join("")}</ul>` : ""}
      ${component.config.map(field => designRegistryField(node, field)).join("")}
      ${designInspectorConnections(node)}
      <div class="design-inspector-actions">${!["input", "output"].includes(node.kind) ? `<button type="button" id="design-test-node" data-perm="agents_write">Test up to this node</button><button type="button" id="design-duplicate-node">Duplicate node</button><button type="button" class="danger" id="design-remove-node">Remove node</button>` : ""}<button type="button" id="design-remove-edges">Remove connections</button></div>`;
    return;
  }
  const fields = node.kind === "prompt" ? [["template", "Prompt template", "textarea", "Use {input} for the agent message."]]
    : node.kind === "model" ? [["model", "Model reference", "input", "Choose a registered Kionga model by ID. This draft does not invoke it yet."]]
    : node.kind === "tool" ? [["tool", "Tool name", "input", "Choose a registered Kionga tool. This draft does not invoke it yet."]]
    : node.kind === "knowledge" ? [["knowledge_base", "Knowledge base ID", "input", "Use a project knowledge base backed by Qdrant or pgvector."], ["query", "Search query", "textarea", "Use {input} to search with the agent message."]]
    : node.kind === "api" ? [["connection", "Approved API connection ID", "input", "Use a connection registered for this project."], ["request", "Request template", "textarea", "Describe the request using {input} where needed."]]
    : node.kind === "condition" ? [["expression", "Condition", "textarea", "Describe the branch rule for the runtime implementation."]]
    : node.kind === "memory" ? [["strategy", "Memory strategy", "input", "Describe what conversation context to retain."]]
    : [];
  panel.innerHTML = `<span class="tag">${escapeHTML(designKinds[node.kind] || node.kind)}</span><label>Node label<input id="design-node-name" maxlength="100" value="${escapeHTML(node.name)}"></label><p class="field-help">ID: <code>${escapeHTML(node.id)}</code></p>${fields.map(([key,label,tag,hint]) => `<label>${label}${tag === "textarea" ? `<textarea data-design-config="${key}" rows="7" spellcheck="false">${escapeHTML(node.config?.[key] || "")}</textarea>` : `<input data-design-config="${key}" ${key === "model" ? 'list="design-model-options"' : key === "tool" ? 'list="design-tool-options"' : key === "knowledge_base" ? 'list="design-knowledge-options"' : key === "connection" ? 'list="design-api-options"' : ""} value="${escapeHTML(node.config?.[key] || "")}">`}<small>${hint}</small></label>`).join("")}
    ${designInspectorConnections(node)}
    <div class="design-inspector-actions">${!["input", "output"].includes(node.kind) ? `<button type="button" id="design-duplicate-node">Duplicate node</button><button type="button" class="danger" id="design-remove-node">Remove node</button>` : ""}<button type="button" id="design-remove-edges">Remove connections</button></div>`;
}
function designInspectorConnections(node) {
  return `<div class="design-connections"><h3>Connections</h3>${agentDesignState.draft.edges.map((edge, index) => ({edge,index})).filter(({edge}) => edge.from === node.id || edge.to === node.id).map(({edge,index}) => { const branch = agentDesignState.draft.nodes.find(item => item.id === edge.from)?.kind === "condition" ? ` (${designEdgePort(edge)})` : ""; return `<div class="design-connection"><span>${escapeHTML(edge.from)}${escapeHTML(branch)} → ${escapeHTML(edge.to)}</span><button type="button" data-design-remove-edge="${index}" aria-label="Remove connection from ${escapeHTML(edge.from)} to ${escapeHTML(edge.to)}">Remove</button></div>`; }).join("") || '<p class="field-help">No connections yet.</p>'}</div>`;
}
async function loadAgentDesigner() {
  const [data, models, tools, knowledge, connections, registry, templates, datasets] = await Promise.all([
    api("/api/v1/agent-designs"),
    hasService("models") ? api("/api/v1/models").catch(() => ({items: []})) : Promise.resolve({items: []}),
    hasService("catalog") ? api("/api/v1/tools").catch(() => ({items: []})) : Promise.resolve({items: []}),
    api("/api/v1/knowledge-bases").catch(() => ({items: []})),
    api("/api/v1/agent-api-connections").catch(() => ({items: []})),
    agentDesignState.registry ? Promise.resolve(agentDesignState.registry) : api("/api/v1/agent-designs/components").catch(() => null),
    agentDesignState.templates.length ? Promise.resolve({items: agentDesignState.templates}) : api("/api/v1/agent-designs/templates").catch(() => ({items: []})),
    api("/api/v1/datasets").catch(() => ({items: []})),
  ]);
  agentDesignState.registry = registry?.components ? registry : null;
  agentDesignState.templates = templates?.items || [];
  agentDesignState.datasets = datasets?.items || [];
  document.querySelector("#design-template").innerHTML = `<option value="">Template…</option>${agentDesignState.templates.map(item => `<option value="${escapeHTML(item.id)}" title="${escapeHTML(item.description)}">${escapeHTML(item.name)}</option>`).join("")}`;
  agentDesignState.list = data.items || [];
  agentDesignState.models = models.items || [];
  agentDesignState.tools = tools.items || [];
  agentDesignState.knowledge = knowledge.items || [];
  agentDesignState.connections = connections.items || [];
  document.querySelector("#design-model-options").innerHTML = agentDesignState.models.map(item => `<option value="${escapeHTML(item.id)}" label="${escapeHTML(item.name)}"></option>`).join("");
  document.querySelector("#design-tool-options").innerHTML = agentDesignState.tools.map(item => `<option value="${escapeHTML(item.name)}"></option>`).join("");
  document.querySelector("#design-project").innerHTML = projectOptions.map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)}</option>`).join("");
  const desired = selectedResource || agentDesignState.draft?.id;
  const current = agentDesignState.list.find(item => item.id === desired && (!selectedProject || item.project_id === selectedProject));
  if (current && (!agentDesignState.dirty || !agentDesignState.draft || agentDesignState.draft.id !== current.id)) {
    agentDesignState.draft = structuredClone(current); agentDesignState.dirty = false;
  } else if (!agentDesignState.draft || (selectedProject && agentDesignState.draft.project_id !== selectedProject)) {
    agentDesignState.draft = blankAgentDesign(); agentDesignState.dirty = false;
  }
  agentDesignState.selected = ""; agentDesignState.connecting = ""; agentDesignState.issues = []; agentDesignState.nodeStatus = {};
  designResetHistory();
  designRender();
  if (typeof designStudioLoaded === "function") designStudioLoaded();
}
registerView("agent-designer", loadAgentDesigner, {eyebrow: "AGENT BUILDER", title: "Agent designer", beforeLeave: () => {
  if (!agentDesignState.dirty || confirm("Discard unsaved agent design changes?")) { agentDesignState.dirty = false; return true; }
  return false;
}, openResource: id => { if (!agentDesignState.list.some(item => item.id === id)) designMessage("Design not found in this project.", true); }});
window.addEventListener("beforeunload", event => {
  if (!agentDesignState.dirty) return;
  event.preventDefault();
  event.returnValue = "";
});
function designOpen(draft, message, dirty = false) {
  agentDesignState.draft = draft; agentDesignState.selected = ""; agentDesignState.connecting = ""; agentDesignState.dirty = dirty; agentDesignState.multi.clear();
  agentDesignState.issues = []; agentDesignState.nodeStatus = {};
  selectedResource = draft.id || ""; history.replaceState({}, "", routeURL("agent-designer", selectedResource));
  designResetHistory(); designMessage(message); designRender();
  if (typeof designStudioLoaded === "function") designStudioLoaded();
}
document.querySelector("#design-new").addEventListener("click", () => { if (agentDesignState.dirty && !confirm("Discard unsaved design changes?")) return; designOpen(blankAgentDesign(), "New design. Add nodes and save the first revision."); });
document.querySelector("#design-list").addEventListener("change", event => { if (agentDesignState.dirty && !confirm("Discard unsaved design changes?")) { designRender(); return; } const item = agentDesignState.list.find(value => value.id === event.target.value); designOpen(item ? structuredClone(item) : blankAgentDesign(), ""); });
// Imported and template graphs open as new unsaved designs in the current
// project; the gateway validates them on save and on Validate.
function designFromGraph(graph, name) {
  const base = blankAgentDesign();
  if (!Array.isArray(graph?.nodes) || !Array.isArray(graph?.edges) || graph.nodes.length > 100 || graph.edges.length > 300) throw new Error("The file is not an agent graph (nodes and edges are required).");
  const nodes = graph.nodes.map(node => ({id: String(node.id || ""), kind: String(node.kind || ""), name: String(node.name || node.id || ""), x: Number(node.x) || 0, y: Number(node.y) || 0, ...(node.group ? {group: String(node.group)} : {}), config: Object.fromEntries(Object.entries(node.config || {}).map(([key, value]) => [key, String(value)]))}));
  const edges = graph.edges.map(edge => ({from: String(edge.from || ""), to: String(edge.to || ""), ...(edge.from_port ? {from_port: String(edge.from_port)} : {}), ...(edge.to_port ? {to_port: String(edge.to_port)} : {})}));
  return {...base, name: String(name || graph.name || base.name).slice(0, 100), description: String(graph.description || "").slice(0, 1000), nodes, edges, ...(graph.settings ? {settings: graph.settings} : {})};
}
document.querySelector("#design-template").addEventListener("change", event => {
  const template = agentDesignState.templates.find(item => item.id === event.target.value);
  event.target.value = "";
  if (!template || (agentDesignState.dirty && !confirm("Discard unsaved design changes?"))) return;
  designOpen(designFromGraph(template.graph, template.name), `${template.name} template loaded. Choose its resources, then save.`, true);
});
document.querySelector("#design-import-button").addEventListener("click", () => document.querySelector("#design-import").click());
document.querySelector("#design-import").addEventListener("change", async event => {
  const file = event.target.files?.[0]; event.target.value = "";
  if (!file) return;
  if (file.size > 2 * 1024 * 1024) { designMessage("Import files can be at most 2 MiB.", true); return; }
  if (agentDesignState.dirty && !confirm("Discard unsaved design changes?")) return;
  try {
    const parsed = JSON.parse(await file.text());
    const graph = parsed.graph && parsed.graph.nodes ? parsed.graph : parsed;
    designOpen(designFromGraph(graph, parsed.name), `Imported ${file.name} as a new unsaved design.`, true);
  } catch (failure) { designMessage(failure.message || "Import failed.", true); }
});
// Filters palette buttons by label, kind and the registry description.
document.querySelector("#design-palette-search").addEventListener("input", event => {
  const query = event.target.value.trim().toLowerCase();
  let shown = 0;
  for (const button of document.querySelectorAll("[data-design-add]")) {
    const component = designComponent(button.dataset.designAdd);
    const text = `${button.textContent} ${button.dataset.designAdd} ${component?.label || ""} ${component?.description || ""} ${component?.category || ""}`.toLowerCase();
    button.hidden = Boolean(query) && !text.includes(query);
    if (!button.hidden) shown++;
  }
  document.querySelector("#design-palette-empty").hidden = shown > 0;
});
document.querySelector("#design-undo").addEventListener("click", () => can("agents_write") && designApplyHistory("undo", "redo"));
document.querySelector("#design-redo").addEventListener("click", () => can("agents_write") && designApplyHistory("redo", "undo"));
function designSetZoom(value) {
  agentDesignState.zoom = Math.max(0.3, Math.min(1.5, Math.round(value * 10) / 10));
  document.querySelector("#design-canvas").style.zoom = String(agentDesignState.zoom);
  document.querySelector("#design-zoom-level").textContent = `${Math.round(agentDesignState.zoom * 100)}%`;
}
document.querySelector("#design-zoom-in").addEventListener("click", () => designSetZoom(agentDesignState.zoom + 0.1));
document.querySelector("#design-zoom-out").addEventListener("click", () => designSetZoom(agentDesignState.zoom - 0.1));
document.querySelector("#design-zoom-fit").addEventListener("click", () => {
  const nodes = agentDesignState.draft?.nodes || [], scroller = document.querySelector(".design-canvas-scroll");
  if (!nodes.length) return;
  const width = Math.max(...nodes.map(node => node.x + designNodeWidth)) + 40, height = Math.max(...nodes.map(node => node.y + designNodeHeight)) + 40;
  designSetZoom(Math.min((scroller.clientWidth || width) / width, (scroller.clientHeight || height) / height, 1));
  scroller.scrollTo?.(0, 0);
});
document.querySelector("#design-project").addEventListener("change", event => { if (agentDesignState.draft.id) { designMessage("Create a new design to change projects.", true); designRender(); return; } agentDesignState.draft.project_id = event.target.value; designMarkDirty(); });
for (const [selector, field] of [["#design-name", "name"], ["#design-description", "description"]]) document.querySelector(selector).addEventListener("input", event => { agentDesignState.draft[field] = event.target.value; designMarkDirty(); });
document.querySelectorAll("[data-design-add]").forEach(button => button.addEventListener("click", () => {
  if (!can("agents_write")) return;
  if (agentDesignState.draft.nodes.length >= 100) { designMessage("A design can have at most 100 nodes.", true); return; }
  const kind = button.dataset.designAdd, id = `${kind}-${Math.random().toString(36).slice(2, 9)}`;
  const count = agentDesignState.draft.nodes.length;
  agentDesignState.draft.nodes.push({id, kind, name: designKinds[kind], x: 250 + (Math.floor((count - 2) / 4) * 205), y: 250 + ((count - 2) % 4) * 130, config: {}});
  agentDesignState.selected = id; designMarkDirty(); designRender();
  designMessage(`${designKinds[kind]} node added. You can save it before connecting it.`);
}));
document.querySelector("#design-nodes").addEventListener("click", event => {
  const out = event.target.closest("[data-design-out]"), input = event.target.closest("[data-design-in]");
  if (out && can("agents_write")) { const port = out.dataset.designPort || "out", same = agentDesignState.connecting === out.dataset.designOut && agentDesignState.connectingPort === port; agentDesignState.connecting = same ? "" : out.dataset.designOut; agentDesignState.connectingPort = same ? "" : port; designMessage(agentDesignState.connecting ? `Choose an input port${port === "out" ? "" : ` for the ${port} branch`}, or press Escape to cancel.` : "Connection cancelled."); designRender(); return; }
  if (input && agentDesignState.connecting && can("agents_write")) {
    const from = agentDesignState.connecting, to = input.dataset.designIn, port = agentDesignState.connectingPort || "out";
    const issue = designConnectionIssue(from, to, agentDesignState.draft, port);
    if (issue) { designMessage(issue, true); return; }
    agentDesignState.connecting = ""; agentDesignState.connectingPort = "";
    agentDesignState.draft.edges.push(port === "out" ? {from,to} : {from, to, from_port: port}); designMarkDirty();
    designMessage("Connection added."); designRender(); return;
  }
  const selected = event.target.closest("[data-design-select]");
  if (selected && event.shiftKey) {
    const id = selected.dataset.designSelect, multi = agentDesignState.multi;
    if (agentDesignState.selected && agentDesignState.selected !== id) multi.add(agentDesignState.selected);
    if (multi.has(id)) { multi.delete(id); if (agentDesignState.selected === id) agentDesignState.selected = [...multi][0] || ""; }
    else { multi.add(id); agentDesignState.selected = id; }
    designRender(); designFocusSelected();
    designMessage(`${designSelection().size} nodes selected. Drag to move them together; Ctrl+C copies, Delete removes.`);
  } else if (selected) designSelect(selected.dataset.designSelect);
});
document.querySelector("#agent-designer").addEventListener("keydown", event => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "z" && can("agents_write") && !event.target.closest("input, textarea, select")) {
    event.preventDefault(); designApplyHistory(event.shiftKey ? "redo" : "undo", event.shiftKey ? "undo" : "redo"); return;
  }
  if (event.key === "Escape" && agentDesignState.connecting) { agentDesignState.connecting = ""; agentDesignState.connectingPort = ""; designMessage("Connection cancelled."); designRender(); return; }
  if (!can("agents_write") || !event.target.closest(".design-canvas-scroll")) return;
  const shortcut = (event.ctrlKey || event.metaKey) ? event.key.toLowerCase() : "";
  if (shortcut === "v" && agentDesignState.clipboard) { event.preventDefault(); designPaste(); designFocusSelected(); return; }
  if (!agentDesignState.selected) return;
  if (shortcut === "c") { event.preventDefault(); designCopySelection(); return; }
  if (event.key === "Delete" || event.key === "Backspace") { event.preventDefault(); designDeleteSelection(); return; }
  const movement = {ArrowLeft:[-1,0],ArrowRight:[1,0],ArrowUp:[0,-1],ArrowDown:[0,1]}[event.key];
  if (!movement) return;
  event.preventDefault();
  const step = event.shiftKey ? 40 : 10;
  for (const node of agentDesignState.draft.nodes.filter(item => designSelection().has(item.id))) {
    node.x = Math.max(0, Math.min(9800, node.x + movement[0] * step));
    node.y = Math.max(0, Math.min(9800, node.y + movement[1] * step));
  }
  designMarkDirty(); designRender(); designFocusSelected();
});
document.querySelector("#design-nodes").addEventListener("pointerdown", event => {
  if (!can("agents_write") || event.target.closest(".design-port")) return;
  const body = event.target.closest(".design-node-body"); if (!body) return;
  const card = body.closest(".design-node"), id = card.dataset.designNode;
  // Dragging a node in a multi-selection moves the whole selection.
  const group = agentDesignState.multi.has(id) ? agentDesignState.draft.nodes.filter(item => designSelection().has(item.id)) : agentDesignState.draft.nodes.filter(item => item.id === id);
  const origins = group.map(item => ({item, x: item.x, y: item.y, card: document.querySelector(`[data-design-node="${CSS.escape(item.id)}"]`)}));
  const start = {x: event.clientX, y: event.clientY}; let moved = false;
  const move = current => { const dx = (current.clientX - start.x) / agentDesignState.zoom, dy = (current.clientY - start.y) / agentDesignState.zoom; if (Math.abs(dx)+Math.abs(dy)<4 && !moved) return; moved = true;
    for (const origin of origins) { origin.item.x = Math.max(0, Math.min(9800, Math.round(origin.x+dx))); origin.item.y = Math.max(0, Math.min(9800, Math.round(origin.y+dy))); if (origin.card) { origin.card.style.left = `${origin.item.x}px`; origin.card.style.top = `${origin.item.y}px`; } }
    designRenderCanvasBounds(); designRenderEdgesOnly(); };
  const finish = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", finish); if (moved) { designMarkDirty(); designRender(); } };
  window.addEventListener("pointermove", move); window.addEventListener("pointerup", finish, {once:true});
});
function designRenderEdgesOnly() {
  const draft = agentDesignState.draft, nodes = new Map(draft.nodes.map(node => [node.id,node]));
  document.querySelectorAll("#design-edges .design-edge").forEach(path => { const [fromID,toID] = path.dataset.designEdge.split(">"); const from = nodes.get(fromID), to = nodes.get(toID); if (!from || !to) return; path.setAttribute("d", designEdgePath(from, to, path.dataset.designPort)); });
}
document.querySelector("#design-inspector-content").addEventListener("input", event => {
  const node = agentDesignState.draft.nodes.find(item => item.id === agentDesignState.selected);
  if (!node) return;
  if (event.target.id === "design-node-name") {
    node.name = event.target.value;
    document.querySelector(`[data-design-node="${CSS.escape(node.id)}"] strong`).textContent = node.name;
  } else if (event.target.id === "design-node-group") {
    if (event.target.value.trim()) node.group = event.target.value; else delete node.group;
  } else if (event.target.dataset.designConfig) {
    node.config ||= {};
    node.config[event.target.dataset.designConfig] = event.target.value;
  }
  designMarkDirty(true);
});
document.querySelector("#design-inspector-content").addEventListener("change", event => {
  if (event.target.tagName !== "SELECT" || !event.target.dataset.designConfig) return;
  const node = agentDesignState.draft.nodes.find(item => item.id === agentDesignState.selected);
  if (!node) return;
  node.config ||= {};
  node.config[event.target.dataset.designConfig] = event.target.value;
  designMarkDirty();
});
document.querySelector("#design-inspector-content").addEventListener("click", event => {
  const node = agentDesignState.draft.nodes.find(item => item.id === agentDesignState.selected);
  if (!node || !can("agents_write")) return;
  const removeEdge = event.target.closest("[data-design-remove-edge]");
  if (removeEdge) {
    agentDesignState.draft.edges.splice(Number(removeEdge.dataset.designRemoveEdge), 1);
    designMarkDirty(); designRender(); designMessage("Connection removed.");
  } else if (event.target.id === "design-duplicate-node" && !["input", "output"].includes(node.kind)) {
    if (agentDesignState.draft.nodes.length >= 100) { designMessage("A design can have at most 100 nodes.", true); return; }
    const copy = {...node, id:`${node.kind}-${Math.random().toString(36).slice(2, 9)}`, name:`${node.name.slice(0, 95)} copy`, x:Math.min(node.x + 35, 9800), y:Math.min(node.y + 110, 9800), config:{...node.config}};
    agentDesignState.draft.nodes.push(copy); agentDesignState.selected = copy.id;
    designMarkDirty(); designRender(); designMessage("Node duplicated without connections.");
  } else if (event.target.id === "design-remove-node") {
    agentDesignState.draft.nodes = agentDesignState.draft.nodes.filter(item => item.id !== node.id);
    agentDesignState.draft.edges = agentDesignState.draft.edges.filter(edge => edge.from !== node.id && edge.to !== node.id);
    agentDesignState.selected = ""; designMarkDirty(); designRender();
  } else if (event.target.id === "design-remove-edges") {
    agentDesignState.draft.edges = agentDesignState.draft.edges.filter(edge => edge.from !== node.id && edge.to !== node.id);
    designMarkDirty(); designRender();
  }
});
document.querySelector("#design-revisions").addEventListener("click", event => {
  const button = event.target.closest("[data-design-restore]"); if (!button || !can("agents_write")) return;
  const snapshot = (agentDesignState.draft.history || []).find(item => String(item.revision) === button.dataset.designRestore);
  if (!snapshot || !confirm(`Restore revision ${snapshot.revision} into the editor? Save to create a new revision.`)) return;
  Object.assign(agentDesignState.draft, {name: snapshot.name, description: snapshot.description || "", nodes: structuredClone(snapshot.nodes), edges: structuredClone(snapshot.edges), settings: snapshot.settings ? structuredClone(snapshot.settings) : undefined});
  agentDesignState.selected = ""; designMarkDirty(); designRender(); designMessage(`Revision ${snapshot.revision} loaded. Save to create a new revision.`);
});
document.querySelector("#design-save").addEventListener("click", async event => {
  if (!can("agents_write") || agentDesignState.saving) return;
  const issues = designValidation();
  if (issues.length) { designMessage(issues.join(" "), true); return; }
  const draft = agentDesignState.draft, path = draft.id ? `/api/v1/agent-designs/${encodeURIComponent(draft.id)}` : "/api/v1/agent-designs";
  const saved = JSON.stringify(designSnapshot());
  agentDesignState.saving = true; designUpdateStatus();
  try {
    const response = await fetch(path, {method:draft.id ? "PUT" : "POST", headers:{"Content-Type":"application/json","Accept":"application/json"}, body:saved});
    const result = await response.json();
    if (!response.ok) { designMessage(response.status === 409 ? "This design changed elsewhere. Reload its latest revision before saving again." : (result.issues || [result.message || result.error || "Save failed."]).join(" "), true); return; }
    agentDesignState.list = [result,...agentDesignState.list.filter(item => item.id !== result.id)];
    if (agentDesignState.draft === draft) {
      const changedDuringSave = JSON.stringify(designSnapshot()) !== saved;
      if (changedDuringSave) {
        Object.assign(draft, {id: result.id, revision: result.revision, history: result.history, updated_at: result.updated_at});
        agentDesignState.dirty = true;
      } else { agentDesignState.draft = result; agentDesignState.dirty = false; designResetHistory(); }
      selectedResource = result.id;
      history.replaceState({}, "", routeURL("agent-designer", result.id));
      designRender();
      designMessage(changedDuringSave ? `Saved revision ${result.revision}. Newer edits are still unsaved.` : `Saved revision ${result.revision}.`);
    }
  } catch (failure) { designMessage(failure.message || "Save failed.", true); }
  finally { agentDesignState.saving = false; designUpdateStatus(); }
});
document.querySelector("#design-export").addEventListener("click", () => { const content = JSON.stringify(designSnapshot(), null, 2); const url = URL.createObjectURL(new Blob([content], {type:"application/json"})); const link = document.createElement("a"); link.href=url; link.download=`${agentDesignState.draft.name.trim().replace(/[^a-z0-9-]+/gi,"-").toLowerCase() || "agent"}.json`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); });
