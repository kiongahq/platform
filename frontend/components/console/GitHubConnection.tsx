"use client";
import { useState } from "react";
import { request } from "@/lib/api";
import { Select } from "@/components/ui/Select";
import {
  State,
  items,
  useResource,
  type RecordData,
} from "@/features/operations/shared";
/** Account credentials stay server-side; imports use the normal, authorized project APIs. */
export function GitHubConnection({ canImport }: { canImport: boolean }) {
  const account = useResource<RecordData>("/api/v1/settings/github");
  const repositories = useResource(
    account.data?.connected ? "/api/v1/settings/github/repositories" : null,
  );
  const installations = useResource(
    account.data?.connected && account.data?.oauth_configured
      ? "/api/v1/settings/github/installations"
      : null,
  );
  const [token, setToken] = useState(""),
    [selected, setSelected] = useState<number[]>([]),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [message, setMessage] = useState("");
  async function perform(work: () => Promise<unknown>, success: string) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await work();
      setMessage(success);
      account.reload();
      repositories.reload();
      installations.reload();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function sync() {
    setBusy(true);
    setError("");
    setMessage("");
    const completed: string[] = [],
      failed: string[] = [];
    try {
      const existing = await request<RecordData[] | { items: RecordData[] }>(
        "/api/v1/projects",
      );
      const normalize = (value: string) =>
        value
          .replace(/\/$/, "")
          .replace(/\.git$/, "")
          .toLowerCase();
      for (const repository of repositories.data?.items || []) {
        if (!selected.includes(repository.id)) continue;
        try {
          const project = items(existing).find(
            (item) =>
              normalize(item.repository?.url || "") ===
              normalize(repository.html_url),
          );
          if (project)
            await request(
              `/api/v1/projects/${encodeURIComponent(project.id)}/repository`,
              {
                method: "PUT",
                body: {
                  url: repository.html_url,
                  default_branch: repository.default_branch,
                },
              },
            );
          else
            await request("/api/v1/projects", {
              method: "POST",
              body: {
                name: repository.full_name,
                description: `GitHub repository ${repository.full_name}`,
                template: "blank-python",
                framework: "python",
                accelerator: "cpu",
                requested_profile: "starter",
                repository_url: repository.html_url,
                default_branch: repository.default_branch,
              },
            });
          completed.push(repository.full_name);
        } catch (error) {
          failed.push(`${repository.full_name}: ${(error as Error).message}`);
        }
      }
      setMessage(
        `${completed.length} project connection${completed.length === 1 ? "" : "s"} synchronized.`,
      );
      if (failed.length) setError(failed.join("\n"));
      window.dispatchEvent(new Event("kionga:refresh"));
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="panel github-connection">
      <h2>GitHub account and repositories</h2>
      <p>
        Connect an account to discover repositories. Install the GitHub App for
        organization access to all or selected repositories, or connect a single
        repository from a project.
      </p>
      <div className="button-row">
        <a
          href="https://github.com/settings/connections/applications"
          target="_blank"
          rel="noopener noreferrer"
        >
          GitHub account settings
        </a>
        <a
          href="https://github.com/settings/personal-access-tokens/new"
          target="_blank"
          rel="noopener noreferrer"
        >
          Create a fine-grained token
        </a>
      </div>
      <State {...account} retry={account.reload}>
        {account.data?.connected ? (
          <p role="status">
            Connected as <strong>{account.data.username}</strong>
            {account.data.installation_id
              ? ` · Installation ${account.data.installation_id}`
              : ""}
          </p>
        ) : (
          <p>
            {account.data?.configured
              ? "Connect with the configured GitHub App or a fine-grained token restricted to the repositories you need."
              : "An administrator must configure KIONGA_CREDENTIAL_KEY before account credentials can be saved."}
          </p>
        )}
        <div className="button-row">
          {account.data?.oauth_configured && (
            <a
              className="button primary"
              href="/api/v1/settings/github/connect"
            >
              {account.data.connected ? "Reconnect" : "Connect"} GitHub account
            </a>
          )}
          {account.data?.install_url && (
            <a
              className="button"
              href={account.data.install_url}
              target="_blank"
              rel="noopener noreferrer"
            >
              Install GitHub App
            </a>
          )}
          {account.data?.connected && (
            <button
              disabled={busy}
              onClick={() =>
                void perform(
                  () =>
                    request("/api/v1/settings/github", { method: "DELETE" }),
                  "GitHub account disconnected.",
                )
              }
            >
              Disconnect GitHub
            </button>
          )}
        </div>
        {!account.data?.oauth_configured && account.data?.configured && (
          <p className="muted">
            GitHub App sign-in is not configured for this installation. An
            administrator can configure it for repository-scoped organization
            access.
          </p>
        )}
        <details>
          <summary>Connect using an access token</summary>
          <form
            className="stack"
            onSubmit={(event) => {
              event.preventDefault();
              const value = token.trim();
              setToken("");
              void perform(
                () =>
                  request("/api/v1/settings/github", {
                    method: "PUT",
                    body: { token: value },
                  }),
                "GitHub account connected.",
              );
            }}
          >
            <label className="field">
              GitHub access token
              <input
                type="password"
                autoComplete="new-password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                disabled={!account.data?.configured}
                required
              />
            </label>
            <button
              type="submit"
              disabled={busy || !token.trim() || !account.data?.configured}
            >
              Connect with token
            </button>
          </form>
        </details>
      </State>
      {account.data?.connected && account.data?.oauth_configured && (
        <section className="stack">
          <h3>App installations</h3>
          <button disabled={busy} onClick={installations.reload}>
            Refresh installations
          </button>
          <State {...installations} retry={installations.reload}>
            <label className="field">
              Use an installation
              <Select
                aria-label="GitHub App installation"
                value={account.data.installation_id || ""}
                disabled={busy}
                onChange={(event) => {
                  if (event.target.value)
                    void perform(
                      () =>
                        request("/api/v1/settings/github", {
                          method: "PUT",
                          body: { installation_id: Number(event.target.value) },
                        }),
                      "Installation connected.",
                    );
                }}
              >
                <option value="">Choose an installation</option>
                {(installations.data?.items || []).map(
                  (installation: RecordData) => (
                    <option key={installation.id} value={installation.id}>
                      {installation.account.login} ·{" "}
                      {installation.repository_selection} repositories
                    </option>
                  ),
                )}
              </Select>
            </label>
            {!installations.data?.items?.length && (
              <p>
                No installation is available to this GitHub account. Install the
                app, then refresh.
              </p>
            )}
          </State>
        </section>
      )}
      {account.data?.connected && (
        <section className="stack">
          <header className="page-header">
            <h3>Available repositories</h3>
            <button onClick={repositories.reload}>Refresh repositories</button>
          </header>
          <State {...repositories} retry={repositories.reload}>
            <p>
              Synchronizing creates missing project records or updates existing
              repository links. Project policies and resource limits apply to
              every import.
            </p>
            <div className="repository-list">
              {(repositories.data?.items || []).map(
                (repository: RecordData) => (
                  <label key={repository.id}>
                    <input
                      type="checkbox"
                      disabled={busy || !canImport}
                      checked={selected.includes(repository.id)}
                      onChange={(event) =>
                        setSelected((old) =>
                          event.target.checked
                            ? [...old, repository.id]
                            : old.filter((id) => id !== repository.id),
                        )
                      }
                    />
                    <span>
                      {repository.full_name}
                      <small>
                        {repository.private ? "Private" : "Public"} ·{" "}
                        {repository.default_branch}
                      </small>
                    </span>
                    <a
                      href={repository.html_url}
                      target="_blank"
                      rel="noopener noreferrer"
                      aria-label={`View ${repository.full_name} on GitHub`}
                    >
                      View
                    </a>
                  </label>
                ),
              )}
            </div>
            <div className="button-row">
              <button
                disabled={busy || !canImport}
                onClick={() =>
                  setSelected(
                    (repositories.data?.items || []).map(
                      (item: RecordData) => item.id,
                    ),
                  )
                }
              >
                Select all repositories
              </button>
              <button
                className="primary"
                disabled={busy || !canImport || !selected.length}
                onClick={() => void sync()}
              >
                {busy ? "Synchronizing…" : `Sync ${selected.length} selected`}
              </button>
            </div>
            {!canImport && (
              <p>
                Project creation and Git write permissions are required to
                synchronize repositories.
              </p>
            )}
          </State>
        </section>
      )}
      {message && (
        <p className="feedback" role="status">
          {message}
        </p>
      )}
      {error && (
        <div
          className="feedback error"
          role="alert"
          style={{ whiteSpace: "pre-wrap" }}
        >
          {error}
        </div>
      )}
    </section>
  );
}
export function GitHubRepositoryPicker({
  onChoose,
}: {
  onChoose: (repository: RecordData) => void;
}) {
  const account = useResource<RecordData>("/api/v1/settings/github");
  const repositories = useResource(
    account.data?.connected ? "/api/v1/settings/github/repositories" : null,
  );
  return (
    <section className="stack repository-picker">
      <h3>Choose from GitHub</h3>
      {account.data?.connected ? (
        <State {...repositories} retry={repositories.reload}>
          <label className="field">
            GitHub repository
            <Select
              defaultValue=""
              onChange={(event) => {
                const value = repositories.data?.items?.find(
                  (item: RecordData) => String(item.id) === event.target.value,
                );
                if (value) onChoose(value);
              }}
            >
              <option value="">Select a repository</option>
              {(repositories.data?.items || []).map((item: RecordData) => (
                <option key={item.id} value={item.id}>
                  {item.full_name}
                </option>
              ))}
            </Select>
          </label>
        </State>
      ) : (
        <p>
          <a href="/console/settings">Connect your GitHub account</a> to choose
          a repository. You can also enter its URL below.
        </p>
      )}
    </section>
  );
}
