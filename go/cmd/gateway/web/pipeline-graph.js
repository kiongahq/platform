/* Pipeline DAG rendering for the console.
 *
 * render(items, options) returns markup (an SVG inside a canvas). Edges are
 * produced only from each node's depends_on, so the drawing always matches
 * the persisted graph; each edge carries data-edge="parent>child" for tests.
 * enhance(container, {onSelect}) adds zoom, pan, fit-to-view and keyboard
 * navigation to rendered graphs. Layout uses the vendored open-source Dagre
 * library, with a dependency-layer fallback when it is unavailable.
 */
(function initializePipelineGraph(root) {
  "use strict";

  let sequence = 0;

  const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, character => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[character]));

  const clip = (value, length) => {
    const text = String(value ?? "");
    return text.length > length ? `${text.slice(0, Math.max(0, length - 1))}…` : text;
  };

  /* Status is conveyed by color AND a symbol AND text. */
  const STATUS = {
    defined: {color: "#0066d6", symbol: "○", label: "Defined"},
    pending: {color: "#8e8e93", symbol: "○", label: "Pending"},
    queued: {color: "#8e5ad6", symbol: "…", label: "Queued"},
    running: {color: "#0a84ff", symbol: "▶", label: "Running"},
    succeeded: {color: "#1c9c46", symbol: "✓", label: "Succeeded"},
    failed: {color: "#d93025", symbol: "✕", label: "Failed"},
    skipped: {color: "#8e8e93", symbol: "↷", label: "Skipped"},
    cancelled: {color: "#c46b10", symbol: "⊘", label: "Cancelled"},
  };
  const statusOf = value => STATUS[value] || {color: "#8e8e93", symbol: "•", label: String(value || "unknown")};

  function normalize(items) {
    if (!Array.isArray(items)) {
      return {nodes: [], errors: ["Jobs must be a JSON array."], cyclic: false, layers: new Map()};
    }
    if (!items.length) {
      return {nodes: [], errors: ["Pipeline needs at least one job."], cyclic: false, layers: new Map()};
    }
    const errors = [];
    const firstByID = new Map();
    const nodes = items.map((item, index) => {
      const source = item && typeof item === "object" ? item : {};
      const id = String(source.id || source.name || `job-${index + 1}`).trim();
      const key = `${id || "job"}\u0000${index}`;
      const dependencies = Array.isArray(source.depends_on)
        ? [...new Set(source.depends_on.map(value => String(value).trim()).filter(Boolean))]
        : [];
      const kind = String(source.kind || (source.function ? "function" : source.image ? "container" : "job"));
      const target = source.function ? `function://${source.function}` : source.image || kind;
      const node = {key, id, name: String(source.name || id || `Job ${index + 1}`), status: String(source.status || "defined"), detail: String(target || "job"), kind, dependencies, parentKeys: []};
      if (!id) errors.push(`Job ${index + 1} needs a name.`);
      if (firstByID.has(id)) errors.push(`Job name “${id}” is duplicated.`);
      else firstByID.set(id, key);
      return node;
    });
    nodes.forEach(node => {
      node.dependencies.forEach(dependency => {
        const parentKey = firstByID.get(dependency);
        if (!parentKey) errors.push(`“${node.name}” depends on missing job “${dependency}”.`);
        else if (parentKey === node.key) errors.push(`“${node.name}” cannot depend on itself.`);
        else node.parentKeys.push(parentKey);
      });
      node.parentKeys = [...new Set(node.parentKeys)];
    });
    const indegree = new Map(nodes.map(node => [node.key, node.parentKeys.length]));
    const children = new Map(nodes.map(node => [node.key, []]));
    nodes.forEach(node => node.parentKeys.forEach(parent => children.get(parent)?.push(node.key)));
    const queue = nodes.filter(node => indegree.get(node.key) === 0).map(node => node.key);
    const ordered = [];
    const layers = new Map();
    queue.forEach(key => layers.set(key, 0));
    let cursor = 0;
    while (cursor < queue.length) {
      const key = queue[cursor++];
      ordered.push(key);
      for (const child of children.get(key) || []) {
        layers.set(child, Math.max(layers.get(child) || 0, (layers.get(key) || 0) + 1));
        const remaining = (indegree.get(child) || 0) - 1;
        indegree.set(child, remaining);
        if (remaining === 0) queue.push(child);
      }
    }
    const orderedKeys = new Set(ordered);
    const unresolved = nodes.filter(node => !orderedKeys.has(node.key));
    const cyclic = unresolved.length > 0;
    if (cyclic) {
      errors.push(`Dependency cycle detected across ${unresolved.map(node => `“${node.name}”`).join(", ")}.`);
      const lastLayer = [...layers.values()].reduce((maximum, value) => Math.max(maximum, value), 0) + 1;
      unresolved.forEach(node => layers.set(node.key, lastLayer));
    }
    return {nodes, byKey: new Map(nodes.map(node => [node.key, node])), errors: [...new Set(errors)], cyclic, layers};
  }

  function fallbackGeometry(analysis, boxWidth, boxHeight) {
    const grouped = new Map();
    analysis.nodes.forEach(node => {
      const layer = analysis.layers.get(node.key) || 0;
      if (!grouped.has(layer)) grouped.set(layer, []);
      grouped.get(layer).push(node);
    });
    const orderedLayers = [...grouped.keys()].sort((left, right) => left - right);
    const columnWidth = boxWidth + 70;
    const rowHeight = boxHeight + 40;
    const positions = new Map();
    orderedLayers.forEach((layer, column) => {
      grouped.get(layer).forEach((node, row) => positions.set(node.key, {x: 22 + column * columnWidth + boxWidth / 2, y: 22 + row * rowHeight + boxHeight / 2}));
    });
    const rows = [...grouped.values()].reduce((maximum, values) => Math.max(maximum, values.length), 1);
    return {
      positions,
      edges: analysis.nodes.flatMap(node => node.parentKeys.map(parent => ({parent, child: node.key, points: []}))),
      width: Math.max(260, orderedLayers.length * columnWidth + 24),
      height: Math.max(100, rows * rowHeight + 24),
      fallback: true,
    };
  }

  function dagreGeometry(analysis, boxWidth, boxHeight) {
    if (!root.dagre || analysis.cyclic) return null;
    try {
      const graph = new root.dagre.graphlib.Graph().setGraph({rankdir: "LR", ranksep: 70, nodesep: 36, marginx: 22, marginy: 22}).setDefaultEdgeLabel(() => ({}));
      analysis.nodes.forEach(node => graph.setNode(node.key, {width: boxWidth, height: boxHeight}));
      analysis.nodes.forEach(node => node.parentKeys.forEach(parent => graph.setEdge(parent, node.key)));
      root.dagre.layout(graph);
      return {
        positions: new Map(analysis.nodes.map(node => [node.key, graph.node(node.key)])),
        edges: graph.edges().map(edge => ({parent: edge.v, child: edge.w, points: graph.edge(edge).points || []})),
        width: Math.max(260, Number(graph.graph().width) || 0),
        height: Math.max(100, Number(graph.graph().height) || 0),
        fallback: false,
      };
    } catch (_error) {
      return null;
    }
  }

  function legend(analysis) {
    const used = [...new Set(analysis.nodes.map(node => node.status))];
    return `<div class="dag-legend" aria-label="Status legend">${used.map(value => {
      const info = statusOf(value);
      return `<span><i aria-hidden="true" style="color:${info.color}">${info.symbol}</i>${escapeHTML(info.label)}</span>`;
    }).join("")}<span><i aria-hidden="true">→</i>runs after</span></div>`;
  }

  function render(items, options = {}) {
    const analysis = normalize(items);
    if (!analysis.nodes.length) {
      const message = analysis.errors[0] || options.emptyMessage || "No jobs in this pipeline.";
      return `<div class="dag-empty">${escapeHTML(message)}</div>`;
    }
    const compact = Boolean(options.compact);
    const boxWidth = compact ? 142 : 196;
    const boxHeight = compact ? 52 : 66;
    const geometry = dagreGeometry(analysis, boxWidth, boxHeight) || fallbackGeometry(analysis, boxWidth, boxHeight);
    const markerID = `dag-arrow-${++sequence}`;
    const byKey = analysis.byKey;

    const edges = geometry.edges.map(edge => {
      const attr = `data-edge="${escapeHTML(`${byKey.get(edge.parent)?.id}>${byKey.get(edge.child)?.id}`)}"`;
      if (edge.points.length) {
        const points = edge.points.map(point => `${Number(point.x).toFixed(1)},${Number(point.y).toFixed(1)}`).join(" ");
        return `<polyline ${attr} points="${points}" class="dag-edge" marker-end="url(#${markerID})"/>`;
      }
      const from = geometry.positions.get(edge.parent);
      const to = geometry.positions.get(edge.child);
      if (!from || !to) return "";
      const startX = from.x + boxWidth / 2;
      const endX = to.x - boxWidth / 2;
      return `<path ${attr} d="M ${startX} ${from.y} C ${startX + 28} ${from.y}, ${endX - 28} ${to.y}, ${endX} ${to.y}" class="dag-edge" marker-end="url(#${markerID})"/>`;
    }).join("");

    const nodes = analysis.nodes.map((node, index) => {
      const position = geometry.positions.get(node.key);
      if (!position) return "";
      const info = statusOf(node.status);
      const title = `${node.name}: ${info.label}. ${node.detail}. ${node.dependencies.length ? `Depends on ${node.dependencies.join(", ")}.` : "No dependencies."}`;
      const tabindex = compact ? "-1" : index === 0 ? "0" : "-1";
      return `<g class="dag-node-group" transform="translate(${position.x - boxWidth / 2},${position.y - boxHeight / 2})" tabindex="${tabindex}" role="button" aria-label="${escapeHTML(title)}" data-graph-node="${escapeHTML(node.id)}" data-status="${escapeHTML(node.status)}"><title>${escapeHTML(title)}</title><rect width="${boxWidth}" height="${boxHeight}" rx="${compact ? 9 : 12}" class="dag-node" style="stroke:${info.color}"/><text x="${compact ? 10 : 14}" y="${compact ? 22 : 26}" class="dag-symbol" fill="${info.color}" aria-hidden="true">${info.symbol}</text><text x="${compact ? 25 : 32}" y="${compact ? 22 : 26}" class="dag-name">${escapeHTML(clip(node.name, compact ? 17 : 23))}</text><text x="${compact ? 10 : 14}" y="${compact ? 41 : 50}" class="dag-status">${escapeHTML(clip(`${info.label} · ${node.detail}`, compact ? 21 : 27))}</text></g>`;
    }).join("");

    const visibleErrors = analysis.errors.slice(0, 6);
    const warnings = analysis.errors.length
      ? `<div class="dag-warning" role="status"><b>Graph needs attention</b><ul>${visibleErrors.map(error => `<li>${escapeHTML(error)}</li>`).join("")}</ul>${analysis.errors.length > visibleErrors.length ? `<span>${analysis.errors.length - visibleErrors.length} more issues.</span>` : ""}</div>`
      : "";
    const label = options.ariaLabel || "Pipeline dependency graph";
    const width = Math.ceil(geometry.width), height = Math.ceil(geometry.height);
    const toolbar = compact ? "" : `<div class="dag-toolbar" role="toolbar" aria-label="Graph controls"><button type="button" data-dag-zoom="in" aria-label="Zoom in" title="Zoom in">+</button><button type="button" data-dag-zoom="out" aria-label="Zoom out" title="Zoom out">−</button><button type="button" data-dag-zoom="fit" aria-label="Fit graph to view" title="Fit to view">⤢</button></div>`;
    const canvasHeight = compact ? Math.min(Math.max(height, 110), 200) : Math.min(Math.max(height + 16, 200), 560);
    return `${warnings}<div class="dag-canvas${compact ? " dag-compact" : ""}" data-layout="${geometry.fallback ? "fallback" : "dagre"}" data-width="${width}" data-height="${height}" style="height:${canvasHeight}px">${toolbar}<svg class="dag-svg" viewBox="0 0 ${width} ${height}" preserveAspectRatio="xMidYMid meet" role="group" aria-label="${escapeHTML(label)}. ${analysis.nodes.length} nodes. Use arrow keys to move between nodes and Enter to open one."><defs><marker id="${markerID}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z"/></marker></defs><g class="dag-viewport">${edges}${nodes}</g></svg></div>${compact ? "" : legend(analysis)}`;
  }

  /* Interactive behavior for rendered graphs inside container. */
  function enhance(container, {onSelect} = {}) {
    container.querySelectorAll(".dag-canvas:not(.dag-compact)").forEach(canvas => {
      if (canvas.dataset.enhanced) return;
      canvas.dataset.enhanced = "true";
      const svg = canvas.querySelector("svg");
      const width = Number(canvas.dataset.width), height = Number(canvas.dataset.height);
      let view = {x: 0, y: 0, w: width, h: height};
      const apply = () => svg.setAttribute("viewBox", `${view.x} ${view.y} ${view.w} ${view.h}`);
      const zoom = (factor, cx = view.x + view.w / 2, cy = view.y + view.h / 2) => {
        const w = Math.min(Math.max(view.w * factor, width / 8), width * 4);
        const h = w * (view.h / view.w);
        view = {x: cx - (cx - view.x) * (w / view.w), y: cy - (cy - view.y) * (h / view.h), w, h};
        apply();
      };
      canvas.querySelectorAll("[data-dag-zoom]").forEach(button => button.addEventListener("click", () => {
        const action = button.dataset.dagZoom;
        if (action === "fit") { view = {x: 0, y: 0, w: width, h: height}; apply(); }
        else zoom(action === "in" ? 0.8 : 1.25);
      }));
      svg.addEventListener("wheel", event => {
        if (!event.ctrlKey && !event.metaKey) return; // plain wheel scrolls the page
        event.preventDefault();
        const rect = svg.getBoundingClientRect();
        const cx = view.x + ((event.clientX - rect.left) / rect.width) * view.w;
        const cy = view.y + ((event.clientY - rect.top) / rect.height) * view.h;
        zoom(event.deltaY < 0 ? 0.85 : 1.18, cx, cy);
      }, {passive: false});
      let drag = null;
      svg.addEventListener("pointerdown", event => {
        if (event.target.closest(".dag-node-group")) return;
        drag = {x: event.clientX, y: event.clientY, view: {...view}};
        svg.setPointerCapture?.(event.pointerId);
      });
      svg.addEventListener("pointermove", event => {
        if (!drag) return;
        const rect = svg.getBoundingClientRect();
        view = {...drag.view, x: drag.view.x - (event.clientX - drag.x) * (view.w / rect.width), y: drag.view.y - (event.clientY - drag.y) * (view.h / rect.height)};
        apply();
      });
      const end = () => { drag = null; };
      svg.addEventListener("pointerup", end);
      svg.addEventListener("pointercancel", end);

      const nodes = [...svg.querySelectorAll(".dag-node-group")];
      const select = node => {
        nodes.forEach(other => other.setAttribute("aria-selected", String(other === node)));
        onSelect?.(node.dataset.graphNode);
      };
      nodes.forEach(node => {
        node.addEventListener("click", () => select(node));
        node.addEventListener("keydown", event => {
          if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(node); return; }
          const index = nodes.indexOf(node);
          const next = {ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1}[event.key];
          if (next === undefined) return;
          event.preventDefault();
          const target = nodes[(index + next + nodes.length) % nodes.length];
          node.setAttribute("tabindex", "-1");
          target.setAttribute("tabindex", "0");
          target.focus();
        });
      });
    });
  }

  /* Edge list derived from the data alone; tests compare it to the SVG. */
  function edgesOf(items) {
    return normalize(items).nodes.flatMap(node => node.dependencies.map(dependency => `${dependency}>${node.id}`)).sort();
  }

  const pipelineGraph = {analyze: normalize, render, enhance, edgesOf, STATUS};
  root.KiongaPipelineGraph = pipelineGraph;
  if (typeof module !== "undefined" && module.exports) module.exports = pipelineGraph;
}(typeof window !== "undefined" ? window : globalThis));
