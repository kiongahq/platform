/* Logs: one renderer used by the node panel and the explorer page.
 * Pages go newest-first with a "before" cursor; live tail uses the SSE
 * stream, which resumes from the last sequence it delivered. */
const severityOrder = ["error", "warn", "info", "debug"];

function logLine(entry, {showRun = false} = {}) {
  const time = new Date(entry.ts);
  const where = [showRun && entry.run_id, entry.node, entry.attempt ? `#${entry.attempt}` : ""].filter(Boolean).join(" ");
  return `<div class="log-line" data-seq="${entry.sequence}"><time datetime="${escapeHTML(entry.ts)}" title="${escapeHTML(time.toLocaleString())}">${escapeHTML(time.toLocaleTimeString())}</time><b title="${escapeHTML(`${entry.source} · ${where}`)}">${escapeHTML(where || entry.source)}</b><span class="sev ${escapeHTML(entry.severity)}">${escapeHTML(entry.severity)}</span><span>${escapeHTML(entry.message)}</span></div>`;
}

/* A self-contained log panel bound to a container element. */
function createLogPanel(container, baseQuery, {showRun = false, emptyText = "No log lines yet."} = {}) {
  const panel = {query: {...baseQuery}, oldest: 0, newest: 0, source: null, loading: false};
  container.innerHTML = `<div class="log-filters">
      <label>Severity<select data-log-severity><option value="">All</option><option value="error">Errors</option><option value="warn,error">Warnings and errors</option><option value="info,warn,error">Info and above</option></select></label>
      <label>Source<select data-log-source><option value="">All sources</option><option value="runner">Container output</option><option value="platform">Platform events</option><option value="engine">Engine</option><option value="k8s">Kubernetes</option></select></label>
      <label>Search<input type="search" data-log-search placeholder="Text in the message"></label>
      <label class="inline-check"><input type="checkbox" data-log-live> Live tail</label>
    </div>
    <div class="logs" role="log" aria-live="polite" tabindex="0"><p class="log-empty">Loading logs…</p></div>
    <div class="button-row start"><button type="button" class="btn-sm" data-log-older hidden>Load earlier lines</button><span class="field-help" data-log-status role="status"></span></div>`;
  const box = container.querySelector(".logs");
  const status = container.querySelector("[data-log-status]");
  const params = extra => {
    const query = new URLSearchParams({...panel.query, ...extra});
    for (const [key, value] of [...query]) if (!value) query.delete(key);
    return query;
  };
  const filters = () => ({
    severity: container.querySelector("[data-log-severity]").value,
    source: container.querySelector("[data-log-source]").value,
    q: container.querySelector("[data-log-search]").value.trim(),
  });
  async function load(reset = true) {
    if (panel.loading) return;
    panel.loading = true;
    try {
      const page = await api(`/api/v1/logs?${params({...filters(), order: "desc", limit: 200, ...(reset ? {} : {before: panel.oldest})})}`);
      const items = [...page.items].reverse();
      if (reset) {
        box.innerHTML = items.length ? items.map(entry => logLine(entry, {showRun})).join("") : `<p class="log-empty">${escapeHTML(emptyText)}</p>`;
        panel.newest = page.items[0]?.sequence || 0;
        box.scrollTop = box.scrollHeight;
      } else if (items.length) {
        const height = box.scrollHeight;
        box.insertAdjacentHTML("afterbegin", items.map(entry => logLine(entry, {showRun})).join(""));
        box.scrollTop = box.scrollHeight - height;
      }
      if (items.length) panel.oldest = items[0].sequence;
      container.querySelector("[data-log-older]").hidden = !page.next_before;
      status.textContent = `${box.querySelectorAll(".log-line").length} lines shown`;
    } catch (error) {
      box.innerHTML = `<p class="log-empty">${escapeHTML(error.message)}</p>`;
    } finally { panel.loading = false; }
  }
  function live(on) {
    panel.source?.close();
    panel.source = null;
    if (!on) { status.textContent = "Live tail off"; return; }
    const stream = new EventSource(`/api/v1/logs/stream?${params({...filters(), after: panel.newest})}`);
    panel.source = stream;
    status.textContent = "Live tail on";
    stream.addEventListener("logs", event => {
      const entries = JSON.parse(event.data);
      box.querySelector(".log-empty")?.remove();
      const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
      box.insertAdjacentHTML("beforeend", entries.map(entry => logLine(entry, {showRun})).join(""));
      panel.newest = entries[entries.length - 1].sequence;
      if (atBottom) box.scrollTop = box.scrollHeight;
    });
    stream.onerror = () => { status.textContent = "Live tail reconnecting…"; };
  }
  let timer = null;
  container.addEventListener("input", event => {
    if (event.target.matches("[data-log-live]")) return;
    clearTimeout(timer);
    timer = setTimeout(async () => { await load(true); if (panel.source) live(true); }, 250);
  });
  container.querySelector("[data-log-live]").addEventListener("change", event => live(event.target.checked));
  container.querySelector("[data-log-older]").addEventListener("click", () => load(false));
  panel.load = load;
  panel.close = () => live(false);
  load(true);
  return panel;
}

// ---- explorer page -----------------------------------------------------------
let explorerPanel = null;
const timeWindows = {"15m": 15 * 60e3, "1h": 3600e3, "24h": 86400e3, "7d": 7 * 86400e3, "": 0};
async function loadLogsView() {
  const run = document.querySelector("#logs-run").value.trim();
  const node = document.querySelector("#logs-node").value.trim();
  const windowValue = document.querySelector("#logs-window").value;
  const query = {project_id: selectedProject, run_id: run, node};
  if (timeWindows[windowValue]) query.since = new Date(Date.now() - timeWindows[windowValue]).toISOString();
  explorerPanel?.close();
  explorerPanel = createLogPanel(document.querySelector("#logs-explorer"), query, {showRun: true, emptyText: "No log lines match these filters."});
}
document.querySelector("#logs-scope-form").addEventListener("submit", event => { event.preventDefault(); loadLogsView().catch(error => feedback(error.message, true)); });

registerView("logs", loadLogsView, {
  eyebrow: "OBSERVABILITY", title: "Logs",
  intro: {
    what: "Container output, platform events and engine messages for every pipeline run, correlated by project, run, node and attempt.",
    why: "Find why a node failed without opening each run, and follow a run live.",
    needs: "The pipelines service. You only see projects you can read; secrets are redacted before storage.",
    action: "Filter, then load earlier lines or turn on live tail.",
    results: "Lines are kept for the configured retention period (30 days by default).",
  },
});
