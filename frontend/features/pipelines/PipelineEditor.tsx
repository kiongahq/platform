"use client";
import { ExecutionGraph } from "@/features/operations/ExecutionGraph";
import { DownloadButton } from "@/components/ui/DownloadButton";
import { Select } from "@/components/ui/Select";
import { CodeEditor } from "@/components/ui/CodeEditor";
import { CodeBlock } from "@/components/ui/CodeBlock";
import React, { useCallback, useEffect, useRef, useState } from "react";
import { request, errorMessage, APIError } from "@/lib/api";
import { useQuery } from "@/hooks/useQuery";
import { useUnsaved } from "@/hooks/useUnsaved";
import { items, hasService, type Entity } from "@/lib/types";
import {
  Feedback,
  DataState,
  ResourceTable,
  type ViewProps,
} from "@/features/shared/View";

type Job = Entity & {
  name: string;
  kind: string;
  depends_on: string[];
  resources: Entity;
  command?: string[];
  image?: string;
  function?: string;
  retries?: number;
  timeout_seconds?: number;
};
type Definition = Entity & {
  project_id: string;
  name: string;
  version: string;
  execution_mode: string;
  description: string;
  jobs: Job[];
  parameters?: Entity;
  triggers?: Entity[];
  overridable?: Entity;
};
type Issue = { message: string; node?: string; line?: number; path?: string };
type Validation = {
  valid: boolean;
  request: Definition;
  yaml: string;
  sha256?: string;
  layers?: string[][];
  issues: Issue[];
};
const blankNode = (index: number): Job => ({
  name: index === 0 ? "extract" : `step-${index + 1}`,
  kind: "container",
  image: "python:3.11-slim",
  command: [],
  depends_on: [],
  resources: { cpu: "500m", memory: "1Gi", gpu: 0 },
  retries: 0,
  timeout_seconds: 0,
});
export function splitPipelineCommand(text: string): string[] {
  const parts: string[] = [];
  let current = "",
    quote = "",
    escaped = false,
    started = false;
  for (const char of text) {
    if (escaped) {
      current += char;
      escaped = false;
      started = true;
    } else if (char === "\\" && quote !== "'") {
      escaped = true;
      started = true;
    } else if (quote) {
      if (char === quote) quote = "";
      else current += char;
      started = true;
    } else if (char === '"' || char === "'") {
      quote = char;
      started = true;
    } else if (/\s/.test(char)) {
      if (started) {
        parts.push(current);
        current = "";
        started = false;
      }
    } else {
      current += char;
      started = true;
    }
  }
  if (quote) throw new Error("Close the quoted command argument.");
  if (escaped) current += "\\";
  if (started) parts.push(current);
  return parts;
}
const commandText = (command?: string[]) =>
  (command || [])
    .map((value) => (/\s|["'\\]/.test(value) ? JSON.stringify(value) : value))
    .join(" ");
function defaultDefinition(project: string): Definition {
  return {
    project_id: project,
    name: "event-pipeline",
    version: "1",
    execution_mode: "prefect",
    description: "",
    jobs: [
      { ...blankNode(0), command: ["python", "-c", "print('extract')"] },
      {
        ...blankNode(1),
        name: "train",
        depends_on: ["extract"],
        command: ["python", "-c", "print('train')"],
      },
    ],
    parameters: {},
    triggers: [],
    overridable: { parameters: [] },
  };
}
export function pipelineLineDiff(before: string, after: string) {
  const a = before.split("\n"),
    b = after.split("\n"),
    table = Array.from({ length: a.length + 1 }, () =>
      Array<number>(b.length + 1).fill(0),
    );
  for (let i = a.length - 1; i >= 0; i--)
    for (let j = b.length - 1; j >= 0; j--)
      table[i][j] =
        a[i] === b[j]
          ? table[i + 1][j + 1] + 1
          : Math.max(table[i + 1][j], table[i][j + 1]);
  const out: { type: "same" | "del" | "add"; text: string }[] = [];
  let i = 0,
    j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ type: "same", text: a[i] });
      i++;
      j++;
    } else if (table[i + 1][j] >= table[i][j + 1])
      out.push({ type: "del", text: a[i++] });
    else out.push({ type: "add", text: b[j++] });
  }
  while (i < a.length) out.push({ type: "del", text: a[i++] });
  while (j < b.length) out.push({ type: "add", text: b[j++] });
  return out;
}
function DefinitionGraph({
  jobs,
  onSelect,
  selected,
}: {
  jobs: Job[];
  onSelect: (job: Job) => void;
  selected?: string;
}) {
  return (
    <div className="pipeline-graph">
      <ExecutionGraph
        jobs={jobs}
        selected={selected}
        onSelect={(name) => {
          const job = jobs.find((item) => item.name === name);
          if (job) onSelect(job);
        }}
      />
    </div>
  );
}

