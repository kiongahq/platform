/* Overview: onboarding progress, headline counts and recent runs. */
async function loadDashboard() {
  const data = await api(`/api/v1/dashboard${selectedProject ? `?project_id=${encodeURIComponent(selectedProject)}` : ""}`);
  document.querySelector("#stat-projects").textContent = data.projects;
  document.querySelector("#stat-runs").textContent = data.active_runs;
  document.querySelector("#stat-health").textContent = `${data.healthy_components}/${data.total_components}`;
  document.querySelector("#progress-ring").style.background = `conic-gradient(var(--accent) ${Number(data.onboarding_percent) || 0}%, var(--neutral-soft) 0)`;
  document.querySelector("#progress-ring strong").textContent = `${data.onboarding_percent}%`;
  document.querySelector("#recent-runs").innerHTML = data.recent_runs.length
    ? data.recent_runs.map(run => `<div class="run-row"><span class="run-icon" aria-hidden="true">↯</span><div><b title="${escapeHTML(run.name)}">${escapeHTML(run.name)}</b><small>${timeTag(run.created_at)}</small></div>${status(run.status)}</div>`).join("")
    : emptyState("No runs yet", "Define a pipeline flow, then run it from Pipelines.");
}

registerView("overview", loadDashboard, {
  eyebrow: "ML WORKSPACE", title: "Overview",
  intro: {
    what: "A summary of your projects, active runs and platform health.",
    why: "See what needs attention before opening a specific area.",
    action: "Choose a card to open that area with the current project filter.",
    results: "Counts update live; failures appear on the Pipelines and Platform pages.",
  },
});
