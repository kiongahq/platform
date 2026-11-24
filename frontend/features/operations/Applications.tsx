"use client";
import { useCallback, useEffect, useState } from "react";
import { APIError, request } from "@/lib/api";
import {
  can,
  csv,
  date,
  encode,
  Facts,
  Form,
  items,
  Modal,
  projectField,
  scoped,
  State,
  Status,
  Table,
  useAction,
  useResource,
  type Field,
  type OperationsProps,
  type RecordData,
} from "./shared";

type NotConfigured = { message: string; steps: string[] };

/** Applications list state: the list, or the 409 not_configured answer. */
export function useApplications() {
  const [revision, setRevision] = useState(0);
  const [state, setState] = useState<{
    loading: boolean;
    error: string;
    data?: RecordData;
    notConfigured?: NotConfigured;
  }>({ loading: true, error: "" });
  const reload = useCallback(() => setRevision((value) => value + 1), []);
  useEffect(() => {
    window.addEventListener("kionga:refresh", reload);
    return () => window.removeEventListener("kionga:refresh", reload);
  }, [reload]);
  useEffect(() => {
    const controller = new AbortController();
    setState((old) => ({ ...old, loading: true, error: "" }));
    request<RecordData>("/api/v1/applications", { signal: controller.signal })
      .then((data) => setState({ loading: false, error: "", data }))
      .catch((error) => {
        if (controller.signal.aborted) return;
        if (error instanceof APIError && error.status === 409 && error.code === "not_configured")
          setState({ loading: false, error: "", notConfigured: { message: error.message, steps: error.body.steps || [] } });
        else setState({ loading: false, error: (error as Error).message });
      });
    return () => controller.abort();
  }, [revision]);
  return { ...state, reload };
}

const MAX_RUN_AS_USER = 2147483647;

/** Turns the form's flat values into the API request. Throws on a bad user ID. */
export function applicationRequest(value: RecordData): RecordData {
  const body: RecordData = {
    name: String(value.name || "").trim(),
    project_id: value.project_id,
    port: Number(value.port || 8080),
    env: value.env || {},
    secret_refs: csv(value.secret_refs),
    resources: { cpu: value.cpu, memory: value.memory },
    replicas: { min: Number(value.min_replicas ?? 1), max: Number(value.max_replicas || 1) },
    scaling: { metric: value.metric, target: Number(value.target || 0) },
    health_path: value.health_path,
    visibility: value.visibility,
  };
  // Empty means the image's own user. Anything else must be a valid user:
  // dropping a bad value would clear the user on a replace.
  const user = String(value.run_as_user ?? "").trim();
  if (user !== "") {
    if (!/^[0-9]{1,10}$/.test(user) || Number(user) < 1 || Number(user) > MAX_RUN_AS_USER)
      throw new Error(`Run as user ID must be a whole number from 1 to ${MAX_RUN_AS_USER}. Root (0) is never allowed.`);
    body.run_as_user = Number(user);
  }
  if (value.source_type === "git")
    body.source = { git: value.git, ref: value.ref, context_dir: value.context_dir, dockerfile: value.dockerfile };
  else body.image = value.image;
  return body;
}

/** Turns an application into the form's flat values, to edit it. */
export function applicationFormValues(app: RecordData): RecordData {
  return {
    name: app.name,
    project_id: app.project_id,
    source_type: app.source ? "git" : "image",
    image: app.image || "",
    git: app.source?.git || "",
    ref: app.source?.ref || "",
    context_dir: app.source?.context_dir || "",
    dockerfile: app.source?.dockerfile || "Dockerfile",
    port: app.port ?? 8080,
    health_path: app.health_path || "/",
    run_as_user: app.run_as_user ?? "",
    cpu: app.resources?.cpu || "",
    memory: app.resources?.memory || "",
    min_replicas: app.replicas?.min ?? 1,
    max_replicas: app.replicas?.max ?? 1,
    metric: app.scaling?.metric || "requests",
    target: app.scaling?.target ?? 100,
    visibility: app.visibility || "sso",
    secret_refs: (app.secret_refs || []).join(", "),
    env: JSON.stringify(app.env || {}, null, 2),
  };
}

