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
      const node = {
        key,
        id,
        name: String(source.name || id || `Job ${index + 1}`),
        status: String(source.status || "defined"),
        detail: String(target || "job"),
        kind,
        dependencies,
        parentKeys: [],
      };
      if (!id) errors.push(`Job ${index + 1} needs a name.`);
      if (firstByID.has(id)) errors.push(`Job name “${id}” is duplicated.`);
      else firstByID.set(id, key);
      return node;
    });

    const byKey = new Map(nodes.map(node => [node.key, node]));
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

    return {nodes, byKey, errors: [...new Set(errors)], cyclic, layers};
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
      grouped.get(layer).forEach((node, row) => positions.set(node.key, {
        x: 22 + column * columnWidth + boxWidth / 2,
        y: 22 + row * rowHeight + boxHeight / 2,
      }));
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
      const graph = new root.dagre.graphlib.Graph()
        .setGraph({rankdir: "LR", ranksep: 70, nodesep: 40, marginx: 22, marginy: 22})
        .setDefaultEdgeLabel(() => ({}));
      analysis.nodes.forEach(node => graph.setNode(node.key, {width: boxWidth, height: boxHeight}));
      analysis.nodes.forEach(node => node.parentKeys.forEach(parent => graph.setEdge(parent, node.key)));
      root.dagre.layout(graph);
      return {
        positions: new Map(analysis.nodes.map(node => [node.key, graph.node(node.key)])),
        edges: graph.edges().map(edge => ({
          parent: edge.v,
          child: edge.w,
          points: graph.edge(edge).points || [],
        })),
        width: Math.max(260, Number(graph.graph().width) || 0),
        height: Math.max(100, Number(graph.graph().height) || 0),
        fallback: false,
      };
    } catch (_error) {
      return null;
    }
  }

  function render(items, options = {}) {
    const analysis = normalize(items);
    if (!analysis.nodes.length) {
      const message = analysis.errors[0] || options.emptyMessage || "No jobs in this pipeline.";
      return `<div class="dag-empty">${escapeHTML(message)}</div>`;
    }

    const compact = Boolean(options.compact);
    const boxWidth = compact ? 142 : 184;
    const boxHeight = compact ? 52 : 64;
    const geometry = dagreGeometry(analysis, boxWidth, boxHeight) || fallbackGeometry(analysis, boxWidth, boxHeight);
    const markerID = `dag-arrow-${++sequence}`;
    const colors = {
      defined: "#0071e3", queued: "#bf5af2", pending: "#8e8e93", running: "#0a84ff",
      succeeded: "#30d158", failed: "#ff453a", skipped: "#8e8e93", cancelled: "#ff9f0a",
    };

    const edges = geometry.edges.map(edge => {
      if (edge.points.length) {
        const points = edge.points.map(point => `${Number(point.x).toFixed(1)},${Number(point.y).toFixed(1)}`).join(" ");
        return `<polyline points="${points}" class="dag-edge" marker-end="url(#${markerID})"/>`;
      }
      const from = geometry.positions.get(edge.parent);
      const to = geometry.positions.get(edge.child);
      if (!from || !to) return "";
      const startX = from.x + boxWidth / 2;
      const endX = to.x - boxWidth / 2;
      return `<path d="M ${startX} ${from.y} C ${startX + 28} ${from.y}, ${endX - 28} ${to.y}, ${endX} ${to.y}" class="dag-edge" marker-end="url(#${markerID})"/>`;
    }).join("");

    const nodes = analysis.nodes.map(node => {
      const position = geometry.positions.get(node.key);
      if (!position) return "";
      const title = `${node.name} — ${node.detail} — ${node.status}`;
      return `<g class="dag-node-group" transform="translate(${position.x - boxWidth / 2},${position.y - boxHeight / 2})" tabindex="0" role="group" aria-label="${escapeHTML(title)}" data-graph-node="${escapeHTML(node.id)}"><title>${escapeHTML(title)}</title><rect width="${boxWidth}" height="${boxHeight}" rx="${compact ? 9 : 12}" class="dag-node"/><circle cx="${compact ? 14 : 18}" cy="${compact ? 19 : 23}" r="${compact ? 5 : 6}" fill="${colors[node.status] || "#8e8e93"}"/><text x="${compact ? 25 : 32}" y="${compact ? 23 : 27}" class="dag-name">${escapeHTML(clip(node.name, compact ? 18 : 24))}</text><text x="${compact ? 14 : 18}" y="${compact ? 41 : 49}" class="dag-status">${escapeHTML(clip(`${node.detail} · ${node.status}`, compact ? 23 : 30))}</text></g>`;
    }).join("");

    const visibleErrors = analysis.errors.slice(0, 4);
    const remainingErrors = analysis.errors.length - visibleErrors.length;
    const warnings = analysis.errors.length
      ? `<div class="dag-warning" role="status"><b>Graph needs attention</b><span>${escapeHTML(visibleErrors.join(" "))}${remainingErrors ? ` ${remainingErrors} more issue${remainingErrors === 1 ? "" : "s"}.` : ""}</span></div>`
      : "";
    const label = options.ariaLabel || "Pipeline dependency graph";
    const minimumWidth = compact ? Math.min(Math.max(geometry.width, 280), 780) : Math.max(geometry.width, 620);
    return `${warnings}<div class="dag-canvas${compact ? " dag-compact" : ""}" data-layout="${geometry.fallback ? "fallback" : "dagre"}"><svg class="dag-svg" style="width:${Math.ceil(minimumWidth)}px" viewBox="0 0 ${Math.ceil(geometry.width)} ${Math.ceil(geometry.height)}" role="img" aria-label="${escapeHTML(label)}"><defs><marker id="${markerID}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z"/></marker></defs>${edges}${nodes}</svg></div>`;
  }

  const pipelineGraph = {analyze: normalize, render};
  root.KiongaPipelineGraph = pipelineGraph;
  if (typeof module !== "undefined" && module.exports) module.exports = pipelineGraph;
}(typeof window !== "undefined" ? window : globalThis));
