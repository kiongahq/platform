const host = location.hostname;
document.querySelectorAll(".workbench-link").forEach(link => { link.href = `http://${host}:8888`; });
document.querySelectorAll(".ide-link").forEach(link => { link.href = `http://${host}:13337`; });

const previewPanels = {
  overview: {
    eyebrow: "YOUR GOLDEN PATH",
    title: "From idea to a reliable model.",
    stats: [["Projects", "12"], ["Active runs", "4"], ["Platform health", "9/9", "healthy"]],
    panels: [
      ["PRODUCTION READINESS", "Everything connected", "progress"],
      ["RECENT RUN", "training-pipeline", "Succeeded", "success"]
    ]
  },
  projects: {
    eyebrow: "SHARED WORKSPACES",
    title: "Projects with context built in.",
    stats: [["Active", "12"], ["Contributors", "28"], ["Templates", "6"]],
    panels: [
      ["CHURN INTELLIGENCE", "Production", "Updated 4m ago", "success"],
      ["DEMAND FORECASTING", "Experimenting", "3 active runs", "info"]
    ]
  },
  pipelines: {
    eyebrow: "ORCHESTRATION",
    title: "Every run is visible and reproducible.",
    stats: [["Running", "4"], ["Succeeded", "142"], ["Reliability", "99.2%", "healthy"]],
    panels: [
      ["TRAINING PIPELINE", "ingest → validate → train", "Running", "info"],
      ["LATEST RUN", "feature-materialize", "Succeeded", "success"]
    ]
  },
  models: {
    eyebrow: "MODEL REGISTRY",
    title: "Promote with evidence, not guesswork.",
    stats: [["Registered", "34"], ["Serving", "8"], ["Evaluations", "216"]],
    panels: [
      ["CHURN CLASSIFIER · V3", "AUC 0.94 · Production", "Serving", "success"],
      ["FRAUD DETECTOR · V7", "Canary · 10% traffic", "Healthy", "success"]
    ]
  },
  agents: {
    eyebrow: "AGENT OPERATIONS",
    title: "Trace every intelligent decision.",
    stats: [["Sessions", "1.8k"], ["P95 latency", "820ms"], ["Pass rate", "96.4%", "healthy"]],
    panels: [
      ["SUPPORT COPILOT", "124 traces · $4.82 today", "Online", "success"],
      ["EVALUATION", "Groundedness suite", "48/50 passed", "info"]
    ]
  },
  features: {
    eyebrow: "FEATURE STORE",
    title: "Fresh features, online and offline.",
    stats: [["Feature views", "18"], ["Online", "12"], ["Freshness", "99.8%", "healthy"]],
    panels: [
      ["CUSTOMER PROFILE", "24 features · Redis", "Fresh", "success"],
      ["TRANSACTION SIGNALS", "Last materialized 8m ago", "On schedule", "info"]
    ]
  },
  storage: {
    eyebrow: "PERSISTENT STORAGE",
    title: "One filesystem across every workspace.",
    stats: [["Artifacts", "2.4 TB"], ["Datasets", "86"], ["Mounts", "14"]],
    panels: [
      ["TEAM WORKSPACE", "/workspace · Jupyter + IDE", "Mounted", "success"],
      ["OBJECT STORAGE", "s3://kionga-artifacts", "Connected", "info"]
    ]
  }
};

const previewContent = document.querySelector("#product-preview-content");
const previewTabs = [...document.querySelectorAll("[data-preview-panel]")];

function renderPreview(panelName) {
  const panel = previewPanels[panelName];
  if (!panel || !previewContent) return;
  previewContent.classList.remove("is-ready");
  previewContent.innerHTML = `
    <p class="mini-eyebrow">${panel.eyebrow}</p>
    <h2>${panel.title}</h2>
    <div class="frame-stats">${panel.stats.map(stat => `<div><small>${stat[0]}</small><strong class="${stat[2] || ""}">${stat[1]}</strong></div>`).join("")}</div>
    <div class="frame-panels">${panel.panels.map(item => `<article><small>${item[0]}</small><strong>${item[1]}</strong>${item[2] === "progress" ? '<div class="progress"><i></i></div>' : `<span class="${item[3] || "info"}">${item[2]}</span>`}</article>`).join("")}</div>`;
  requestAnimationFrame(() => previewContent.classList.add("is-ready"));
}

previewTabs.forEach((tab, index) => {
  tab.addEventListener("click", () => {
    previewTabs.forEach(item => {
      const selected = item === tab;
      item.classList.toggle("active", selected);
      item.setAttribute("aria-selected", String(selected));
    });
    renderPreview(tab.dataset.previewPanel);
  });
  tab.addEventListener("keydown", event => {
    if (!["ArrowDown", "ArrowRight", "ArrowUp", "ArrowLeft"].includes(event.key)) return;
    event.preventDefault();
    const direction = ["ArrowDown", "ArrowRight"].includes(event.key) ? 1 : -1;
    previewTabs[(index + direction + previewTabs.length) % previewTabs.length].focus();
  });
});

fetch("/api/v1/blogs")
  .then(response => response.ok ? response.json() : Promise.reject(new Error("blog unavailable")))
  .then(data => {
    const posts = (data.items || []).slice(0, 3);
    document.querySelector("#landing-blog-grid").innerHTML = posts.length ? posts.map(post => `<article class="landing-blog-card"><div class="blog-card-meta"><span>${post.tags[0] || "Engineering"}</span><time>${new Date(post.published_at || post.created_at).toLocaleDateString(undefined,{year:"numeric",month:"short",day:"numeric"})}</time></div><h3>${escapeLanding(post.title)}</h3><p>${escapeLanding(post.summary)}</p><a href="/blog.html?slug=${encodeURIComponent(post.slug)}">Read article →</a></article>`).join("") : "<p>No engineering posts published yet.</p>";
  })
  .catch(() => { document.querySelector("#landing-blog-grid").innerHTML = "<p>Engineering posts are temporarily unavailable.</p>"; });

function escapeLanding(value) {
  return String(value || "").replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[character]));
}
