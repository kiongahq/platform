/* Users & access tabs beyond Users: groups, IAM-style policies (form and
 * JSON editors), attachments, and the access simulator. Loaded after
 * access.js; the access view loader calls loadIAM(). */
let iamState = {groups: [], policies: [], attachments: [], catalog: {actions: [], profiles: []}};
let accessTab = "users";
let policyEditorMode = "form";

const accessTabs = ["users", "groups", "policies", "attachments", "simulator"];
function showAccessTab(tab) {
  accessTab = accessTabs.includes(tab) ? tab : "users";
  for (const id of accessTabs) {
    const button = document.querySelector(`#access-tab-${id}`);
    button.setAttribute("aria-selected", String(id === accessTab));
    button.tabIndex = id === accessTab ? 0 : -1;
    document.querySelector(`#access-panel-${id}`).hidden = id !== accessTab;
  }
  document.querySelector("#add-user-access").hidden = accessTab !== "users";
}

async function loadIAM() {
  const [groups, policies, attachments, catalog] = await Promise.all([
    api("/api/v1/admin/iam/groups"), api("/api/v1/admin/iam/policies"),
    api("/api/v1/admin/iam/attachments"), api("/api/v1/iam/catalog"),
  ]);
  iamState = {groups: groups.items || [], policies: policies.items || [], attachments: attachments.items || [], catalog};
  renderIAMGroups();
  renderIAMPolicies();
  renderIAMAttachments();
  renderIAMOptions();
  showAccessTab(accessTab);
}

const attachmentsOf = predicate => iamState.attachments.filter(predicate);
const principalLabel = item => `${item.principal_type === "group" ? "Group" : "User"} · ${item.principal_id}`;

function renderIAMGroups() {
  document.querySelector("#iam-group-table").innerHTML = iamState.groups.length ? iamState.groups.map(group => {
    const attached = attachmentsOf(item => item.principal_type === "group" && item.principal_id === group.id);
    return `<tr>
      <td><div class="cell-primary"><b>${escapeHTML(group.name)}</b><small class="mono">${escapeHTML(group.id)}</small>${group.description ? `<small>${escapeHTML(group.description)}</small>` : ""}</div></td>
      <td><div class="tags">${group.members.map(member => `<span class="tag">${escapeHTML(member)}</span>`).join("") || "No members"}</div></td>
      <td><div class="tags">${attached.map(item => `<span class="tag">${escapeHTML(item.policy_id)}</span>`).join("") || "None"}</div></td>
      <td>${timeTag(group.updated_at)}</td>
      <td class="actions"><button type="button" data-iam-group-edit="${escapeHTML(group.id)}">Edit</button><button type="button" class="danger" data-iam-group-delete="${escapeHTML(group.id)}">Delete</button></td>
    </tr>`;
  }).join("") : tableEmpty(5, "No groups yet", "Create a group, add members, then attach policies to it.");
}

function renderIAMPolicies() {
  document.querySelector("#iam-policy-table").innerHTML = iamState.policies.map(policy => {
    const attached = attachmentsOf(item => item.policy_id === policy.id);
    const actions = policy.managed
      ? `<button type="button" data-iam-policy-view="${escapeHTML(policy.id)}">View</button>`
      : `<button type="button" data-iam-policy-edit="${escapeHTML(policy.id)}">Edit</button><button type="button" data-iam-policy-history="${escapeHTML(policy.id)}">History</button><button type="button" class="danger" data-iam-policy-delete="${escapeHTML(policy.id)}">Delete</button>`;
    return `<tr>
      <td><div class="cell-primary"><b>${escapeHTML(policy.name)}</b><small class="mono">${escapeHTML(policy.id)}</small>${policy.managed ? `<span class="tag">Built-in role baseline</span>` : ""}</div></td>
      <td>v${escapeHTML(policy.version)}</td>
      <td>${plural(policy.statements.length, "statement")}<br><small>${policy.statements.filter(item => item.effect === "deny").length} deny</small></td>
      <td>${policy.managed ? `<small>Every principal with the ${escapeHTML(policy.id.replace("kionga-", ""))} role</small>` : `<div class="tags">${attached.map(item => `<span class="tag">${escapeHTML(principalLabel(item))}</span>`).join("") || "Not attached"}</div>`}</td>
      <td>${policy.managed ? "—" : timeTag(policy.updated_at)}</td>
      <td class="actions">${actions}</td>
    </tr>`;
  }).join("");
}

