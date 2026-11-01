"use client";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { DownloadButton } from "@/components/ui/DownloadButton";
import { Select } from "@/components/ui/Select";
import { CodeEditor } from "@/components/ui/CodeEditor";
import { useEffect, useState, type FormEvent } from "react";
import { request } from "@/lib/api";
import { Logs } from "./Logs";
import { ExecutionGraph } from "./ExecutionGraph";
import {
  can,
  date,
  Detail,
  encode,
  Facts,
  items,
  Modal,
  scoped,
  State,
  Status,
  Table,
  useAction,
  useResource,
  type OperationsProps,
  type RecordData,
} from "./shared";
export function Pipelines({
  identity,
  project,
  resource,
  onNavigate,
}: OperationsProps) {
  const runs = useResource("/api/v1/pipelines/runs"),
    definitions = useResource("/api/v1/pipelines/definitions"),
    projects = useResource("/api/v1/project-options");
  const [runConfig, setRunConfig] = useState<RecordData | null>(null),
    [definition, setDefinition] = useState<RecordData | null>(null),
    [revision, setRevision] = useState<unknown>(),
    [step, setStep] = useState("");
  const run = useResource<RecordData>(
    resource ? `/api/v1/pipelines/runs/${encode(resource)}` : null,
  );
  const revisions = useResource(
    definition
      ? `/api/v1/pipelines/definitions/${encode(definition.id)}/revisions`
      : null,
  );
  useEffect(() => {
    setStep("");
  }, [resource]);
  useEffect(() => {
    if (!run.data || !["running", "queued"].includes(run.data.status)) return;
    const timer = setTimeout(run.reload, 2000);
    return () => clearTimeout(timer);
  }, [run.data]);
  const activeDefinition = items(definitions.data).find(
    (item) => item.id === run.data?.definition_id,
  );
  function resumeFromNode() {
    if (!run.data || !step) return;
    const nodes = run.data.steps || [],
      descendants = new Set([step]);
    for (let pass = 0; pass < nodes.length; pass++)
      for (const node of nodes)
        if (
          (node.depends_on || []).some((name: string) => descendants.has(name))
        )
          descendants.add(node.name);
    setRunConfig({
      definition_id: run.data.definition_id,
      project_id: run.data.project_id,
      rerun_from: run.data.id,
      selected_nodes: [...descendants],
    });
    onNavigate("pipelines", "");
  }
  const action = useAction(() => {
    runs.reload();
    definitions.reload();
    revisions.reload();
    run.reload();
  });
  return (
    <>
      {action.feedback}
      <header className="page-header">
        <p>Reusable flows, execution history and schedules.</p>
        <div className="button-row">
          <button
            disabled={!can(identity, "pipelines_write")}
            onClick={() => onNavigate("pipeline-editor")}
          >
            Define flow
          </button>
          <button
            className="primary"
            disabled={!can(identity, "pipelines_write")}
            onClick={() => setRunConfig({ project_id: project })}
          >
            Run pipeline
          </button>
        </div>
      </header>
      <section className="panel">
        <h2>Flow definitions</h2>
        <State {...definitions} retry={definitions.reload}>
          <Table
            rows={scoped(items(definitions.data), project)}
            columns={[
              { key: "name", label: "Flow" },
              { key: "execution_mode", label: "Execution" },
              { key: "version", label: "Version" },
              {
                key: "jobs",
                label: "Jobs",
                render: (row) => (row.jobs || row.nodes || []).length,
              },
              {
                key: "triggers",
                label: "Schedule",
                render: (row) =>
                  (row.triggers || [])
                    .filter(
                      (trigger: RecordData) => trigger.type === "schedule",
                    )
                    .map((trigger: RecordData, index: number) => (
                      <p key={index}>
                        {trigger.paused ? "Paused" : "Active"}{" "}
                        <code>{trigger.cron}</code> {trigger.timezone || "UTC"}
                        <small>Next {date(trigger.next_run_at)}</small>
                      </p>
                    )),
              },
            ]}
            actions={(row) => (
              <>
                <button onClick={() => setDefinition(row)}>Details</button>
                <button
                  disabled={!can(identity, "pipelines_write")}
                  onClick={() => onNavigate("pipeline-editor", row.id)}
                >
                  Edit
                </button>
                <button
                  disabled={!can(identity, "pipelines_write")}
                  onClick={() =>
                    setRunConfig({
                      definition_id: row.id,
                      project_id: row.project_id,
                    })
                  }
                >
                  Run
                </button>
              </>
            )}
          />
        </State>
      </section>
      <section className="panel">
        <h2>Run history</h2>
        <State {...runs} retry={runs.reload}>
          <Table
            rows={scoped(items(runs.data), project)}
            columns={[
              { key: "name", label: "Run" },
              { key: "project_id", label: "Project" },
              { key: "trigger", label: "Trigger" },
              {
                key: "status",
                label: "Status",
                render: (row) => <Status value={row.status} />,
              },
              {
                key: "progress",
                label: "Progress",
                render: (row) => (
                  <>
                    <progress
                      max={100}
                      value={Number(row.progress) || 0}
                      aria-label={`${row.name} progress`}
                    />
                    {Number(row.progress) || 0}%
                  </>
                ),
              },
              {
                key: "created_at",
                label: "Started",
                render: (row) => date(row.created_at),
              },
            ]}
            actions={(row) => (
              <button onClick={() => onNavigate("pipelines", row.id)}>
                Open run
              </button>
            )}
          />
        </State>
      </section>
      {resource && (
        <Modal
          title={run.data?.name || "Pipeline run"}
          onClose={() => onNavigate("pipelines", "")}
        >
          <State {...run} retry={run.reload}>
            {run.data && (
              <>
                <Status value={run.data.status} />
                <Facts
                  value={{
                    run_id: run.data.id,
                    project: run.data.project_id,
                    engine_run_id: run.data.engine_run_id,
                    execution_mode: run.data.execution_mode,
                    progress: run.data.progress,
                    provenance: run.data.provenance,
                  }}
                />
                <h3>Execution graph</h3>
                <ExecutionGraph
                  jobs={run.data.steps || []}
                  selected={step}
                  onSelect={setStep}
                />
                <Table
                  rows={run.data.steps || []}
                  columns={[
                    { key: "name", label: "Node" },
                    {
                      key: "status",
                      label: "Status",
                      render: (row) => <Status value={row.status} />,
                    },
                    { key: "depends_on", label: "Dependencies" },
                    { key: "attempt", label: "Attempt" },
                    {
                      key: "started_at",
                      label: "Started",
                      render: (row) => date(row.started_at),
                    },
                    {
                      key: "ended_at",
                      label: "Ended",
                      render: (row) => date(row.ended_at),
                    },
                  ]}
                  actions={(row) => (
                    <button onClick={() => setStep(row.name)}>
                      Inspect node
                    </button>
                  )}
                />
                {step ? (
                  <section
                    className="pipeline-node-inspector"
                    aria-label="Selected pipeline node"
                  >
                    <header className="page-header">
                      <h3>{step}</h3>
                      <button onClick={() => setStep("")}>
                        Show all run logs
                      </button>
                    </header>
                    <Facts
                      value={
                        (run.data.steps || []).find(
                          (item: RecordData) => item.name === step,
                        ) || {}
                      }
                    />
                    {can(identity, "pipelines_write") &&
                      run.data.definition_id &&
                      ["failed", "cancelled"].includes(run.data.status) && (
                        <div className="stack">
                          <p>
                            Resume with recorded successful outputs from this
                            run. The selected node and its downstream nodes are
                            included; required ancestors are resolved by
                            preflight.
                          </p>
                          <button
                            disabled={
                              !activeDefinition?.overridable?.nodes ||
                              action.busy
                            }
                            onClick={resumeFromNode}
                          >
                            {(run.data.steps || []).find(
                              (item: RecordData) => item.name === step,
                            )?.status === "succeeded"
                              ? "Resume after"
                              : "Resume from"}{" "}
                            {step}
                          </button>
                          {!activeDefinition?.overridable?.nodes && (
                            <small>
                              This flow locks node selection. Enable node
                              overrides in the definition before resuming a
                              subset.
                            </small>
                          )}
                        </div>
                      )}
                    <h4>Node logs</h4>
                    <Logs
                      project={run.data.project_id}
                      run={run.data.id}
                      node={step}
                    />
                  </section>
                ) : (
                  <section className="pipeline-node-inspector">
                    <h3>Run logs</h3>
                    <p>
                      Select a graph node for its details and filtered logs.
                    </p>
                    <Logs project={run.data.project_id} run={run.data.id} />
                  </section>
                )}
                <div className="button-row">
                  {can(identity, "pipelines_write") && (
                    <>
                      {["queued", "running"].includes(run.data.status) && (
                        <button
                          className="danger"
                          onClick={() => {
                            if (
                              window.confirm(
                                "Stop running jobs and cancel this run?",
                              )
                            )
                              void action
                                .run(
                                  () =>
                                    request(
                                      `/api/v1/pipelines/runs/${encode(resource)}/cancel`,
                                      { method: "POST", body: {} },
                                    ),
                                  "Cancellation requested.",
                                )
                                .catch(() => {});
                          }}
                        >
                          Cancel run
                        </button>
                      )}
                      <button
                        onClick={() =>
                          void action
                            .run(
                              () =>
                                request(
                                  `/api/v1/pipelines/runs/${encode(resource)}/retry`,
                                  { method: "POST", body: {} },
                                ),
                              "Retry submitted.",
                            )
                            .catch(() => {})
                        }
                      >
                        Retry whole run
                      </button>
                      {["failed", "cancelled"].includes(run.data.status) &&
                        run.data.definition_id && (
                          <button
                            onClick={() => {
                              setRunConfig({
                                definition_id: run.data!.definition_id,
                                project_id: run.data!.project_id,
                                rerun_from: run.data!.id,
                              });
                              onNavigate("pipelines", "");
                            }}
                          >
                            Rerun from failure
                          </button>
                        )}
                    </>
                  )}
                </div>
              </>
            )}
          </State>
        </Modal>
      )}
      {definition && (
        <Modal
          title={definition.name}
          onClose={() => {
            setDefinition(null);
            setRevision(undefined);
          }}
        >
          <Facts
            value={{
              id: definition.id,
              project_id: definition.project_id,
              version: definition.version,
              execution_mode: definition.execution_mode,
            }}
          />
          <h3>Jobs</h3>
          <ExecutionGraph
            jobs={definition.jobs || []}
            onSelect={(name) =>
              setRevision(
                (definition.jobs || []).find(
                  (job: RecordData) => job.name === name,
                ),
              )
            }
          />
          <Table
            rows={definition.jobs || []}
            columns={[
              { key: "name", label: "Job" },
              { key: "kind", label: "Kind" },
              {
                key: "image",
                label: "Image / function",
                render: (row) => row.image || row.function,
              },
              { key: "depends_on", label: "Depends on" },
            ]}
          />
          <h3>Schedule</h3>
          {(definition.triggers || []).map(
            (trigger: RecordData, index: number) =>
              trigger.type === "schedule" && (
                <div className="toolbar" key={index}>
                  <code>{trigger.cron}</code>
                  <span>{trigger.timezone || "UTC"}</span>
                  <Status value={trigger.paused ? "paused" : "active"} />
                  <span>Next {date(trigger.next_run_at)}</span>
                  <button
                    disabled={!can(identity, "pipelines_write")}
                    onClick={() =>
                      void action
                        .run(
                          () =>
                            request<RecordData>(
                              `/api/v1/pipelines/definitions/${encode(definition.id)}/triggers/${index}/${trigger.paused ? "resume" : "pause"}`,
                              { method: "POST", body: {} },
                            ),
                          "Schedule updated.",
                        )
                        .then((value) => setDefinition(value))
                        .catch(() => {})
                    }
                  >
                    {trigger.paused ? "Resume" : "Pause"}
                  </button>
                </div>
              ),
          )}
          <h3>Revision history</h3>
          <State {...revisions} retry={revisions.reload}>
            <Table
              rows={items(revisions.data)}
              columns={[
                { key: "revision", label: "Revision" },
                { key: "message", label: "Message" },
                { key: "author", label: "Author" },
                {
                  key: "created_at",
                  label: "Saved",
                  render: (row) => date(row.created_at),
                },
              ]}
              actions={(row) => (
                <>
                  <button
                    onClick={() =>
                      void request(
                        `/api/v1/pipelines/definitions/${encode(definition.id)}/revisions/${row.revision}`,
                      ).then(setRevision)
                    }
                  >
                    View revision
                  </button>
                  <button
                    disabled={!can(identity, "pipelines_write")}
                    onClick={() => {
                      if (
                        window.confirm(
                          `Restore revision ${row.revision} as a new revision?`,
                        )
                      )
                        void action
                          .run(
                            () =>
                              request(
                                `/api/v1/pipelines/definitions/${encode(definition.id)}/revisions/${row.revision}/rollback`,
                                { method: "POST", body: {} },
                              ),
                            "Revision restored.",
                          )
                          .then(() => {
                            revisions.reload();
                            setDefinition(null);
                          })
                          .catch(() => {});
                    }}
                  >
                    Restore revision
                  </button>
                </>
              )}
            />
          </State>
          {revision !== undefined && (
            <>
              {typeof (revision as RecordData)?.yaml === "string" ? (
                <div className="stack">
                  <DownloadButton
                    filename={`${definition.name}.kionga.yaml`}
                    content={(revision as RecordData).yaml}
                    language="yaml"
                  >
                    Download YAML
                  </DownloadButton>
                  <CodeBlock
                    language="yaml"
                    filename={`${definition.name}.kionga.yaml`}
                  >
                    {(revision as RecordData).yaml}
                  </CodeBlock>
                </div>
              ) : (
                <Detail value={revision} />
              )}
            </>
          )}
          <button
            onClick={() =>
              void request(
                `/api/v1/pipelines/definitions/${encode(definition.id)}/yaml`,
              ).then(setRevision)
            }
          >
            View YAML
          </button>
        </Modal>
      )}
      {runConfig && (
        <RunConfiguration
          initial={runConfig}
          definitions={items(definitions.data)}
          projects={items(projects.data)}
          onClose={() => setRunConfig(null)}
          onSaved={(id) => {
            setRunConfig(null);
            runs.reload();
            onNavigate("pipelines", id);
          }}
        />
      )}
    </>
  );
}

