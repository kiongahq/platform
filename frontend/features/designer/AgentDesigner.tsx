"use client";
import {Select} from "@/components/ui/Select";
import {
  useEffect,
  useReducer,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { request, APIError, errorMessage } from "@/lib/api";
import { useQuery } from "@/hooks/useQuery";
import { useUnsaved } from "@/hooks/useUnsaved";
import { items, hasService, type Entity } from "@/lib/types";
import {
  DataState,
  Feedback,
  Detail,
  type ViewProps,
} from "@/features/shared/View";
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
  useEffect(() => { if (window.matchMedia) setPaletteOpen(!window.matchMedia("(max-width: 640px)").matches); }, []);
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
  const width = Math.max(1200, ...draft.nodes.map((n) => n.x + 300)),
    height = Math.max(650, ...draft.nodes.map((n) => n.y + 200));
  const update = (next: GraphDraft, record = true) =>
    dispatch({ type: "change", draft: next, record });
  function open(next: GraphDraft, changed = false) {
    if (dirty && !window.confirm("Discard unsaved changes?")) return;
    generation.current++;
    dispatch({ type: "open", draft: { ...next, nodes: next.nodes.map(node => ({ ...node, config: node.config || {} })) } });
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
      dispatch({ type: "open", draft: { ...value, nodes: value.nodes.map(node => ({ ...node, config: node.config || {} })) } });
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
  function connect(to: string) {
    if (!connecting) return;
    const issue = connectionIssue(draft, connecting.id, to, connecting.port);
    if (issue) {
      setError(issue);
      return;
    }
    update({
      ...draft,
      edges: [
        ...draft.edges,
        { from: connecting.id, to, from_port: connecting.port },
      ],
    });
    setConnecting(null);
    setError("");
  }
  function drag(event: ReactPointerEvent<HTMLButtonElement>, node: GraphNode) {
    if (!writable || event.button !== 0) return;
    if (event.shiftKey) {
      setSelected((old) =>
        old.includes(node.id)
          ? old.filter((id) => id !== node.id)
          : [...old, node.id],
      );
      return;
    }
    const ids = selected.includes(node.id) ? selected : [node.id];
    setSelected(ids);
    const start = structuredClone(draft),
      x = event.clientX,
      y = event.clientY,
      element = event.currentTarget;
    let recorded = false;
    element.setPointerCapture(event.pointerId);
    const move = (e: PointerEvent) => {
      const dx = (e.clientX - x) / zoom,
        dy = (e.clientY - y) / zoom;
      if (Math.abs(dx) + Math.abs(dy) < 3) return;
      const next = {
        ...start,
        nodes: start.nodes.map((n) =>
          !ids.includes(n.id)
            ? n
            : {
                ...n,
                x: Math.max(
                  0,
                  snap ? Math.round((n.x + dx) / 20) * 20 : n.x + dx,
                ),
                y: Math.max(
                  0,
                  snap ? Math.round((n.y + dy) / 20) * 20 : n.y + dy,
                ),
              },
        ),
      };
      update(next, !recorded);
      recorded = true;
    };
    const end = () => {
      element.removeEventListener("pointermove", move);
      element.removeEventListener("pointerup", end);
      element.removeEventListener("pointercancel", end);
    };
    element.addEventListener("pointermove", move);
    element.addEventListener("pointerup", end);
    element.addEventListener("pointercancel", end);
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
      dispatch({ type: changed ? "change" : "open", record: false, draft: changed ? { ...latest, id: saved.id, revision: saved.revision } : saved });
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
  function add(kind: string) {
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
        x: 80 + (draft.nodes.length % 4) * 240,
        y: 80 + Math.floor(draft.nodes.length / 4) * 150,
        config: Object.fromEntries(
          (spec?.config || [])
            .filter((f: Entity) => f.default !== undefined)
            .map((f: Entity) => [f.key, f.default]),
        ),
      };
    update({ ...draft, nodes: [...draft.nodes, node] });
    setSelected([id]);
  }
  return (
    <section id="agent-designer" aria-label="Agent designer">
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
        <div className="toolbar">
          <div className="button-row">
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
                setZoom(
                  Math.min(1, (scroller.current?.clientWidth || 800) / width),
                );
                scroller.current?.scrollTo(0, 0);
              }}
            >
              Fit graph
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
        <div className="graph-workbench">
          <aside className="graph-palette"><details open={paletteOpen} onToggle={event => setPaletteOpen(event.currentTarget.open)}><summary>Components</summary><div className="component-picker">
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
          </div></details></aside>
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
                            snap ? Math.round((n.x + dx) / 20) * 20 : n.x + dx,
                          ),
                          y: Math.max(
                            0,
                            snap ? Math.round((n.y + dy) / 20) * 20 : n.y + dy,
                          ),
                        }
                      : n,
                  ),
                });
              }
            }}
          >
            <div
              id="design-canvas"
              className="graph-canvas"
              style={{ width: width * zoom, height: height * zoom }}
            >
              <div
                style={{
                  width,
                  height,
                  transform: `scale(${zoom})`,
                  transformOrigin: "top left",
                  position: "relative",
                }}
              >
                <svg
                  className="graph-edges"
                  width={width}
                  height={height}
                  role="img"
                  aria-label="Agent graph connections"
                >
                  <defs>
                    <marker
                      id="graph-arrow"
                      markerWidth="8"
                      markerHeight="8"
                      refX="7"
                      refY="4"
                      orient="auto"
                    >
                      <path d="M0 0L8 4L0 8Z" />
                    </marker>
                  </defs>
                  {draft.edges.map((edge, index) => {
                    const a = draft.nodes.find((n) => n.id === edge.from),
                      b = draft.nodes.find((n) => n.id === edge.to);
                    if (!a || !b) return null;
                    const x = a.x + 190,
                      y = a.y + 48,
                      endX = b.x,
                      endY = b.y + 48;
                    return (
                      <path
                        key={`${edge.from}-${edge.to}-${edge.from_port}-${index}`}
                        className="design-edge"
                        d={`M${x},${y} C${x + 70},${y} ${endX - 70},${endY} ${endX},${endY}`}
                        fill="none"
                        markerEnd="url(#graph-arrow)"
                      >
                        <title>
                          {a.name} {edge.from_port || "out"} to {b.name}
                        </title>
                      </path>
                    );
                  })}
                </svg>
                {draft.nodes.map((node) => (
                  <div
                    key={node.id}
                    className={`graph-node design-node${selected.includes(node.id) ? " selected" : ""}${nodeStatus[node.id] ? ` run-${nodeStatus[node.id]}` : ""}`}
                    data-design-node={node.id}
                    style={{
                      transform: `translate(${node.x}px,${node.y}px)`,
                      minHeight: Math.max(100, ports(node).length * 27 + 40),
                    }}
                  >
                    {node.kind !== "input" && (
                      <button
                        className="graph-input"
                        data-design-in={node.id}
                        aria-label={`Connect to ${node.name}`}
                        disabled={!writable || !connecting}
                        onClick={() => connect(node.id)}
                      >
                        In
                      </button>
                    )}
                    <button
                      className="graph-node-body"
                      data-design-select={node.id}
                      aria-pressed={selected.includes(node.id)}
                      onPointerDown={(event) => drag(event, node)}
                      onClick={() => {
                        if (!selected.includes(node.id)) setSelected([node.id]);
                      }}
                    >
                      <span>{node.kind}</span>
                      <strong>{node.name}</strong>
                      <code>{node.id}</code>
                    </button>
                    <div className="graph-ports">
                      {ports(node).map((port: string) => (
                        <button
                          key={port}
                          data-design-out={node.id}
                          data-design-port={port}
                          aria-label={`Connect ${node.name} ${port}`}
                          aria-pressed={
                            connecting?.id === node.id &&
                            connecting.port === port
                          }
                          disabled={!writable}
                          onClick={() => setConnecting({ id: node.id, port })}
                        >
                          {port}
                        </button>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
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
                        selectedNode.config?.[field.key] ?? field.default ?? "",
                      own = (resources[field.resource] || []).filter(
                        (r) =>
                          !r.project_id || r.project_id === draft.project_id,
                      ),
                      options =
                        field.resource === "model"
                          ? [{ id: "llm", name: "Platform chat model" }, ...own]
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
                        ) : field.type === "template" ? (
                          <textarea
                            disabled={!writable}
                            rows={5}
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
                        ) : (
                          <input
                            disabled={!writable}
                            type={field.type === "integer" ? "number" : "text"}
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
                    !writable || ["input", "output"].includes(selectedNode.kind)
                  }
                  onClick={remove}
                >
                  Remove node
                </button>
              </>
            ) : (
              <p>Select a node to configure its resources and connections.</p>
            )}
            <svg
              className="graph-minimap"
              viewBox={`0 0 ${width} ${height}`}
              role="img"
              aria-label="Graph overview"
              onClick={(event) => {
                const box = event.currentTarget.getBoundingClientRect();
                scroller.current?.scrollTo({
                  left:
                    ((event.clientX - box.left) / box.width) * width * zoom -
                    200,
                  top:
                    ((event.clientY - box.top) / box.height) * height * zoom -
                    150,
                });
              }}
            >
              {draft.nodes.map((node) => (
                <rect
                  key={node.id}
                  x={node.x}
                  y={node.y}
                  width={190}
                  height={100}
                  className={selected.includes(node.id) ? "selected" : ""}
                />
              ))}
            </svg>
            <Detail
              value={{
                nodes: draft.nodes.length,
                connections: draft.edges.length,
              }}
            />
          </aside>
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