function renderIAMAttachments() {
  document.querySelector("#iam-attachment-table").innerHTML = iamState.attachments.length ? iamState.attachments.map(item => `<tr>
    <td><code>${escapeHTML(item.policy_id)}</code></td><td>${escapeHTML(principalLabel(item))}</td><td>${timeTag(item.created_at)}</td><td>${escapeHTML(item.created_by)}</td>
    <td class="actions"><button type="button" class="danger" data-iam-detach="${escapeHTML(item.id)}">Detach</button></td>
  </tr>`).join("") : tableEmpty(5, "No policies are attached", "Without attachments every principal has exactly its role baseline.");
}

function renderIAMOptions() {
  const custom = iamState.policies.filter(policy => !policy.managed);
  const attachSelect = document.querySelector("#iam-attach-policy");
  const previous = attachSelect.value;
  attachSelect.innerHTML = custom.length ? custom.map(policy => `<option value="${escapeHTML(policy.id)}">${escapeHTML(policy.name)} (${escapeHTML(policy.id)})</option>`).join("") : `<option value="">Create a policy first</option>`;
  if (custom.some(policy => policy.id === previous)) attachSelect.value = previous;
  const actions = iamState.catalog.actions || [];
  const actionSelect = document.querySelector("#simulator-action");
  const chosen = actionSelect.value;
  actionSelect.innerHTML = actions.map(item => `<option value="${escapeHTML(item.action)}" title="${escapeHTML(item.description)}">${escapeHTML(item.action)}</option>`).join("");
  if (chosen) actionSelect.value = chosen;
  const namespaces = iamState.catalog.namespaces || [];
  document.querySelector("#iam-actions").innerHTML = ["*", ...namespaces.map(item => `${item}:*`), ...actions.map(item => item.action)].map(value => `<option value="${escapeHTML(value)}"></option>`).join("");
  document.querySelector("#simulator-profile").innerHTML = `<option value="">Caller's profile</option>${(iamState.catalog.profiles || []).map(name => `<option value="${escapeHTML(name)}">${escapeHTML(name)}</option>`).join("")}`;
  const principals = [...new Set([...(typeof accessCache !== "undefined" ? accessCache : []).map(item => item.subject), ...iamState.groups.map(group => group.id)])];
  document.querySelector("#iam-principals").innerHTML = principals.map(value => `<option value="${escapeHTML(value)}"></option>`).join("");
}

// ---- groups -------------------------------------------------------------------
function openGroupDialog(group = null) {
  const form = document.querySelector("#iam-group-form");
  form.reset();
  form.elements.original_id.value = group?.id || "";
  form.elements.name.value = group?.name || "";
  form.elements.id.value = group?.id || "";
  form.elements.id.disabled = Boolean(group);
  form.elements.description.value = group?.description || "";
  form.elements.members.value = (group?.members || []).join("\n");
  document.querySelector("#iam-group-title").textContent = group ? `Edit ${group.name}` : "New group";
  document.querySelector("#iam-group-error").textContent = "";
  document.querySelector("#iam-group-dialog").showModal();
}

const fieldMessages = failure => (failure.details || []).map(item => `${item.field}: ${item.message}`).join(" · ");

