"use client";
import {CodeBlock} from "@/components/ui/CodeBlock";
import { useEffect, useState } from "react";
import { request } from "@/lib/api";
import {
  can,
  date,
  Detail,
  encode,
  Facts,
  Form,
  has,
  items,
  Modal,
  option,
  projectField,
  scoped,
  State,
  Status,
  Table,
  useAction,
  useResource,
  type OperationsProps,
  type RecordData,
} from "./shared";

export function Overview({ project, onNavigate }: OperationsProps) {
  const query = useResource(
    `/api/v1/dashboard${project ? `?project_id=${encode(project)}` : ""}`,
  );
  const data = query.data;
  return (
    <State {...query} retry={query.reload}>
      <div className="stat-row">
        {[
          ["Projects", data?.projects, "projects"],
          ["Active runs", data?.active_runs, "pipelines"],
          [
            "Healthy components",
            `${data?.healthy_components ?? "—"} / ${data?.total_components ?? "—"}`,
            "platform",
          ],
        ].map(([label, value, view]) => (
          <button
            key={label}
            className="stat"
            onClick={() => onNavigate(String(view))}
          >
            <span>{label}</span>
            <strong>{value ?? "—"}</strong>
          </button>
        ))}
      </div>
      <section className="panel">
        <header className="page-header">
          <h2>Recent runs</h2>
          <button onClick={() => onNavigate("pipelines")}>
            View pipelines
          </button>
        </header>
        <Table
          rows={data?.recent_runs || []}
          columns={[
            { key: "name", label: "Run" },
            {
              key: "status",
              label: "Status",
              render: (row) => <Status value={row.status} />,
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
      </section>
    </State>
  );
}

export function Functions({ identity, project }: OperationsProps) {
  const query = useResource("/api/v1/functions"),
    projects = useResource("/api/v1/project-options");
  const [deploy, setDeploy] = useState(false),
    [invoke, setInvoke] = useState<RecordData | null>(null),
    [output, setOutput] = useState<unknown>();
  const action = useAction(query.reload);
  const rows = scoped(items(query.data), project);
  return (
    <>
      <header className="page-header">
        <p>OCI functions deployed through OpenFaaS.</p>
        <button
          className="primary"
          disabled={
            !can(identity, "functions_write") || !query.data?.configured
          }
          onClick={() => setDeploy(true)}
        >
          Deploy function
        </button>
      </header>
      {action.feedback}
      <State {...query} retry={query.reload}>
        <p className="feedback">
          Runtime: {query.data?.configured ? "Connected" : "Not configured"}
        </p>
        <Table
          rows={rows}
          columns={[
            { key: "name", label: "Function" },
            { key: "image", label: "Image" },
            {
              key: "status",
              label: "Status",
              render: (row) => <Status value={row.status} />,
            },
            { key: "replicas", label: "Replicas" },
            {
              key: "annotations",
              label: "Trigger",
              render: (row) =>
                row.annotations?.schedule
                  ? `Cron ${row.annotations.schedule}`
                  : row.annotations?.topic
                    ? `Kafka ${row.annotations.topic}`
                    : row.annotations?.["io.kionga.invocation"] === "async"
                      ? "Async queue"
                      : "HTTP / webhook",
            },
          ]}
          actions={(row) => (
            <>
              <button
                disabled={!can(identity, "functions_write")}
                onClick={() => {
                  setInvoke(row);
                  setOutput(undefined);
                }}
              >
                Invoke
              </button>
              {row.project_id && (
                <button
                  className="danger"
                  disabled={!can(identity, "functions_write") || action.busy}
                  onClick={() => {
                    if (
                      window.confirm(
                        `Remove ${row.name}? Pipelines referencing it will no longer run.`,
                      )
                    )
                      void action
                        .run(
                          () =>
                            request(`/api/v1/functions/${encode(row.name)}`, {
                              method: "DELETE",
                            }),
                          "Function removed.",
                        )
                        .catch(() => {});
                  }}
                >
                  Remove
                </button>
              )}
            </>
          )}
        />
      </State>
      {deploy && (
        <Modal title="Deploy function" onClose={() => setDeploy(false)}>
          <Form
            initial={{
              project_id: project,
              cpu: "500m",
              memory: "512Mi",
              env_vars: "{}",
              trigger_type: "http",
            }}
            fields={[
              projectField(items(projects.data)),
              { name: "name", label: "Name", required: true },
              { name: "image", label: "OCI image", required: true },
              { name: "cpu", label: "CPU" },
              { name: "memory", label: "Memory" },
              {
                name: "env_vars",
                label: "Environment variables",
                type: "json",
              },
              {
                name: "trigger_type",
                label: "Trigger",
                type: "select",
                options: ["http", "async", "cron", "kafka"].map(option),
              },
              {
                name: "trigger_source",
                label: "Cron expression or Kafka topic",
                help: "Required for scheduled and Kafka triggers.",
              },
            ]}
            submit="Deploy function"
            onSubmit={async (value) => {
              const annotations: RecordData = {
                "io.kionga.invocation":
                  value.trigger_type === "async" ? "async" : "sync",
              };
              if (value.trigger_type === "cron") {
                if (!value.trigger_source)
                  throw new Error("A cron expression is required.");
                Object.assign(annotations, {
                  topic: "cron-function",
                  schedule: value.trigger_source,
                });
              }
              if (value.trigger_type === "kafka") {
                if (!value.trigger_source)
                  throw new Error("A Kafka topic is required.");
                annotations.topic = value.trigger_source;
              }
              const { trigger_type, trigger_source, ...body } = value;
              await request("/api/v1/functions", {
                method: "POST",
                body: { ...body, annotations },
              });
              setDeploy(false);
              query.reload();
            }}
          />
        </Modal>
      )}
      {invoke && (
        <Modal title={`Invoke ${invoke.name}`} onClose={() => setInvoke(null)}>
          <Form
            initial={{ payload: "{}" }}
            fields={[{ name: "payload", label: "JSON payload", type: "json" }]}
            submit={
              invoke.annotations?.["io.kionga.invocation"] === "async"
                ? "Queue invocation"
                : "Invoke"
            }
            onSubmit={async (value) => {
              const route =
                invoke.annotations?.["io.kionga.invocation"] === "async"
                  ? "invoke-async"
                  : "invoke";
              setOutput(
                await request(
                  `/api/v1/functions/${encode(invoke.name)}/${route}`,
                  { method: "POST", body: value.payload },
                ),
              );
            }}
          />
          {output !== undefined && <Detail value={output} />}
        </Modal>
      )}
    </>
  );
}

export function Models({ identity, project, resource, onNavigate }: OperationsProps) {
  const query = useResource("/api/v1/models"),
    projects = useResource("/api/v1/project-options");
  const [selected, setSelected] = useState<RecordData | null>(null),
    [predict, setPredict] = useState<RecordData | null>(null),
    [output, setOutput] = useState<unknown>(),
    [search, setSearch] = useState<RecordData[]>([]),
    [notebook, setNotebook] = useState(""),
    [notebookProject, setNotebookProject] = useState(project);
  const action = useAction(query.reload);
  const rows = scoped(items(query.data), project);
  useEffect(() => {
    if (resource) {
      setSelected(items(query.data).find((model) => model.id === resource) || null);
    } else {
      setSelected((previous) => previous?.hubImport ? previous : null);
    }
  }, [resource, query.data]);
  function code(model: RecordData) {
    return `from mlaiops_sdk.models import Model\nfrom mlaiops_sdk.huggingface import download_model\n\nmodel = Model.model_validate_json(${JSON.stringify(JSON.stringify(model))})\npath = download_model(model)\nprint(path)\n`;
  }
  return (
    <>
      <p>Registered model versions, quality gates and serving deployments.</p>
      {action.feedback}
      <section className="panel">
        <h2>Registry</h2>
        <State {...query} retry={query.reload}>
          <Table
            rows={rows}
            columns={[
              {
                key: "name",
                label: "Model",
                render: (row) => (
                  <button
                    className="text-button"
                    onClick={() => { setSelected(row); onNavigate("models", row.id); }}
                  >
                    {row.name} v{row.version}
                  </button>
                ),
              },
              { key: "stage", label: "Stage" },
              {
                key: "gate_status",
                label: "Quality gate",
                render: (row) => <Status value={row.gate_status} />,
              },
              {
                key: "deployment_status",
                label: "Deployment",
                render: (row) => <Status value={row.deployment_status} />,
              },
              {
                key: "metrics",
                label: "Metrics",
                render: (row) => (
                  <dl>
                    {Object.entries(row.metrics || {}).map(([key, value]) => (
                      <div key={key}>
                        <dt>{key}</dt>
                        <dd>{String(value)}</dd>
                      </div>
                    ))}
                  </dl>
                ),
              },
            ]}
            actions={(row) =>
              row.artifact_uri?.startsWith("hf://") ? (
                <button
                  onClick={() => {
                    setNotebookProject(row.project_id || project);
                    setNotebook(code(row));
                  }}
                >
                  Download / notebook
                </button>
              ) : (
                <>
                  {["promote", "deploy", "rollback"].map((operation) => (
                    <button
                      key={operation}
                      disabled={!can(identity, "models_write") || action.busy}
                      onClick={() =>
                        void action
                          .run(
                            () =>
                              request(
                                `/api/v1/models/${encode(row.id)}/${operation}`,
                                {
                                  method: "POST",
                                  body:
                                    operation === "promote"
                                      ? { stage: "production" }
                                      : operation === "deploy"
                                        ? { canary_weight: 0 }
                                        : {},
                                },
                              ),
                            `${operation} requested.`,
                          )
                          .catch(() => {})
                      }
                    >
                      {operation[0].toUpperCase() + operation.slice(1)}
                    </button>
                  ))}
                  {row.endpoint_url?.startsWith("http") && (
                    <button
                      disabled={!can(identity, "models_write")}
                      onClick={() => {
                        setPredict(row);
                        setOutput(undefined);
                      }}
                    >
                      Test
                    </button>
                  )}
                </>
              )
            }
          />
        </State>
      </section>
      <section className="panel">
        <header className="page-header">
          <h2>Hugging Face discovery</h2>
          <button onClick={() => onNavigate("settings")}>
            Connect account
          </button>
        </header>
        <Form
          initial={{
            search: "",
            task: "",
            project_id: project,
            revision: "main",
          }}
          fields={[
            { name: "search", label: "Repository or search phrase" },
            {
              name: "task",
              label: "Task",
              type: "select",
              options: [
                "",
                "text-generation",
                "text-classification",
                "sentence-similarity",
                "image-classification",
                "automatic-speech-recognition",
              ].map((value) => ({ value, label: value || "All tasks" })),
            },
          ]}
          submit="Search models"
          onSubmit={async (value) =>
            setSearch(
              items(
                await request(
                  `/api/v1/models/huggingface?${new URLSearchParams({ search: value.search, task: value.task })}`,
                ),
              ),
            )
          }
        />
        <Table
          rows={search}
          columns={[
            { key: "id", label: "Repository" },
            { key: "pipeline_tag", label: "Task" },
            { key: "downloads", label: "Downloads" },
            {
              key: "tags",
              label: "License",
              render: (row) =>
                (row.tags || []).find((tag: string) =>
                  tag.startsWith("license:"),
                ) || "Review model card",
            },
          ]}
          actions={(row) => (
            <>
              <a
                href={`https://huggingface.co/${encodeURI(row.id)}`}
                target="_blank"
                rel="noopener noreferrer"
              >
                Model card
              </a>
              <button
                disabled={!can(identity, "models_write")}
                onClick={() => setSelected({ ...row, hubImport: true })}
              >
                Register in project
              </button>
            </>
          )}
        />
      </section>
      {selected && (
        <Modal
          title={
            selected.hubImport
              ? "Register Hub model"
              : `${selected.name} v${selected.version}`
          }
          onClose={() => { setSelected(null); if (!selected.hubImport) onNavigate("models", ""); }}
        >
          {selected.hubImport ? (
            <Form
              initial={{ project_id: project, revision: "main" }}
              fields={[
                projectField(items(projects.data)),
                { name: "revision", label: "Revision", required: true },
              ]}
              submit="Register model"
              onSubmit={async (value) => {
                const model = await request<RecordData>(
                  "/api/v1/models/huggingface/import",
                  { method: "POST", body: { ...value, repo_id: selected.id } },
                );
                setNotebookProject(model.project_id || value.project_id);
                setNotebook(code(model));
                setSelected(null);
                query.reload();
              }}
            />
          ) : (
            <Facts value={selected} />
          )}
        </Modal>
      )}
      {predict && (
        <Modal title={`Test ${predict.name}`} onClose={() => setPredict(null)}>
          <Form
            initial={{ payload: "{}" }}
            fields={[
              { name: "payload", label: "Request payload", type: "json" },
            ]}
            submit="Predict"
            onSubmit={async (value) =>
              setOutput(
                await request(`/api/v1/models/${encode(predict.id)}/predict`, {
                  method: "POST",
                  body: value.payload,
                }),
              )
            }
          />
          {output !== undefined && <Detail value={output} />}
        </Modal>
      )}
      {notebook && (
        <Modal title="Download model weights" onClose={() => setNotebook("")}>
          <p>
            The pinned model revision downloads without executing repository
            code. Connect private accounts through Settings.
          </p>
          <CodeBlock language="python">{notebook}</CodeBlock>
          <div className="button-row">
            <button
              onClick={() => void navigator.clipboard.writeText(notebook)}
            >
              Copy code
            </button>
            <button
              onClick={() => {
                const doc = {
                  nbformat: 4,
                  nbformat_minor: 5,
                  metadata: {
                    kernelspec: {
                      display_name: "Python 3",
                      language: "python",
                      name: "python3",
                    },
                  },
                  cells: [
                    {
                      cell_type: "code",
                      id: "download-model",
                      metadata: {},
                      execution_count: null,
                      outputs: [],
                      source: notebook.split(/(?<=\n)/),
                    },
                  ],
                };
                const url = URL.createObjectURL(
                  new Blob([JSON.stringify(doc, null, 2)], {
                    type: "application/x-ipynb+json",
                  }),
                );
                const link = document.createElement("a");
                link.href = url;
                link.download = "huggingface-model.ipynb";
                link.click();
                URL.revokeObjectURL(url);
              }}
            >
              Download notebook
            </button>
            {has(identity, "workbench") && (
              <a
                href={`/workspace?tool=workbench&project=${encode(notebookProject)}`}
              >
                Open JupyterLab
              </a>
            )}
          </div>
        </Modal>
      )}
    </>
  );
}
