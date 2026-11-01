"use client";
import { GitHubRepositoryPicker } from "@/components/console/GitHubConnection";
import { FileTree } from "@/components/ui/FileTree";
import { Select } from "@/components/ui/Select";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { useEffect, useState } from "react";
import { request } from "@/lib/api";
import {
  admin,
  can,
  Detail,
  encode,
  Facts,
  Form,
  has,
  items,
  Modal,
  option,
  scoped,
  State,
  Status,
  Table,
  useAction,
  useResource,
  type OperationsProps,
  type RecordData,
} from "./shared";

export function Projects(props: OperationsProps) {
  const { identity, project, resource, onNavigate } = props;
  const query = useResource("/api/v1/projects"),
    templates = useResource("/api/v1/project-templates"),
    workspaces = useResource("/api/v1/workspaces");
  const [creating, setCreating] = useState(false),
    [template, setTemplate] = useState<RecordData | null>(null),
    [repo, setRepo] = useState<RecordData | null>(null),
    [plan, setPlan] = useState<RecordData | null>(null),
    [job, setJob] = useState<RecordData | null>(null);
  const selected = useResource<RecordData>(
    resource ? `/api/v1/projects/${encode(resource)}` : null,
  );
  const jobs = useResource(
    resource ? `/api/v1/projects/${encode(resource)}/scaffold-jobs` : null,
  );
  const action = useAction(query.reload);
  const entry =
    selected.data || items(query.data).find((item) => item.id === resource);
  useEffect(() => {
    setJob(items(jobs.data)[0] || null);
  }, [jobs.data]);
  useEffect(() => {
    if (!job || !["running", "queued"].includes(job.status) || !resource)
      return;
    const controller = new AbortController();
    const timer = setTimeout(
      () =>
        request<RecordData>(
          `/api/v1/projects/${encode(resource)}/scaffold-jobs/${encode(job.id)}`,
          { signal: controller.signal },
        )
          .then(setJob)
          .catch((error) => {
            if (!controller.signal.aborted) console.error(error);
          }),
      1000,
    );
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [job, resource]);
  const grant =
    !admin(identity) && identity.roles.includes("user")
      ? identity.entitlements
      : null;
  const profileSizes: Record<string, number[]> = {
    starter: [2, 4, 0, 25],
    team: [4, 8, 0, 100],
    power: [8, 16, 0, 250],
    gpu: [8, 32, 1, 500],
  };
  const fitsProfile = (name: string) =>
    !grant ||
    !profileSizes[name] ||
    profileSizes[name].every(
      (amount, index) =>
        Number(
          [
            grant.compute?.vcpus,
            grant.compute?.memory_gb,
            grant.compute?.gpus,
            grant.storage?.size_gb,
          ][index] || 0,
        ) >= amount,
    );
  const fitsAccelerator = (name: string) =>
    !grant ||
    Number(grant.compute?.gpus || 0) >=
      (name === "multi-gpu" ? 2 : name === "single-gpu" ? 1 : 0);
  const chooseTemplate = (id: string) =>
    setTemplate(items(templates.data).find((item) => item.id === id) || null);
  return (
    <>
      {action.feedback}
      {!resource ? (
        <>
          <header className="page-header">
            <p>
              Code, pipelines, models and workspace files grouped by project.
            </p>
            <button
              className="primary"
              disabled={!can(identity, "projects_write")}
              onClick={() => {
                setCreating(true);
                setTemplate(items(templates.data)[0] || null);
              }}
            >
              New project
            </button>
          </header>
          <State {...query} retry={query.reload}>
            <Table
              rows={scoped(items(query.data), project)}
              columns={[
                { key: "name", label: "Project" },
                { key: "template", label: "Template" },
                { key: "framework", label: "Framework" },
                { key: "namespace", label: "Namespace" },
                {
                  key: "status",
                  label: "Status",
                  render: (row) => <Status value={row.status} />,
                },
              ]}
              actions={(row) => (
                <button onClick={() => onNavigate("projects", row.id)}>
                  Open project
                </button>
              )}
            />
          </State>
        </>
      ) : (
        <State
          loading={selected.loading}
          error={selected.error}
          retry={selected.reload}
        >
          <button onClick={() => onNavigate("projects", "")}>
            All projects
          </button>
          {entry && (
            <>
              <header className="page-header">
                <div>
                  <h2>{entry.name}</h2>
                  <p>{entry.description}</p>
                </div>
                <div className="button-row">
                  {["workbench", "ide"]
                    .filter((tool) => has(identity, tool))
                    .map((tool) => {
                      const state = items(workspaces.data).find(
                        (item) => item.id === tool,
                      );
                      return state?.available ? (
                        <a
                          key={tool}
                          href={`/workspace?tool=${tool}&project=${encode(entry.id)}`}
                          target="_blank"
                          rel="noopener"
                        >
                          Open {tool === "ide" ? "IDE" : "JupyterLab"}
                        </a>
                      ) : (
                        <button
                          key={tool}
                          disabled
                          title={state?.message || "Workspace unavailable"}
                        >
                          Open {tool === "ide" ? "IDE" : "JupyterLab"}
                        </button>
                      );
                    })}
                  <button
                    disabled={!can(identity, "git_write")}
                    onClick={() => setRepo(entry)}
                  >
                    Connect repository
                  </button>
                </div>
              </header>
              <section className="panel">
                <h3>Related resources</h3>
                <div className="button-row">
                  {["pipelines", "functions", "models", "agents"]
                    .filter((view) => has(identity, view))
                    .map((view) => (
                      <a
                        key={view}
                        href={`/console/${view}?project=${encode(entry.id)}`}
                      >
                        {view[0].toUpperCase() + view.slice(1)}
                      </a>
                    ))}
                </div>
              </section>
              <section className="panel">
                <h3>Project record</h3>
                <Facts value={Object.fromEntries(Object.entries(entry).filter(([key])=>key!=="scaffold_command"))} />
              </section>
              {entry.scaffold_command && (
                <section className="panel">
                  <h3>Generate starter code</h3>
                  <p>
                    The command runs in your assigned workspace and does not
                    overwrite an existing populated directory.
                  </p>
                  <CodeBlock language="bash" filename="scaffold.sh">
                    {entry.scaffold_command}
                  </CodeBlock>
                  <div className="button-row">
                    <button
                      onClick={() =>
                        void navigator.clipboard.writeText(
                          entry.scaffold_command,
                        )
                      }
                    >
                      Copy command
                    </button>
                    <button
                      className="primary"
                      disabled={
                        !can(identity, "projects_write") ||
                        action.busy ||
                        !(has(identity, "workbench") || has(identity, "ide"))
                      }
                      onClick={() =>
                        void action
                          .run(
                            () =>
                              request<RecordData>(
                                `/api/v1/projects/${encode(entry.id)}/scaffold-plan`,
                              ),
                            "Scaffold plan loaded.",
                          )
                          .then(setPlan)
                          .catch(() => {})
                      }
                    >
                      Run in workspace
                    </button>
                  </div>
                  {job && (
                    <>
                      <div className="scaffold-result stack">
                        <header className="page-header">
                          <h4>Last scaffold run</h4>
                          <Status value={job.status} />
                        </header>
                        {job.error && (
                          <div
                            role="alert"
                            className="feedback error scaffold-error"
                          >
                            <strong>Scaffold failed</strong>
                            <p>{job.error}</p>
                            <small>
                              Review the workspace output below before retrying.
                            </small>
                          </div>
                        )}
                        <Facts
                          value={{
                            workspace: job.workspace,
                            output_dir: job.output_dir,
                            requested_by: job.requested_by,
                          }}
                        />
                        {job.files?.length > 0 && (
                          <details open={job.status === "succeeded"}>
                            <summary>Produced files</summary>
                            <FileTree paths={job.files} />
                          </details>
                        )}
                        {job.output_tail && (
                          <CodeBlock language="text" filename="workspace-output.log">{job.output_tail}</CodeBlock>
                        )}
                        {job.git_status?.length > 0 && (
                          <CodeBlock language="text" filename="git-status.txt">
                            {job.git_status.join("\n")}
                          </CodeBlock>
                        )}
                      </div>
                    </>
                  )}
                </section>
              )}
            </>
          )}
        </State>
      )}
      {creating && (
        <Modal title="New project" onClose={() => setCreating(false)}>
          <State
            loading={templates.loading}
            error={templates.error}
            retry={templates.reload}
          >
            <label className="field">
              Template
              <Select
                value={template?.id || ""}
                onChange={(event) => chooseTemplate(event.target.value)}
              >
                {items(templates.data).map((item) => (
                  <option value={item.id} key={item.id}>
                    {item.name} v{item.version}
                  </option>
                ))}
              </Select>
            </label>
            {template && (
              <>
                <p>{template.description}</p>
                <p>
                  Required services:{" "}
                  {(template.required_services || []).join(", ")}
                </p>
                <Form
                  key={template.id}
                  initial={{
                    framework: template.frameworks?.[0] || "",
                    accelerator: "cpu",
                    requested_profile: fitsProfile(
                      template.recommended_profile || "starter",
                    )
                      ? template.recommended_profile || "starter"
                      : "custom",
                    default_branch: "main",
                  }}
                  fields={[
                    { name: "name", label: "Project name", required: true },
                    {
                      name: "description",
                      label: "Description",
                      type: "textarea",
                    },
                    {
                      name: "framework",
                      label: "Framework",
                      type: "select",
                      options: (template.frameworks || []).map(option),
                    },
                    {
                      name: "accelerator",
                      label: "Accelerator",
                      type: "select",
                      options: (template.accelerators || ["cpu"]).map(
                        (value: string) => ({
                          ...option(value),
                          disabled: !fitsAccelerator(value),
                          label:
                            value +
                            (fitsAccelerator(value)
                              ? ""
                              : " (not provisioned)"),
                        }),
                      ),
                    },
                    {
                      name: "requested_profile",
                      label: "Requested allocation",
                      type: "select",
                      options: [
                        "starter",
                        "team",
                        "power",
                        "gpu",
                        "custom",
                      ].map((value) => ({
                        ...option(value),
                        disabled: !fitsProfile(value),
                        label:
                          value +
                          (fitsProfile(value) ? "" : " (exceeds allocation)"),
                      })),
                    },
                    ...(can(identity, "git_write")
                      ? [
                          {
                            name: "repository_url",
                            label: "Git repository URL",
                          },
                          { name: "default_branch", label: "Default branch" },
                        ]
                      : []),
                  ]}
                  submit="Create project"
                  onSubmit={async (value) => {
                    if (
                      template.id === "production-ml" &&
                      value.framework === "scikit-learn" &&
                      value.accelerator !== "cpu"
                    )
                      throw new Error("scikit-learn uses the CPU accelerator.");
                    const created = await request<RecordData>(
                      "/api/v1/projects",
                      {
                        method: "POST",
                        body: {
                          ...value,
                          template: template.id,
                          template_version: template.version,
                        },
                      },
                    );
                    setCreating(false);
                    query.reload();
                    onNavigate("projects", created.id);
                  }}
                />
              </>
            )}
          </State>
        </Modal>
      )}
      {repo && (
        <Modal title="Git repository" onClose={() => setRepo(null)}>
          <GitHubRepositoryPicker
            onChoose={(repository) =>
              setRepo({
                ...repo,
                repository: {
                  url: repository.html_url,
                  default_branch: repository.default_branch,
                },
              })
            }
          />
          <Form
            key={repo.repository?.url || "manual"}
            initial={{
              url: repo.repository?.url || "",
              default_branch: repo.repository?.default_branch || "main",
            }}
            fields={[
              { name: "url", label: "Repository URL", required: true },
              {
                name: "default_branch",
                label: "Default branch",
                required: true,
              },
            ]}
            submit="Connect repository"
            onSubmit={async (value) => {
              await request(`/api/v1/projects/${encode(repo.id)}/repository`, {
                method: "PUT",
                body: value,
              });
              setRepo(null);
              selected.reload();
              query.reload();
            }}
          />
        </Modal>
      )}
      {plan && (
        <Modal title="Run scaffold in workspace" onClose={() => setPlan(null)}>
          <Facts
            value={{
              workspace: plan.workspace,
              working_dir: plan.working_dir,
              output_dir: plan.output_dir,
            }}
          />
          <h3>Exact command</h3>
          <CodeBlock language="bash" filename="scaffold.sh">{plan.command}</CodeBlock>
          <ol>
            {(plan.argv || []).map((argument: string, index: number) => (
              <li key={index}>
                <code>{argument}</code>
              </li>
            ))}
          </ol>
          {!plan.available && <p role="alert">{plan.reason}</p>}
          <button
            className="primary"
            disabled={!plan.available || action.busy}
            onClick={() =>
              void action
                .run(
                  () =>
                    request<RecordData>(
                      `/api/v1/projects/${encode(resource)}/scaffold-jobs`,
                      {
                        method: "POST",
                        body: plan.workspace
                          ? { options: { workspace: plan.workspace } }
                          : {},
                      },
                    ),
                  "Scaffold started.",
                )
                .then((value) => {
                  setJob(value);
                  setPlan(null);
                })
                .catch(() => {})
            }
          >
            Run command
          </button>
        </Modal>
      )}
    </>
  );
}