export function PipelineEditor({
  identity,
  project,
  resource,
  onNavigate,
}: ViewProps) {
  const definitionQuery = useQuery<Definition>(
      resource
        ? `/api/v1/pipelines/definitions/${encodeURIComponent(resource)}`
        : null,
    ),
    projects = useQuery<Entity>("/api/v1/project-options"),
    functions = useQuery<Entity>(
      hasService(identity, "functions") ? "/api/v1/functions" : null,
    ),
    revisions = useQuery<Entity>(
      resource
        ? `/api/v1/pipelines/definitions/${encodeURIComponent(resource)}/revisions`
        : null,
    );
  const [dirty, setDirty] = useState(false);
  useUnsaved(dirty);
  const [definition, setDefinition] = useState<Definition>(() =>
      defaultDefinition(project),
    ),
    [mode, setMode] = useState<"form" | "yaml">("form"),
    [yaml, setYaml] = useState(""),
    [parameters, setParameters] = useState("{}"),
    [overridable, setOverridable] = useState(""),
    [commandDrafts, setCommandDrafts] = useState<Record<number, string>>({}),
    [message, setMessage] = useState(""),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false),
    [validation, setValidation] = useState<Validation | null>(null),
    [selectedNode, setSelectedNode] = useState<Job | null>(null),
    [diff, setDiff] = useState<{
      revision: number;
      parts: ReturnType<typeof pipelineLineDiff>;
    } | null>(null);
  const yamlRef = useRef<HTMLTextAreaElement>(null),
    sequence = useRef(0),
    loaded = useRef("");
  const writable = identity.permissions.pipelines_write === true;
  const adopt = useCallback((value: Definition) => {
    setDefinition({
      ...value,
      jobs: value.jobs?.length
        ? value.jobs.map((job) => ({
            ...job,
            kind: job.kind || "container",
            depends_on: job.depends_on || [],
            resources: job.resources || {},
            command: job.command || [],
          }))
        : [],
      description: value.description || "",
      execution_mode: value.execution_mode || "prefect",
      version: String(value.version || "1"),
    });
    setParameters(JSON.stringify(value.parameters || {}, null, 2));
    setOverridable((value.overridable?.parameters || []).join(", "));
    setCommandDrafts({});
    setDirty(false);
  }, []);
  useEffect(() => {
    if (resource && definitionQuery.data && loaded.current !== resource) {
      loaded.current = resource;
      adopt(definitionQuery.data);
    } else if (!resource && loaded.current !== "new") {
      loaded.current = "new";
      adopt(defaultDefinition(project || items(projects.data)[0]?.id || ""));
    }
  }, [resource, definitionQuery.data, projects.data, project, adopt]);
  useEffect(() => {
    if (!definition.project_id && !resource && items(projects.data).length)
      setDefinition((current) => ({
        ...current,
        project_id: project || items(projects.data)[0].id,
      }));
  }, [projects.data, project, resource, definition.project_id]);
  function buildRequest(): Definition {
    let parsed;
    try {
      parsed = JSON.parse(parameters || "{}");
    } catch {
      throw new Error("Default parameters must be a JSON object.");
    }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
      throw new Error("Default parameters must be a JSON object.");
    return {
      ...definition,
      jobs: definition.jobs.map((job, index) => {
        const next = { ...job, resources: { ...job.resources } };
        if (next.kind === "function") {
          delete next.image;
          delete next.command;
        } else {
          delete next.function;
          next.command =
            commandDrafts[index] === undefined
              ? job.command || []
              : splitPipelineCommand(commandDrafts[index]);
        }
        return next;
      }),
      parameters: parsed,
      overridable: {
        ...definition.overridable,
        parameters: overridable
          .split(",")
          .map((value) => value.trim())
          .filter(Boolean),
      },
    };
  }
  const validate = useCallback(async (): Promise<Validation | null> => {
    const order = ++sequence.current;
    try {
      const body = mode === "yaml" ? { yaml } : { request: buildRequest() };
      const result = await request<Validation>("/api/v1/pipelines/validate", {
        method: "POST",
        body,
      });
      if (order === sequence.current) {
        setValidation(result);
        setError("");
      }
      return result;
    } catch (failure) {
      if (order === sequence.current) {
        setError(errorMessage(failure));
        setValidation(null);
      }
      return null;
    }
  }, [mode, yaml, definition, parameters, overridable, commandDrafts]);
  useEffect(() => {
    if (resource && !definitionQuery.data) return;
    const timer = setTimeout(() => void validate(), 300);
    return () => clearTimeout(timer);
  }, [validate, resource, definitionQuery.data]);
  function patch(patch: Partial<Definition>) {
    setDefinition((current) => ({ ...current, ...patch }));
  }
  function patchJob(index: number, patch: Partial<Job>) {
    setDefinition((current) => ({
      ...current,
      jobs: current.jobs.map((job, i) =>
        i === index ? { ...job, ...patch } : job,
      ),
    }));
  }
  async function switchMode(next: "form" | "yaml") {
    if (next === mode) return;
    const result = await validate();
    if (!result) return;
    if (next === "yaml") {
      if (!result.yaml) {
        setError("Fix the form before opening YAML.");
        return;
      }
      setYaml(result.yaml);
    } else {
      if (
        (result.issues || []).some((issue) =>
          ["", "apiVersion", "kind"].includes(issue.path || ""),
        )
      ) {
        setError("Fix the YAML syntax before switching to the form.");
        return;
      }
      adopt(result.request);
    }
    setMode(next);
  }
  async function save(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setNotice("");
    setError("");
    try {
      const result = await validate();
      if (!result?.valid)
        throw new Error("Fix the highlighted issues before saving.");
      const id = resource;
      const saved = await request<Entity>(
        id
          ? `/api/v1/pipelines/definitions/${encodeURIComponent(id)}/yaml`
          : "/api/v1/pipelines/definitions/yaml",
        {
          method: id ? "PUT" : "POST",
          body: { yaml: mode === "yaml" ? yaml : result.yaml, message },
        },
      );
      setNotice(`Saved as revision ${saved.revision || 1}.`);
      loaded.current = "";
      definitionQuery.reload();
      revisions.reload();
      setDirty(false);
      if (!resource) {
        const url = new URL(location.href);
        url.searchParams.set("resource", saved.id);
        history.replaceState(null, "", url);
      }
    } catch (failure) {
      setError(errorMessage(failure));
      if (failure instanceof APIError && Array.isArray(failure.body.details))
        setValidation((current) => ({
          ...(current || { request: definition, yaml: "", layers: [] }),
          valid: false,
          issues: failure.body.details,
        }));
    } finally {
      setBusy(false);
    }
  }
  const graphJobs = validation?.request?.jobs || definition.jobs;
  const schedules = (definition.triggers || [])
    .map(
      (trigger, index) => ({ ...trigger, index }) as Entity & { index: number },
    )
    .filter((trigger) => trigger.type === "schedule");
  return (
    <>
      <header className="page-header">
        <div>
          <h2>{resource ? "Edit flow" : "Compose a reusable flow"}</h2>
          <p>Define jobs, dependencies and execution settings.</p>
        </div>
        {!resource && <button type="button" onClick={() => {
          if (dirty && !confirm("Start over with an empty pipeline? Unsaved changes will be discarded.")) return;
          adopt({ ...defaultDefinition(definition.project_id), name: "new-pipeline", jobs: [] });
          setDirty(true);
        }}>Start from scratch</button>}
        <button type="button" onClick={() => onNavigate("pipelines")}>
          Back to pipelines
        </button>
      </header>
      <DataState
        loading={!!resource && definitionQuery.loading}
        error={definitionQuery.error || projects.error}
        onRetry={() => {
          definitionQuery.reload();
          projects.reload();
        }}
      >
        <form
          id="pipeline-definition-form"
          onSubmit={save}
          onInput={() => setDirty(true)}
          onChange={() => setDirty(true)}
        >
          <div className="toolbar">
            <div className="tabs" role="tablist" aria-label="Definition editor">
              <button
                type="button"
                role="tab"
                aria-selected={mode === "form"}
                aria-controls="definition-form-panel"
                id="definition-tab-form"
                onClick={() => void switchMode("form")}
              >
                Form
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={mode === "yaml"}
                aria-controls="definition-yaml-panel"
                id="definition-tab-yaml"
                onClick={() => void switchMode("yaml")}
              >
                YAML
              </button>
            </div>
            <span id="pipeline-preview-status" role="status">
              {validation
                ? validation.valid
                  ? `${validation.request.jobs?.length || 0} nodes, ${validation.layers?.length || 0} stages, valid`
                  : `${validation.issues?.length || 0} issues`
                : "Validating…"}
            </span>
            <button type="button" onClick={() => void validate()}>
              Validate
            </button>
            <DownloadButton
              filename={`${definition.name || "pipeline"}.kionga.yaml`}
              content={mode === "yaml" ? yaml : validation?.yaml || ""}
              language="yaml"
            >
              Download YAML
            </DownloadButton>
          </div>
          <div
            id="definition-form-panel"
            role="tabpanel"
            aria-labelledby="definition-tab-form"
            hidden={mode !== "form"}
          >
            <fieldset
              disabled={!writable || busy || mode === "yaml"}
              className="panel"
            >
              <legend>Flow</legend>
              <div className="form-grid">
                <label>
                  Project
                  <Select
                    name="project_id"
                    required
                    value={definition.project_id}
                    onChange={(e) => patch({ project_id: e.target.value })}
                  >
                    <option value="">Choose a project</option>
                    {items(projects.data).map((item) => (
                      <option value={item.id} key={item.id}>
                        {item.name}
                      </option>
                    ))}
                  </Select>
                </label>
                <label>
                  Name
                  <input
                    name="name"
                    required
                    value={definition.name}
                    onChange={(e) => patch({ name: e.target.value })}
                  />
                </label>
                <label>
                  Version
                  <input
                    name="version"
                    required
                    value={definition.version}
                    onChange={(e) => patch({ version: e.target.value })}
                  />
                </label>
                <label>
                  Execution
                  <Select
                    name="execution_mode"
                    value={definition.execution_mode}
                    onChange={(e) => patch({ execution_mode: e.target.value })}
                  >
                    <option value="prefect">
                      Containers / pipeline runner
                    </option>
                    <option value="functions">Functions / OpenFaaS</option>
                  </Select>
                </label>
                <label className="field-wide">
                  Description
                  <textarea
                    name="description"
                    value={definition.description}
                    onChange={(e) => patch({ description: e.target.value })}
                  />
                </label>
              </div>
            </fieldset>
            <section className="panel">
              <header className="toolbar">
                <h2>Jobs</h2>
                <button
                  type="button"
                  id="definition-add-node"
                  disabled={!writable || busy}
                  onClick={() => {
                    const job = blankNode(definition.jobs.length);
                    job.depends_on = definition.jobs.length
                      ? [definition.jobs[definition.jobs.length - 1].name]
                      : [];
                    patch({ jobs: [...definition.jobs, job] });
                  }}
                >
                  Add node
                </button>
              </header>
              <div id="definition-nodes">
                {definition.jobs.map((job, index) => (
                  <fieldset
                    key={index}
                    disabled={!writable || busy || mode === "yaml"}
                    className={`node-editor${validation?.issues?.some((issue) => issue.node === job.name) ? " has-issue" : ""}`}
                    data-node-index={index}
                  >
                    <legend>Node {index + 1}</legend>
                    <div className="toolbar">
                      <strong>{job.name || "Unnamed node"}</strong>
                      <button
                        type="button"
                        className="danger"
                        data-remove-node={index}
                        disabled={definition.jobs.length === 1}
                        onClick={() => {
                          const removed = job.name;
                          patch({
                            jobs: definition.jobs
                              .filter((_, i) => i !== index)
                              .map((node) => ({
                                ...node,
                                depends_on: node.depends_on.filter(
                                  (name) => name !== removed,
                                ),
                              })),
                          });
                          setCommandDrafts((current) =>
                            Object.fromEntries(
                              Object.entries(current)
                                .filter(([key]) => Number(key) !== index)
                                .map(([key, value]) => [
                                  Number(key) > index
                                    ? Number(key) - 1
                                    : Number(key),
                                  value,
                                ]),
                            ),
                          );
                        }}
                      >
                        Remove
                      </button>
                    </div>
                    <div className="form-grid">
                      <label>
                        Node ID
                        <input
                          data-field="name"
                          required
                          pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?"
                          value={job.name}
                          onChange={(e) => {
                            const name = e.target.value;
                            patch({
                              jobs: definition.jobs.map((node, i) =>
                                i === index
                                  ? { ...node, name }
                                  : {
                                      ...node,
                                      depends_on: node.depends_on.map((dep) =>
                                        dep === job.name ? name : dep,
                                      ),
                                    },
                              ),
                            });
                          }}
                        />
                      </label>
                      <label>
                        Type
                        <Select
                          data-field="kind"
                          value={job.kind}
                          onChange={(e) =>
                            patchJob(index, {
                              kind: e.target.value,
                              ...(e.target.value === "function" && !job.function
                                ? {
                                    function:
                                      items(functions.data)[0]?.name || "",
                                  }
                                : {}),
                            })
                          }
                        >
                          <option value="container">Container</option>
                          <option value="function">Function</option>
                        </Select>
                      </label>
                      {job.kind === "function" ? (
                        <label>
                          Function
                          <Select
                            data-field="function"
                            value={job.function || ""}
                            onChange={(e) =>
                              patchJob(index, { function: e.target.value })
                            }
                          >
                            <option value="">Choose a function</option>
                            {job.function &&
                              !items(functions.data).some(
                                (fn) => fn.name === job.function,
                              ) && (
                                <option value={job.function}>
                                  {job.function}
                                </option>
                              )}
                            {items(functions.data).map((fn) => (
                              <option key={fn.name} value={fn.name}>
                                {fn.name}
                              </option>
                            ))}
                          </Select>
                        </label>
                      ) : (
                        <>
                          <label>
                            Image
                            <input
                              data-field="image"
                              value={job.image || ""}
                              onChange={(e) =>
                                patchJob(index, { image: e.target.value })
                              }
                            />
                          </label>
                          <label>
                            Command
                            <input
                              data-field="command"
                              value={
                                commandDrafts[index] ?? commandText(job.command)
                              }
                              onChange={(e) =>
                                setCommandDrafts((current) => ({
                                  ...current,
                                  [index]: e.target.value,
                                }))
                              }
                            />
                            <small>Quote arguments containing spaces.</small>
                          </label>
                        </>
                      )}
                      <label>
                        CPU
                        <input
                          data-field="cpu"
                          value={job.resources?.cpu || ""}
                          onChange={(e) =>
                            patchJob(index, {
                              resources: {
                                ...job.resources,
                                cpu: e.target.value,
                              },
                            })
                          }
                        />
                      </label>
                      <label>
                        Memory
                        <input
                          data-field="memory"
                          value={job.resources?.memory || ""}
                          onChange={(e) =>
                            patchJob(index, {
                              resources: {
                                ...job.resources,
                                memory: e.target.value,
                              },
                            })
                          }
                        />
                      </label>
                      <label>
                        GPUs
                        <input
                          data-field="gpu"
                          type="number"
                          min={0}
                          value={job.resources?.gpu || 0}
                          onChange={(e) =>
                            patchJob(index, {
                              resources: {
                                ...job.resources,
                                gpu: Number(e.target.value),
                              },
                            })
                          }
                        />
                      </label>
                      <label>
                        Retries
                        <input
                          data-field="retries"
                          type="number"
                          min={0}
                          max={10}
                          value={job.retries || 0}
                          onChange={(e) =>
                            patchJob(index, { retries: Number(e.target.value) })
                          }
                        />
                      </label>
                      <label>
                        Retry backoff (seconds)
                        <input
                          type="number"
                          min={0}
                          value={job.retry_backoff_seconds || 0}
                          onChange={(e) =>
                            patchJob(index, {
                              retry_backoff_seconds: Number(e.target.value),
                            })
                          }
                        />
                      </label>
                      <label>
                        Timeout (seconds)
                        <input
                          data-field="timeout_seconds"
                          type="number"
                          min={0}
                          max={86400}
                          value={job.timeout_seconds || 0}
                          onChange={(e) =>
                            patchJob(index, {
                              timeout_seconds: Number(e.target.value),
                            })
                          }
                        />
                        <small>0 uses the one-hour default.</small>
                      </label>
                    </div>
                    <fieldset>
                      <legend>Runs after</legend>
                      {definition.jobs
                        .filter((_, i) => i !== index)
                        .map((other) => (
                          <label className="inline-check" key={other.name}>
                            <input
                              type="checkbox"
                              data-dep={other.name}
                              checked={job.depends_on.includes(other.name)}
                              onChange={(e) =>
                                patchJob(index, {
                                  depends_on: e.target.checked
                                    ? [...job.depends_on, other.name]
                                    : job.depends_on.filter(
                                        (name) => name !== other.name,
                                      ),
                                })
                              }
                            />
                            {other.name || "Unnamed node"}
                          </label>
                        ))}
                      {definition.jobs.length === 1 && (
                        <p>No other nodes. This job starts immediately.</p>
                      )}
                    </fieldset>
                  </fieldset>
                ))}
              </div>
            </section>
            <fieldset
              className="panel"
              disabled={!writable || busy || mode === "yaml"}
            >
              <legend>Parameters and run overrides</legend>
              <div className="field">
                <span className="field-caption">
                  Default parameters (JSON object)
                </span>
                <CodeEditor
                  language="json"
                  label="Default parameters (JSON object)"
                  id="definition-parameters"
                  rows={6}
                  value={parameters}
                  onChange={(e) => setParameters(e.target.value)}
                  spellCheck={false}
                />
              </div>
              <label>
                Parameters overridable at run time
                <input
                  id="definition-overridable"
                  value={overridable}
                  onChange={(e) => setOverridable(e.target.value)}
                  placeholder="epochs, learning_rate"
                />
              </label>
              <div className="form-grid">
                {["resources", "image", "nodes", "retries", "timeout"].map(
                  (key) => (
                    <label className="inline-check" key={key}>
                      <input
                        id={`definition-override-${key}`}
                        type="checkbox"
                        checked={!!definition.overridable?.[key]}
                        onChange={(e) =>
                          patch({
                            overridable: {
                              ...definition.overridable,
                              [key]: e.target.checked,
                            },
                          })
                        }
                      />
                      Allow {key} overrides
                    </label>
                  ),
                )}
              </div>
            </fieldset>
            <fieldset
              className="panel"
              disabled={!writable || busy || mode === "yaml"}
            >
              <legend>Schedule</legend>
              <label className="inline-check">
                <input
                  id="definition-schedule-enabled"
                  type="checkbox"
                  checked={schedules.length > 0}
                  onChange={(e) =>
                    patch({
                      triggers: e.target.checked
                        ? [
                            ...(definition.triggers || []),
                            {
                              type: "schedule",
                              cron: "0 0 * * *",
                              timezone: "UTC",
                            },
                          ]
                        : (definition.triggers || []).filter(
                            (trigger) => trigger.type !== "schedule",
                          ),
                    })
                  }
                />
                Enable scheduled execution
              </label>
              {schedules.map((schedule, i) => (
                <div className="form-grid" key={schedule.index}>
                  <label>
                    Cron expression
                    <input
                      id={i === 0 ? "definition-cron" : undefined}
                      value={schedule.cron || ""}
                      onChange={(e) =>
                        patch({
                          triggers: (definition.triggers || []).map(
                            (trigger, index) =>
                              index === schedule.index
                                ? { ...trigger, cron: e.target.value }
                                : trigger,
                          ),
                        })
                      }
                    />
                  </label>
                  <label>
                    Timezone
                    <input
                      id={i === 0 ? "definition-timezone" : undefined}
                      value={schedule.timezone || "UTC"}
                      onChange={(e) =>
                        patch({
                          triggers: (definition.triggers || []).map(
                            (trigger, index) =>
                              index === schedule.index
                                ? { ...trigger, timezone: e.target.value }
                                : trigger,
                          ),
                        })
                      }
                    />
                  </label>
                </div>
              ))}
            </fieldset>
          </div>
          <div
            id="definition-yaml-panel"
            role="tabpanel"
            aria-labelledby="definition-tab-yaml"
            hidden={mode !== "yaml"}
          >
            <div className="field">
              <span className="field-caption">Definition YAML</span>
              <CodeEditor
                language="yaml"
                label="Definition YAML"
                ref={yamlRef}
                id="definition-yaml"
                rows={24}
                value={yaml}
                disabled={!writable || busy}
                spellCheck={false}
                onChange={(e) => setYaml(e.target.value)}
              />
            </div>
          </div>
          <ul id="definition-issues" className="issue-list" role="list">
            {validation?.issues?.map((issue, index) => (
              <li key={index}>
                <strong>
                  {[
                    issue.node && `Node ${issue.node}`,
                    issue.line && `Line ${issue.line}`,
                    !issue.node && !issue.line && issue.path,
                  ]
                    .filter(Boolean)
                    .join(", ")}
                </strong>{" "}
                {issue.message}
                {issue.line && mode === "yaml" && (
                  <button
                    type="button"
                    onClick={() => {
                      const lines = yaml.split("\n"),
                        line = issue.line! - 1,
                        start = lines
                          .slice(0, line)
                          .reduce((sum, text) => sum + text.length + 1, 0);
                      yamlRef.current?.focus();
                      yamlRef.current?.setSelectionRange(
                        start,
                        start + (lines[line] || "").length,
                      );
                    }}
                  >
                    Go to line
                  </button>
                )}
              </li>
            ))}
          </ul>
          <Feedback error={error} message={notice} />
          {resource && (
            <label>
              Revision message
              <input
                id="definition-message"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
              />
            </label>
          )}
          <div className="button-row">
            <button type="button" onClick={() => onNavigate("pipelines")}>
              Cancel
            </button>
            <button
              type="submit"
              className="primary"
              disabled={!writable || busy}
            >
              {busy ? "Saving…" : "Save flow"}
            </button>
          </div>
        </form>
        <section className="panel">
          <h2>Dependency graph</h2>
          <DefinitionGraph
            jobs={graphJobs}
            onSelect={setSelectedNode}
            selected={selectedNode?.name}
          />
          {selectedNode && (
            <section className="node-detail-panel">
              <h3>{selectedNode.name}</h3>
              <dl>
                <dt>Type</dt>
                <dd>{selectedNode.kind}</dd>
                <dt>
                  {selectedNode.kind === "function" ? "Function" : "Image"}
                </dt>
                <dd>
                  <code>{selectedNode.function || selectedNode.image}</code>
                </dd>
                <dt>Command</dt>
                <dd>
                  <code>
                    {selectedNode.command?.join(" ") || "Image default"}
                  </code>
                </dd>
                <dt>Dependencies</dt>
                <dd>
                  {(selectedNode.depends_on || []).join(", ") ||
                    "Starts immediately"}
                </dd>
                <dt>Resources</dt>
                <dd>
                  {selectedNode.resources?.cpu || "default"} CPU,{" "}
                  {selectedNode.resources?.memory || "default"} memory,{" "}
                  {selectedNode.resources?.gpu || 0} GPUs
                </dd>
                <dt>Retries</dt>
                <dd>{selectedNode.retries || 0}</dd>
                <dt>Timeout</dt>
                <dd>
                  {selectedNode.timeout_seconds
                    ? `${selectedNode.timeout_seconds}s`
                    : "One-hour default"}
                </dd>
                {selectedNode.when && (
                  <>
                    <dt>Runs when</dt>
                    <dd>
                      {selectedNode.when.param} ={" "}
                      {String(selectedNode.when.equals)}
                    </dd>
                  </>
                )}
              </dl>
            </section>
          )}
        </section>
        {resource && (
          <>
            <section className="panel">
              <h2>Active schedules</h2>
              {schedules.length ? (
                schedules.map((schedule) => (
                  <div className="toolbar" key={schedule.index}>
                    <span className="status">
                      {schedule.paused ? "Paused" : "Active"}
                    </span>
                    <code>{schedule.cron}</code>
                    <span>{schedule.timezone || "UTC"}</span>
                    <span>
                      Next:{" "}
                      {schedule.paused
                        ? "paused"
                        : schedule.next_run_at
                          ? new Date(schedule.next_run_at).toLocaleString()
                          : "not yet scheduled"}
                    </span>
                    <span>
                      Last:{" "}
                      {schedule.last_run_at
                        ? new Date(schedule.last_run_at).toLocaleString()
                        : "never"}
                    </span>
                    <button
                      disabled={!writable || busy}
                      onClick={async () => {
                        setBusy(true);
                        try {
                          await request(
                            `/api/v1/pipelines/definitions/${encodeURIComponent(resource)}/triggers/${schedule.index}/${schedule.paused ? "resume" : "pause"}`,
                            { method: "POST", body: {} },
                          );
                          loaded.current = "";
                          definitionQuery.reload();
                          setNotice(
                            schedule.paused
                              ? "Schedule resumed."
                              : "Schedule paused.",
                          );
                        } catch (failure) {
                          setError(errorMessage(failure));
                        } finally {
                          setBusy(false);
                        }
                      }}
                    >
                      {schedule.paused ? "Resume" : "Pause"}
                    </button>
                  </div>
                ))
              ) : (
                <p>Manual / API execution only.</p>
              )}
            </section>
            <section className="panel">
              <h2>Revisions</h2>
              <DataState
                loading={revisions.loading}
                error={revisions.error}
                onRetry={revisions.reload}
              >
                <ResourceTable
                  rows={items(revisions.data)}
                  columns={[
                    { key: "revision", label: "Revision" },
                    { key: "message", label: "Message" },
                    { key: "author", label: "Author" },
                    {
                      key: "created_at",
                      label: "Saved",
                      render: (row) =>
                        new Date(row.created_at).toLocaleString(),
                    },
                  ]}
                  actions={(revision) =>
                    revision.revision !== definition.revision ? (
                      <>
                        <button
                          onClick={async () => {
                            try {
                              const [old, current] = await Promise.all([
                                request<Entity>(
                                  `/api/v1/pipelines/definitions/${encodeURIComponent(resource)}/revisions/${revision.revision}`,
                                ),
                                request<Entity>(
                                  `/api/v1/pipelines/definitions/${encodeURIComponent(resource)}/yaml`,
                                ),
                              ]);
                              setDiff({
                                revision: revision.revision,
                                parts: pipelineLineDiff(old.yaml, current.yaml),
                              });
                            } catch (failure) {
                              setError(errorMessage(failure));
                            }
                          }}
                        >
                          Compare with current
                        </button>
                        <button
                          disabled={!writable || busy}
                          onClick={async () => {
                            if (
                              !confirm(
                                `Restore revision ${revision.revision} as a new revision? History is retained.`,
                              )
                            )
                              return;
                            setBusy(true);
                            try {
                              await request(
                                `/api/v1/pipelines/definitions/${encodeURIComponent(resource)}/revisions/${revision.revision}/rollback`,
                                { method: "POST", body: {} },
                              );
                              loaded.current = "";
                              definitionQuery.reload();
                              revisions.reload();
                              setNotice(
                                `Restored revision ${revision.revision}.`,
                              );
                            } catch (failure) {
                              setError(errorMessage(failure));
                            } finally {
                              setBusy(false);
                            }
                          }}
                        >
                          Roll back
                        </button>
                      </>
                    ) : (
                      <span>Current revision</span>
                    )
                  }
                />
              </DataState>
              {diff && (
                <div role="region" aria-label="Line differences">
                  <h3>Revision {diff.revision} to current</h3>
                  <CodeBlock
                    language="yaml"
                    format={false}
                    lineClasses={diff.parts.map(
                      (part) => `diff-line ${part.type}`,
                    )}
                  >
                    {diff.parts
                      .map(
                        (part) =>
                          `${part.type === "add" ? "+" : part.type === "del" ? "-" : " "} ${part.text}`,
                      )
                      .join("\n")}
                  </CodeBlock>
                </div>
              )}
              <button
                onClick={async () => {
                  try {
                    const saved = await request<Entity>(
                      `/api/v1/pipelines/definitions/${encodeURIComponent(resource)}/yaml`,
                    );
                    setYaml(saved.yaml);
                    setMode("yaml");
                  } catch (failure) {
                    setError(errorMessage(failure));
                  }
                }}
              >
                View saved YAML
              </button>
              <button
                onClick={async () => {
                  try {
                    const saved = await request<Entity>(
                      `/api/v1/pipelines/definitions/${encodeURIComponent(resource)}/yaml`,
                    );
                    await navigator.clipboard.writeText(saved.yaml);
                    setNotice("YAML copied.");
                  } catch (failure) {
                    setError(errorMessage(failure));
                  }
                }}
              >
                Copy saved YAML
              </button>
            </section>
          </>
        )}
      </DataState>
    </>
  );
}
