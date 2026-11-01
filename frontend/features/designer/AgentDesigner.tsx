"use client";
import {
  GraphCanvas,
  type GraphCanvasHandle,
} from "@/components/ui/GraphCanvas";
import { CodeEditor } from "@/components/ui/CodeEditor";
import { Icon } from "@/components/ui/Icon";
import { Select } from "@/components/ui/Select";
import { useEffect, useReducer, useRef, useState } from "react";
import { request, APIError, errorMessage } from "@/lib/api";
import { useQuery } from "@/hooks/useQuery";
import { useUnsaved } from "@/hooks/useUnsaved";
import { items, hasService, type Entity } from "@/lib/types";
import { DataState, Feedback, type ViewProps } from "@/features/shared/View";
import {
  blankGraph,
  connectionIssue,
  fromGraph,
  graphIssues,
  graphPayload,
  type GraphDraft,
  type GraphNode,
  type GraphEdge,
} from "./graph";
import { DesignStudio } from "./DesignStudio";
type History = { draft: GraphDraft; past: GraphDraft[]; future: GraphDraft[] };
function history(
  state: History,
  action: { type: string; draft?: GraphDraft; record?: boolean },
): History {
  if (action.type === "open")
    return { draft: action.draft!, past: [], future: [] };
  if (action.type === "change")
    return {
      draft: action.draft!,
      past:
        action.record === false
          ? state.past
          : [...state.past, state.draft].slice(-100),
      future: [],
    };
  if (action.type === "undo" && state.past.length)
    return {
      draft: state.past.at(-1)!,
      past: state.past.slice(0, -1),
      future: [state.draft, ...state.future],
    };
  if (action.type === "redo" && state.future.length)
    return {
      draft: state.future[0],
      past: [...state.past, state.draft],
      future: state.future.slice(1),
    };
  return state;
}
export function AgentDesigner({ identity, project, resource }: ViewProps) {
  const designs = useQuery<Entity>("/api/v1/agent-designs"),
    registry = useQuery<Entity>("/api/v1/agent-designs/components"),
    templates = useQuery<Entity>("/api/v1/agent-designs/templates"),
    projects = useQuery<Entity>("/api/v1/project-options");
  const paths: Record<string, string> = {
    model: "/api/v1/models",
    tool: "/api/v1/tools",
    knowledge_base: "/api/v1/knowledge-bases",
    connection: "/api/v1/agent-api-connections",
    dataset: "/api/v1/datasets",
    agent_version: "/api/v1/agent-versions",
  };
  const [resources, setResources] = useState<Record<string, Entity[]>>({});
  const [state, dispatch] = useReducer(history, {
      draft: blankGraph(project),
      past: [],
      future: [],
    }),
    [baseline, setBaseline] = useState(
      JSON.stringify(graphPayload(blankGraph(project))),
    ),
    [selected, setSelected] = useState<string[]>([]),
    [connecting, setConnecting] = useState<{ id: string; port: string } | null>(
      null,
    ),
    [search, setSearch] = useState(""),
    [zoom, setZoom] = useState(1),
    [snap, setSnap] = useState(true),
    [error, setError] = useState(""),
    [message, setMessage] = useState(""),
    [saving, setSaving] = useState(false),
    [validation, setValidation] = useState<Entity | null>(null),
    [conflict, setConflict] = useState<Entity | null>(null);
  const [paletteOpen, setPaletteOpen] = useState(true);
  const [expanded, setExpanded] = useState(false),
    [context, setContext] = useState<{
      id: string;
      x: number;
      y: number;
    } | null>(null);
  const flow = useRef<GraphCanvasHandle>(null);
  useEffect(() => {
    if (!expanded) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setExpanded(false);
        setContext(null);
      }
    };
    window.addEventListener("keydown", escape);
    return () => {
      document.body.style.overflow = previous;
      window.removeEventListener("keydown", escape);
    };
  }, [expanded]);
  useEffect(() => {
    if (!context) return;
    const close = () => setContext(null);
    window.addEventListener("pointerdown", close);
    window.addEventListener("blur", close);
    return () => {
      window.removeEventListener("pointerdown", close);
      window.removeEventListener("blur", close);
    };
  }, [context]);
  useEffect(() => {
    if (window.matchMedia)
      setPaletteOpen(!window.matchMedia("(max-width: 640px)").matches);
  }, []);
  const [nodeStatus, setNodeStatus] = useState<Record<string, string>>({});
  const generation = useRef(0);
  const current = useRef(state.draft);
  current.current = state.draft;
  const copied = useRef<{ nodes: GraphNode[]; edges: GraphEdge[] } | null>(
      null,
    ),
    scroller = useRef<HTMLDivElement>(null),
    file = useRef<HTMLInputElement>(null);
  const draft = state.draft,
    dirty = JSON.stringify(graphPayload(draft)) !== baseline,
    writable = identity.permissions.agents_write === true;
  useUnsaved(dirty);
  const components = registry.data?.components || [],
    component = (kind: string) =>
      components.find((c: Entity) => c.kind === kind),
    ports = (node: GraphNode) =>
      component(node.kind)?.outputs?.map((p: Entity) => p.name) ||
      (node.kind === "condition"
        ? ["true", "false"]
        : node.kind === "output"
          ? []
          : ["out"]);
  const update = (next: GraphDraft, record = true) =>
    dispatch({ type: "change", draft: next, record });
  function open(next: GraphDraft, changed = false) {
    if (dirty && !window.confirm("Discard unsaved changes?")) return;
    generation.current++;
    dispatch({
      type: "open",
      draft: {
        ...next,
        nodes: next.nodes.map((node) => ({
          ...node,
          config: node.config || {},
        })),
      },
    });
    setBaseline(changed ? "" : JSON.stringify(graphPayload(next)));
    setSelected([]);
    setValidation(null);
    setConflict(null);
  }
  useEffect(() => {
    if (!designs.data) return;
    const next = items(designs.data).find(
      (item) =>
        item.id === resource && (!project || item.project_id === project),
    );
    if (!dirty) {
      const value =
        (next as GraphDraft) ||
        blankGraph(project || items(projects.data)[0]?.id || "");
      generation.current++;
      dispatch({
        type: "open",
        draft: {
          ...value,
          nodes: value.nodes.map((node) => ({
            ...node,
            config: node.config || {},
          })),
        },
      });
      setBaseline(JSON.stringify(graphPayload(value)));
    }
  }, [resource, project, designs.data, projects.data]);
  useEffect(() => {
    const abort = new AbortController();
    Promise.all(
      Object.entries(paths).map(async ([kind, path]) => {
        if (
          (kind === "model" && !hasService(identity, "models")) ||
          (kind === "tool" && !hasService(identity, "catalog"))
        )
          return [kind, []];
        try {
          return [kind, items(await request(path, { signal: abort.signal }))];
        } catch {
          return [kind, []];
        }
      }),
    ).then((values) => {
      if (!abort.signal.aborted) setResources(Object.fromEntries(values));
    });
    return () => abort.abort();
  }, [identity.subject]);
  function remove() {
    const ids = new Set(
      selected.filter(
        (id) =>
          !["input", "output"].includes(
            draft.nodes.find((n) => n.id === id)?.kind || "",
          ),
      ),
    );
    update({
      ...draft,
      nodes: draft.nodes.filter((n) => !ids.has(n.id)),
      edges: draft.edges.filter((e) => !ids.has(e.from) && !ids.has(e.to)),
    });
    setSelected([]);
  }
  function copy() {
    const ids = new Set(selected),
      nodes = draft.nodes.filter(
        (n) => ids.has(n.id) && !["input", "output"].includes(n.kind),
      ),
      kept = new Set(nodes.map((n) => n.id));
    copied.current = {
      nodes: structuredClone(nodes),
      edges: structuredClone(
        draft.edges.filter((e) => kept.has(e.from) && kept.has(e.to)),
      ),
    };
    setMessage(`Copied ${nodes.length} nodes.`);
  }
  function paste() {
    const clip = copied.current;
    if (!clip) return;
    if (draft.nodes.length + clip.nodes.length > 100) {
      setError("A design can have at most 100 nodes.");
      return;
    }
    const ids = new Map(
      clip.nodes.map((n) => [
        n.id,
        `${n.kind}-${crypto.randomUUID().slice(0, 8)}`,
      ]),
    );
    const nodes = clip.nodes.map((n) => ({
      ...structuredClone(n),
      id: ids.get(n.id)!,
      x: n.x + 40,
      y: n.y + 40,
    }));
    update({
      ...draft,
      nodes: [...draft.nodes, ...nodes],
      edges: [
        ...draft.edges,
        ...clip.edges.map((e) => ({
          ...e,
          from: ids.get(e.from)!,
          to: ids.get(e.to)!,
        })),
      ],
    });
    setSelected(nodes.map((n) => n.id));
  }
  function connect(to: string, source = connecting) {
    if (!source) return;
    const issue = connectionIssue(draft, source.id, to, source.port);
    if (issue) {
      setError(issue);
      return;
    }
    update({
      ...draft,
      edges: [...draft.edges, { from: source.id, to, from_port: source.port }],
    });
    setConnecting(null);
    setError("");
  }
  async function save() {
    if (graphIssues(draft).length) {
      setError(graphIssues(draft).join(" "));
      return;
    }
    const started = generation.current;
    const submitted = JSON.stringify(graphPayload(draft));
    setSaving(true);
    setError("");
    try {
      const saved = await request<GraphDraft>(
        draft.id
          ? `/api/v1/agent-designs/${draft.id}`
          : "/api/v1/agent-designs",
        { method: draft.id ? "PUT" : "POST", body: graphPayload(draft) },
      );
      if (generation.current !== started) return;
      const latest = current.current;
      const changed = JSON.stringify(graphPayload(latest)) !== submitted;
      dispatch({
        type: changed ? "change" : "open",
        record: false,
        draft: changed
          ? { ...latest, id: saved.id, revision: saved.revision }
          : saved,
      });
      setBaseline(JSON.stringify(graphPayload(saved)));
      designs.reload();
      const url = new URL(location.href);
      url.searchParams.set("resource", saved.id!);
      historyReplace(url);
      setMessage(`Saved revision ${saved.revision}.`);
    } catch (e) {
      if (generation.current !== started) return;
      setError(errorMessage(e));
      if (e instanceof APIError && e.status === 409)
        setConflict(e.body.latest || {});
    } finally {
      setSaving(false);
    }
  }
  async function validate() {
    setError("");
    try {
      const result = await request<Entity>(
        draft.id
          ? `/api/v1/agent-designs/${draft.id}/validate`
          : "/api/v1/agent-designs/validate",
        {
          method: "POST",
          body: {
            project_id: draft.project_id,
            nodes: graphPayload(draft).nodes,
            edges: draft.edges,
            ...(draft.settings ? { settings: draft.settings } : {}),
          },
        },
      );
      setValidation(result);
      return result;
    } catch (e) {
      setError(errorMessage(e));
      if (e instanceof APIError && e.body.issues)
        setValidation({ runnable: false, issues: e.body.issues });
    }
  }
  const selectedNode = draft.nodes.find((n) => n.id === selected[0]);
  function updateNode(patch: Partial<GraphNode>) {
    if (selectedNode)
      update({
        ...draft,
        nodes: draft.nodes.map((node) =>
          node.id === selectedNode.id ? { ...node, ...patch } : node,
        ),
      });
  }
  function add(kind: string, position?: { x: number; y: number }) {
    if (draft.nodes.length >= 100) {
      setError("A design can have at most 100 nodes.");
      return;
    }
    const spec = component(kind);
    if (spec?.singleton && draft.nodes.some((n) => n.kind === kind)) return;
    const id = `${kind}-${crypto.randomUUID().slice(0, 8)}`,
      node = {
        id,
        kind,
        name: spec?.label || kind,
        x: Math.max(0, position?.x ?? 80 + (draft.nodes.length % 4) * 240),
        y: Math.max(
          0,
          position?.y ?? 80 + Math.floor(draft.nodes.length / 4) * 150,
        ),
        config: Object.fromEntries(
          (spec?.config || [])
            .filter((f: Entity) => f.default !== undefined)
            .map((f: Entity) => [f.key, f.default]),
        ),
      };
    update({ ...draft, nodes: [...draft.nodes, node] });
    setSelected([id]);
    if (expanded && window.innerWidth < 900) setPaletteOpen(false);
  }
  return (
    <section
      id="agent-designer"
      className={expanded ? "has-expanded-workspace" : undefined}
      aria-label="Agent designer"
    >
      {context && (
        <div
          role="menu"
          aria-label="Node actions"
          className="node-context-menu"
          style={{
            left: Math.min(context.x, window.innerWidth - 230),
            top: Math.min(context.y, window.innerHeight - 210),
          }}
          onPointerDown={(event) => event.stopPropagation()}
          onKeyDown={(event) => {
            if (event.key === "Escape") setContext(null);
          }}
        >
          <button
            role="menuitem"
            autoFocus
            onClick={() => {
              setSelected([context.id]);
              setContext(null);
              requestAnimationFrame(() =>
                document.getElementById("design-node-name")?.focus(),
              );
            }}
          >
            Configure node
          </button>
          <button
            role="menuitem"
            disabled={
              !writable ||
              ["input", "output"].includes(
                draft.nodes.find((n) => n.id === context.id)?.kind || "",
              )
            }
            onClick={() => {
              const original = draft.nodes.find((n) => n.id === context.id)!;
              const clone = {
                ...structuredClone(original),
                id: `${original.kind}-${crypto.randomUUID().slice(0, 8)}`,
                name: `${original.name} copy`,
                x: original.x + 40,
                y: original.y + 40,
              };
              if (draft.nodes.length < 100) {
                update({ ...draft, nodes: [...draft.nodes, clone] });
                setSelected([clone.id]);
              }
              setContext(null);
            }}
          >
            Duplicate node
          </button>
          <button
            role="menuitem"
            disabled={!writable}
            onClick={() => {
              update({
                ...draft,
                edges: draft.edges.filter(
                  (edge) => edge.from !== context.id && edge.to !== context.id,
                ),
              });
              setContext(null);
            }}
          >
            Disconnect node
          </button>
          <button
            role="menuitem"
            disabled={
              !writable ||
              ["input", "output"].includes(
                draft.nodes.find((n) => n.id === context.id)?.kind || "",
              )
            }
            onClick={() => {
              remove();
              setContext(null);
            }}
          >
            Delete node
          </button>
        </div>
      )}
      <div className="toolbar">
        <label className="field">
          Design
          <Select
            id="design-list"
            value={draft.id || ""}
            onChange={(event) => {
              const item = items(designs.data).find(
                (d) => d.id === event.target.value,
              );
              open((item as GraphDraft) || blankGraph(project));
            }}
          >
            <option value="">New design</option>
            {items(designs.data)
              .filter((d) => !project || d.project_id === project)
              .map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name} revision {d.revision}
                </option>
              ))}
          </Select>
        </label>
        <label className="field">
          Template
          <Select
            id="design-template"
            value=""
            onChange={(event) => {
              const template = items(templates.data).find(
                (t) => t.id === event.target.value,
              );
              if (template)
                open(
                  fromGraph(template.graph, draft.project_id, template.name),
                  true,
                );
            }}
          >
            <option value="">Choose template</option>
            {items(templates.data).map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </Select>
        </label>
        <div className="button-row">
          <button
            id="design-new"
            onClick={() => open(blankGraph(project || draft.project_id))}
          >
            New design
          </button>
          <button onClick={() => file.current?.click()}>Import graph</button>
          <button
            onClick={() => {
              const blob = new Blob(
                [
                  JSON.stringify(
                    {
                      schema_version: draft.schema_version || 2,
                      nodes: graphPayload(draft).nodes,
                      edges: draft.edges,
                      settings: draft.settings,
                    },
                    null,
                    2,
                  ),
                ],
                { type: "application/json" },
              );
              const url = URL.createObjectURL(blob),
                link = document.createElement("a");
              link.href = url;
              link.download = `${draft.name || "agent"}.json`;
              link.click();
              URL.revokeObjectURL(url);
            }}
          >
            Export graph
          </button>
          <button
            id="design-save"
            className="primary"
            disabled={!writable || saving}
            onClick={() => void save()}
          >
            {saving ? "Saving…" : "Save revision"}
          </button>
        </div>
        <input
          ref={file}
          type="file"
          accept="application/json"
          hidden
          onChange={async (event) => {
            const input = event.target,
              upload = input.files?.[0];
            input.value = "";
            if (!upload) return;
            if (upload.size > 2097152) {
              setError("Graph imports can be at most 2 MiB.");
              return;
            }
            try {
              open(
                fromGraph(JSON.parse(await upload.text()), draft.project_id),
                true,
              );
            } catch (e) {
              setError(errorMessage(e));
            }
          }}
        />
      </div>
      <div id="design-message">
        <Feedback error={error} message={message} />
      </div>
      {conflict && (
        <div className="feedback error">
          <p>A newer revision is available. Your unsaved graph remains open.</p>
          <button
            onClick={() =>
              void request<GraphDraft>(
                `/api/v1/agent-designs/${draft.id}`,
              ).then((value) => open(value))
            }
          >
            Load latest revision
          </button>
        </div>
      )}
      <DataState
        loading={designs.loading && !designs.data}
        error={designs.error}
        onRetry={designs.reload}
      >
        <div className="form-grid">
          <label className="field">
            Project
            <Select
              id="design-project"
              disabled={!!draft.id || !writable}
              value={draft.project_id}
              onChange={(e) => update({ ...draft, project_id: e.target.value })}
            >
              <option value="">Choose project</option>
              {items(projects.data).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          </label>
          <label className="field">
            Name
            <input
              id="design-name"
              value={draft.name}
              disabled={!writable}
              onChange={(e) => update({ ...draft, name: e.target.value })}
            />
          </label>
          <label className="field field-wide">
            Description
            <textarea
              id="design-description"
              value={draft.description}
              disabled={!writable}
              onChange={(e) =>
                update({ ...draft, description: e.target.value })
              }
            />
          </label>
        </div>
        <p id="design-revision" role="status">
          {draft.revision ? `Revision ${draft.revision}` : "New design"}
          {dirty ? " — unsaved changes" : ""}
        </p>
        <div
          className={`agent-graph-workspace${expanded ? " designer-expanded" : ""}`}
        >
          <div className="toolbar">
            <div className="button-row">
              {expanded && (
                <>
                  <label className="expanded-name">
                    Name
                    <input
                      aria-label="Expanded design name"
                      value={draft.name}
                      onChange={(event) =>
                        update({ ...draft, name: event.target.value })
                      }
                    />
                  </label>
                  <button
                    className="primary"
                    disabled={!writable || saving}
                    onClick={() => void save()}
                  >
                    Save graph
                  </button>
                  <span role="status">{message}</span>
                  {error && (
                    <span role="alert" className="feedback error">
                      {error}
                    </span>
                  )}
                </>
              )}
              <button
                id="design-undo"
                disabled={!writable || !state.past.length}
                onClick={() => dispatch({ type: "undo" })}
              >
                Undo
              </button>
              <button
                id="design-redo"
                disabled={!writable || !state.future.length}
                onClick={() => dispatch({ type: "redo" })}
              >
                Redo
              </button>
              <button disabled={!writable || !selected.length} onClick={copy}>
                Copy nodes
              </button>
              <button disabled={!writable || !copied.current} onClick={paste}>
                Paste nodes
              </button>
              <button disabled={!writable || !selected.length} onClick={remove}>
                Delete selection
              </button>
              <button
                onClick={() => setZoom((z) => Math.max(0.3, z - 0.1))}
                aria-label="Zoom out"
              >
                −
              </button>
              <span>{Math.round(zoom * 100)}%</span>
              <button
                onClick={() => setZoom((z) => Math.min(1.5, z + 0.1))}
                aria-label="Zoom in"
              >
                +
              </button>
              <button
                onClick={() => {
                  flow.current?.fit();
                  scroller.current?.scrollIntoView({ block: "nearest" });
                }}
              >
                Fit graph
              </button>
              <button
                aria-pressed={expanded}
                onClick={() => {
                  setExpanded(!expanded);
                  if (!expanded && window.innerWidth < 900)
                    setPaletteOpen(false);
                }}
              >
                <Icon name={expanded ? "close" : "expand"} />
                {expanded ? "Exit expanded view" : "Expand workspace"}
              </button>
              <label>
                <input
                  id="design-snap"
                  type="checkbox"
                  checked={snap}
                  onChange={(e) => setSnap(e.target.checked)}
                />{" "}
                Snap to grid
              </label>
            </div>
          </div>
          <p className="graph-help">
            Drag components onto the canvas. Move nodes by their labels, connect
            output and input handles, and right-click a node to configure it.
            Drag the background to pan.
          </p>
          <div className="graph-workbench">
            <aside className="graph-palette">
              <details
                open={paletteOpen}
                onToggle={(event) => setPaletteOpen(event.currentTarget.open)}
              >
                <summary>Components</summary>
                <div className="component-picker">
                  <label className="field">
                    Find component
                    <input
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                    />
                  </label>
                  <DataState
                    loading={registry.loading}
                    error={registry.error}
                    onRetry={registry.reload}
                  >
                    {components
                      .filter((c: Entity) =>
                        `${c.label} ${c.kind} ${c.description}`
                          .toLowerCase()
                          .includes(search.toLowerCase()),
                      )
                      .map((c: Entity) => (
                        <button
                          key={c.kind}
                          data-design-add={c.kind}
                          draggable={writable}
                          onDragStart={(event) => {
                            event.dataTransfer.setData(
                              "application/kionga-component",
                              c.kind,
                            );
                            event.dataTransfer.effectAllowed = "copy";
                          }}
                          title={c.description}
                          disabled={
                            !writable ||
                            (c.singleton &&
                              draft.nodes.some((n) => n.kind === c.kind))
                          }
                          onClick={() => add(c.kind)}
                        >
                          {c.label}
                        </button>
                      ))}
                  </DataState>
                </div>
              </details>
            </aside>
            <div
              ref={scroller}
              className="graph-canvas-scroll"
              tabIndex={0}
              aria-label="Agent graph canvas"
              onKeyDown={(event) => {
                if (
                  (event.target as HTMLElement).matches("input,textarea,select")
                )
                  return;
                const meta = event.ctrlKey || event.metaKey;
                if (meta && event.key.toLowerCase() === "z") {
                  event.preventDefault();
                  dispatch({ type: event.shiftKey ? "redo" : "undo" });
                } else if (meta && event.key.toLowerCase() === "c") {
                  event.preventDefault();
                  copy();
                } else if (meta && event.key.toLowerCase() === "v") {
                  event.preventDefault();
                  if (writable) paste();
                } else if (
                  ["Delete", "Backspace"].includes(event.key) &&
                  writable
                ) {
                  event.preventDefault();
                  remove();
                } else if (event.key === "Escape") {
                  setConnecting(null);
                  setSelected([]);
                } else if (
                  ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(
                    event.key,
                  ) &&
                  writable &&
                  selected.length
                ) {
                  event.preventDefault();
                  const move = snap || event.shiftKey ? 20 : 5,
                    dx =
                      event.key === "ArrowLeft"
                        ? -move
                        : event.key === "ArrowRight"
                          ? move
                          : 0,
                    dy =
                      event.key === "ArrowUp"
                        ? -move
                        : event.key === "ArrowDown"
                          ? move
                          : 0;
                  update({
                    ...draft,
                    nodes: draft.nodes.map((n) =>
                      selected.includes(n.id)
                        ? {
                            ...n,
                            x: Math.max(
                              0,
                              snap
                                ? Math.round((n.x + dx) / 20) * 20
                                : n.x + dx,
                            ),
                            y: Math.max(
                              0,
                              snap
                                ? Math.round((n.y + dy) / 20) * 20
                                : n.y + dy,
                            ),
                          }
                        : n,
                    ),
                  });
                }
              }}
            >
              <div id="design-canvas" className="agent-flow-surface">
                <GraphCanvas
                  ref={flow}
                  label="Agent graph editor"
                  editable={writable}
                  zoom={zoom}
                  onZoom={setZoom}
                  snap={snap}
                  onSelect={setSelected}
                  onDragStart={() => update(current.current, true)}
                  onPositions={(positions) =>
                    update(
                      {
                        ...current.current,
                        nodes: current.current.nodes.map((node) =>
                          positions[node.id]
                            ? { ...node, ...positions[node.id] }
                            : node,
                        ),
                      },
                      false,
                    )
                  }
                  onAdd={add}
                  onConnect={(from, to, port) =>
                    connect(to, { id: from, port })
                  }
                  canConnect={(from, to, port) =>
                    !connectionIssue(current.current, from, to, port)
                  }
                  onContext={(event, id) => {
                    setSelected([id]);
                    setContext({ id, x: event.clientX, y: event.clientY });
                  }}
                  edges={draft.edges.map((edge, index) => ({
                    id: `${edge.from}-${edge.to}-${edge.from_port || "out"}-${index}`,
                    from: edge.from,
                    to: edge.to,
                    port: edge.from_port || "out",
                    label:
                      edge.from_port && edge.from_port !== "out"
                        ? edge.from_port
                        : undefined,
                  }))}
                  nodes={draft.nodes.map((node) => ({
                    id: node.id,
                    x: node.x,
                    y: node.y,
                    selected: selected.includes(node.id),
                    inputs: node.kind !== "input",
                    outputs: ports(node),
                    dragHandle: ".graph-node-body",
                    content: (
                      <div
                        className={`design-node design-node-card${selected.includes(node.id) ? " selected" : ""}${nodeStatus[node.id] ? ` run-${nodeStatus[node.id]}` : ""}`}
                        data-design-node={node.id}
                      >
                        {node.kind !== "input" && (
                          <button
                            className="graph-input nodrag"
                            data-design-in={node.id}
                            aria-label={`Connect to ${node.name}`}
                            disabled={!writable || !connecting}
                            onClick={() => connect(node.id)}
                          >
                            In
                          </button>
                        )}
                        <div
                          className="graph-node-body"
                          data-design-select={node.id}
                          role="button"
                          tabIndex={0}
                          aria-label={`${node.kind} ${node.name} ${node.id}`}
                          aria-pressed={selected.includes(node.id)}
                          onClick={() => setSelected([node.id])}
                          onKeyDown={(event) => {
                            if (event.key === "Enter" || event.key === " ") {
                              event.preventDefault();
                              setSelected([node.id]);
                            }
                          }}
                        >
                          <span>{node.kind}</span>
                          <strong>{node.name}</strong>
                          <code>{node.id}</code>
                        </div>
                        <div className="graph-ports">
                          {ports(node).map((port: string) => (
                            <button
                              className="nodrag"
                              key={port}
                              data-design-out={node.id}
                              data-design-port={port}
                              aria-label={`Connect ${node.name} ${port}`}
                              aria-pressed={
                                connecting?.id === node.id &&
                                connecting.port === port
                              }
                              disabled={!writable}
                              onClick={() =>
                                setConnecting({ id: node.id, port })
                              }
                            >
                              {port}
                            </button>
                          ))}
                        </div>
                      </div>
                    ),
                  }))}
                />
              </div>
            </div>
            <aside className="graph-inspector">
              {selectedNode ? (
                <>
                  <h2>Node settings</h2>
                  <label className="field">
                    Label
                    <input
                      id="design-node-name"
                      disabled={!writable}
                      value={selectedNode.name}
                      onChange={(e) => updateNode({ name: e.target.value })}
                    />
                  </label>
                  <label className="field">
                    Group
                    <input
                      disabled={!writable}
                      value={selectedNode.group || ""}
                      onChange={(e) => updateNode({ group: e.target.value })}
                    />
                  </label>
                  {(component(selectedNode.kind)?.config || []).map(
                    (field: Entity) => {
                      const value =
                          selectedNode.config?.[field.key] ??
                          field.default ??
                          "",
                        own = (resources[field.resource] || []).filter(
                          (r) =>
                            !r.project_id || r.project_id === draft.project_id,
                        ),
                        options =
                          field.resource === "model"
                            ? [
                                { id: "llm", name: "Platform chat model" },
                                ...own,
                              ]
                            : own;
                      return (
                        <label className="field" key={field.key}>
                          {field.label}
                          {field.type === "enum" ? (
                            <Select
                              disabled={!writable}
                              value={String(value)}
                              onChange={(e) =>
                                updateNode({
                                  config: {
                                    ...(selectedNode.config || {}),
                                    [field.key]: e.target.value,
                                  },
                                })
                              }
                            >
                              {(field.options || []).map((option: string) => (
                                <option key={option}>{option}</option>
                              ))}
                            </Select>
                          ) : field.type === "resource" ? (
                            <Select
                              disabled={!writable}
                              value={String(value)}
                              onChange={(e) =>
                                updateNode({
                                  config: {
                                    ...(selectedNode.config || {}),
                                    [field.key]: e.target.value,
                                  },
                                })
                              }
                            >
                              <option value="">Choose resource</option>
                              {options.map((r) => (
                                <option
                                  key={r.id || r.name}
                                  value={
                                    field.resource === "tool" ? r.name : r.id
                                  }
                                >
                                  {r.name || r.design_name || r.id}
                                  {r.version ? ` v${r.version}` : ""}
                                </option>
                              ))}
                              {value &&
                                !options.some(
                                  (r) => (r.id || r.name) === value,
                                ) && (
                                  <option value={String(value)}>
                                    {String(value)} (unavailable)
                                  </option>
                                )}
                            </Select>
                          ) : ["template", "expression", "json"].includes(
                              field.type,
                            ) ? (
                            <CodeEditor
                              disabled={!writable}
                              label={field.label}
                              language={field.type === "json" ? "json" : "text"}
                              value={String(value)}
                              onChange={(event) =>
                                updateNode({
                                  config: {
                                    ...(selectedNode.config || {}),
                                    [field.key]: event.target.value,
                                  },
                                })
                              }
                            />
                          ) : (
                            <input
                              disabled={!writable}
                              type={
                                field.type === "integer" ? "number" : "text"
                              }
                              min={field.minimum}
                              max={field.maximum}
                              value={String(value)}
                              onChange={(e) =>
                                updateNode({
                                  config: {
                                    ...(selectedNode.config || {}),
                                    [field.key]: e.target.value,
                                  },
                                })
                              }
                            />
                          )}
                          <small>{field.help}</small>
                        </label>
                      );
                    },
                  )}
                  <h3>Connections</h3>
                  {draft.edges
                    .filter(
                      (e) =>
                        e.from === selectedNode.id || e.to === selectedNode.id,
                    )
                    .map((edge, i) => (
                      <p key={i}>
                        {edge.from} {edge.from_port || "out"} → {edge.to}
                        <button
                          disabled={!writable}
                          aria-label={`Remove connection ${edge.from} to ${edge.to}`}
                          onClick={() =>
                            update({
                              ...draft,
                              edges: draft.edges.filter((e) => e !== edge),
                            })
                          }
                        >
                          Remove
                        </button>
                      </p>
                    ))}
                  <button
                    disabled={
                      !writable ||
                      ["input", "output"].includes(selectedNode.kind)
                    }
                    onClick={remove}
                  >
                    Remove node
                  </button>
                </>
              ) : (
                <p>Select a node to configure its resources and connections.</p>
              )}
            </aside>
          </div>
        </div>
        {!!draft.history?.length && (
          <details>
            <summary>Revision history</summary>
            <ul>
              {draft.history
                .slice()
                .reverse()
                .map((revision: Entity) => (
                  <li key={revision.revision}>
                    Revision {revision.revision}
                    <button
                      data-design-restore={revision.revision}
                      disabled={!writable}
                      onClick={() => {
                        if (
                          !window.confirm(
                            `Restore revision ${revision.revision} as unsaved changes?`,
                          )
                        )
                          return;
                        update({
                          ...draft,
                          name: revision.name,
                          description: revision.description || "",
                          nodes: structuredClone(revision.nodes),
                          edges: structuredClone(revision.edges),
                          settings: revision.settings,
                        });
                        setMessage(
                          `Revision ${revision.revision} restored. Save to create a new revision.`,
                        );
                      }}
                    >
                      Restore
                    </button>
                  </li>
                ))}
            </ul>
          </details>
        )}
        <p id="design-graph-summary" role="status">
          {draft.nodes.length} nodes / {draft.edges.length} connections /{" "}
          {graphIssues(draft).join(" ") || "Ready to save"}
        </p>
        <DesignStudio
          draft={draft}
          dirty={dirty}
          writable={writable}
          validation={validation}
          onValidate={validate}
          onIssueNode={(id) => setSelected([id])}
          onSettingsChange={(settings) => update({ ...draft, settings })}
          onNodeStatus={(id, status) =>
            setNodeStatus((old) => (id ? { ...old, [id]: status } : {}))
          }
        />
      </DataState>
    </section>
  );
}
function historyReplace(url: URL) {
  window.history.replaceState(null, "", url);
}
