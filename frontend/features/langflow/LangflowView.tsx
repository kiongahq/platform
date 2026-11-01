"use client";
import {Select} from "@/components/ui/Select";
import { useState } from "react";
import { request, errorMessage } from "@/lib/api";
import { useQuery } from "@/hooks/useQuery";
import { items, isAdministrator, hasService, type Entity } from "@/lib/types";
import {
  ActionForm,
  DataState,
  Detail,
  Feedback,
  Modal,
  ResourceTable,
  Status,
  type ViewProps,
  type Field,
} from "@/features/shared/View";
export function LangflowView({ identity, project, onNavigate }: ViewProps) {
  const workspaces = useQuery<Entity>("/api/v1/langflow-workspaces"),
    snapshots = useQuery<Entity>("/api/v1/langflow-snapshots"),
    agents = useQuery<Entity>(
      hasService(identity, "agents") ? "/api/v1/agents" : null,
    ),
    projects = useQuery<Entity>("/api/v1/project-options");
  const [dialog, setDialog] = useState<{
      kind: string;
      item?: Entity;
      flows?: Entity[];
    } | null>(null),
    [error, setError] = useState(""),
    [message, setMessage] = useState("");
  const managed = workspaces.data?.mode === "kubernetes",
    writable = identity.permissions.agents_write === true;
  const scope = (value: Entity[]) =>
    value.filter(
      (item) => !project || item.project_id === project || item.id === "local",
    );
  const projectOptions = items(projects.data).map((item) => ({
    value: item.id,
    label: item.name,
  }));
  async function act(action: string, item: Entity) {
    setError("");
    try {
      if (action === "open") {
        location.assign(
          `/workspace?${new URLSearchParams({ tool: "langflow", instance: item.id, ...((item.project_id || project) ? { project: item.project_id || project } : {}) })}`,
        );
        return;
      }
      if (action === "flows") {
        const data = await request<Entity>(
          `/api/v1/langflow-workspaces/${encodeURIComponent(item.id)}/flows`,
        );
        if (!items(data).length)
          throw new Error(
            "Save a flow in the Langflow canvas before capturing it.",
          );
        setDialog({ kind: "capture", item, flows: items(data) });
        return;
      }
      if (
        action === "delete" &&
        !window.confirm(
          "Delete this workspace and its stored flows permanently?",
        )
      )
        return;
      await request(
        `/api/v1/langflow-workspaces/${encodeURIComponent(item.id)}${action === "delete" ? "" : `/${action}`}`,
        { method: action === "delete" ? "DELETE" : "POST" },
      );
      workspaces.reload();
      setMessage(`Workspace ${action} requested.`);
    } catch (e) {
      setError(errorMessage(e));
    }
  }
  return (
    <section id="langflow" aria-label="Langflow workspaces">
      <div className="toolbar">
        <p>Build flows in persistent Langflow workspaces.</p>
        <div className="button-row">
          <button onClick={() => onNavigate("agent-designer")}>
            Native designer
          </button>
          <button
            onClick={() => {
              workspaces.reload();
              snapshots.reload();
            }}
          >
            Refresh
          </button>
          {managed && !identity.roles.includes("viewer") && (
            <button
              className="primary"
              onClick={() => setDialog({ kind: "create" })}
            >
              Create workspace
            </button>
          )}
        </div>
      </div>
      <Feedback error={error} message={message} />
      <DataState
        loading={workspaces.loading}
        error={workspaces.error}
        onRetry={workspaces.reload}
      >
        <div id="langflow-list">
          <ResourceTable
            rows={scope(items(workspaces.data))}
            columns={[
              { key: "name", label: "Workspace" },
              {
                key: "project_id",
                label: "Project",
                render: (item) =>
                  projectOptions.find((p) => p.value === item.project_id)
                    ?.label || "Local administrator",
              },
              {
                key: "state",
                label: "State",
                render: (item) => <Status value={item.state} />,
              },
              {
                key: "cpu",
                label: "Resources",
                render: (item) =>
                  item.id === "local"
                    ? "Shared local canvas"
                    : `${item.cpu} CPU / ${item.memory_gb} GB memory / ${item.storage_gb} GB storage`,
              },
            ]}
            actions={(item) => (
              <>
                <button
                  className="primary"
                  disabled={!["ready", "configured"].includes(item.state)}
                  onClick={() => void act("open", item)}
                >
                  Open canvas
                </button>
                <button
                  disabled={!["ready", "configured"].includes(item.state)}
                  onClick={() => void act("flows", item)}
                >
                  Capture flow
                </button>
                {item.id !== "local" && !identity.roles.includes("viewer") && (
                  <>
                    <button
                      onClick={() =>
                        void act(
                          item.state === "stopped" ? "start" : "stop",
                          item,
                        )
                      }
                    >
                      {item.state === "stopped" ? "Start" : "Stop"}
                    </button>
                    <button
                      disabled={item.state === "stopped"}
                      onClick={() => void act("restart", item)}
                    >
                      Restart
                    </button>
                    <button
                      className="danger"
                      onClick={() => void act("delete", item)}
                    >
                      Delete
                    </button>
                  </>
                )}
              </>
            )}
          />
        </div>
        {!scope(items(workspaces.data)).length && (
          <div className="empty-state">
            <h2>No Langflow workspaces</h2>
            <p>
              {managed
                ? "Create a workspace assigned to your project."
                : "The local canvas requires a running Langflow service, a gateway upstream configuration, and the local bootstrap administrator account."}
            </p>
            <a href="https://kiongahq.github.io/docs/guides/langflow/">
              Langflow setup guide
            </a>
          </div>
        )}
      </DataState>
      <section>
        <h2>Flow snapshots</h2>
        <DataState
          loading={snapshots.loading}
          error={snapshots.error}
          onRetry={snapshots.reload}
        >
          <ResourceTable
            rows={scope(items(snapshots.data))}
            columns={[
              { key: "name", label: "Flow" },
              { key: "revision", label: "Revision" },
              { key: "flow_id", label: "Flow ID" },
              {
                key: "invoke_path",
                label: "Stable API",
                render: (item) =>
                  item.invoke_path ? (
                    <code>{item.invoke_path}</code>
                  ) : (
                    "Not activated"
                  ),
              },
              {
                key: "deployments",
                label: "Deployments",
                render: (item) => (
                  <>
                    {items(agents.data)
                      .filter((agent) => agent.snapshot_id === item.id)
                      .map((agent) => (
                        <div key={agent.id}>
                          <span>
                            {agent.name} revision {agent.snapshot_revision}{" "}
                          </span>
                          <Status value={agent.status} />
                          {managed &&
                            item.owner_subject === identity.subject &&
                            writable && hasService(identity, "agents") &&
                            agent.status === "ready" &&
                            item.active_agent_id !== agent.id && (
                              <button
                                onClick={() =>
                                  void request(
                                    `/api/v1/langflow-snapshots/${encodeURIComponent(item.id)}/activate`,
                                    {
                                      method: "POST",
                                      body: { agent_id: agent.id },
                                    },
                                  )
                                    .then(() => snapshots.reload())
                                    .catch((e) => setError(errorMessage(e)))
                                }
                              >
                                {item.active_agent_id && agent.snapshot_revision < item.active_revision ? "Roll back to" : "Activate"} revision {agent.snapshot_revision}
                              </button>
                            )}
                        </div>
                      ))}
                  </>
                ),
              },
            ]}
            actions={(item) => (
              <>
                {managed &&
                  item.owner_subject === identity.subject &&
                  writable && (
                    <button
                      onClick={() => setDialog({ kind: "publish", item })}
                    >
                      Publish
                    </button>
                  )}
                <Detail value={item} />
              </>
            )}
          />
        </DataState>
      </section>
      <LangflowResources
        project={project}
        admin={isAdministrator(identity)}
        projectOptions={projectOptions}
      />
      {dialog && (
        <Modal
          title={
            dialog.kind === "create"
              ? "Create Langflow workspace"
              : dialog.kind === "capture"
                ? "Capture saved flow"
                : "Publish flow snapshot"
          }
          onClose={() => setDialog(null)}
        >
          {dialog.kind === "create" ? (
            <ActionForm
              fields={[
                { name: "name", label: "Name", required: true },
                {
                  name: "project_id",
                  label: "Project",
                  type: "select",
                  options: projectOptions,
                  value: project || projectOptions[0]?.value,
                  required: true,
                },
                {
                  name: "cpu",
                  label: "CPU",
                  type: "number",
                  value: 1,
                  min: 1,
                  max: 32,
                },
                {
                  name: "memory_gb",
                  label: "Memory (GB)",
                  type: "number",
                  value: 2,
                  min: 1,
                  max: 128,
                },
                {
                  name: "storage_gb",
                  label: "Storage (GB)",
                  type: "number",
                  value: 10,
                  min: 1,
                  max: 1000,
                },
              ]}
              submit="Create workspace"
              onSubmit={async (body) => {
                await request("/api/v1/langflow-workspaces", {
                  method: "POST",
                  body,
                });
                setDialog(null);
                workspaces.reload();
              }}
            />
          ) : dialog.kind === "capture" ? (
            <CaptureFlowForm workspace={dialog.item!} flows={dialog.flows || []} snapshots={items(snapshots.data)} project={project} projectOptions={projectOptions} onSaved={() => { setDialog(null); snapshots.reload(); }} />
          ) : (
            <ActionForm
              fields={[
                {
                  name: "revision",
                  label: "Revision",
                  type: "select",
                  value: String(dialog.item?.revision),
                  options: [
                    dialog.item?.revision,
                    ...(dialog.item?.history || []).map(
                      (item: Entity) => item.revision,
                    ),
                  ].map((revision) => String(revision)),
                },
                {
                  name: "min_replicas",
                  label: "Minimum replicas",
                  type: "number",
                  value: 1,
                  min: 1,
                },
                {
                  name: "max_replicas",
                  label: "Maximum replicas",
                  type: "number",
                  value: 1,
                  min: 1,
                },
                { name: "cpu", label: "CPU", value: "500m" },
                { name: "memory", label: "Memory", value: "1Gi" },
              ]}
              submit="Publish"
              onSubmit={async (values) => {
                if (!Number.isInteger(values.min_replicas) || !Number.isInteger(values.max_replicas) || values.min_replicas < 1 || values.max_replicas < values.min_replicas) throw new Error("Maximum replicas must be at least the positive minimum replicas.");
                await request(
                  `/api/v1/langflow-snapshots/${encodeURIComponent(dialog.item?.id)}/publish`,
                  {
                    method: "POST",
                    body: {
                      revision: Number(values.revision),
                      autoscaling: {
                        min_replicas: values.min_replicas,
                        max_replicas: values.max_replicas,
                      },
                      resources: { cpu: values.cpu, memory: values.memory },
                    },
                  },
                );
                setDialog(null);
                agents.reload();
                snapshots.reload();
              }}
            />
          )}
        </Modal>
      )}
    </section>
  );
}
function LangflowResources({
  project,
  admin,
  projectOptions,
}: {
  project: string;
  admin: boolean;
  projectOptions: Array<{ value: string; label: string }>;
}) {
  const specs = [
    {
      key: "datasets",
      title: "Project datasets",
      path: "/api/v1/datasets",
      admin: "/api/v1/admin/datasets",
      fields: [
        { name: "name", label: "Name", required: true },
        { name: "bucket", label: "Bucket", required: true },
        { name: "prefix", label: "Object prefix" },
        { name: "description", label: "Description" },
      ],
    },
    {
      key: "registered-storage",
      title: "Registered storage",
      path: "/api/v1/registered-storage",
    },
    {
      key: "knowledge-bases",
      title: "Knowledge bases",
      path: "/api/v1/knowledge-bases",
      admin: "/api/v1/admin/knowledge-bases",
      fields: [
        { name: "name", label: "Name", required: true },
        {
          name: "backend",
          label: "Backend",
          type: "select",
          options: ["qdrant", "pgvector"],
          value: "qdrant",
        },
        { name: "endpoint", label: "Qdrant endpoint" },
        {
          name: "dimensions",
          label: "Dimensions",
          type: "number",
          value: 1536,
          min: 1,
        },
        { name: "secret_ref", label: "Qdrant secret reference" },
        { name: "dsn_ref", label: "PostgreSQL DSN reference" },
      ],
    },
    {
      key: "agent-api-connections",
      title: "Approved API connections",
      path: "/api/v1/agent-api-connections",
      admin: "/api/v1/admin/agent-api-connections",
      fields: [
        { name: "name", label: "Name", required: true },
        { name: "endpoint", label: "Endpoint", required: true },
        { name: "secret_ref", label: "Secret reference", required: true },
        { name: "description", label: "Description" },
      ],
    },
  ];
  return (
    <>
      {specs.map((spec) => (
        <ResourceSection
          key={spec.key}
          spec={spec}
          project={project}
          admin={admin}
          projectOptions={projectOptions}
        />
      ))}
    </>
  );
}
function ResourceSection({
  spec,
  project,
  admin,
  projectOptions,
}: {
  spec: Entity;
  project: string;
  admin: boolean;
  projectOptions: Array<{ value: string; label: string }>;
}) {
  const query = useQuery<Entity>(spec.path),
    [open, setOpen] = useState(false), [backend, setBackend] = useState("qdrant");
  return (
    <section>
      <header className="toolbar">
        <h2>{spec.title}</h2>
        {admin && spec.admin && (
          <button aria-label={`Register ${spec.title.toLowerCase()}`} onClick={() => setOpen(!open)}>Register</button>
        )}
      </header>
      <DataState
        loading={query.loading}
        error={query.error}
        onRetry={query.reload}
      >
        <ResourceTable
          rows={items(query.data).filter(
            (item) => !project || item.project_id === project,
          )}
          columns={[
            { key: "name", label: "Name" },
            { key: "id", label: "ID" },
            { key: "description", label: "Description" },
          ]}
          actions={(item) => <Detail value={item} />}
        />
      </DataState>
      {open && (<div className="panel">
        {spec.key === "knowledge-bases" && <label className="field">Backend<Select value={backend} onChange={event => setBackend(event.target.value)}><option value="qdrant">Qdrant</option><option value="pgvector">PostgreSQL pgvector</option></Select></label>}
        <ActionForm
          fields={
            [
              {
                name: "project_id",
                label: "Project",
                type: "select",
                options: projectOptions,
                value: project || projectOptions[0]?.value,
                required: true,
              },
              ...spec.fields.filter((field: Field) => spec.key !== "knowledge-bases" || (field.name !== "backend" && (backend === "pgvector" ? !["endpoint", "secret_ref"].includes(field.name) : field.name !== "dsn_ref"))),
            ] as Field[]
          }
          submit="Register"
          onSubmit={async (body) => {
            if (spec.key === "knowledge-bases") body.backend = backend;
            if (body.backend === "qdrant") delete body.dsn_ref;
            else if (body.backend === "pgvector") {
              delete body.endpoint;
              delete body.secret_ref;
            }
            await request(spec.admin, { method: "POST", body });
            setOpen(false);
            query.reload();
          }}
        />
      </div>)}
    </section>
  );
}

