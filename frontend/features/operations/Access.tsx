"use client";
import { AccessGuide } from "@/components/console/AccessGuide";
import { Select } from "@/components/ui/Select";
import { CodeEditor } from "@/components/ui/CodeEditor";
import { useEffect, useState, type FormEvent } from "react";
import { request } from "@/lib/api";
import {
  csv,
  date,
  Detail,
  encode,
  Facts,
  Form,
  items,
  Modal,
  option,
  services,
  State,
  Status,
  Table,
  useAction,
  useResource,
  type OperationsProps,
  type RecordData,
  type Field,
} from "./shared";
const userFields: Field[] = [
  { name: "subject", label: "Account subject", required: true },
  { name: "email", label: "Email" },
  {
    name: "role",
    label: "Role",
    type: "select",
    options: ["user", "viewer", "operator", "admin"].map(option),
  },
  {
    name: "services",
    label: "Assigned services",
    type: "multiselect",
    options: services.map(option),
  },
  { name: "project_ids", label: "Assigned project IDs" },
  { name: "storage_gb", label: "Storage GB", type: "number", min: 0 },
  { name: "buckets", label: "Granted buckets" },
  { name: "vcpus", label: "vCPU", type: "number", min: 0 },
  { name: "memory_gb", label: "Memory GB", type: "number", min: 0 },
  { name: "gpus", label: "GPUs", type: "number", min: 0 },
  { name: "gpu_type", label: "GPU type" },
  { name: "max_vms", label: "Maximum workspaces", type: "number", min: 0 },
  { name: "max_projects", label: "Maximum projects", type: "number", min: 0 },
  {
    name: "max_runs",
    label: "Concurrent pipeline runs",
    type: "number",
    min: 0,
  },
  { name: "max_functions", label: "Maximum functions", type: "number", min: 0 },
  {
    name: "local_password",
    label: "Local password",
    type: "password",
    help: "Optional. Existing passwords remain unchanged when empty.",
  },
  { name: "disabled", label: "Suspend account", type: "checkbox" },
];
export function Access({ identity }: OperationsProps) {
  const users = useResource("/api/v1/admin/users"),
    requests = useResource("/api/v1/admin/access-requests"),
    profiles = useResource("/api/v1/admin/resource-profiles");
  const [tab, setTab] = useState("users"),
    [edit, setEdit] = useState<RecordData | null>(null),
    [profile, setProfile] = useState("custom"),
    [reject, setReject] = useState<RecordData | null>(null);
  const action = useAction(() => {
    users.reload();
    requests.reload();
  });
  useEffect(() => {
    const read = () => {
      const value = new URLSearchParams(location.search).get("tab");
      if (
        value &&
        ["users", "groups", "policies", "attachments", "simulator"].includes(
          value,
        )
      )
        setTab(value);
    };
    read();
    window.addEventListener("popstate", read);
    return () => window.removeEventListener("popstate", read);
  }, []);
  function open(entry: RecordData) {
    setEdit(entry);
    setProfile(entry.compute?.profile || "starter");
  }
  return (
    <>
      <AccessGuide />
      <div className="tabs" role="tablist" aria-label="Access management">
        {["users", "groups", "policies", "attachments", "simulator"].map(
          (value) => (
            <button
              role="tab"
              key={value}
              aria-selected={value === tab}
              onClick={() => {
                setTab(value);
                const url = new URL(location.href);
                url.searchParams.set("tab", value);
                history.replaceState(null, "", url);
              }}
            >
              {value[0].toUpperCase() + value.slice(1)}
            </button>
          ),
        )}
      </div>
      {action.feedback}
      {tab === "users" ? (
        <>
          <section className="panel">
            <header className="page-header">
              <h2>Users and resource grants</h2>
              <button className="primary" onClick={() => open({})}>
                Provision user
              </button>
            </header>
            <State {...users} retry={users.reload}>
              <Table
                rows={items(users.data)}
                columns={[
                  {
                    key: "email",
                    label: "Identity",
                    render: (row) => (
                      <>
                        {row.email || row.subject}
                        <small>{row.subject}</small>
                      </>
                    ),
                  },
                  { key: "role", label: "Role" },
                  {
                    key: "services",
                    label: "Services",
                    render: (row) => (row.services || []).join(", "),
                  },
                  {
                    key: "project_ids",
                    label: "Projects",
                    render: (row) => (row.project_ids || []).join(", "),
                  },
                  {
                    key: "compute",
                    label: "Capacity",
                    render: (row) =>
                      `${row.compute?.vcpus || 0} vCPU, ${row.compute?.memory_gb || 0} GB RAM, ${row.storage?.size_gb || 0} GB storage`,
                  },
                  {
                    key: "disabled",
                    label: "Status",
                    render: (row) => (
                      <Status value={row.disabled ? "suspended" : "active"} />
                    ),
                  },
                ]}
                actions={(row) => (
                  <>
                    <button onClick={() => open(row)}>Edit</button>
                    <button
                      className="danger"
                      disabled={action.busy}
                      onClick={() => {
                        if (
                          window.confirm(
                            `Revoke all access for ${row.subject}? Their sessions stop working on the next request.`,
                          )
                        )
                          void action
                            .run(async () => {
                              await request(
                                `/api/v1/admin/users/${encode(row.subject)}`,
                                { method: "DELETE" },
                              );
                              if (
                                identity.local_accounts ??
                                identity.mode === "local"
                              )
                                await request(
                                  `/api/v1/admin/users/${encode(row.subject)}/local-password`,
                                  { method: "DELETE" },
                                ).catch(() => {});
                            }, "Access revoked.")
                            .catch(() => {});
                      }}
                    >
                      Revoke access
                    </button>
                  </>
                )}
              />
            </State>
          </section>
          <section className="panel">
            <h2>Access requests</h2>
            <State {...requests} retry={requests.reload}>
              <Table
                rows={items(requests.data)}
                columns={[
                  { key: "subject", label: "Subject" },
                  {
                    key: "requested_services",
                    label: "Requested services",
                    render: (row) => (row.requested_services || []).join(", "),
                  },
                  { key: "reason", label: "Reason" },
                  {
                    key: "status",
                    label: "Status",
                    render: (row) => <Status value={row.status} />,
                  },
                  {
                    key: "created_at",
                    label: "Requested",
                    render: (row) => date(row.created_at),
                  },
                ]}
                actions={(row) =>
                  row.status === "pending" ? (
                    <>
                      <button
                        onClick={() =>
                          open({
                            subject: row.subject,
                            email: row.email,
                            services: row.requested_services,
                            request_id: row.id,
                          })
                        }
                      >
                        Review and provision
                      </button>
                      <button className="danger" onClick={() => setReject(row)}>
                        Reject
                      </button>
                    </>
                  ) : null
                }
              />
            </State>
          </section>
        </>
      ) : (
        <IAM tab={tab} />
      )}
      {edit && (
        <Modal
          title={edit.subject ? `Provision ${edit.subject}` : "Provision user"}
          onClose={() => setEdit(null)}
        >
          <label className="field">
            Resource profile
            <Select
              value={profile}
              onChange={(event) => setProfile(event.target.value)}
            >
              <option value="custom">Custom allocation</option>
              {items(profiles.data)
                .filter((row) => row.name !== "custom")
                .map((row) => (
                  <option key={row.name} value={row.name}>
                    {row.label || row.name}
                  </option>
                ))}
            </Select>
          </label>
          {(() => {
            const preset = items(profiles.data).find(
              (row) => row.name === profile,
            );
            const compute =
              profile === "custom"
                ? edit.compute || {}
                : preset?.compute || edit.compute || {};
            const initial = {
              subject: edit.subject || "",
              email: edit.email || "",
              role: edit.role || "user",
              services: edit.services || [],
              project_ids: (edit.project_ids || []).join(", "),
              buckets: (edit.storage?.buckets || []).join(", "),
              storage_gb:
                profile === "custom"
                  ? edit.storage?.size_gb || 0
                  : (preset?.storage_gb ?? edit.storage?.size_gb ?? 25),
              vcpus: compute.vcpus ?? 2,
              memory_gb: compute.memory_gb ?? 4,
              gpus: compute.gpus || 0,
              gpu_type: compute.gpu_type || "",
              max_vms: compute.max_vms ?? 1,
              max_projects: compute.max_projects ?? 2,
              max_runs: compute.max_concurrent_runs ?? 1,
              max_functions: compute.max_functions ?? 3,
              disabled: !!edit.disabled,
            };
            return (
              <Form
                key={profile}
                fields={userFields
                  .filter(
                    (field) =>
                      field.name !== "local_password" ||
                      (identity.local_accounts ?? identity.mode === "local"),
                  )
                  .map((field) => ({
                    ...field,
                    readOnly:
                      (field.name === "subject" &&
                        items(users.data).some(
                          (row) => row.subject === edit.subject,
                        )) ||
                      (profile !== "custom" &&
                        [
                          "storage_gb",
                          "vcpus",
                          "memory_gb",
                          "gpus",
                          "gpu_type",
                          "max_vms",
                          "max_projects",
                          "max_runs",
                          "max_functions",
                        ].includes(field.name)),
                  }))}
                initial={initial}
                submit="Save access"
                onSubmit={async (value) => {
                  const subject = value.subject.trim();
                  const body = {
                    email: value.email,
                    role: value.role,
                    services: value.services,
                    project_ids: csv(value.project_ids),
                    storage: {
                      size_gb: value.storage_gb,
                      buckets: csv(value.buckets),
                    },
                    compute: {
                      profile,
                      vcpus: value.vcpus,
                      memory_gb: value.memory_gb,
                      gpus: value.gpus,
                      gpu_type: value.gpu_type,
                      max_vms: value.max_vms,
                      max_projects: value.max_projects,
                      max_concurrent_runs: value.max_runs,
                      max_functions: value.max_functions,
                    },
                    disabled: !!value.disabled,
                  };
                  await request(`/api/v1/admin/users/${encode(subject)}`, {
                    method: "PUT",
                    body,
                  });
                  if (value.local_password)
                    await request(
                      `/api/v1/admin/users/${encode(subject)}/local-password`,
                      {
                        method: "PUT",
                        body: { password: value.local_password },
                      },
                    );
                  if (edit.request_id)
                    await request(
                      `/api/v1/admin/access-requests/${encode(edit.request_id)}`,
                      {
                        method: "PATCH",
                        body: {
                          status: "approved",
                          note: "Provisioned through the admin console",
                        },
                      },
                    );
                  setEdit(null);
                  users.reload();
                  requests.reload();
                }}
              />
            );
          })()}
        </Modal>
      )}
      {reject && (
        <Modal title="Reject access request" onClose={() => setReject(null)}>
          <Form
            fields={[
              {
                name: "note",
                label: "Reason shown to requester",
                type: "textarea",
                required: true,
              },
            ]}
            submit="Reject request"
            onSubmit={async (value) => {
              await request(
                `/api/v1/admin/access-requests/${encode(reject.id)}`,
                {
                  method: "PATCH",
                  body: { status: "rejected", note: value.note },
                },
              );
              setReject(null);
              requests.reload();
            }}
          />
        </Modal>
      )}
    </>
  );
}

