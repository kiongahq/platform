/* Agents: deployments, sessions, prompts, chat test console, traffic. */
let agentCache = [];
async function loadAgents() {
  const [data, prompts] = await Promise.all([api("/api/v1/agents"), hasService("catalog") ? api("/api/v1/prompts").catch(() => ({items: [], unavailable: true})) : Promise.resolve({items: []})]);
  data.items = scoped(data.items || []);
  agentCache = data.items || [];
  const sessionGroups = await Promise.all(data.items.map(agent => api(`/api/v1/agents/${encodeURIComponent(agent.id)}/sessions`).catch(() => ({items: []}))));
  const sessions = sessionGroups.flatMap(group => group.items);
  const tokens = sessions.reduce((sum, item) => sum + item.input_tokens + item.output_tokens, 0);
  const cost = sessions.reduce((sum, item) => sum + item.cost_usd, 0);
  const readyAgents = data.items.filter(agent => agent.status === "ready").length;
  document.querySelector("#agent-summary").innerHTML = `<article><span>Agent versions</span><strong>${data.items.length}</strong><small>${plural(readyAgents, "runtime")} ready</small></article><article><span>Active sessions</span><strong>${sessions.filter(item => item.status === "running").length}</strong><small>${plural(sessions.length, "session")} total</small></article><article><span>LLM cost</span><strong>$${cost.toFixed(4)}</strong><small>${tokens.toLocaleString()} tokens</small></article>`;
  document.querySelector("#agent-grid").innerHTML = data.items.length ? data.items.map(agent => {
    const ready = agent.status === "ready";
    const actions = can("agents_write") ? `<button type="button" data-agent-traffic="${escapeHTML(agent.id)}">Traffic</button><button type="button" class="primary" data-agent-chat="${escapeHTML(agent.id)}" data-agent-name="${escapeHTML(agent.name)}" ${ready ? "" : "disabled title=\"The runtime health check has not passed yet\""}>Chat</button>` : `<span class="tag">Read-only</span>`;
    const resources = agent.resources || {};
    const scaling = agent.autoscaling || {min_replicas: agent.replicas || 1, max_replicas: agent.replicas || 1};
    return `<article class="card interactive-card" role="button" tabindex="0" data-agent-detail="${escapeHTML(agent.id)}" aria-label="Open agent ${escapeHTML(agent.name)}"><span class="kind">${escapeHTML(agent.llm_backend)} · v${escapeHTML(agent.version)}</span><h3>${escapeHTML(agent.name)}</h3><p class="truncate" title="${escapeHTML(agent.graph_module)}">${escapeHTML(agent.graph_module)}</p><div class="tags"><span class="tag">${escapeHTML(resources.cpu || "500m")} CPU</span><span class="tag">${escapeHTML(resources.memory || "1Gi")} RAM</span><span class="tag">${scaling.min_replicas}–${scaling.max_replicas} replicas</span>${resources.gpu ? `<span class="tag">${resources.gpu} × ${escapeHTML(resources.gpu_type)}</span>` : ""}${(agent.tools || []).map(tool => `<span class="tag">${escapeHTML(tool)}</span>`).join("")}</div><footer>${status(agent.status)}<span class="tag">${agent.canary_weight}% canary</span>${actions}</footer></article>`;
  }).join("") : emptyState("No agents deployed yet", "Scaffold a production-agent project, build its image, then deploy it here.");
  document.querySelector("#session-table").innerHTML = sessions.length ? sessions.map(session => `<tr><td><span class="truncate mono" title="${escapeHTML(session.id)}">${escapeHTML(session.id)}</span></td><td>${escapeHTML(session.agent_id)}</td><td>${escapeHTML(session.current_node)}</td><td>${status(session.status)}</td><td>${session.turns}</td><td>${(session.input_tokens + session.output_tokens).toLocaleString()}</td><td>$${session.cost_usd.toFixed(4)}</td></tr>`).join("") : tableEmpty(7, "No sessions yet", "Chat with a ready agent to start one.");
  document.querySelector("#prompt-list").closest("article").hidden = !hasService("catalog");
  document.querySelector("#prompt-list").innerHTML = prompts.unavailable ? emptyState("Prompts are temporarily unavailable", "Use the refresh button to retry.") : prompts.configured
    ? (prompts.items.length ? prompts.items.map(prompt => `<div class="prompt-row"><b>${escapeHTML(prompt.name)}</b><span class="tag">v${escapeHTML(prompt.version ?? "?")}</span>${(prompt.labels || []).map(label => `<span class="tag">${escapeHTML(label)}</span>`).join("")}</div>`).join("") : emptyState("No prompts stored yet", "Langfuse is connected."))
    : emptyState("Langfuse not configured", "Prompts appear here once Langfuse is connected on the Platform page.");
}