/** The create form's fields. Editing fixes the name and the project. */
function applicationFields(projects: RecordData[], sourceType: string, editing = false): Field[] {
  const source: Field[] =
    sourceType === "git"
      ? [
          { name: "git", label: "Git repository", required: true, placeholder: "https://github.com/acme/billing.git" },
          { name: "ref", label: "Branch, tag or commit", placeholder: "main" },
          { name: "context_dir", label: "Context directory", placeholder: "." },
          { name: "dockerfile", label: "Dockerfile", placeholder: "Dockerfile" },
        ]
      : [{ name: "image", label: "OCI image", required: true, placeholder: "ghcr.io/acme/billing:1.4" }];
  const identity: Field[] = editing
    ? [{ name: "name", label: "Name", readOnly: true, help: "The name is the host name and cannot change." }]
    : [
        projectField(projects),
        { name: "name", label: "Name", required: true, help: "Lowercase letters, digits and hyphens. It becomes the host name." },
      ];
  return [
    ...identity,
    {
      name: "source_type",
      label: "Source",
      type: "select",
      options: [
        { value: "image", label: "Container image" },
        { value: "git", label: "Git repository (built in the cluster)" },
      ],
    },
    ...source,
    { name: "port", label: "Container port", type: "number", min: 1, max: 65535 },
    { name: "health_path", label: "Health path", help: "Used for readiness, liveness and startup probes." },
    {
      name: "run_as_user",
      label: "Run as user ID",
      placeholder: "The image's own user",
      help: "Optional. Needed when the image runs as root or names its user instead of numbering it: Kubernetes refuses to start such an image. Enter a non-root user ID such as 1000. Root (0) is never allowed.",
    },
    { name: "cpu", label: "CPU per replica" },
    { name: "memory", label: "Memory per replica" },
    { name: "min_replicas", label: "Minimum replicas", type: "number", min: 0, max: 100, help: "0 scales to zero when idle (requests metric only)." },
    { name: "max_replicas", label: "Maximum replicas", type: "number", min: 1, max: 100 },
    {
      name: "metric",
      label: "Scale on",
      type: "select",
      options: [
        { value: "requests", label: "Concurrent requests" },
        { value: "cpu", label: "CPU utilisation" },
      ],
    },
    { name: "target", label: "Target per replica", type: "number", min: 1, help: "Requests per replica, or CPU percent." },
    {
      name: "visibility",
      label: "Visibility",
      type: "select",
      options: [
        { value: "sso", label: "Signed-in users (single sign-on)" },
        { value: "public", label: "Public" },
        { value: "internal", label: "Internal only (no address)" },
      ],
    },
    { name: "secret_refs", label: "Secrets", help: "Kubernetes Secret names, comma separated. Their keys become environment variables." },
    { name: "env", label: "Environment variables", type: "json" },
  ];
}

export function NotConfiguredPanel({ value }: { value: NotConfigured }) {
  return (
    <section className="panel" aria-labelledby="applications-not-configured">
      <h2 id="applications-not-configured">Applications are not enabled on this install</h2>
      <p>{value.message}</p>
      {value.steps.length > 0 && (
        <>
          <p>To enable them, an administrator:</p>
          <ol>
            {value.steps.map((step) => (
              <li key={step}>{step}</li>
            ))}
          </ol>
        </>
      )}
    </section>
  );
}