function IAM({ tab }: { tab: string }) {
  const groups = useResource("/api/v1/admin/iam/groups"),
    policies = useResource("/api/v1/admin/iam/policies"),
    attachments = useResource("/api/v1/admin/iam/attachments"),
    catalog = useResource("/api/v1/iam/catalog");
  const [group, setGroup] = useState<RecordData | null>(null),
    [policy, setPolicy] = useState<RecordData | null>(null),
    [history, setHistory] = useState<RecordData[] | null>(null),
    [selectedPolicy, setSelectedPolicy] = useState<RecordData | null>(null),
    [explanation, setExplanation] = useState<RecordData | null>(null),
    [effective, setEffective] = useState<RecordData | null>(null);
  const action = useAction(() => {
    groups.reload();
    policies.reload();
    attachments.reload();
  });
  return (
    <>
      {action.feedback}
      {tab === "groups" && (
        <section className="panel">
          <header className="page-header">
            <h2>Groups</h2>
            <button className="primary" onClick={() => setGroup({})}>
              New group
            </button>
          </header>
          <State {...groups} retry={groups.reload}>
            <Table
              rows={items(groups.data)}
              columns={[
                { key: "name", label: "Group" },
                { key: "id", label: "ID" },
                {
                  key: "members",
                  label: "Members",
                  render: (row) => (row.members || []).join(", "),
                },
                {
                  key: "policies",
                  label: "Policies",
                  render: (row) =>
                    items(attachments.data)
                      .filter(
                        (item) =>
                          item.principal_type === "group" &&
                          item.principal_id === row.id,
                      )
                      .map((item) => item.policy_id)
                      .join(", "),
                },
                {
                  key: "updated_at",
                  label: "Updated",
                  render: (row) => date(row.updated_at),
                },
              ]}
              actions={(row) => (
                <>
                  <button onClick={() => setGroup(row)}>Edit</button>
                  <button
                    className="danger"
                    onClick={() => {
                      if (
                        window.confirm(
                          `Delete group ${row.id}? Detach its policies first.`,
                        )
                      )
                        void action
                          .run(
                            () =>
                              request(
                                `/api/v1/admin/iam/groups/${encode(row.id)}`,
                                { method: "DELETE" },
                              ),
                            "Group deleted.",
                          )
                          .catch(() => {});
                    }}
                  >
                    Delete
                  </button>
                </>
              )}
            />
          </State>
        </section>
      )}
      {tab === "policies" && (
        <section className="panel">
          <header className="page-header">
            <h2>Policies</h2>
            <button className="primary" onClick={() => setPolicy({})}>
              New policy
            </button>
          </header>
          <State {...policies} retry={policies.reload}>
            <Table
              rows={items(policies.data)}
              columns={[
                { key: "name", label: "Policy" },
                { key: "id", label: "ID" },
                { key: "version", label: "Version" },
                {
                  key: "statements",
                  label: "Statements",
                  render: (row) =>
                    `${row.statements?.length || 0} statements, ${(row.statements || []).filter((statement: RecordData) => statement.effect === "deny").length} deny`,
                },
                {
                  key: "managed",
                  label: "Source",
                  render: (row) =>
                    row.managed ? "Built-in role baseline" : "Custom policy",
                },
              ]}
              actions={(row) => (
                <>
                  <button
                    onClick={() =>
                      row.managed ? setSelectedPolicy(row) : setPolicy(row)
                    }
                  >
                    {row.managed ? "View" : "Edit"}
                  </button>
                  {!row.managed && (
                    <>
                      <button
                        onClick={() =>
                          void action
                            .run(
                              () =>
                                request<RecordData>(
                                  `/api/v1/admin/iam/policies/${encode(row.id)}/revisions`,
                                ),
                              "History loaded.",
                            )
                            .then((value) => {
                              setSelectedPolicy(row);
                              setHistory(items(value));
                            })
                            .catch(() => {})
                        }
                      >
                        History
                      </button>
                      <button
                        className="danger"
                        onClick={() => {
                          if (
                            window.confirm(
                              `Delete policy ${row.id}? Immutable revisions remain in history.`,
                            )
                          )
                            void action
                              .run(
                                () =>
                                  request(
                                    `/api/v1/admin/iam/policies/${encode(row.id)}`,
                                    { method: "DELETE" },
                                  ),
                                "Policy deleted.",
                              )
                              .catch(() => {});
                        }}
                      >
                        Delete
                      </button>
                    </>
                  )}
                </>
              )}
            />
          </State>
        </section>
      )}
      {tab === "attachments" && (
        <section className="panel">
          <h2>Policy attachments</h2>
          <Form
            fields={[
              {
                name: "policy_id",
                label: "Policy",
                type: "select",
                required: true,
                options: [
                  { value: "", label: "Choose a policy" },
                  ...items(policies.data)
                    .filter((row) => !row.managed)
                    .map((row) => ({ value: row.id, label: row.name })),
                ],
              },
              {
                name: "principal_type",
                label: "Principal type",
                type: "select",
                options: ["user", "group"].map(option),
              },
              {
                name: "principal_id",
                label: "Subject or group ID",
                required: true,
              },
            ]}
            initial={{ principal_type: "user" }}
            submit="Attach policy"
            onSubmit={async (body) => {
              await request("/api/v1/admin/iam/attachments", {
                method: "POST",
                body,
              });
              attachments.reload();
            }}
          />
          <State {...attachments} retry={attachments.reload}>
            <Table
              rows={items(attachments.data)}
              columns={[
                { key: "policy_id", label: "Policy" },
                { key: "principal_type", label: "Principal type" },
                { key: "principal_id", label: "Principal" },
                { key: "created_by", label: "Attached by" },
                {
                  key: "created_at",
                  label: "Attached",
                  render: (row) => date(row.created_at),
                },
              ]}
              actions={(row) => (
                <button
                  className="danger"
                  onClick={() => {
                    if (
                      window.confirm(
                        "Detach this policy? Its grants and denials stop applying on the next request.",
                      )
                    )
                      void action
                        .run(
                          () =>
                            request(
                              `/api/v1/admin/iam/attachments/${encode(row.id)}`,
                              { method: "DELETE" },
                            ),
                          "Policy detached.",
                        )
                        .catch(() => {});
                  }}
                >
                  Detach
                </button>
              )}
            />
          </State>
        </section>
      )}
      {tab === "simulator" && (
        <section className="panel">
          <h2>Access simulator</h2>
          <p>
            Evaluate an action and resource using effective role, group and
            policy bindings.
          </p>
          <Form
            fields={[
              {
                name: "action",
                label: "Action",
                type: "select",
                required: true,
                options: (catalog.data?.actions || []).map(
                  (item: RecordData) => ({
                    value: item.action,
                    label: item.action,
                  }),
                ),
              },
              {
                name: "resource",
                label: "Resource",
                required: true,
                placeholder: "kionga:project/project-id/pipeline/pipeline-id",
              },
              {
                name: "principal",
                label: "Account subject",
                help: "Empty evaluates the current caller.",
              },
              { name: "ip", label: "Source IP address" },
              {
                name: "resource_profile",
                label: "Resource profile",
                type: "select",
                options: [
                  { value: "", label: "Caller's profile" },
                  ...(catalog.data?.profiles || []).map(option),
                ],
              },
            ]}
            submit="Explain access"
            onSubmit={async (value) => {
              const query = new URLSearchParams(
                Object.entries(value)
                  .filter(([, value]) => !!value)
                  .map(([key, value]) => [key, String(value)]),
              );
              const result = await request<RecordData>(
                `/api/v1/iam/explain?${query}`,
              );
              setExplanation(result);
              setEffective(
                await request<RecordData>(
                  `/api/v1/iam/effective${value.principal ? `?principal=${encode(value.principal)}` : ""}`,
                ),
              );
            }}
          />
          {explanation && (
            <>
              <h3>Decision</h3>
              <Status
                value={explanation.decision?.allowed ? "Allowed" : "Denied"}
              />
              <Facts value={explanation} />
              <h3>Effective policies</h3>
              <Detail value={effective} />
            </>
          )}
        </section>
      )}
      {group && (
        <Modal
          title={group.id ? `Edit ${group.name}` : "New group"}
          onClose={() => setGroup(null)}
        >
          <Form
            initial={{
              id: group.id || "",
              name: group.name || "",
              description: group.description || "",
              members: (group.members || []).join("\n"),
            }}
            fields={[
              { name: "id", label: "Group ID", readOnly: !!group.id },
              { name: "name", label: "Name", required: true },
              { name: "description", label: "Description" },
              {
                name: "members",
                label: "Members",
                type: "textarea",
                help: "Subjects separated by commas or new lines.",
              },
            ]}
            onSubmit={async (value) => {
              const body: RecordData = {
                name: value.name,
                description: value.description,
                members: csv(value.members),
              };
              if (!group.id && value.id) body.id = value.id;
              await request(
                group.id
                  ? `/api/v1/admin/iam/groups/${encode(group.id)}`
                  : "/api/v1/admin/iam/groups",
                { method: group.id ? "PUT" : "POST", body },
              );
              setGroup(null);
              groups.reload();
            }}
          />
        </Modal>
      )}
      {policy && (
        <PolicyEditor
          policy={policy}
          onClose={() => setPolicy(null)}
          onSaved={() => {
            setPolicy(null);
            policies.reload();
          }}
        />
      )}
      {selectedPolicy && (
        <Modal
          title={history ? "Policy revision history" : selectedPolicy.name}
          onClose={() => {
            setSelectedPolicy(null);
            setHistory(null);
          }}
        >
          {history ? (
            history.map((revision) => (
              <details key={revision.version}>
                <summary>
                  Version {revision.version}, {revision.author},{" "}
                  {date(revision.created_at)}
                </summary>
                <Detail value={revision.policy} />
                <button
                  onClick={() => {
                    setPolicy({
                      ...selectedPolicy,
                      ...revision.policy,
                      version: selectedPolicy.version,
                      id: selectedPolicy.id,
                    });
                    setHistory(null);
                    setSelectedPolicy(null);
                  }}
                >
                  Load into editor
                </button>
              </details>
            ))
          ) : (
            <>
              <p>
                Built-in policies are shown unscoped; effective grants still
                apply project and service boundaries.
              </p>
              <Facts value={selectedPolicy} />
            </>
          )}
        </Modal>
      )}
    </>
  );
}