export function CaptureFlowForm({ workspace, flows, snapshots, project, projectOptions, onSaved }: { workspace: Entity; flows: Entity[]; snapshots: Entity[]; project: string; projectOptions: Array<{value:string;label:string}>; onSaved: () => void }) {
  const [flowID, setFlowID] = useState(flows[0]?.id || ''), [projectID, setProjectID] = useState(workspace.project_id || project || projectOptions[0]?.value || ''), [snapshotID,setSnapshotID] = useState(''), [busy,setBusy] = useState(false), [error,setError] = useState('');
  const matching = snapshots.filter(item => item.workspace_id === workspace.id && item.flow_id === flowID && item.project_id === projectID);
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setError('');
    try {
      const current = matching.find(item => item.id === snapshotID);
      if (snapshotID && !current) throw new Error('Select a snapshot for this flow and project.');
      await request(`/api/v1/langflow-workspaces/${encodeURIComponent(workspace.id)}/snapshots`, { method:'POST', body:{flow_id:flowID,project_id:projectID,snapshot_id:snapshotID,revision:current?.revision || 0} });
      onSaved();
    } catch(failure) { setError(errorMessage(failure)); } finally { setBusy(false); }
  }
  return <form onSubmit={save} aria-busy={busy}><label className="field">Saved flow<Select value={flowID} required disabled={busy} onChange={event => {setFlowID(event.target.value);setSnapshotID('');}}>{flows.map(flow=><option key={flow.id} value={flow.id}>{flow.name || flow.id}</option>)}</Select></label><label className="field">Project<Select value={projectID} required disabled={busy || !!workspace.project_id} onChange={event=>{setProjectID(event.target.value);setSnapshotID('');}}>{projectOptions.map(option=><option key={option.value} value={option.value}>{option.label}</option>)}</Select></label><label className="field">Snapshot<Select value={snapshotID} disabled={busy} onChange={event=>setSnapshotID(event.target.value)}><option value="">New snapshot</option>{matching.map(snapshot=><option key={snapshot.id} value={snapshot.id}>{snapshot.name} revision {snapshot.revision}</option>)}</Select></label><Feedback error={error}/><button type="submit" disabled={busy || !flowID || !projectID}>{busy ? 'Saving…' : 'Save snapshot revision'}</button></form>;
}