export function Applications({ identity, project, resource, onNavigate }: OperationsProps) {
  const query = useApplications(),
    projects = useResource("/api/v1/project-options");
  const [creating, setCreating] = useState(false),
    [sourceType, setSourceType] = useState("image");
  const action = useAction(query.reload);
  const writable = can(identity, "applications_write");
  const rows = scoped(items(query.data), project);
  const selected = resource ? rows.find((row) => row.name === resource) : undefined;
  if (query.notConfigured) return <NotConfiguredPanel value={query.notConfigured} />;
  return (
    <>
      <header className="page-header">
        <p>
          Your services, each at its own address under{" "}
          <code>{query.data?.base_domain || "the applications domain"}</code>, scaled and restarted by Kubernetes.
        </p>
        <button className="primary" id="create-application" disabled={!writable} onClick={() => setCreating(true)}>
          Deploy application
        </button>
      </header>
      {action.feedback}
      <State loading={query.loading && !query.data} error={query.error} retry={query.reload}>
        <Table
          rows={rows}
          rowKey={(row) => row.name}
          columns={[
            {
              key: "name",
              label: "Application",
              render: (row) => (
                <button className="text-button" onClick={() => onNavigate("applications", row.name)}>
                  {row.name}
                </button>
              ),
            },
            { key: "status", label: "Status", render: (row) => <Status value={row.status} /> },
            {
              key: "url",
              label: "Address",
              render: (row) =>
                row.url ? (
                  <a href={row.url} target="_blank" rel="noopener noreferrer">
                    {row.url.replace(/^https?:\/\//, "")}
                  </a>
                ) : (
                  "—"
                ),
            },
            {
              key: "ready_replicas",
              label: "Replicas",
              render: (row) => `${row.ready_replicas ?? 0} ready (${row.replicas?.min ?? 0}–${row.replicas?.max ?? 0})`,
            },
            {
              key: "scaling",
              label: "Scaling",
              render: (row) => (row.scaling?.metric === "cpu" ? `CPU ${row.scaling.target}%` : `${row.scaling?.target ?? "—"} requests`),
            },
            { key: "updated_at", label: "Updated", render: (row) => date(row.updated_at) },
          ]}
          actions={(row) => (
            <>
              <button onClick={() => onNavigate("applications", row.name)}>Details</button>
              <button
                disabled={!writable || action.busy}
                onClick={() =>
                  void action
                    .run(() => request(`/api/v1/applications/${encode(row.name)}/redeploy`, { method: "POST" }), `Redeploying ${row.name}.`)
                    .catch(() => {})
                }
              >
                Redeploy
              </button>
            </>
          )}
        />
      </State>
      {creating && (
        <Modal title="Deploy application" onClose={() => setCreating(false)}>
          <Form
            initial={{
              project_id: project,
              source_type: "image",
              port: 8080,
              health_path: "/",
              cpu: "250m",
              memory: "256Mi",
              min_replicas: 1,
              max_replicas: 3,
              metric: "requests",
              target: 100,
              visibility: "sso",
              ref: "main",
              dockerfile: "Dockerfile",
              env: "{}",
            }}
            fields={applicationFields(items(projects.data), sourceType)}
            onChange={(value) => setSourceType(value.source_type || "image")}
            submit="Deploy application"
            onSubmit={async (value) => {
              const created = await request<RecordData>("/api/v1/applications", { method: "POST", body: applicationRequest(value) });
              setCreating(false);
              query.reload();
              onNavigate("applications", created.name);
            }}
          />
        </Modal>
      )}
      {resource && (
        <ApplicationDetail
          name={resource}
          summary={selected}
          writable={writable}
          onClose={() => onNavigate("applications", "")}
          onChanged={query.reload}
        />
      )}
    </>
  );
}

function ApplicationDetail({
  name,
  summary,
  writable,
  onClose,
  onChanged,
}: {
  name: string;
  summary?: RecordData;
  writable: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState(false),
    [sourceType, setSourceType] = useState("image");
  const detail = useResource(`/api/v1/applications/${encode(name)}`),
    logs = useResource(`/api/v1/applications/${encode(name)}/logs?tail=200`),
    events = useResource(`/api/v1/applications/${encode(name)}/events`);
  const action = useAction(() => {
    detail.reload();
    events.reload();
    onChanged();
  });
  const app = detail.data || summary || {};
  return (
    <Modal title={name} onClose={onClose}>
      {action.feedback}
      <div className="button-row">
        {app.url && (
          <a className="button" href={app.url} target="_blank" rel="noopener noreferrer">
            Open application
          </a>
        )}
        <button
          disabled={!writable || action.busy}
          onClick={() =>
            void action.run(() => request(`/api/v1/applications/${encode(name)}/redeploy`, { method: "POST" }), "Redeploy requested.").catch(() => {})
          }
        >
          Redeploy
        </button>
        <button
          disabled={!writable || action.busy || !detail.data || editing}
          onClick={() => {
            setSourceType(app.source ? "git" : "image");
            setEditing(true);
          }}
        >
          Edit
        </button>
        <button
          className="danger"
          disabled={!writable || action.busy}
          onClick={() => {
            if (!window.confirm(`Delete ${name}? Its pods, address and autoscaler are removed.`)) return;
            void action
              .run(() => request(`/api/v1/applications/${encode(name)}`, { method: "DELETE" }), "Application deleted.")
              .then(onClose)
              .catch(() => {});
          }}
        >
          Delete
        </button>
      </div>
      {editing && (
        <section aria-labelledby="application-edit">
          <h3 id="application-edit">Edit configuration</h3>
          <p>Saving rolls out new pods. The address does not change.</p>
          <Form
            initial={applicationFormValues(app)}
            fields={applicationFields([], sourceType, true)}
            onChange={(value) => setSourceType(value.source_type || "image")}
            submit="Save and roll out"
            onSubmit={async (value) => {
              await request(`/api/v1/applications/${encode(name)}`, { method: "PUT", body: applicationRequest(value) });
              setEditing(false);
              detail.reload();
              events.reload();
              onChanged();
            }}
          >
            <button type="button" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </Form>
        </section>
      )}
      <State loading={detail.loading && !detail.data} error={detail.error} retry={detail.reload}>
        <section aria-labelledby="application-status">
          <h3 id="application-status">
            Status <Status value={app.status} />
          </h3>
          {app.message && <p className="feedback">{app.message}</p>}
          <Facts
            value={{
              url: app.url,
              ready_replicas: app.ready_replicas,
              replicas: `${app.replicas?.min ?? 0}–${app.replicas?.max ?? 0}`,
              image: app.image || (app.source ? `${app.source.git}@${app.source.ref}` : ""),
              port: app.port,
              health_path: app.health_path,
              run_as_user: app.run_as_user ?? "Image default",
              visibility: app.visibility,
              redeployed_at: app.redeployed_at,
            }}
          />
          {(app.conditions || []).length > 0 && (
            <Table
              rows={app.conditions}
              rowKey={(row) => row.type}
              columns={[
                { key: "type", label: "Condition" },
                { key: "status", label: "Status" },
                { key: "reason", label: "Reason" },
                { key: "message", label: "Message" },
              ]}
            />
          )}
        </section>
      </State>
      <section aria-labelledby="application-logs">
        <header className="page-header">
          <h3 id="application-logs">Logs</h3>
          <button onClick={logs.reload}>Refresh logs</button>
        </header>
        <State loading={logs.loading && !logs.data} error={logs.error} retry={logs.reload}>
          {items(logs.data).length === 0 ? (
            <p className="empty-state">No running pods. An application scaled to zero has no logs until it receives a request.</p>
          ) : (
            items(logs.data).map((entry) => (
              <details key={entry.pod} open>
                <summary>
                  {entry.pod} <Status value={String(entry.phase || "").toLowerCase()} />
                </summary>
                {entry.error && <p className="feedback error">{entry.error}</p>}
                <pre className="log-output" tabIndex={0} aria-label={`Logs of ${entry.pod}`}>
                  {entry.lines || "No output."}
                </pre>
              </details>
            ))
          )}
        </State>
      </section>
      <section aria-labelledby="application-events">
        <h3 id="application-events">Events</h3>
        <State loading={events.loading && !events.data} error={events.error} retry={events.reload}>
          <Table
            rows={items(events.data)}
            columns={[
              { key: "last_seen", label: "Last seen", render: (row) => date(row.last_seen) },
              { key: "type", label: "Type" },
              { key: "reason", label: "Reason" },
              { key: "object", label: "Object" },
              { key: "message", label: "Message" },
            ]}
          />
        </State>
      </section>
    </Modal>
  );
}
