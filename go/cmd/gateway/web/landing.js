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

const workspaceTabs = [...document.querySelectorAll("[data-workspace-tab]")];
const workspaceContent = document.querySelector("#workspace-preview-content");
const notebookPreview = workspaceContent?.innerHTML || "";
const trainPreview = `
  <p class="preview-note">Preview file · churn-model/train.py</p>
  <pre class="workspace-source"><span class="muted">01</span> <span class="blue">from</span> pathlib <span class="blue">import</span> Path
<span class="muted">02</span> <span class="blue">import</span> mlflow
<span class="muted">03</span> <span class="blue">from</span> sklearn.metrics <span class="blue">import</span> roc_auc_score
<span class="muted">04</span> <span class="blue">from</span> churn_model.data <span class="blue">import</span> load_dataset
<span class="muted">05</span> <span class="blue">from</span> churn_model.model <span class="blue">import</span> fit_classifier
<span class="muted">06</span>
<span class="muted">07</span> <span class="blue">def</span> <span class="green">train</span>(dataset: Path) -> float:
<span class="muted">08</span>     features, labels = load_dataset(dataset)
<span class="muted">09</span>     model = fit_classifier(features, labels)
<span class="muted">10</span>     score = roc_auc_score(labels, model.predict_proba(features)[:, 1])
<span class="muted">11</span>     mlflow.log_metric(<span class="green">"auc"</span>, score)
<span class="muted">12</span>     mlflow.sklearn.log_model(model, <span class="green">"churn-classifier"</span>)
<span class="muted">13</span>     <span class="blue">return</span> score</pre>
  <p class="preview-footnote">Generated code preview. Open Jupyter or the IDE to work in your own workspace.</p>`;
let workspaceRunTimer;

function wireWorkspaceRun() {
  const runButton = workspaceContent.querySelector("[data-workspace-run]");
  const output = workspaceContent.querySelector("[data-workspace-output]");
  if (!runButton || !output) return;
  runButton.addEventListener("click", () => {
    clearTimeout(workspaceRunTimer);
    runButton.disabled = true;
    runButton.textContent = "Running preview…";
    output.replaceChildren();
    const steps = [
      "Created project structure",
      "Added training pipeline and evaluation",
      "Connected MLflow model registration",
      "8 sample tests passed"
    ];
    const showStep = index => {
      const line = document.createElement("div");
      line.className = "preview-output-line";
      line.textContent = `✓ ${steps[index]}`;
      output.append(line);
      if (index + 1 < steps.length) {
        workspaceRunTimer = setTimeout(() => showStep(index + 1),
          window.matchMedia("(prefers-reduced-motion: reduce)").matches ? 0 : 370);
      } else {
        runButton.disabled = false;
        runButton.innerHTML = 'Run again <span aria-hidden="true">↻</span>';
      }
    };
    showStep(0);
  });
}

workspaceTabs.forEach((tab, index) => {
  tab.addEventListener("click", () => {
    clearTimeout(workspaceRunTimer);
    workspaceTabs.forEach(item => {
      const selected = item === tab;
      item.classList.toggle("active", selected);
      item.setAttribute("aria-selected", String(selected));
    });
    workspaceContent.setAttribute("aria-labelledby", tab.id);
    workspaceContent.innerHTML = tab.dataset.workspaceTab === "train" ? trainPreview : notebookPreview;
    wireWorkspaceRun();
  });
  tab.addEventListener("keydown", event => {
    if (!["ArrowLeft", "ArrowRight"].includes(event.key)) return;
    event.preventDefault();
    const direction = event.key === "ArrowRight" ? 1 : -1;
    workspaceTabs[(index + direction + workspaceTabs.length) % workspaceTabs.length].focus();
  });
});
if (workspaceContent) wireWorkspaceRun();

fetch("/api/v1/blogs")
  .then(response => response.ok ? response.json() : Promise.reject(new Error("blog unavailable")))
  .then(data => {
    const posts = (data.items || []).slice(0, 3);
    document.querySelector("#landing-blog-grid").innerHTML = posts.length ? posts.map(post => `<article class="landing-blog-card">${post.cover && post.cover.url ? `<img class="landing-blog-cover" src="${escapeLanding(post.cover.url)}" srcset="${escapeLanding(post.cover.srcset)}" sizes="(max-width: 760px) 100vw, 380px" alt="${escapeLanding(post.cover.alt)}" loading="lazy">` : ""}<div class="blog-card-meta"><span>${escapeLanding((post.tags || [])[0] || "Engineering")}</span><time>${new Date(post.published_at || post.created_at).toLocaleDateString(undefined,{year:"numeric",month:"short",day:"numeric"})}</time></div><h3>${escapeLanding(post.title)}</h3><p>${escapeLanding(post.summary)}</p><a href="/blog.html?slug=${encodeURIComponent(post.slug)}">Read article →</a></article>`).join("") : "<p>No engineering posts published yet.</p>";
  })
  .catch(() => { document.querySelector("#landing-blog-grid").innerHTML = "<p>Engineering posts are temporarily unavailable.</p>"; });

function escapeLanding(value) {
  return String(value || "").replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[character]));
}