document.querySelector("#iam-group-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target, error = document.querySelector("#iam-group-error");
  const original = form.elements.original_id.value;
  const body = {name: form.elements.name.value, description: form.elements.description.value, members: form.elements.members.value.split(/[\n,]/).map(item => item.trim()).filter(Boolean)};
  if (!original && form.elements.id.value.trim()) body.id = form.elements.id.value.trim();
  error.textContent = "";
  try {
    await withBusy(event.submitter, () => api(original ? `/api/v1/admin/iam/groups/${encodeURIComponent(original)}` : "/api/v1/admin/iam/groups", {method: original ? "PUT" : "POST", body: JSON.stringify(body)}), {failure: false});
    document.querySelector("#iam-group-dialog").close();
    toast("Group saved.");
    await loadIAM();
  } catch (failure) { error.textContent = fieldMessages(failure) || failure.message; }
});

// ---- policy editor --------------------------------------------------------------
const listText = values => (values || []).join(", ");
function statementRow(statement = {}, index = 0) {
  const c = statement.conditions || {};
  const tags = Object.entries(c.resource_tags || {}).map(([key, value]) => `${key}=${value}`).join(", ");
  return `<fieldset class="node-editor policy-statement" data-statement="${index}">
    <legend class="sr-only">Statement ${index + 1}</legend>
    <div class="node-head"><b>Statement ${index + 1}</b><button type="button" class="danger" data-remove-statement aria-label="Remove statement ${index + 1}">Remove</button></div>
    <label>Statement ID<input name="sid" value="${escapeHTML(statement.sid || "")}" placeholder="Statement${index + 1}"></label>
    <label>Effect<select name="effect"><option value="allow" ${statement.effect !== "deny" ? "selected" : ""}>Allow</option><option value="deny" ${statement.effect === "deny" ? "selected" : ""}>Deny</option></select></label>
    <label class="wide-field">Actions<input name="actions" list="iam-actions" value="${escapeHTML(listText(statement.actions))}" placeholder="pipeline:Read, pipeline:Run"></label>
    <label class="wide-field">Resources<input name="resources" value="${escapeHTML(listText(statement.resources))}" placeholder="kionga:project/prj-demo/*"></label>
    <details class="wide-field statement-conditions" ${c.ip_cidr || c.utc_hours || tags || c.resource_profile ? "open" : ""}><summary>Conditions (optional)</summary>
      <div class="form-grid">
        <label>Source networks<input name="ip_cidr" value="${escapeHTML(listText(c.ip_cidr))}" placeholder="10.0.0.0/8"></label>
        <label>UTC hours from<input name="utc_start" type="number" min="0" max="23" value="${c.utc_hours ? escapeHTML(c.utc_hours.start) : ""}" placeholder="9"></label>
        <label>UTC hours until<input name="utc_end" type="number" min="0" max="24" value="${c.utc_hours ? escapeHTML(c.utc_hours.end) : ""}" placeholder="17"></label>
        <label>Resource tags<input name="resource_tags" value="${escapeHTML(tags)}" placeholder="template=llm-finetune"></label>
        <label>Resource profiles<input name="resource_profile" value="${escapeHTML(listText(c.resource_profile))}" placeholder="starter, team"></label>
      </div>
    </details>
  </fieldset>`;
}

function renderStatements(statements) {
  const list = statements.length ? statements : [{effect: "allow", actions: [], resources: []}];
  document.querySelector("#policy-statements").innerHTML = list.map(statementRow).join("");
}

function statementFromRow(row) {
  const value = name => row.querySelector(`[name='${name}']`).value.trim();
  const statement = {sid: value("sid"), effect: value("effect"), actions: csv(value("actions")), resources: csv(value("resources"))};
  const conditions = {};
  if (value("ip_cidr")) conditions.ip_cidr = csv(value("ip_cidr"));
  if (value("utc_start") !== "" || value("utc_end") !== "") conditions.utc_hours = {start: Number(value("utc_start") || 0), end: Number(value("utc_end") || 0)};
  if (value("resource_tags")) conditions.resource_tags = Object.fromEntries(csv(value("resource_tags")).map(pair => { const [key, ...rest] = pair.split("="); return [key.trim(), rest.join("=").trim()]; }));
  if (value("resource_profile")) conditions.resource_profile = csv(value("resource_profile"));
  if (Object.keys(conditions).length) statement.conditions = conditions;
  return statement;
}