function PolicyEditor({
  policy,
  onClose,
  onSaved,
}: {
  policy: RecordData;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [mode, setMode] = useState("form"),
    [name, setName] = useState(policy.name || ""),
    [id, setId] = useState(policy.id || ""),
    [description, setDescription] = useState(policy.description || ""),
    [statements, setStatements] = useState<RecordData[]>(
      policy.statements || [],
    ),
    [json, setJson] = useState(""),
    [error, setError] = useState(""),
    [details, setDetails] = useState<RecordData[]>([]),
    [busy, setBusy] = useState(false);
  function changeMode(next: string) {
    setError("");
    try {
      if (next === "json")
        setJson(JSON.stringify({ name, description, statements }, null, 2));
      else {
        const draft = JSON.parse(json);
        setName(draft.name || "");
        setDescription(draft.description || "");
        setStatements(draft.statements || []);
      }
      setMode(next);
    } catch (error) {
      setError(
        `Fix the JSON before switching to the form: ${(error as Error).message}`,
      );
    }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    setError("");
    setDetails([]);
    setBusy(true);
    try {
      const draft =
        mode === "json" ? JSON.parse(json) : { name, description, statements };
      const body: RecordData = {
        name: draft.name,
        description: draft.description || "",
        statements: draft.statements || [],
      };
      if (policy.id) body.version = policy.version;
      else if (id.trim() || draft.id) body.id = id.trim() || draft.id;
      await request(
        policy.id
          ? `/api/v1/admin/iam/policies/${encode(policy.id)}`
          : "/api/v1/admin/iam/policies",
        { method: policy.id ? "PUT" : "POST", body },
      );
      onSaved();
    } catch (error) {
      const failure = error as any;
      setError(failure.message);
      setDetails(failure.body?.details || failure.details || []);
    } finally {
      setBusy(false);
    }
  }
  function update(index: number, patch: RecordData) {
    setStatements((old) =>
      old.map((statement, i) =>
        i === index ? { ...statement, ...patch } : statement,
      ),
    );
  }
  return (
    <Modal
      title={
        policy.id ? `Edit ${policy.name} v${policy.version}` : "New policy"
      }
      onClose={onClose}
    >
      <div className="tabs">
        <button
          aria-pressed={mode === "form"}
          onClick={() => mode !== "form" && changeMode("form")}
        >
          Form
        </button>
        <button
          aria-pressed={mode === "json"}
          onClick={() => mode !== "json" && changeMode("json")}
        >
          JSON
        </button>
      </div>
      <form onSubmit={save} className="stack">
        <label>
          Policy ID
          <input
            value={id}
            readOnly={!!policy.id}
            onChange={(event) => setId(event.target.value)}
          />
        </label>
        {mode === "json" ? (
          <div className="field">
            <span className="field-caption">Policy JSON</span>
            <CodeEditor
              language="json"
              label="Policy JSON"
              rows={20}
              value={json}
              onChange={(event) => setJson(event.target.value)}
            />
          </div>
        ) : (
          <>
            <label>
              Name
              <input
                required
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            <label>
              Description
              <input
                value={description}
                onChange={(event) => setDescription(event.target.value)}
              />
            </label>
            {statements.map((statement, index) => (
              <fieldset key={index}>
                <legend>Statement {index + 1}</legend>
                <div className="form-grid">
                  <label>
                    Statement ID
                    <input
                      value={statement.sid || ""}
                      onChange={(event) =>
                        update(index, { sid: event.target.value })
                      }
                    />
                  </label>
                  <label>
                    Effect
                    <Select
                      value={statement.effect || "allow"}
                      onChange={(event) =>
                        update(index, { effect: event.target.value })
                      }
                    >
                      <option value="allow">Allow</option>
                      <option value="deny">Deny</option>
                    </Select>
                  </label>
                  <label>
                    Actions
                    <input
                      value={(statement.actions || []).join(", ")}
                      onChange={(event) =>
                        update(index, { actions: csv(event.target.value) })
                      }
                    />
                    <small>
                      Comma-separated action names or namespace wildcards.
                    </small>
                  </label>
                  <label>
                    Resources
                    <textarea
                      rows={3}
                      value={(statement.resources || []).join("\n")}
                      onChange={(event) =>
                        update(index, { resources: csv(event.target.value) })
                      }
                    />
                    <small>
                      Kionga resource names, separated by commas or new lines.
                    </small>
                  </label>
                </div>
                <ConditionEditor
                  value={statement.conditions || {}}
                  onChange={(conditions) => update(index, { conditions })}
                />
                <button
                  type="button"
                  className="danger"
                  onClick={() =>
                    setStatements((old) => old.filter((_, i) => i !== index))
                  }
                >
                  Remove statement
                </button>
              </fieldset>
            ))}
            <button
              type="button"
              onClick={() =>
                setStatements((old) => [
                  ...old,
                  { effect: "allow", actions: [], resources: [] },
                ])
              }
            >
              Add statement
            </button>
          </>
        )}
        {error && (
          <p role="alert" className="feedback error">
            {error}
          </p>
        )}
        {details.length > 0 && (
          <ul>
            {details.map((detail, index) => (
              <li key={index}>
                <code>{detail.field}</code> {detail.message}
              </li>
            ))}
          </ul>
        )}
        <button disabled={busy} type="submit" className="primary">
          {busy ? "Saving…" : "Save policy"}
        </button>
      </form>
    </Modal>
  );
}
function ConditionEditor({
  value,
  onChange,
}: {
  value: RecordData;
  onChange: (conditions: RecordData) => void;
}) {
  return (
    <details>
      <summary>Conditions</summary>
      <div className="form-grid">
        {["ip_cidr", "resource_profile"].map((key) => (
          <label key={key}>
            {key.replaceAll("_", " ")}
            <input
              value={(value[key] || []).join(", ")}
              onChange={(event) => {
                const next = { ...value };
                const values = csv(event.target.value);
                if (values.length) next[key] = values;
                else delete next[key];
                onChange(next);
              }}
            />
          </label>
        ))}
        <label>
          Resource tags
          <input
            value={Object.entries(value.resource_tags || {})
              .map(([key, value]) => `${key}=${value}`)
              .join(", ")}
            onChange={(event) => {
              const next = { ...value };
              const pairs = csv(event.target.value);
              if (pairs.length)
                next.resource_tags = Object.fromEntries(
                  pairs.map((pair) => {
                    const [key, ...rest] = pair.split("=");
                    return [key.trim(), rest.join("=").trim()];
                  }),
                );
              else delete next.resource_tags;
              onChange(next);
            }}
          />
        </label>
        <label>
          UTC start hour
          <input
            type="number"
            min={0}
            max={23}
            value={value.utc_hours?.start ?? ""}
            onChange={(event) =>
              onChange({
                ...value,
                utc_hours: {
                  start: Number(event.target.value || 0),
                  end: value.utc_hours?.end ?? 0,
                },
              })
            }
          />
        </label>
        <label>
          UTC end hour
          <input
            type="number"
            min={0}
            max={24}
            value={value.utc_hours?.end ?? ""}
            onChange={(event) =>
              onChange({
                ...value,
                utc_hours: {
                  start: value.utc_hours?.start ?? 0,
                  end: Number(event.target.value || 0),
                },
              })
            }
          />
        </label>
      </div>
      {value.utc_hours && (
        <button
          type="button"
          onClick={() => {
            const next = { ...value };
            delete next.utc_hours;
            onChange(next);
          }}
        >
          Remove UTC window
        </button>
      )}
    </details>
  );
}
