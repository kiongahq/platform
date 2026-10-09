/* Projects: list, routed detail page, creation wizard, Git binding and
 * scaffold handoff. Detail is a page (?view=projects&resource=<id>) rather
 * than a dialog so it scrolls naturally and its URL can be shared. */
let projectCache = [];

const projectHandoffs = [
  {view: "pipelines", label: "Pipelines", url: "/api/v1/pipelines/runs", count: (data, id) => data.filter(run => run.project_id === id).length, noun: "run"},
  {view: "functions", label: "Functions", url: "/api/v1/functions", count: (data, id) => (data.items || []).filter(fn => fn.project_id === id).length, noun: "function"},
  {view: "models", label: "Models", url: "/api/v1/models", count: (data, id) => (data.items || []).filter(model => model.project_id === id).length, noun: "model"},
  {view: "agents", label: "Agents", url: "/api/v1/agents", count: (data, id) => (data.items || []).filter(agent => agent.project_id === id).length, noun: "agent"},
];

function projectCard(project) {
  const repository = project.repository
    ? `<span class="repository-badge" title="${escapeHTML(project.repository.url)}"><span aria-hidden="true">⌘</span><b>${escapeHTML(project.repository.provider)}</b><small>${escapeHTML(project.repository.default_branch)}</small></span>`
    : `<span class="repository-badge unbound"><span aria-hidden="true">＋</span><b>No Git repository</b></span>`;
  return `<article class="card interactive-card" role="button" tabindex="0" data-project-detail="${escapeHTML(project.id)}" aria-label="Open project ${escapeHTML(project.name)}">
    <span class="kind">${escapeHTML(project.template)}${project.template_version ? ` · v${escapeHTML(project.template_version)}` : ""}</span>
    <h3>${escapeHTML(project.name)}</h3>
    <p class="line-clamp-2">${escapeHTML(project.description || "No description yet.")}</p>
    <div class="tags">${[project.framework, project.accelerator, project.requested_profile && `${project.requested_profile} profile`].filter(Boolean).map(value => `<span class="tag">${escapeHTML(value)}</span>`).join("")}</div>
    ${repository}
    <footer><span class="tag" title="Namespace">${escapeHTML(project.namespace)}</span>${status(project.status)}</footer>
  </article>`;
}

async function loadProjects() {
  const projects = scoped(await api("/api/v1/projects"));
  projectCache = projects;
  document.querySelector("#project-grid").innerHTML = projects.length
    ? projects.map(projectCard).join("")
    : emptyState(selectedProject ? "This project is not visible to you" : "No projects yet", selectedProject ? "Choose All assigned projects, or ask an administrator to assign it." : "Create a project from a template to get a scaffold, Git binding and workspace folder.", can("projects_write") && !selectedProject ? `<button class="primary" type="button" data-open-new-project>＋ New project</button>` : "");
  if (!selectedResource) showProjectList();
  await loadProjectOptions();
  return projects;
}

function showProjectList() {
  document.querySelector("#project-list-panel").hidden = false;
  document.querySelector("#project-detail-panel").hidden = true;
}

