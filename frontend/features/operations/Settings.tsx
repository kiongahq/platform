"use client";
import { GitHubConnection } from "@/components/console/GitHubConnection";
import { Select } from "@/components/ui/Select";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { useEffect, useState } from "react";
import { request } from "@/lib/api";
import {
  admin,
  can,
  csv,
  date,
  Detail,
  Form,
  has,
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
} from "./shared";
export function Settings({ identity }: OperationsProps) {
  const tokens = useResource("/api/v1/settings/tokens"),
    account = useResource("/api/v1/settings/huggingface"),
    workspaces = useResource("/api/v1/workspaces");
  const [create, setCreate] = useState(false),
    [secret, setSecret] = useState(""),
    [token, setToken] = useState(""),
    [preferences, setPreferences] = useState<RecordData>({
      theme: "light",
      compact: false,
      reduceMotion: false,
      live: true,
      startView: "overview",
    }),
    [saved, setSaved] = useState("");
  const action = useAction(() => {
    tokens.reload();
    account.reload();
  });
  useEffect(() => {
    try {
      const stored = JSON.parse(
        localStorage.getItem("kionga.console.preferences") || "{}",
      );
      setPreferences((old) => ({
        ...old,
        ...stored,
        theme: stored?.theme === "dark" ? "dark" : "light",
      }));
    } catch {
      /* defaults */
    }
  }, []);
  function preference(name: string, value: unknown) {
    const next = { ...preferences, [name]: value };
    setPreferences(next);
    try {
      localStorage.setItem("kionga.console.preferences", JSON.stringify(next));
      document.documentElement.dataset.theme =
        next.theme === "dark" ? "dark" : "light";
      document.body.classList.toggle("compact-ui", !!next.compact);
      document.documentElement.dataset.reduceMotion = String(
        !!next.reduceMotion,
      );
      window.dispatchEvent(new Event("kionga:preferences"));
      setSaved("Preferences saved in this browser.");
    } catch {
      setSaved("Browser preferences could not be saved.");
    }
  }
  return (
    <>
      {action.feedback}
      <section className="panel">
        <h2>Profile and session</h2>
        <p>{identity.email || identity.subject}</p>
        <p>
          {identity.roles?.join(", ")} access using {identity.mode} sign-in.
        </p>
        <a href="/auth/logout">Sign out</a>
      </section>
      <section className="panel">
        <h2>Preferences</h2>
        <div className="form-grid">
          <label>
            Theme
            <Select
              value={preferences.theme}
              onChange={(event) => preference("theme", event.target.value)}
            >
              {["light", "dark"].map((value) => (
                <option key={value} value={value}>
                  {value[0].toUpperCase() + value.slice(1)}
                </option>
              ))}
            </Select>
          </label>
          <label>
            Start page
            <Select
              value={preferences.startView}
              onChange={(event) => preference("startView", event.target.value)}
            >
              {services
                .filter(
                  (service) =>
                    !["git", "workbench", "ide"].includes(service) &&
                    has(identity, service),
                )
                .map((value) => (
                  <option value={value} key={value}>
                    {value}
                  </option>
                ))}
            </Select>
          </label>
          {[
            ["compact", "Compact density"],
            ["reduceMotion", "Reduce motion"],
            ["live", "Live updates"],
          ].map(([key, label]) => (
            <label key={key}>
              <input
                type="checkbox"
                checked={!!preferences[key]}
                onChange={(event) => preference(key, event.target.checked)}
              />
              {label}
            </label>
          ))}
        </div>
        {saved && <p role="status">{saved}</p>}
      </section>
      <section className="panel">
        <header className="page-header">
          <h2>Personal API keys</h2>
          <button className="primary" onClick={() => setCreate(true)}>
            Create API key
          </button>
        </header>
        <State {...tokens} retry={tokens.reload}>
          <Table
            rows={items(tokens.data)}
            columns={[
              { key: "name", label: "Name" },
              { key: "prefix", label: "Prefix" },
              {
                key: "services",
                label: "Services",
                render: (row) => (row.services || []).join(", "),
              },
              {
                key: "project_ids",
                label: "Projects",
                render: (row) =>
                  (row.project_ids || []).join(", ") || "Granted projects",
              },
              {
                key: "expires_at",
                label: "Expires",
                render: (row) => date(row.expires_at),
              },
              {
                key: "last_used_at",
                label: "Last used",
                render: (row) => date(row.last_used_at),
              },
            ]}
            actions={(row) =>
              row.revoked_at ? (
                <Status value="revoked" />
              ) : (
                <button
                  className="danger"
                  disabled={action.busy}
                  onClick={() => {
                    if (
                      window.confirm(
                        "Revoke this key? Tools using it immediately lose access.",
                      )
                    )
                      void action
                        .run(
                          () =>
                            request(
                              `/api/v1/settings/tokens/${encodeURIComponent(row.id)}`,
                              { method: "DELETE" },
                            ),
                          "API key revoked.",
                        )
                        .catch(() => {});
                  }}
                >
                  Revoke
                </button>
              )
            }
          />
        </State>
      </section>
      <GitHubConnection
        canImport={
          can(identity, "projects_write") && can(identity, "git_write")
        }
      />
      <section className="panel">
        <h2>Hugging Face account</h2>
        <div className="button-row">
          <a
            href="https://huggingface.co/join"
            target="_blank"
            rel="noopener noreferrer"
          >
            Create a Hugging Face account
          </a>
          <a
            href="https://huggingface.co/settings/tokens"
            target="_blank"
            rel="noopener noreferrer"
          >
            Manage access tokens
          </a>
          {account.data?.connected && (
            <a
              href={`https://huggingface.co/${encodeURIComponent(account.data.username)}`}
              target="_blank"
              rel="noopener noreferrer"
            >
              View connected account
            </a>
          )}
        </div>
        <State {...account} retry={account.reload}>
          <p>
            {account.data?.connected
              ? `Connected as ${account.data.username}`
              : account.data?.configured
                ? "Public discovery is available without an account. Connect to access private or gated models."
                : "An administrator must configure KIONGA_CREDENTIAL_KEY to enable encrypted account connections."}
          </p>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const value = token.trim();
              setToken("");
              void action
                .run(
                  () =>
                    request("/api/v1/settings/huggingface", {
                      method: "PUT",
                      body: { token: value },
                    }),
                  "Account connected.",
                )
                .catch(() => {});
            }}
          >
            <label>
              Access token
              <input
                type="password"
                autoComplete="new-password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                required
              />
            </label>
            <div className="button-row">
              <button
                className="primary"
                disabled={!account.data?.configured || action.busy}
              >
                Connect account
              </button>
              {account.data?.connected && (
                <button
                  type="button"
                  disabled={action.busy}
                  onClick={() =>
                    void action
                      .run(
                        () =>
                          request("/api/v1/settings/huggingface", {
                            method: "DELETE",
                          }),
                        "Account disconnected.",
                      )
                      .catch(() => {})
                  }
                >
                  Disconnect
                </button>
              )}
            </div>
          </form>
        </State>
      </section>
      <section className="panel">
        <h2>Developer workspaces</h2>
        <State {...workspaces} retry={workspaces.reload}>
          <div className="button-row">
            {items(workspaces.data)
              .filter((item) => has(identity, item.id))
              .map((item) =>
                item.id === "langflow" ? (
                  <a key={item.id} href="/console/langflow">
                    Langflow workspaces
                  </a>
                ) : item.available ? (
                  <a
                    key={item.id}
                    href={`/workspace?tool=${encodeURIComponent(item.id)}`}
                  >
                    {item.name}
                  </a>
                ) : (
                  <button key={item.id} disabled title={item.message}>
                    {item.name}: {item.state || "unavailable"}
                  </button>
                ),
              )}
            <a href="/api-docs">API reference</a>
          </div>
        </State>
      </section>
      {create && (
        <Modal title="Create personal API key" onClose={() => setCreate(false)}>
          <Form
            fields={[
              { name: "name", label: "Key name", required: true },
              {
                name: "expires_in_days",
                label: "Expires in days",
                type: "number",
                required: true,
                min: 1,
                max: 365,
              },
              {
                name: "services",
                label: "Service scopes",
                type: "multiselect",
                options: services
                  .filter(
                    (service) =>
                      !["workbench", "ide"].includes(service) &&
                      has(identity, service),
                  )
                  .map(option),
              },
              {
                name: "project_ids",
                label: "Project IDs",
                help: "Comma-separated. Empty uses your assigned projects.",
              },
            ]}
            initial={{ expires_in_days: 30, services: [] }}
            submit="Create key"
            onSubmit={async (value) => {
              if (!value.services?.length)
                throw new Error("Choose at least one service scope.");
              const created = await request<RecordData>(
                "/api/v1/settings/tokens",
                {
                  method: "POST",
                  body: { ...value, project_ids: csv(value.project_ids) },
                },
              );
              setSecret(created.secret);
              setCreate(false);
              tokens.reload();
            }}
          />
        </Modal>
      )}
      {secret && (
        <Modal title="Copy your API key" onClose={() => setSecret("")}>
          <p>This key is shown once. Store it securely before closing.</p>
          <CodeBlock copy={false}>{secret}</CodeBlock>
          <button onClick={() => void navigator.clipboard.writeText(secret)}>
            Copy API key
          </button>
        </Modal>
      )}
    </>
  );
}

