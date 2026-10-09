/* My access (every user) and Users & access (administrators). */
const accessServices = ["overview", "projects", "pipelines", "functions", "models", "agents", "features", "storage", "realtime", "catalog", "platform", "git", "workbench", "ide"];
let accessCache = [];
let resourceProfiles = [];

function accessPayload(form) {
  const value = Object.fromEntries(new FormData(form));
  return {
    email: value.email, role: value.role,
    services: [...form.querySelectorAll("[name='services']:checked")].map(input => input.value),
    project_ids: csv(value.project_ids),
    storage: {size_gb: Number(value.storage_gb), buckets: csv(value.buckets)},
    compute: {profile: value.profile, vcpus: Number(value.vcpus), memory_gb: Number(value.memory_gb), gpus: Number(value.gpus), gpu_type: value.gpu_type, max_vms: Number(value.max_vms), max_projects: Number(value.max_projects), max_concurrent_runs: Number(value.max_runs), max_functions: Number(value.max_functions)},
    disabled: form.elements.disabled.checked,
  };
}

function setResourceProfile(name) {
  const form = document.querySelector("#access-form");
  const profile = resourceProfiles.find(item => item.name === name);
  const custom = name === "custom" || !profile;
  if (profile && !custom) {
    const values = {...profile.compute, storage_gb: profile.storage_gb, max_runs: profile.compute.max_concurrent_runs};
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
    <td><div class="cell-primary"><b>${escapeHTML(item.email || item.subject)}</b><small class="truncate mono" title="${escapeHTML(item.subject)}">${escapeHTML(item.subject)}</small></div></td>
    <td><span class="role-badge ${escapeHTML(item.role)}">${escapeHTML(item.role)}</span></td><td><div class="tags">${item.services.map(service => `<span class="tag">${escapeHTML(service)}</span>`).join("") || "None"}</div></td>
    <td><span class="tag">${escapeHTML(item.compute.profile || "custom")}</span> ${item.compute.vcpus} vCPU · ${item.compute.memory_gb} GB${item.compute.gpus ? ` · ${item.compute.gpus} GPU` : ""}<br><small>${item.compute.max_vms} VM · ${item.compute.max_projects} projects · ${item.compute.max_concurrent_runs} runs · ${item.compute.max_functions || 0} functions</small></td>
    <td>${item.storage.size_gb} GB<br><small>${(item.storage.buckets || []).map(escapeHTML).join(", ") || "No buckets"}</small></td>
    <td>${status(item.disabled ? "suspended" : "active")}</td>
    <td class="actions"><button type="button" data-access-edit="${escapeHTML(item.subject)}">Edit</button><button type="button" class="danger" data-access-delete="${escapeHTML(item.subject)}">Revoke</button></td>
  </tr>`).join("") : tableEmpty(7, "No users have been provisioned", "Unprovisioned identities can sign in but can only request access.");
  const pending = (requests.items || []).filter(item => item.status === "pending");
  document.querySelector("#access-request-count").textContent = `${pending.length} pending`;
  document.querySelector("#access-request-table").innerHTML = (requests.items || []).length ? requests.items.map(item => `<tr>
    <td><div class="cell-primary"><b>${escapeHTML(item.email || item.subject)}</b><small class="truncate mono">${escapeHTML(item.subject)}</small></div></td>
    <td><div class="tags">${item.requested_services.map(service => `<span class="tag">${escapeHTML(service)}</span>`).join("")}</div></td>
    <td class="request-reason">${escapeHTML(item.reason)}</td><td>${timeTag(item.created_at)}</td><td>${status(item.status)}</td>
    <td class="actions">${item.status === "pending" ? `<button type="button" data-request-review="${escapeHTML(item.id)}">Provision</button><button type="button" class="danger" data-request-reject="${escapeHTML(item.id)}">Reject</button>` : `<small>${escapeHTML(item.reviewer || "reviewed")}</small>`}</td>
  </tr>`).join("") : tableEmpty(6, "No access requests yet");
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
      <div class="allocation-grid">${[[grant.compute.vcpus, "vCPUs"], [grant.compute.memory_gb, "GB memory"], [grant.compute.gpus || 0, "GPUs"], [grant.compute.max_vms, "VMs"], [grant.compute.max_projects, "Projects"], [grant.compute.max_concurrent_runs, "Concurrent runs"], [grant.compute.max_functions || 0, "Functions"], [grant.storage.size_gb, "GB storage"]].map(([value, label]) => `<div><strong>${value}</strong><span>${label}</span></div>`).join("")}</div>
    </article>` : `
    <article class="panel access-allocation"><p class="eyebrow">COMPUTE ALLOCATION</p><h3>${isAdmin() ? "Administrative access" : "No capacity assigned yet"}</h3><p class="field-help">${isAdmin() ? "Administrators are not constrained by user workspace quotas." : "Request access below. Your administrator can assign services, project access, and workspace capacity."}</p></article>`;
  const latestRequest = (requests.items || [])[0];
  const requestButton = document.querySelector("#request-access");
  requestButton.disabled = latestRequest?.status === "pending";
  requestButton.textContent = latestRequest?.status === "pending" ? "Request pending" : "Request access";
  document.querySelector("#my-access-summary").innerHTML = `
    <div class="access-identity panel">
      <div><span class="role-badge ${escapeHTML(me.roles[0] || "unknown")}">${escapeHTML(me.roles.join(", ") || "no role")}</span><h3>${escapeHTML(me.email || me.subject || "Unknown identity")}</h3><p><code>${escapeHTML(me.subject)}</code> · ${escapeHTML(me.mode)} sign-in</p></div>
      <div>${status(roleState)}<small class="field-help">${me.provisioned ? "Administrator-provisioned profile" : isAdmin() ? "Full platform administrator" : "Contact an administrator for access"}</small></div>
    </div>
    <div class="split">
      <article class="panel"><p class="eyebrow">ASSIGNED SERVICES</p><h3>${plural(services.length, "service")} available</h3><div class="service-grant-grid">${services.map(service => ["workbench", "ide", "git"].includes(service) ? `<span class="tag"><span aria-hidden="true">✓</span> ${escapeHTML(service)}</span>` : `<button type="button" data-view-target="${escapeHTML(service)}"><span aria-hidden="true">✓</span>${escapeHTML(service)}</button>`).join("") || `<p class="field-help">No services assigned.</p>`}</div></article>
      <article class="panel"><p class="eyebrow">PROJECT SCOPE</p><h3>${grant?.project_ids?.length || 0} explicitly assigned</h3><div class="tags">${(grant?.project_ids || []).map(id => `<span class="tag">${escapeHTML(id)}</span>`).join("") || `<span class="tag">${isAdmin() ? "All projects" : "Owned projects only"}</span>`}</div><p class="eyebrow access-subhead">STORAGE BUCKETS</p><div class="tags">${(grant?.storage?.buckets || []).map(name => `<span class="tag">${escapeHTML(name)}</span>`).join("") || `<span class="tag">${isAdmin() ? "All buckets" : "None assigned"}</span>`}</div></article>
    </div>${latestRequest ? `<article class="panel access-request-state"><div><p class="eyebrow">LATEST ACCESS REQUEST</p><h3>${escapeHTML(latestRequest.requested_services.join(", "))}</h3><p class="field-help">${escapeHTML(latestRequest.reason)}</p></div><div>${status(latestRequest.status)}<small class="field-help">${timeTag(latestRequest.updated_at)}</small></div></article>` : ""}${allocation}`;
}

function fillAccessForm(item, requestId = "") {
  const form = document.querySelector("#access-form");
  form.reset();
  form.elements.original_subject.value = item.subject;
  form.elements.request_id.value = requestId;
  form.elements.subject.value = item.subject;
  form.elements.subject.disabled = true;
  form.elements.email.value = item.email || "";
  if (item.role) form.elements.role.value = item.role;
  form.elements.project_ids.value = (item.project_ids || []).join(", ");
  const compute = item.compute || {};
  form.elements.profile.value = compute.profile || (requestId ? "starter" : "custom");
  if (item.compute) {
    form.elements.vcpus.value = compute.vcpus; form.elements.memory_gb.value = compute.memory_gb;
    form.elements.gpus.value = compute.gpus || 0; form.elements.gpu_type.value = compute.gpu_type || "nvidia.com/gpu";
    form.elements.max_vms.value = compute.max_vms; form.elements.max_projects.value = compute.max_projects;
    form.elements.max_runs.value = compute.max_concurrent_runs; form.elements.max_functions.value = compute.max_functions || 0;
    form.elements.storage_gb.value = item.storage.size_gb; form.elements.buckets.value = (item.storage.buckets || []).join(", ");
    form.elements.disabled.checked = item.disabled;
  }
  const services = item.services || item.requested_services || [];
  form.querySelectorAll("[name='services']").forEach(input => { input.checked = services.includes(input.value); });
  setResourceProfile(form.elements.profile.value);
  configureLocalPasswordField();
  document.querySelector("#access-error").textContent = "";
  document.querySelector("#access-dialog").showModal();
}

function configureLocalPasswordField() {
  const section = document.querySelector("#local-password-section");
  section.hidden = me.mode !== "local";
  section.querySelector("input").value = "";
}

document.querySelector("#service-grants").innerHTML = accessServices.map(service => `<label class="inline-check"><input type="checkbox" name="services" value="${service}"> ${service}</label>`).join("");
document.querySelector("#request-service-grants").innerHTML = accessServices.map(service => `<label class="inline-check"><input type="checkbox" name="services" value="${service}"> ${service}</label>`).join("");
document.querySelector("#resource-profile").addEventListener("change", event => setResourceProfile(event.target.value));
document.querySelector("#add-user-access").addEventListener("click", async () => {
  if (!resourceProfiles.length) resourceProfiles = (await api("/api/v1/admin/resource-profiles")).items || [];
  const form = document.querySelector("#access-form");
  form.reset(); form.elements.original_subject.value = ""; form.elements.request_id.value = ""; form.elements.subject.disabled = false;
  form.elements.profile.value = "starter"; setResourceProfile("starter");
  configureLocalPasswordField();
  document.querySelector("#access-error").textContent = "";
  document.querySelector("#access-dialog").showModal();
});
document.querySelector("#access-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target, error = document.querySelector("#access-error");
  const subject = form.elements.original_subject.value || form.elements.subject.value.trim();
  const password = form.elements.local_password.value;
  error.textContent = "";
  try {
    await withBusy(event.submitter, async () => {
      await api(`/api/v1/admin/users/${encodeURIComponent(subject)}`, {method: "PUT", body: JSON.stringify(accessPayload(form))});
      if (password) await api(`/api/v1/admin/users/${encodeURIComponent(subject)}/local-password`, {method: "PUT", body: JSON.stringify({password})});
      if (form.elements.request_id.value) {
        await api(`/api/v1/admin/access-requests/${encodeURIComponent(form.elements.request_id.value)}`, {method: "PATCH", body: JSON.stringify({status: "approved", note: "Provisioned through the admin console"})});
      }
    }, {failure: false});
    form.reset(); document.querySelector("#access-dialog").close();
    toast(password ? `Access saved. ${subject} can now sign in with the password you set.` : "User access saved.");
    await loadAccess();
  } catch (failure) { error.textContent = failure.message; }
});
document.querySelector("#request-access").addEventListener("click", () => {
  document.querySelector("#access-request-form").reset();
  document.querySelector("#access-request-error").textContent = "";
  document.querySelector("#access-request-dialog").showModal();
});
document.querySelector("#access-request-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target, error = document.querySelector("#access-request-error");
  const requested_services = [...form.querySelectorAll("[name='services']:checked")].map(input => input.value);
  error.textContent = "";
  try {
    if (!requested_services.length) throw new Error("Choose at least one service.");
    await withBusy(event.submitter, () => api("/api/v1/access-requests", {method: "POST", body: JSON.stringify({reason: form.elements.reason.value, requested_services})}), {failure: false});
    form.reset(); document.querySelector("#access-request-dialog").close(); toast("Access request submitted."); await loadMyAccess();
  } catch (failure) { error.textContent = failure.message; }
});
onClick("#my-access-summary [data-view-target]", node => showView(node.dataset.viewTarget, {resource: ""}));
onClick("[data-access-edit]", node => {
  const item = accessCache.find(value => value.subject === node.dataset.accessEdit);
  if (item) fillAccessForm(item);
});
onClick("[data-request-review]", async node => {
  const requestData = (await api("/api/v1/admin/access-requests")).items.find(item => item.id === node.dataset.requestReview);
  if (requestData) fillAccessForm({subject: requestData.subject, email: requestData.email, requested_services: requestData.requested_services}, requestData.id);
});
onClick("[data-request-reject]", async node => {
  const note = prompt("Why is this request being rejected? The requester sees this note.");
  if (note === null) return;
  await withBusy(node, () => api(`/api/v1/admin/access-requests/${encodeURIComponent(node.dataset.requestReject)}`, {method: "PATCH", body: JSON.stringify({status: "rejected", note})}), {success: "Access request rejected."});
  await loadAccess();
});
onClick("[data-access-delete]", async node => {
  if (!confirm(`Revoke all access for ${node.dataset.accessDelete}? Their sessions stop working on the next request.`)) return;
  await withBusy(node, async () => {
    await api(`/api/v1/admin/users/${encodeURIComponent(node.dataset.accessDelete)}`, {method: "DELETE"});
    if (me.mode === "local") await api(`/api/v1/admin/users/${encodeURIComponent(node.dataset.accessDelete)}/local-password`, {method: "DELETE"}).catch(() => {});
  }, {success: "User access revoked."});
  await loadAccess();
});

registerView("profile", loadMyAccess, {
  eyebrow: "YOUR AUTHORIZATION", title: "My access",
  intro: {
    what: "Your effective role, assigned services, project scope and resource allocation.",
    why: "Know what you can use before you try, and request more when you need it.",
    action: "Request access sends your administrators a reasoned request.",
    results: "The latest request and its decision appear on this page.",
  },
});
registerView("access", loadAccess, {
  eyebrow: "IDENTITY & CAPACITY", title: "Users & access",
  intro: {
    what: "Administrator tools for assigning services, projects, storage and compute to identities.",
    why: "Unassigned access is denied by default; this is where you grant exactly what people need.",
    needs: "Administrator or operator role.",
    action: "Provision user stores an access profile. In local sign-in mode you can also set a password login.",
    results: "Changes apply on the user's next request and are written to the audit log.",
  },
});