document.querySelector("#deploy-agent").addEventListener("click", async () => {
  const modal = document.querySelector("#deploy-agent-dialog");
  const form = document.querySelector("#deploy-agent-form");
  form.reset();
  document.querySelector("#deploy-agent-error").textContent = "";
  document.querySelector("#agent-tool-options").innerHTML = `<p class="field-help">Loading tools…</p>`;
  if (!modal.open) modal.showModal();
  try {
    const [, tools] = await Promise.all([loadProjectOptions(), api("/api/v1/tools").catch(() => ({items: [], unavailable: true}))]);
    if (!projectOptions.length) throw new Error("Ask your administrator to assign a project before deploying an agent.");
    const options = tools.items || [];
    document.querySelector("#agent-tool-options").innerHTML = options.length
      ? options.map(tool => `<label class="check-card"><input type="checkbox" name="agent_tools" value="${escapeHTML(tool.name)}"><span><b>${escapeHTML(tool.name)}</b><small>v${escapeHTML(tool.version)} · ${escapeHTML(tool.description || "Registered platform tool")}</small></span></label>`).join("")
      : `<p class="field-help">${tools.unavailable ? "Tool catalog access is not assigned. " : "No tools are registered. "}The agent can be deployed without tools.</p>`;
    configureAgentCapacityFields(form);
  } catch (failure) { document.querySelector("#deploy-agent-error").textContent = failure.message; }
});

function configureAgentCapacityFields(form) {
  const grant = effectiveGrant();
  if (!grant) { document.querySelector("#agent-capacity-hint").textContent = "Capacity is checked against the administrator's grant."; return; }
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
    name: documentValue.metadata?.name || "", version: spec.version || "", image: spec.image || "",
    graph_module: spec.graphModule || "", llm_backend: spec.llm?.backend || "mock",
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
  const form = event.target, error = document.querySelector("#deploy-agent-error");
  error.textContent = "";
  try {
    const payload = Object.fromEntries(new FormData(form));
    payload.autoscaling = {min_replicas: Number(payload.min_replicas), max_replicas: Number(payload.max_replicas)};
    payload.resources = {cpu: payload.agent_cpu, memory: payload.agent_memory, gpu: Number(payload.agent_gpu), gpu_type: payload.agent_gpu_type};
    payload.replicas = payload.autoscaling.min_replicas;
    payload.tools = [...form.querySelectorAll("[name='agent_tools']:checked")].map(input => input.value);
    if (payload.autoscaling.max_replicas < payload.autoscaling.min_replicas) throw new Error("Maximum replicas cannot be below minimum replicas.");
    const grant = effectiveGrant();
    if (grant && payload.autoscaling.max_replicas > Number(grant.compute?.max_vms || 0)) throw new Error("Maximum replicas exceed your assigned workload capacity.");
    if (grant && payload.resources.gpu * payload.autoscaling.max_replicas > Number(grant.compute?.gpus || 0)) throw new Error("GPU capacity at maximum replicas exceeds your assignment.");
    ["agent_tools", "min_replicas", "max_replicas", "agent_cpu", "agent_memory", "agent_gpu", "agent_gpu_type"].forEach(key => delete payload[key]);
    await withBusy(event.submitter, () => api("/api/v1/agents", {method: "POST", body: JSON.stringify(payload)}), {failure: false});
    document.querySelector("#deploy-agent-dialog").close();
    toast("Agent deployment accepted. It becomes chat-ready after its health check passes.");
    await loadAgents();
  } catch (failure) { error.textContent = failure.message; }
});

document.querySelector("#agent-framework-preset").addEventListener("change", event => {
  const kind = event.target.value;
  if (!kind) return;
  document.querySelector("#deploy-agent-form [name=graph_module]").value = kind === "langgraph" ? "agents.customer_support.graph:build" : `agents.framework_examples:build_${kind}`;
  document.querySelector("#agent-framework-help").textContent = kind === "nooa"
    ? "NOOA requires a separate Python 3.12–3.13 image, NOOA_MODEL, provider credentials, and OS-level sandboxing. The example is disabled until KIONGA_ALLOW_CODE_EXECUTION=1. This flag is not a sandbox."
    : kind === "agno" ? "Build with the Agno extra; configure AGNO_MODEL and OPENAI_API_KEY in the deployment. The factory owns its provider configuration; the LangGraph LLM selector does not configure Agno."
    : kind === "custom" ? "Replace the example with your adapter. The factory owns provider configuration and durable session storage."
    : "LangGraph uses the platform model and checkpointer settings.";
});