async function openProjectDetail(id) {
  const panel = document.querySelector("#project-detail-panel");
  document.querySelector("#project-list-panel").hidden = true;
  panel.hidden = false;
  panel.innerHTML = `<div class="skeleton" aria-label="Loading project"></div>`;
  const project = projectCache.find(item => item.id === id) || await api(`/api/v1/projects/${encodeURIComponent(id)}`);
  const handoffs = projectHandoffs.filter(item => hasService(item.view));
  const counts = await Promise.all(handoffs.map(item => api(item.url).then(data => item.count(data, id)).catch(() => null)));
  const repository = project.repository;
  const repoURL = repository && !repository.url.startsWith("git@") ? repository.url.replace(/\.git$/, "") : "";
  const related = handoffs.map((item, index) => `<button type="button" class="related-tile" data-navigate="${item.view}" data-project="${escapeHTML(id)}"><strong>${counts[index] ?? "—"}</strong><span>${escapeHTML(item.label)}</span><small class="field-help">${counts[index] === null ? "Count unavailable" : counts[index] === 0 ? `No ${item.noun}s yet · open to create` : `Open ${plural(counts[index], item.noun)}`}</small></button>`).join("");
  const workspaces = ["workbench", "ide"].filter(hasService).map(tool => {
    const state = workspaceState[tool];
    const label = tool === "ide" ? "Open in IDE" : "Open in Jupyter";
    return state?.available
      ? `<a class="button-link" target="_blank" rel="noopener" href="${workspaceURL(tool, id)}" title="Opens /workspace/projects/${escapeHTML(project.namespace)}">${label} ↗</a>`
      : `<button type="button" disabled title="${escapeHTML(state?.message || "Checking workspace availability")}">${label}</button>`;
  }).join("");
  const output = `/workspace/projects/${project.namespace}`;
  const canRun = can("projects_write") && (hasService("workbench") || hasService("ide"));
  const scaffold = project.scaffold_command ? `<article class="panel" id="scaffold-panel" data-scaffold-project="${escapeHTML(project.id)}">
      <div class="panel-heading"><div><p class="eyebrow">SCAFFOLD</p><h3>Generate the starter code</h3><p>The scaffold command writes a production-shaped starter (code, tests, CI, container and a platform manifest) from this project's versioned template. It never overwrites a folder that already has files.</p></div></div>
      ${metaList([["Output folder", `<code>${escapeHTML(output)}</code>`], ["Template", `${escapeHTML(project.template)} v${escapeHTML(project.template_version || "—")}`], ["Opens in", "JupyterLab and the IDE open this same folder"]])}
      <div class="command-block"><code>${escapeHTML(project.scaffold_command)}</code>${copyButton(project.scaffold_command, "Copy command")}</div>
      <p class="field-help">To run it yourself, open a terminal in <code>/workspace/projects</code> and paste the command.</p>
      <div class="button-row start">${canRun ? `<button type="button" class="primary" data-scaffold-run="${escapeHTML(project.id)}">Run in workspace…</button>` : `<button type="button" disabled title="${escapeHTML(can("projects_write") ? "Running in a workspace needs JupyterLab or the IDE assigned to you." : denialReason())}">Run in workspace…</button>`}</div>
      <div id="scaffold-run" class="scaffold-run" aria-live="polite" hidden></div>
      ${repository ? `<p class="field-help" style="margin-top:var(--space-3)">Already scaffolded and pushed? Sync it into a workspace with <code>kionga project sync ${escapeHTML(project.id)}</code>.</p>` : ""}
    </article>` : "";
  panel.innerHTML = `<nav class="breadcrumb" aria-label="Breadcrumb"><button type="button" class="quiet" data-project-back>← All projects</button></nav>
    <div class="project-detail">
      <div class="detail-header">
        <div><p class="eyebrow">PROJECT · ${escapeHTML(project.template)}${project.template_version ? ` v${escapeHTML(project.template_version)}` : ""}</p><h2>${escapeHTML(project.name)}</h2><p>${escapeHTML(project.description || "No description yet.")}</p></div>
        <div class="heading-actions">${workspaces}${can("git_write") ? `<button type="button" data-project-repository="${escapeHTML(project.id)}">${repository ? "Update repository" : "Connect Git repository"}</button>` : ""}</div>
      </div>
      <div class="detail-grid">
        <div class="stack">
          <article class="panel"><div class="panel-heading"><div><p class="eyebrow">WORK IN THIS PROJECT</p><h3>Related resources</h3><p>Each area opens filtered to this project.</p></div></div><div class="related-grid">${related || `<p class="field-help">No project services are assigned to you.</p>`}</div></article>
          ${scaffold}
        </div>
        <article class="panel"><div class="panel-heading"><div><p class="eyebrow">DETAILS</p><h3>Project record</h3></div>${status(project.status)}</div>
          ${metaList([
            ["Project ID", copyable(project.id)],
            ["Namespace", `<code>${escapeHTML(project.namespace)}</code>`],
            ["Framework", escapeHTML(project.framework || "—")],
            ["Accelerator", escapeHTML(project.accelerator || "—")],
            ["Requested profile", escapeHTML(project.requested_profile || "—")],
            ["Repository", repository ? `${repoURL ? `<a href="${escapeHTML(repoURL)}" target="_blank" rel="noreferrer" title="${escapeHTML(repository.url)}">${escapeHTML(repository.url)} ↗</a>` : `<code>${escapeHTML(repository.url)}</code>`} <span class="tag">${escapeHTML(repository.default_branch)}</span>` : "Not connected"],
            ["Owner", escapeHTML(project.owner_subject || "—")],
            ["Created", timeTag(project.created_at)],
          ])}
          <details class="raw-json"><summary>Raw record</summary><pre class="hint metadata-json">${escapeHTML(JSON.stringify(project, null, 2))}</pre></details>
        </article>
      </div>
    </div>`;
  panel.querySelector("h2").focus?.();
  if (project.scaffold_command && hasService("projects")) loadLatestScaffoldJob(project.id).catch(() => {});
}

