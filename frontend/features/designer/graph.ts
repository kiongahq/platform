import type { Entity } from "@/lib/types";
export type GraphNode = {
  id: string;
  kind: string;
  name: string;
  x: number;
  y: number;
  config: Entity;
  group?: string;
  [key: string]: any;
};
export type GraphEdge = {
  from: string;
  to: string;
  from_port?: string;
  to_port?: string;
  [key: string]: any;
};
export type GraphDraft = {
  id?: string;
  project_id: string;
  name: string;
  description: string;
  revision: number;
  nodes: GraphNode[];
  edges: GraphEdge[];
  settings?: Entity;
  schema_version?: number;
  [key: string]: any;
};
export function blankGraph(project = ""): GraphDraft {
  return {
    project_id: project,
    name: "New agent",
    description: "",
    revision: 0,
    nodes: [
      { id: "input", kind: "input", name: "Input", x: 40, y: 100, config: {} },
      {
        id: "output",
        kind: "output",
        name: "Output",
        x: 460,
        y: 100,
        config: {},
      },
    ],
    edges: [{ from: "input", to: "output" }],
  };
}
export function graphPayload(draft: GraphDraft) {
  return {
    project_id: draft.project_id,
    name: draft.name.trim(),
    description: (draft.description || "").trim(),
    revision: draft.revision,
    nodes: draft.nodes.map((node) => ({
      id: node.id,
      kind: node.kind,
      name: node.name,
      x: node.x,
      y: node.y,
      ...(node.group ? { group: node.group } : {}),
      config: Object.fromEntries(
        Object.entries(node.config || {}).map(([key, value]) => [
          key,
          String(value),
        ]),
      ),
    })),
    edges: draft.edges,
    ...(draft.settings ? { settings: draft.settings } : {}),
  };
}
export function reachable(from: string, to: string, edges: GraphEdge[]) {
  const seen = new Set<string>(),
    pending = [from];
  while (pending.length) {
    const id = pending.pop()!;
    if (id === to) return true;
    if (seen.has(id)) continue;
    seen.add(id);
    for (const edge of edges) if (edge.from === id) pending.push(edge.to);
  }
  return false;
}
export function connectionIssue(
  draft: GraphDraft,
  from: string,
  to: string,
  port = "out",
) {
  const source = draft.nodes.find((n) => n.id === from),
    target = draft.nodes.find((n) => n.id === to);
  if (
    !source ||
    !target ||
    from === to ||
    source.kind === "output" ||
    target.kind === "input"
  )
    return "Choose two different compatible ports.";
  if (
    draft.edges.some(
      (e) => e.from === from && e.to === to && (e.from_port || "out") === port,
    )
  )
    return "These ports are already connected.";
  if (draft.edges.length >= 300)
    return "A design can have at most 300 connections.";
  if (reachable(to, from, draft.edges))
    return "That connection would create a cycle.";
  return "";
}
export function graphIssues(draft: GraphDraft) {
  return [
    !draft.project_id ? "Choose a project." : "",
    !draft.name.trim() ? "Enter a design name." : "",
    draft.nodes.length > 100 ? "A design can have at most 100 nodes." : "",
    draft.edges.length > 300
      ? "A design can have at most 300 connections."
      : "",
    draft.nodes.some((n) => !n.name.trim()) ? "Give every node a name." : "",
  ].filter(Boolean);
}
export function fromGraph(
  value: Entity,
  project: string,
  name = "Imported agent",
): GraphDraft {
  const graph = value.graph || value;
  return {
    ...blankGraph(project),
    name,
    description: graph.description || "",
    schema_version: graph.schema_version,
    settings: graph.settings,
    nodes: (graph.nodes || []).map((node: Entity, i: number) => ({
      ...node,
      config: node.config || {},
      x: node.x ?? 40 + (i % 4) * 240,
      y: node.y ?? 80 + Math.floor(i / 4) * 160,
    })),
    edges: graph.edges || [],
  };
}