function RunConfiguration({
  initial,
  definitions,
  projects,
  onClose,
  onSaved,
}: {
  initial: RecordData;
  definitions: RecordData[];
  projects: RecordData[];
  onClose: () => void;
  onSaved: (id: string) => void;
}) {
  const [values, setValues] = useState({
      project_id: initial.project_id || "",
      name: "",
      definition_id: initial.definition_id || "",
      parameters: "{}",
      overrides: "{}",
    }),
    [checked, setChecked] = useState<RecordData | null>(null),
    [checkedKey, setCheckedKey] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const definition = definitions.find(
    (item) => item.id === values.definition_id,
  );
  const [parameters, setParameters] = useState<RecordData>({}),
    [nodes, setNodes] = useState<RecordData>({}),
    [selected, setSelected] = useState<string[]>([]),
    [parallelism, setParallelism] = useState("");
  useEffect(() => {
    setParameters({});
    setNodes({});
    setSelected(
      initial.selected_nodes ||
        (definition?.jobs || []).map((job: RecordData) => job.name),
    );
    setChecked(null);
    setCheckedKey("");
    if (definition)
      setValues((old) => ({ ...old, project_id: definition.project_id }));
  }, [definition?.id]);
  const loose = (value: string) => {
    try {
      return JSON.parse(value);
    } catch {
      return value;
    }
  };
  function body() {
    const data: RecordData = {
      project_id: values.project_id,
      name: values.name.trim(),
    };
    if (values.definition_id) data.definition_id = values.definition_id;
    if (!definition || !Object.keys(definition.parameters || {}).length)
      data.parameters = JSON.parse(values.parameters || "{}");
    const overrides: RecordData = { ...JSON.parse(values.overrides || "{}") };
    if (Object.keys(parameters).length)
      overrides.parameters = Object.fromEntries(
        Object.entries(parameters)
          .filter(([, value]) => String(value).trim())
          .map(([key, value]) => [key, loose(String(value))]),
      );
    if (Object.keys(nodes).length)
      overrides.nodes = Object.fromEntries(
        Object.entries(nodes).map(([name, node]) => {
          const { cpu, memory, ...rest } = node;
          const cleaned = Object.fromEntries(
            Object.entries(rest)
              .filter(([, value]) => String(value).trim())
              .map(([key, value]) => [
                key,
                ["retries", "timeout_seconds"].includes(key)
                  ? Number(value)
                  : value,
              ]),
          );
          if (cpu || memory)
            cleaned.resources = {
              ...(cpu ? { cpu } : {}),
              ...(memory ? { memory } : {}),
            };
          return [name, cleaned];
        }),
      );
    if (
      definition?.overridable?.nodes &&
      selected.length !== definition.jobs.length
    )
      overrides.selected_nodes = selected;
    if (parallelism) overrides.max_parallelism = Number(parallelism);
    if (initial.rerun_from) overrides.rerun_from_run_id = initial.rerun_from;
    if (Object.keys(overrides).length) data.overrides = overrides;
    return data;
  }
  async function check() {
    const payload = body();
    const result = await request<RecordData>("/api/v1/pipelines/preflight", {
      method: "POST",
      body: payload,
    });
    setChecked(result);
    setCheckedKey(result.allowed ? JSON.stringify(payload) : "");
    return result;
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    try {
      const payload = body();
      if (checkedKey !== JSON.stringify(payload)) {
        const result = await check();
        if (!result.allowed) return;
      }
      const run = await request<RecordData>("/api/v1/pipelines/submit", {
        method: "POST",
        body: payload,
      });
      onSaved(run.id);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const changed = () => {
    setChecked(null);
    setCheckedKey("");
  };
  return (
    <Modal title="Run pipeline" onClose={onClose}>
      <form className="stack" onSubmit={submit} onChange={changed}>
        <div className="form-grid">
          <label>
            Project
            <Select
              required
              value={values.project_id}
              disabled={!!definition}
              onChange={(event) =>
                setValues({ ...values, project_id: event.target.value })
              }
            >
              <option value="">Choose project</option>
              {projects.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name}
                </option>
              ))}
            </Select>
          </label>
          <label>
            Run name
            <input
              required
              value={values.name}
              onChange={(event) =>
                setValues({ ...values, name: event.target.value })
              }
            />
          </label>
          <label>
            Flow definition
            <Select
              value={values.definition_id}
              onChange={(event) =>
                setValues({ ...values, definition_id: event.target.value })
              }
            >
              <option value="">Built-in training pipeline</option>
              {definitions.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name} v{item.version}
                </option>
              ))}
            </Select>
          </label>
        </div>
        {initial.rerun_from && (
          <p>
            Reuse succeeded nodes from {initial.rerun_from}; run failed and
            downstream nodes again.
          </p>
        )}
        {definition && Object.keys(definition.parameters || {}).length ? (
          <section>
            <h3>Parameters</h3>
            {Object.entries(definition.parameters).map(([name, value]) => (
              <label key={name}>
                {name} <small>Default: {JSON.stringify(value)}</small>
                <input
                  disabled={!definition.overridable?.parameters?.includes(name)}
                  value={parameters[name] || ""}
                  placeholder={JSON.stringify(value)}
                  onChange={(event) =>
                    setParameters({ ...parameters, [name]: event.target.value })
                  }
                />
                {!definition.overridable?.parameters?.includes(name) && (
                  <small>Not overridable in this flow.</small>
                )}
              </label>
            ))}
          </section>
        ) : (
          <div className="field">
            <span className="field-caption">Parameters JSON</span>
            <CodeEditor
              language="json"
              label="Parameters JSON"
              rows={4}
              value={values.parameters}
              onChange={(event) =>
                setValues({ ...values, parameters: event.target.value })
              }
            />
          </div>
        )}
        {definition && (
          <section>
            <h3>Node overrides</h3>
            {(definition.jobs || []).map((job: RecordData) => (
              <fieldset key={job.name}>
                <legend>{job.name}</legend>
                {definition.overridable?.nodes && (
                  <label>
                    <input
                      type="checkbox"
                      checked={selected.includes(job.name)}
                      onChange={(event) =>
                        setSelected(
                          event.target.checked
                            ? [...selected, job.name]
                            : selected.filter((name) => name !== job.name),
                        )
                      }
                    />
                    Run node
                  </label>
                )}
                <div className="form-grid">
                  {[
                    [
                      "image",
                      "Image digest",
                      definition.overridable?.image && job.kind === "container",
                    ],
                    ["cpu", "CPU", definition.overridable?.resources],
                    ["memory", "Memory", definition.overridable?.resources],
                    ["retries", "Retries", definition.overridable?.retries],
                    [
                      "timeout_seconds",
                      "Timeout seconds",
                      definition.overridable?.timeout,
                    ],
                  ].map(([field, label, allowed]) => (
                    <label key={String(field)}>
                      {String(label)}
                      <input
                        disabled={!allowed}
                        value={nodes[job.name]?.[String(field)] || ""}
                        placeholder={String(
                          job[String(field)] ||
                            job.resources?.[String(field)] ||
                            "Definition default",
                        )}
                        onChange={(event) =>
                          setNodes({
                            ...nodes,
                            [job.name]: {
                              ...nodes[job.name],
                              [String(field)]: event.target.value,
                            },
                          })
                        }
                      />
                      {!allowed && <small>Not overridable.</small>}
                    </label>
                  ))}
                </div>
              </fieldset>
            ))}
            {definition.overridable?.parallelism && (
              <label>
                Maximum parallelism
                <input
                  type="number"
                  min={1}
                  value={parallelism}
                  onChange={(event) => setParallelism(event.target.value)}
                />
              </label>
            )}
          </section>
        )}
        <details>
          <summary>Additional permitted overrides</summary>
          <div className="field">
            <span className="field-caption">Overrides JSON</span>
            <CodeEditor
              language="json"
              label="Overrides JSON"
              value={values.overrides}
              rows={5}
              onChange={(event) =>
                setValues({ ...values, overrides: event.target.value })
              }
            />
          </div>
          <p>
            The server enforces override permissions during preflight and
            submission.
          </p>
        </details>
        {error && (
          <p className="feedback error" role="alert">
            {error}
          </p>
        )}
        {checked && (
          <>
            <ul>
              {(checked.checks || []).map(
                (check: RecordData, index: number) => (
                  <li key={index}>
                    <Status value={check.status} />
                    {check.node && <strong>{check.node}: </strong>}
                    {check.message}
                  </li>
                ),
              )}
            </ul>
            {!!checked.fields?.length && (
              <section aria-label="Effective run fields">
                <h3>Effective run configuration</h3>
                <Table
                  rows={checked.fields}
                  columns={[
                    { key: "field", label: "Field" },
                    { key: "default", label: "Default" },
                    { key: "effective", label: "Effective value" },
                    {
                      key: "overridden",
                      label: "Overridden",
                      render: (field) => (field.overridden ? "Yes" : "No"),
                    },
                    {
                      key: "locked",
                      label: "Permission",
                      render: (field) => (field.locked ? "Locked" : "Editable"),
                    },
                    { key: "reason", label: "Reason" },
                  ]}
                />
              </section>
            )}
            {checked.policy_decision && (
              <p>
                Policy decision: <code>{checked.policy_decision}</code>
              </p>
            )}
            {checked.reused_nodes?.length > 0 && (
              <p>Reusing {checked.reused_nodes.join(", ")}</p>
            )}
          </>
        )}
        <div className="button-row">
          <button
            type="button"
            disabled={busy}
            onClick={() => {
              setError("");
              setBusy(true);
              void check()
                .catch((error) => setError(error.message))
                .finally(() => setBusy(false));
            }}
          >
            Check run
          </button>
          <button
            type="submit"
            className="primary"
            disabled={busy || !checked?.allowed || !checkedKey}
          >
            {busy ? "Working…" : "Submit run"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