// ---- scaffold jobs -----------------------------------------------------------
/* A scaffold job runs the project's stored, catalog-derived argv inside the
 * user's workspace. The browser never sends a command: it asks for the plan,
 * shows it verbatim in a confirmation dialog, then starts the job and polls. */
let scaffoldPoll = null;
const scaffoldPollMS = 1000;
let scaffoldPlan = null;

function renderScaffoldJob(job) {
  const node = document.querySelector("#scaffold-run");
  if (!node || document.querySelector("#scaffold-panel")?.dataset.scaffoldProject !== job.project_id) return false;
  node.hidden = false;
  const active = job.status === "queued" || job.status === "running";
  const files = job.files || [], git = job.git_status || [];
  node.innerHTML = `<div class="run-heading"><h4>Last run ${status(job.status)}</h4><small>${active ? "Running in your workspace…" : `Finished ${timeTag(job.ended_at)}`} · ${escapeHTML(job.workspace === "ide" ? "IDE" : "JupyterLab")} · requested by ${escapeHTML(job.requested_by || "—")}</small></div>
    ${active ? `<p class="field-help">Generating files in <code>${escapeHTML(job.output_dir)}</code>. This usually takes a few seconds.</p>` : ""}
    ${job.error ? `<p class="form-error" role="alert">${escapeHTML(job.error)}</p>` : ""}
    ${job.status === "succeeded" ? `<p class="field-help">Created ${plural(files.length, "file")} in <code>${escapeHTML(job.output_dir)}</code>. Open the project in JupyterLab or the IDE to start working.</p>` : ""}
    ${files.length ? `<details ${job.status === "succeeded" ? "open" : ""}><summary>Produced files (${files.length})</summary><ul class="file-list">${files.map(file => `<li>${escapeHTML(file)}</li>`).join("")}</ul></details>` : ""}
    ${git.length ? `<details><summary>git status (${git.length})</summary><pre class="hint scaffold-output">${escapeHTML(git.join("\n"))}</pre></details>` : (!active && job.status === "succeeded" ? `<p class="field-help">No Git repository was initialized in the folder.</p>` : "")}
    ${job.output_tail ? `<details ${job.status === "failed" ? "open" : ""}><summary>Command output</summary><pre class="hint scaffold-output">${escapeHTML(job.output_tail)}</pre></details>` : ""}`;
  return active;
}

function stopScaffoldPoll() { clearTimeout(scaffoldPoll); scaffoldPoll = null; }

async function pollScaffoldJob(projectID, jobID) {
  stopScaffoldPoll();
  const job = await api(`/api/v1/projects/${encodeURIComponent(projectID)}/scaffold-jobs/${encodeURIComponent(jobID)}`);
  if (renderScaffoldJob(job)) scaffoldPoll = setTimeout(() => pollScaffoldJob(projectID, jobID).catch(error => toast(error.message, "error")), scaffoldPollMS);
  else if (job.status === "succeeded") toast("Starter code generated.");
  return job;
}

async function loadLatestScaffoldJob(projectID) {
  stopScaffoldPoll();
  const jobs = (await api(`/api/v1/projects/${encodeURIComponent(projectID)}/scaffold-jobs`)).items || [];
  if (!jobs.length) return;
  if (renderScaffoldJob(jobs[0])) scaffoldPoll = setTimeout(() => pollScaffoldJob(projectID, jobs[0].id).catch(() => {}), scaffoldPollMS);
}