function policyFromForm() {
  const form = document.querySelector("#iam-policy-form");
  return {
    name: form.elements.name.value, description: form.elements.description.value,
    statements: [...document.querySelectorAll("#policy-statements .policy-statement")].map(statementFromRow),
  };
}

/* Reads the policy from whichever editor is active. */
function currentPolicyDraft() {
  if (policyEditorMode === "json") {
    try { return JSON.parse(document.querySelector("#policy-json").value); }
    catch (error) { throw Object.assign(new Error(`The JSON is not valid: ${error.message}`), {details: [{field: "json", message: error.message}]}); }
  }
  return policyFromForm();
}

function fillPolicyForm(policy) {
  const form = document.querySelector("#iam-policy-form");
  form.elements.name.value = policy.name || "";
  form.elements.description.value = policy.description || "";
  renderStatements(policy.statements || []);
}

function setPolicyEditorMode(mode) {
  const issues = document.querySelector("#policy-issues");
  if (mode === policyEditorMode) return;
  if (mode === "json") {
    document.querySelector("#policy-json").value = JSON.stringify(policyFromForm(), null, 2);
  } else {
    let draft;
    try { draft = JSON.parse(document.querySelector("#policy-json").value); }
    catch (error) { issues.innerHTML = `<li><span class="where">json</span><span>${escapeHTML(error.message)}. Fix it before switching to the form.</span></li>`; return; }
    fillPolicyForm(draft);
  }
  issues.innerHTML = "";
  setPolicyEditorModeForce(mode);
}

function openPolicyDialog(policy = null) {
  const form = document.querySelector("#iam-policy-form");
  form.reset();
  setPolicyEditorModeForce("form");
  form.elements.original_id.value = policy?.id || "";
  form.elements.version.value = policy?.version || "";
  form.elements.id.value = policy?.id || "";
  form.elements.id.disabled = Boolean(policy);
  fillPolicyForm(policy || {statements: []});
  document.querySelector("#iam-policy-title").textContent = policy ? `Edit ${policy.name} (v${policy.version})` : "New policy";
  document.querySelector("#policy-issues").innerHTML = "";
  document.querySelector("#iam-policy-error").textContent = "";
  document.querySelector("#iam-policy-dialog").showModal();
}

function setPolicyEditorModeForce(mode) {
  policyEditorMode = mode;
  document.querySelector("#policy-tab-form").setAttribute("aria-selected", String(mode === "form"));
  document.querySelector("#policy-tab-json").setAttribute("aria-selected", String(mode === "json"));
  document.querySelector("#policy-form-panel").hidden = mode !== "form";
  document.querySelector("#policy-json-panel").hidden = mode !== "json";
}

function showPolicyIssues(failure) {
  const details = failure.details || [];
  document.querySelectorAll("#policy-statements .policy-statement").forEach(row => row.classList.remove("has-issue"));
  for (const item of details) {
    const match = /^statements\[(\d+)\]/.exec(item.field || "");
    if (match) document.querySelector(`#policy-statements [data-statement="${match[1]}"]`)?.classList.add("has-issue");
  }
  document.querySelector("#policy-issues").innerHTML = details.map(item => `<li><span class="where">${escapeHTML(item.field)}</span><span>${escapeHTML(item.message)}</span></li>`).join("");
  document.querySelector("#iam-policy-error").textContent = details.length ? `${plural(details.length, "problem")} to fix.` : failure.message;
}

