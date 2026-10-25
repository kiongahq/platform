/* Native Kionga agent graph drafts. Connections and positions are stored by
 * the platform API; the browser never stores credentials in the graph. */
const agentDesignState = {draft: null, list: [], selected: "", connecting: "", dirty: false, models: [], tools: []};
const designKinds = {input: "Input", prompt: "Prompt", model: "Model", tool: "Tool", output: "Output"};
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
  return {project_id: draft.project_id, name: (draft.name || "").trim(), description: (draft.description || "").trim(), revision: draft.revision, nodes: draft.nodes, edges: draft.edges};
}
function designMarkDirty() {
  agentDesignState.dirty = true;
  document.querySelector("#design-revision").textContent = agentDesignState.draft.revision ? `Revision ${agentDesignState.draft.revision} · unsaved changes` : "Unsaved";
}
function designSelect(id) { agentDesignState.selected = id; designRender(); }
function designRender() {
  const draft = agentDesignState.draft;
  if (!draft) return;
  document.querySelector("#design-project").value = draft.project_id;
  document.querySelector("#design-name").value = draft.name;
  document.querySelector("#design-description").value = draft.description || "";
  for (const control of document.querySelectorAll("#agent-designer input, #agent-designer textarea, #agent-designer [data-design-add], #design-remove-node, #design-remove-edges")) control.disabled = !can("agents_write");
  document.querySelector("#design-project").disabled = !can("agents_write") || Boolean(draft.id);
  document.querySelector("#design-revision").textContent = draft.revision ? `Revision ${draft.revision}${agentDesignState.dirty ? " · unsaved changes" : ""}` : "Unsaved";
  const options = agentDesignState.list.filter(item => !selectedProject || item.project_id === selectedProject);
  document.querySelector("#design-list").innerHTML = `<option value="">New design</option>${options.map(item => `<option value="${escapeHTML(item.id)}" ${item.id === draft.id ? "selected" : ""}>${escapeHTML(item.name)} · r${item.revision}</option>`).join("")}`;
  const nodes = new Map(draft.nodes.map(node => [node.id, node]));
  document.querySelector("#design-edges").innerHTML = `<defs><marker id="design-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z"/></marker></defs>` + draft.edges.map(edge => {
    const from = nodes.get(edge.from), to = nodes.get(edge.to);
    if (!from || !to) return "";
    const x1 = from.x + designNodeWidth, y1 = from.y + designNodeHeight / 2, x2 = to.x, y2 = to.y + designNodeHeight / 2;
    return `<path class="design-edge" data-design-edge="${escapeHTML(edge.from + ">" + edge.to)}" d="M ${x1} ${y1} C ${x1 + 75} ${y1}, ${x2 - 75} ${y2}, ${x2} ${y2}" marker-end="url(#design-arrow)"><title>${escapeHTML(from.name)} to ${escapeHTML(to.name)}</title></path>`;
  }).join("");
  document.querySelector("#design-nodes").innerHTML = draft.nodes.map(node => `<div class="design-node ${node.id === agentDesignState.selected ? "selected" : ""}" style="left:${Number(node.x)}px;top:${Number(node.y)}px" data-design-node="${escapeHTML(node.id)}">
    ${node.kind !== "input" ? `<button class="design-port in" type="button" data-design-in="${escapeHTML(node.id)}" aria-label="Connect to ${escapeHTML(node.name)}" title="Connect here"></button>` : ""}
    <button type="button" class="design-node-body" data-design-select="${escapeHTML(node.id)}"><span class="design-kind">${escapeHTML(designKinds[node.kind])}</span><strong>${escapeHTML(node.name)}</strong><small>${escapeHTML(node.id)}</small></button>
    ${node.kind !== "output" ? `<button class="design-port out ${agentDesignState.connecting === node.id ? "pending" : ""}" type="button" data-design-out="${escapeHTML(node.id)}" aria-label="Connect from ${escapeHTML(node.name)}" title="Connect from here"></button>` : ""}
  </div>`).join("");
  designRenderInspector();
  const history = [...(draft.history || [])].reverse();
  document.querySelector("#design-revisions").innerHTML = draft.id ? `<h3>Earlier revisions</h3>${history.length ? history.map(item => `<button type="button" data-design-restore="${item.revision}" ${can("agents_write") ? "" : "disabled"}>Restore r${item.revision} <small>${escapeHTML(new Date(item.saved_at).toLocaleString())}</small></button>`).join("") : `<p class="field-help">No earlier revisions yet.</p>`}` : "";
  for (const control of document.querySelectorAll("#design-inspector-content input, #design-inspector-content textarea, #design-inspector-content button")) control.disabled = !can("agents_write");
}
function designRenderInspector() {
  const node = agentDesignState.draft?.nodes.find(item => item.id === agentDesignState.selected);
  const panel = document.querySelector("#design-inspector-content");
  if (!node) { panel.innerHTML = `<p>Select a node to edit it.</p><p class="field-help">Drag nodes to arrange the canvas. Select an output port, then an input port, to connect nodes.</p>`; return; }
  const fields = node.kind === "prompt" ? [["template", "Prompt template", "textarea", "Use {input} for the agent message."]]
    : node.kind === "model" ? [["model", "Model reference", "input", "Choose a registered Kionga model by ID. This draft does not invoke it yet."]]
    : node.kind === "tool" ? [["tool", "Tool name", "input", "Choose a registered Kionga tool. This draft does not invoke it yet."]]
    : [];
  panel.innerHTML = `<span class="tag">${escapeHTML(designKinds[node.kind])}</span><label>Node label<input id="design-node-name" maxlength="100" value="${escapeHTML(node.name)}"></label><p class="field-help">ID: <code>${escapeHTML(node.id)}</code></p>${fields.map(([key,label,tag,hint]) => `<label>${label}${tag === "textarea" ? `<textarea data-design-config="${key}" rows="7" spellcheck="false">${escapeHTML(node.config?.[key] || "")}</textarea>` : `<input data-design-config="${key}" ${key === "model" ? 'list="design-model-options"' : key === "tool" ? 'list="design-tool-options"' : ""} value="${escapeHTML(node.config?.[key] || "")}">`}<small>${hint}</small></label>`).join("")}
    <div class="design-inspector-actions">${!["input", "output"].includes(node.kind) ? `<button type="button" class="danger" id="design-remove-node">Remove node</button>` : ""}<button type="button" id="design-remove-edges">Remove connections</button></div>`;
}
async function loadAgentDesigner() {
  const [data, models, tools] = await Promise.all([
    api("/api/v1/agent-designs"),
    hasService("models") ? api("/api/v1/models").catch(() => ({items: []})) : Promise.resolve({items: []}),
    hasService("catalog") ? api("/api/v1/tools").catch(() => ({items: []})) : Promise.resolve({items: []}),
  ]);
  agentDesignState.list = data.items || [];
  agentDesignState.models = models.items || [];
  agentDesignState.tools = tools.items || [];
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
  agentDesignState.selected = ""; agentDesignState.connecting = "";
  designRender();
}
registerView("agent-designer", loadAgentDesigner, {eyebrow: "AGENT BUILDER", title: "Agent designer", openResource: id => { if (!agentDesignState.list.some(item => item.id === id)) designMessage("Design not found in this project.", true); }});
document.querySelector("#design-new").addEventListener("click", () => { if (agentDesignState.dirty && !confirm("Discard unsaved design changes?")) return; agentDesignState.draft = blankAgentDesign(); agentDesignState.selected = ""; agentDesignState.connecting = ""; agentDesignState.dirty = false; selectedResource = ""; history.replaceState({}, "", routeURL("agent-designer", "")); designMessage("New design. Add nodes and save the first revision."); designRender(); });
document.querySelector("#design-list").addEventListener("change", event => { if (agentDesignState.dirty && !confirm("Discard unsaved design changes?")) { designRender(); return; } const item = agentDesignState.list.find(value => value.id === event.target.value); agentDesignState.draft = item ? structuredClone(item) : blankAgentDesign(); agentDesignState.dirty = false; agentDesignState.selected = ""; selectedResource = item?.id || ""; history.replaceState({}, "", routeURL("agent-designer", selectedResource)); designMessage(""); designRender(); });
document.querySelector("#design-project").addEventListener("change", event => { if (agentDesignState.draft.id) { designMessage("Create a new design to change projects.", true); designRender(); return; } agentDesignState.draft.project_id = event.target.value; designMarkDirty(); });
for (const [selector, field] of [["#design-name", "name"], ["#design-description", "description"]]) document.querySelector(selector).addEventListener("input", event => { agentDesignState.draft[field] = event.target.value; designMarkDirty(); });
document.querySelectorAll("[data-design-add]").forEach(button => button.addEventListener("click", () => {
  if (!can("agents_write")) return;
  const kind = button.dataset.designAdd, id = `${kind}-${Math.random().toString(36).slice(2, 9)}`;
  const count = agentDesignState.draft.nodes.length;
  agentDesignState.draft.nodes.push({id, kind, name: designKinds[kind], x: 250 + (Math.floor((count - 2) / 4) * 205), y: 250 + ((count - 2) % 4) * 130, config: {}});
  agentDesignState.selected = id; designMarkDirty(); designRender();
}));
document.querySelector("#design-nodes").addEventListener("click", event => {
  const out = event.target.closest("[data-design-out]"), input = event.target.closest("[data-design-in]");
  if (out && can("agents_write")) { agentDesignState.connecting = out.dataset.designOut; designMessage("Choose an input port to complete the connection."); designRender(); return; }
  if (input && agentDesignState.connecting && can("agents_write")) {
    const from = agentDesignState.connecting, to = input.dataset.designIn; agentDesignState.connecting = "";
    if (from !== to && !agentDesignState.draft.edges.some(edge => edge.from === from && edge.to === to)) { agentDesignState.draft.edges.push({from,to}); designMarkDirty(); }
    designMessage(""); designRender(); return;
  }
  const selected = event.target.closest("[data-design-select]"); if (selected) designSelect(selected.dataset.designSelect);
});
document.querySelector("#design-nodes").addEventListener("pointerdown", event => {
  if (!can("agents_write") || event.target.closest(".design-port")) return;
  const body = event.target.closest(".design-node-body"); if (!body) return;
  const card = body.closest(".design-node"), node = agentDesignState.draft.nodes.find(item => item.id === card.dataset.designNode);
  const start = {x: event.clientX, y: event.clientY, nx: node.x, ny: node.y}; let moved = false;
  const move = current => { const dx = current.clientX - start.x, dy = current.clientY - start.y; if (Math.abs(dx)+Math.abs(dy)<4 && !moved) return; moved = true; node.x = Math.max(0, Math.min(9800, Math.round(start.nx+dx))); node.y = Math.max(0, Math.min(9800, Math.round(start.ny+dy))); card.style.left = `${node.x}px`; card.style.top = `${node.y}px`; designRenderEdgesOnly(); };
  const finish = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", finish); if (moved) { designMarkDirty(); designRender(); } };
  window.addEventListener("pointermove", move); window.addEventListener("pointerup", finish, {once:true});
});
function designRenderEdgesOnly() {
  const draft = agentDesignState.draft, nodes = new Map(draft.nodes.map(node => [node.id,node]));
  document.querySelectorAll("#design-edges .design-edge").forEach(path => { const [fromID,toID] = path.dataset.designEdge.split(">"); const from = nodes.get(fromID), to = nodes.get(toID); if (!from || !to) return; const x1=from.x+designNodeWidth,y1=from.y+designNodeHeight/2,x2=to.x,y2=to.y+designNodeHeight/2; path.setAttribute("d",`M ${x1} ${y1} C ${x1+75} ${y1}, ${x2-75} ${y2}, ${x2} ${y2}`); });
}
document.querySelector("#design-inspector-content").addEventListener("input", event => { const node = agentDesignState.draft.nodes.find(item => item.id === agentDesignState.selected); if (!node) return; if (event.target.id === "design-node-name") { node.name = event.target.value; document.querySelector(`[data-design-node="${CSS.escape(node.id)}"] strong`).textContent = node.name; } else if (event.target.dataset.designConfig) { node.config ||= {}; node.config[event.target.dataset.designConfig] = event.target.value; } designMarkDirty(); });
document.querySelector("#design-inspector-content").addEventListener("click", event => { const node = agentDesignState.draft.nodes.find(item => item.id === agentDesignState.selected); if (!node) return; if (event.target.id === "design-remove-node") { agentDesignState.draft.nodes = agentDesignState.draft.nodes.filter(item => item.id !== node.id); agentDesignState.draft.edges = agentDesignState.draft.edges.filter(edge => edge.from !== node.id && edge.to !== node.id); agentDesignState.selected = ""; designMarkDirty(); designRender(); } else if (event.target.id === "design-remove-edges") { agentDesignState.draft.edges = agentDesignState.draft.edges.filter(edge => edge.from !== node.id && edge.to !== node.id); designMarkDirty(); designRender(); } });
document.querySelector("#design-revisions").addEventListener("click", event => {
  const button = event.target.closest("[data-design-restore]"); if (!button || !can("agents_write")) return;
  const snapshot = (agentDesignState.draft.history || []).find(item => String(item.revision) === button.dataset.designRestore);
  if (!snapshot || !confirm(`Restore revision ${snapshot.revision} into the editor? Save to create a new revision.`)) return;
  Object.assign(agentDesignState.draft, {name: snapshot.name, description: snapshot.description || "", nodes: structuredClone(snapshot.nodes), edges: structuredClone(snapshot.edges)});
  agentDesignState.selected = ""; designMarkDirty(); designRender(); designMessage(`Revision ${snapshot.revision} loaded. Save to create a new revision.`);
});
document.querySelector("#design-save").addEventListener("click", async event => {
  if (!can("agents_write")) return;
  const draft = agentDesignState.draft, path = draft.id ? `/api/v1/agent-designs/${encodeURIComponent(draft.id)}` : "/api/v1/agent-designs";
  try {
    const response = await fetch(path, {method:draft.id ? "PUT" : "POST", headers:{"Content-Type":"application/json","Accept":"application/json"}, body:JSON.stringify(designSnapshot())});
    const result = await response.json();
    if (!response.ok) { designMessage((result.issues || [result.message || result.error || "Save failed."]).join(" "), true); return; }
    agentDesignState.draft = result; agentDesignState.dirty = false; agentDesignState.list = [result,...agentDesignState.list.filter(item => item.id !== result.id)]; selectedResource = result.id; history.replaceState({}, "", routeURL("agent-designer", result.id)); designMessage(`Saved revision ${result.revision}.`); designRender();
  } catch (failure) { designMessage(failure.message, true); }
});
document.querySelector("#design-export").addEventListener("click", () => { const content = JSON.stringify(designSnapshot(), null, 2); const url = URL.createObjectURL(new Blob([content], {type:"application/json"})); const link = document.createElement("a"); link.href=url; link.download=`${agentDesignState.draft.name.trim().replace(/[^a-z0-9-]+/gi,"-").toLowerCase() || "agent"}.json`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); });