onClick("[data-scaffold-run]", async node => {
  const projectID = node.dataset.scaffoldRun;
  scaffoldPlan = await withBusy(node, () => api(`/api/v1/projects/${encodeURIComponent(projectID)}/scaffold-plan`), {});
  const detail = document.querySelector("#scaffold-confirm-detail");
  document.querySelector("#scaffold-confirm-error").textContent = scaffoldPlan.available ? "" : scaffoldPlan.reason;
  document.querySelector("#scaffold-confirm-run").disabled = !scaffoldPlan.available;
  detail.innerHTML = `${metaList([
    ["Workspace", escapeHTML(scaffoldPlan.workspace === "ide" ? "Browser IDE" : scaffoldPlan.workspace ? "JupyterLab" : "None available")],
    ["Runs in", `<code>${escapeHTML(scaffoldPlan.working_dir)}</code>`],
    ["Writes to", `<code>${escapeHTML(scaffoldPlan.output_dir)}</code>`],
  ])}<p class="eyebrow">EXACT COMMAND</p><div class="command-block"><code id="scaffold-confirm-command">${escapeHTML(scaffoldPlan.command)}</code></div><p class="field-help">Arguments, passed one by one:</p><ol class="argv-list" aria-label="Command arguments">${scaffoldPlan.argv.map(arg => `<li>${escapeHTML(arg)}</li>`).join("")}</ol>`;
  document.querySelector("#scaffold-confirm-dialog").dataset.project = projectID;
  document.querySelector("#scaffold-confirm-dialog").showModal();
});

document.querySelector("#scaffold-confirm-form").addEventListener("submit", async event => {
  event.preventDefault();
  const dialog = document.querySelector("#scaffold-confirm-dialog");
  const projectID = dialog.dataset.project, error = document.querySelector("#scaffold-confirm-error");
  error.textContent = "";
  try {
    const body = scaffoldPlan?.workspace ? {options: {workspace: scaffoldPlan.workspace}} : {};
    const job = await withBusy(event.submitter, () => api(`/api/v1/projects/${encodeURIComponent(projectID)}/scaffold-jobs`, {method: "POST", body: JSON.stringify(body)}), {failure: false});
    dialog.close();
    renderScaffoldJob(job);
    await pollScaffoldJob(projectID, job.id);
  } catch (failure) { error.textContent = failure.message; }
});

onClick("[data-project-back]", () => { stopScaffoldPoll(); selectedResource = ""; history.pushState({}, "", routeURL("projects", "")); showProjectList(); });
onClick("[data-project-detail]", async node => {
  selectedResource = node.dataset.projectDetail;
  history.pushState({}, "", routeURL("projects", selectedResource));
  await openProjectDetail(selectedResource);
}, {ignoreControls: true});
/* Legacy handoff attribute kept for API-generated markup and tests. */
onClick("[data-project-view]", async node => navigateTo(node.dataset.projectView, {project: node.dataset.projectId}));
onClick("[data-open-new-project]", () => openNewProject());
onClick("[data-project-repository]", node => {
  const item = projectCache.find(project => project.id === node.dataset.projectRepository);
  const form = document.querySelector("#project-repository-form");
  form.elements.project_id.value = node.dataset.projectRepository;
  form.elements.url.value = item?.repository?.url || "";
  form.elements.default_branch.value = item?.repository?.default_branch || "main";
  document.querySelector("#project-repository-error").textContent = "";
  document.querySelector("#project-repository-dialog").showModal();
});

document.querySelector("#project-repository-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.target, error = document.querySelector("#project-repository-error"); error.textContent = "";
  try {
    const projectId = form.elements.project_id.value;
    await withBusy(event.submitter, () => api(`/api/v1/projects/${encodeURIComponent(projectId)}/repository`, {method: "PUT", body: JSON.stringify({url: form.elements.url.value, default_branch: form.elements.default_branch.value})}), {failure: false});
    document.querySelector("#project-repository-dialog").close();
    toast("Git repository connected.");
    await loadProjects();
    if (selectedResource) await openProjectDetail(selectedResource);
  } catch (failure) { error.textContent = failure.message; }
});