document.querySelector("#policy-tab-form").addEventListener("click", () => setPolicyEditorMode("form"));
document.querySelector("#policy-tab-json").addEventListener("click", () => setPolicyEditorMode("json"));
document.querySelector("#add-policy-statement").addEventListener("click", () => {
  const statements = policyFromForm().statements;
  statements.push({effect: "allow", actions: [], resources: []});
  renderStatements(statements);
});
onClick("[data-remove-statement]", node => {
  const index = Number(node.closest("[data-statement]").dataset.statement);
  renderStatements(policyFromForm().statements.filter((_, i) => i !== index));
});
document.querySelector("#iam-policy-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target;
  const original = form.elements.original_id.value;
  document.querySelector("#policy-issues").innerHTML = "";
  document.querySelector("#iam-policy-error").textContent = "";
  try {
    const draft = currentPolicyDraft();
    const body = {name: draft.name, description: draft.description || "", statements: draft.statements || []};
    if (original) body.version = Number(form.elements.version.value) || 0;
    else if (form.elements.id.value.trim() || draft.id) body.id = form.elements.id.value.trim() || draft.id;
    const saved = await withBusy(event.submitter, () => api(original ? `/api/v1/admin/iam/policies/${encodeURIComponent(original)}` : "/api/v1/admin/iam/policies", {method: original ? "PUT" : "POST", body: JSON.stringify(body)}), {failure: false});
    document.querySelector("#iam-policy-dialog").close();
    toast(`Policy ${saved.id} saved as version ${saved.version}.`);
    await loadIAM();
  } catch (failure) { showPolicyIssues(failure); }
});

// ---- attachments ------------------------------------------------------------------
document.querySelector("#iam-attach-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target, error = document.querySelector("#iam-attach-error");
  error.textContent = "";
  const body = {policy_id: form.elements.policy_id.value, principal_type: form.elements.principal_type.value, principal_id: form.elements.principal_id.value.trim()};
  try {
    await withBusy(event.submitter, () => api("/api/v1/admin/iam/attachments", {method: "POST", body: JSON.stringify(body)}), {failure: false});
    form.elements.principal_id.value = "";
    toast(`Attached ${body.policy_id} to ${body.principal_type} ${body.principal_id}.`);
    await loadIAM();
  } catch (failure) { error.textContent = fieldMessages(failure) || failure.message; }
});

// ---- simulator ---------------------------------------------------------------------
async function runSimulator(form) {
  const value = Object.fromEntries(new FormData(form));
  const query = new URLSearchParams({action: value.action, resource: value.resource.trim()});
  if (value.principal.trim()) query.set("principal", value.principal.trim());
  if (value.ip.trim()) query.set("ip", value.ip.trim());
  if (value.resource_profile) query.set("resource_profile", value.resource_profile);
  const result = await api(`/api/v1/iam/explain?${query}`);
  const who = result.principal || {};
  document.querySelector("#access-simulator-result").innerHTML = `<p class="field-help">Principal <code>${escapeHTML(who.subject)}</code> · roles ${escapeHTML((who.roles || []).join(", ") || "none")} · groups ${escapeHTML((who.groups || []).join(", ") || "none")} · ${escapeHTML(who.source || "")}</p>${decisionHTML(result.decision)}`;
  const effectiveQuery = new URLSearchParams(value.principal.trim() ? {principal: value.principal.trim()} : {});
  const effective = await api(`/api/v1/iam/effective${effectiveQuery.size ? `?${effectiveQuery}` : ""}`);
  document.querySelector("#access-effective").innerHTML = (effective.policies || []).map(binding => `<div class="effective-policy"><div><b>${escapeHTML(binding.policy.name)}</b> <small class="mono">${escapeHTML(binding.policy.id)}@v${escapeHTML(binding.policy.version)}</small></div><span class="tag">${escapeHTML(binding.source)}</span><small>${plural(binding.policy.statements.length, "statement")}</small></div>`).join("") || emptyState("No policies apply", "Without a role or attachment, everything is denied.");
}

document.querySelector("#access-simulator-form").addEventListener("submit", async event => {
  event.preventDefault();
  const error = document.querySelector("#access-simulator-error");
  error.textContent = "";
  try { await withBusy(event.submitter, () => runSimulator(event.target), {failure: false}); }
  catch (failure) { error.textContent = fieldMessages(failure) || failure.message; }
});