export function Profile({ identity, onNavigate }: OperationsProps) {
  const requests = useResource("/api/v1/access-requests");
  const [open, setOpen] = useState(false);
  return (
    <>
      <section className="panel">
        <h2>{identity.email || identity.subject}</h2>
        <p>
          {identity.roles?.join(", ") || "No role"} using {identity.mode}{" "}
          sign-in
        </p>
        <Status
          value={
            identity.entitlements?.status ||
            (identity.provisioned || admin(identity)
              ? "active"
              : "not provisioned")
          }
        />
      </section>
      <section className="panel">
        <h2>Assigned services</h2>
        <div className="button-row">
          {services
            .filter((service) => has(identity, service))
            .map((service) =>
              ["workbench", "ide"].includes(service) ? (
                <a key={service} href={`/workspace?tool=${service}`}>
                  {service}
                </a>
              ) : service === "git" ? (
                <span key={service}>Git repositories</span>
              ) : (
                <button key={service} onClick={() => onNavigate(service)}>
                  {service}
                </button>
              ),
            )}
        </div>
      </section>
      <section className="panel">
        <h2>Resource allocation</h2>
        {identity.entitlements ? (
          <Detail value={identity.entitlements} />
        ) : (
          <p>
            {admin(identity)
              ? "Administrators are not constrained by user workspace quotas."
              : "No workspace capacity has been assigned. Request access below."}
          </p>
        )}
      </section>
      <section className="panel">
        <header className="page-header">
          <h2>Access requests</h2>
          <button
            disabled={items(requests.data)[0]?.status === "pending"}
            onClick={() => setOpen(true)}
          >
            {items(requests.data)[0]?.status === "pending"
              ? "Request pending"
              : "Request access"}
          </button>
        </header>
        <State {...requests} retry={requests.reload}>
          <Table
            rows={items(requests.data)}
            columns={[
              {
                key: "requested_services",
                label: "Services",
                render: (row) => (row.requested_services || []).join(", "),
              },
              { key: "reason", label: "Reason" },
              {
                key: "status",
                label: "Status",
                render: (row) => <Status value={row.status} />,
              },
              { key: "note", label: "Decision" },
              {
                key: "created_at",
                label: "Requested",
                render: (row) => date(row.created_at),
              },
            ]}
          />
        </State>
      </section>
      {open && (
        <Modal title="Request platform access" onClose={() => setOpen(false)}>
          <Form
            fields={[
              {
                name: "requested_services",
                label: "Requested services",
                type: "multiselect",
                options: services.map(option),
              },
              {
                name: "reason",
                label: "Reason",
                type: "textarea",
                required: true,
              },
            ]}
            initial={{ requested_services: [] }}
            submit="Submit request"
            onSubmit={async (value) => {
              if (!value.requested_services.length)
                throw new Error("Choose at least one service.");
              await request("/api/v1/access-requests", {
                method: "POST",
                body: value,
              });
              setOpen(false);
              requests.reload();
            }}
          />
        </Modal>
      )}
    </>
  );
}