// ---- project creation --------------------------------------------------------
const projectProfiles = {
  starter: {label: "Starter", detail: "2 vCPU · 4 GB RAM · 25 GB storage", description: "Notebook exploration and small development runs.", vcpus: 2, memory: 4, gpus: 0, storage: 25},
  team: {label: "Team", detail: "4 vCPU · 8 GB RAM · 100 GB storage", description: "Daily model development and shared pipelines.", vcpus: 4, memory: 8, gpus: 0, storage: 100},
  power: {label: "Power", detail: "8 vCPU · 16 GB RAM · 250 GB storage", description: "Large training runs and production delivery.", vcpus: 8, memory: 16, gpus: 0, storage: 250},
  gpu: {label: "GPU", detail: "8 vCPU · 32 GB RAM · 1 GPU · 500 GB storage", description: "Accelerated model and generative AI development.", vcpus: 8, memory: 32, gpus: 1, storage: 500},
  custom: {label: "Current allocation", detail: "Use exactly what your administrator assigned", description: "The backend still enforces your effective resource grant."},
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
  const profile = projectProfiles[document.querySelector("#project-requested-profile").value] || projectProfiles.custom;
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
  document.querySelector("#project-framework").innerHTML = (template.frameworks || []).map(value => `<option value="${escapeHTML(value)}">${escapeHTML(value)}</option>`).join("");
  configureProjectProfileOptions();
  if (!configureProjectAccelerators(template)) {
    document.querySelector("#project-template-preview").innerHTML = emptyState("Accelerator capacity required", "This template needs accelerator capacity that has not been assigned to you.");
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
    return `<button type="button" class="project-template-option${missing.length ? " unavailable" : ""}" role="radio" aria-checked="false" aria-disabled="${missing.length > 0}" data-project-template="${escapeHTML(template.id)}" ${missing.length ? `disabled title="${escapeHTML(reason)}"` : ""}><span class="template-option-heading"><b>${escapeHTML(template.name)}</b><small>${escapeHTML(template.category)} · v${escapeHTML(template.version)}</small></span><span>${escapeHTML(template.description)}</span><span class="template-option-meta">${(template.frameworks || []).map(escapeHTML).join(" · ")}<i>${missing.length ? escapeHTML(reason) : `${escapeHTML(template.recommended_profile)} profile`}</i></span></button>`;
  }).join("");
  const available = projectTemplateCache.find(template => !unavailable(template).length);
  const selected = projectTemplateCache.find(template => template.id === selectedProjectTemplate && !unavailable(template).length);
  selectedProjectTemplate = "";
  document.querySelector("#project-template-id").value = "";
  document.querySelector("#project-template-version").value = "";
  if (selected || available) selectProjectTemplate((selected || available).id);
  else document.querySelector("#project-template-preview").innerHTML = emptyState("No templates available", "Your administrator must assign the services a project template requires.");
}
async function loadProjectTemplates() {
  if (!projectTemplateCache.length) projectTemplateCache = (await api("/api/v1/project-templates")).items || [];
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

async function openNewProject() {
  const dialog = document.querySelector("#project-dialog");
  const fields = document.querySelector("#new-project-git-fields"), allowed = can("git_write");
  fields.hidden = !allowed; fields.querySelectorAll("input").forEach(input => { input.disabled = !allowed; });
  document.querySelector("#form-error").textContent = "";
  if (!dialog.open) dialog.showModal();
  try { await loadProjectTemplates(); }
  catch (failure) {
    document.querySelector("#project-template-catalog").innerHTML = emptyState("Template catalog unavailable", escapeHTML(failure.message));
    document.querySelector("#form-error").textContent = failure.message;
  }
}
document.querySelector("#new-project").addEventListener("click", openNewProject);
document.querySelector("#project-form").addEventListener("submit", async event => {
  event.preventDefault();
  const error = document.querySelector("#form-error");
  error.textContent = "";
  try {
    if (!event.target.elements.template.value) throw new Error("Choose a project template.");
    const created = await withBusy(event.submitter, () => api("/api/v1/projects", {method: "POST", body: JSON.stringify(Object.fromEntries(new FormData(event.target)))}), {failure: false});
    event.target.reset(); selectedProjectTemplate = "";
    document.querySelector("#project-dialog").close();
    toast("Project created. Generate its code from the scaffold panel.");
    await loadProjectOptions();
    await navigateTo("projects", {project: "", resource: created.id});
  } catch (failure) { error.textContent = failure.message; }
});

registerView("projects", loadProjects, {
  eyebrow: "GIT-NATIVE WORKSPACES", title: "Projects",
  openResource: openProjectDetail,
  intro: {
    what: "A project groups code, pipelines, functions, models and agents under one namespace and optional Git repository.",
    why: "Every other area filters by project, and Jupyter and the IDE open its folder.",
    needs: "The projects service. Creating projects also needs project quota.",
    action: "New project stores the record and a scaffold command. Files are written only when you run it: copy it into a terminal, or choose Run in workspace and confirm.",
    results: "Open a project to see related resources, its scaffold command, the last run's files, errors and git status.",
  },
});