const chatState = {agentId: "", sessionId: ""};
document.querySelector("#chat-form").addEventListener("submit", async event => {
  event.preventDefault();
  const input = document.querySelector("#chat-input"), log = document.querySelector("#chat-log");
  const message = input.value.trim();
  if (!message) return;
  input.value = "";
  if (log.querySelector(".empty-state,.field-help")) log.innerHTML = "";
  log.insertAdjacentHTML("beforeend", `<div class="chat-turn user"><span>You</span><p>${escapeHTML(message)}</p></div>`);
  log.insertAdjacentHTML("beforeend", `<div class="chat-turn agent pending"><span>Agent</span><p>…</p></div>`);
  log.scrollTop = log.scrollHeight;
  try {
    const reply = await api(`/api/v1/agents/${encodeURIComponent(chatState.agentId)}/invoke`, {method: "POST", body: JSON.stringify({message, session_id: chatState.sessionId, user_id: "console"})});
    chatState.sessionId = reply.session_id;
    log.lastElementChild.outerHTML = `<div class="chat-turn agent"><span>Agent · ${reply.usage_available === false ? "Usage unavailable" : `${reply.input_tokens + reply.output_tokens} tokens`} · ${reply.duration_ms}ms</span><p>${escapeHTML(reply.reply)}</p></div>`;
  } catch (failure) {
    log.lastElementChild.outerHTML = `<div class="chat-turn agent failed"><span>Agent</span><p>${escapeHTML(failure.message)}</p></div>`;
  }
  log.scrollTop = log.scrollHeight;
});

const trafficState = {agentId: ""};
document.querySelector("#traffic-weight").addEventListener("input", event => { document.querySelector("#traffic-value").textContent = `${event.target.value}%`; });
document.querySelector("#traffic-form").addEventListener("submit", async event => {
  event.preventDefault();
  const error = document.querySelector("#traffic-error");
  const weight = Number(document.querySelector("#traffic-weight").value);
  error.textContent = "";
  try {
    await withBusy(event.submitter, () => api(`/api/v1/agents/${encodeURIComponent(trafficState.agentId)}/traffic`, {method: "PUT", body: JSON.stringify({canary_weight: weight})}), {failure: false});
    document.querySelector("#traffic-dialog").close();
    toast(`Canary weight set to ${weight}%.`);
    await loadAgents();
  } catch (failure) { error.textContent = failure.message; }
});
onClick("[data-agent-chat]", node => {
  chatState.agentId = node.dataset.agentChat; chatState.sessionId = "";
  document.querySelector("#chat-agent-name").textContent = node.dataset.agentName;
  document.querySelector("#chat-log").innerHTML = `<p class="field-help">Ask something. The turn runs through the real agent runtime and is traced.</p>`;
  document.querySelector("#chat-dialog").showModal();
});
onClick("[data-agent-traffic]", node => {
  trafficState.agentId = node.dataset.agentTraffic;
  document.querySelector("#traffic-weight").value = "10";
  document.querySelector("#traffic-value").textContent = "10%";
  document.querySelector("#traffic-error").textContent = "";
  document.querySelector("#traffic-dialog").showModal();
});
onClick("[data-agent-detail]", node => {
  const item = agentCache.find(agent => agent.id === node.dataset.agentDetail);
  if (!item) return;
  selectedResource = item.id; history.replaceState({}, "", routeURL("agents", item.id));
  showMetadata("AGENT", `${item.name} v${item.version}`, item, metaList([["Agent ID", copyable(item.id)], ["Project", escapeHTML(projectName(item.project_id))], ["Status", status(item.status)], ["Image", `<code>${escapeHTML(item.image)}</code>`], ["Entrypoint", `<code>${escapeHTML(item.graph_module)}</code>`], ["Canary", `${item.canary_weight}%`], ["Deployed", timeTag(item.created_at)]]));
}, {ignoreControls: true});

registerView("agents", loadAgents, {
  eyebrow: "AGENT OPERATIONS", title: "Agents",
  openResource: id => document.querySelector(`[data-agent-detail="${CSS.escape(id)}"]`)?.click(),
  intro: {
    what: "Deployed AI agents (LangGraph, Agno, NOOA or custom adapters) with sessions, traces, token usage and cost.",
    why: "Ship agents with resource limits, canary traffic and observable turns instead of opaque scripts.",
    needs: "The agents service, a project, and an agent image built from a production-agent project.",
    action: "Deploy agent registers the runtime. Chat sends a real turn through it.",
    results: "Sessions and cost update below; failed turns show the runtime's error in the chat console.",
  },
});