// ---- table actions ---------------------------------------------------------------------
onClick("[data-access-tab]", node => {
  showAccessTab(node.dataset.accessTab);
  node.focus();
});
document.querySelector("#access-tabs").addEventListener("keydown", event => {
  if (!["ArrowLeft", "ArrowRight"].includes(event.key)) return;
  const index = accessTabs.indexOf(accessTab) + (event.key === "ArrowRight" ? 1 : -1);
  const next = accessTabs[(index + accessTabs.length) % accessTabs.length];
  showAccessTab(next);
  document.querySelector(`#access-tab-${next}`).focus();
});
document.querySelector("#add-iam-group").addEventListener("click", () => openGroupDialog());
document.querySelector("#add-iam-policy").addEventListener("click", () => openPolicyDialog());
onClick("[data-iam-group-edit]", node => openGroupDialog(iamState.groups.find(group => group.id === node.dataset.iamGroupEdit)));
onClick("[data-iam-group-delete]", async node => {
  if (!confirm(`Delete group ${node.dataset.iamGroupDelete}? Detach its policies first.`)) return;
  await withBusy(node, () => api(`/api/v1/admin/iam/groups/${encodeURIComponent(node.dataset.iamGroupDelete)}`, {method: "DELETE"}), {success: "Group deleted."});
  await loadIAM();
});
onClick("[data-iam-policy-edit]", node => openPolicyDialog(iamState.policies.find(policy => policy.id === node.dataset.iamPolicyEdit)));
onClick("[data-iam-policy-view]", node => {
  const policy = iamState.policies.find(item => item.id === node.dataset.iamPolicyView);
  document.querySelector("#metadata-detail").innerHTML = `<p class="eyebrow">BUILT-IN ROLE BASELINE</p><h2>${escapeHTML(policy.name)}</h2><p>${escapeHTML(policy.description)}</p><p class="field-help">Shown unscoped. At evaluation, project resources are limited to the principal's assigned and owned projects, and normal users only receive statements for their assigned services.</p><pre class="yaml-view">${escapeHTML(JSON.stringify(policy.statements, null, 2))}</pre>`;
  document.querySelector("#metadata-dialog").showModal();
});
onClick("[data-iam-policy-history]", async node => {
  const id = node.dataset.iamPolicyHistory;
  const revisions = (await api(`/api/v1/admin/iam/policies/${encodeURIComponent(id)}/revisions`)).items || [];
  document.querySelector("#metadata-detail").innerHTML = `<p class="eyebrow">POLICY HISTORY</p><h2>${escapeHTML(id)}</h2><p class="field-help">Revisions are immutable. Restore one by pasting its JSON into the editor.</p>${revisions.map(revision => `<details class="raw-json"><summary>Version ${escapeHTML(revision.version)} · ${escapeHTML(revision.author)} · ${timeTag(revision.created_at)}</summary><pre class="yaml-view">${escapeHTML(JSON.stringify({name: revision.policy.name, description: revision.policy.description, statements: revision.policy.statements}, null, 2))}</pre></details>`).join("")}`;
  document.querySelector("#metadata-dialog").showModal();
});
onClick("[data-iam-policy-delete]", async node => {
  if (!confirm(`Delete policy ${node.dataset.iamPolicyDelete}? Its revisions stay in history.`)) return;
  await withBusy(node, () => api(`/api/v1/admin/iam/policies/${encodeURIComponent(node.dataset.iamPolicyDelete)}`, {method: "DELETE"}), {success: "Policy deleted."});
  await loadIAM();
});
onClick("[data-iam-detach]", async node => {
  if (!confirm("Detach this policy? The principal loses its grants (or denials) on the next request.")) return;
  await withBusy(node, () => api(`/api/v1/admin/iam/attachments/${encodeURIComponent(node.dataset.iamDetach)}`, {method: "DELETE"}), {success: "Policy detached."});
  await loadIAM();
});
