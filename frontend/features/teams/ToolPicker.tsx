"use client";
import { useQuery } from "@/hooks/useQuery";
import { Icon } from "@/components/ui/Icon";
import { Select } from "@/components/ui/Select";
import { hasService, items, type Identity } from "@/lib/types";
import type { ProjectResource, ToolGrant } from "./types";
import type { FieldErrors } from "./validation";

type RefKind = "tool" | "connection" | "knowledge" | "dataset";
const SOURCES: Array<{
  kind: RefKind;
  label: string;
  path: string;
  hint: string;
  none: string;
  service?: string;
}> = [
  { kind: "tool", label: "Registered tools", path: "/api/v1/tools", hint: "Each call needs a person's approval.", none: "No registered tools in this project.", service: "catalog" },
  { kind: "connection", label: "API connections", path: "/api/v1/agent-api-connections", hint: "Each call needs a person's approval.", none: "No API connections in this project." },
  { kind: "knowledge", label: "Knowledge bases", path: "/api/v1/knowledge-bases", hint: "Read-only search.", none: "No knowledge bases in this project." },
  { kind: "dataset", label: "Datasets", path: "/api/v1/datasets", hint: "Read-only access.", none: "No datasets in this project." },
];
/** The grant reference: registered tools go by name, other resources by ID. */
const refOf = (kind: RefKind, item: ProjectResource) =>
  (kind === "tool" ? item.name : item.id) || "";
const inProject = (item: ProjectResource, project: string) =>
  !item.project_id || item.project_id === project;

function ResourceGroup({
  source,
  project,
  identity,
  tools,
  disabled,
  onChange,
}: {
  source: (typeof SOURCES)[number];
  project: string;
  identity: Identity;
  tools: ToolGrant[];
  disabled?: boolean;
  onChange: (tools: ToolGrant[]) => void;
}) {
  const permitted = !source.service || hasService(identity, source.service);
  const query = useQuery<unknown>(permitted && project ? source.path : null);
  const available = items<ProjectResource>(query.data).filter((item) => inProject(item, project));
  const refs = new Set(available.map((item) => refOf(source.kind, item)));
  const selected = tools.flatMap((tool) => (tool.kind === source.kind ? [tool.ref] : []));
  const toggle = (ref: string, checked: boolean) =>
    onChange(
      checked
        ? [...tools, { kind: source.kind, ref }]
        : tools.filter((tool) => !(tool.kind === source.kind && tool.ref === ref)),
    );
  return (
    <fieldset className="team-tool-group" data-tool-kind={source.kind}>
      <legend>{source.label}</legend>
      <small>{source.hint}</small>
      {!project ? (
        <p className="team-muted">Select a project to list its resources.</p>
      ) : !permitted ? (
        <p className="team-muted">Your account does not have access to the catalog.</p>
      ) : query.loading && !query.data ? (
        <p role="status" className="team-muted">Loading…</p>
      ) : query.error ? (
        <div className="feedback error" role="alert">
          <p>{source.label} could not be loaded: {query.error}</p>
          <button type="button" onClick={query.reload}>Retry</button>
        </div>
      ) : !available.length && !selected.length ? (
        <p className="team-muted">{source.none}</p>
      ) : null}
      <div className="team-tool-options">
        {project && permitted && !query.error &&
          available.map((item) => {
            const ref = refOf(source.kind, item);
            return (
              <label key={ref}>
                <input
                  type="checkbox"
                  disabled={disabled}
                  checked={selected.includes(ref)}
                  onChange={(event) => toggle(ref, event.target.checked)}
                />
                <span>
                  {item.name || ref}
                  {source.kind !== "tool" && <small className="team-mono">{ref}</small>}
                </span>
              </label>
            );
          })}
        {/* A saved grant stays visible when its resource is gone, so it can be removed knowingly. */}
        {!query.loading &&
          selected
            .filter((ref) => !refs.has(ref))
            .map((ref) => (
              <label key={ref} className="team-tool-missing">
                <input
                  type="checkbox"
                  disabled={disabled}
                  checked
                  onChange={(event) => toggle(ref, event.target.checked)}
                />
                <span>
                  <span className="team-mono">{ref}</span>
                  <small>Not available in this project</small>
                </span>
              </label>
            ))}
      </div>
    </fieldset>
  );
}
function GitGrants({
  project,
  tools,
  errors,
  disabled,
  onChange,
}: {
  project: string;
  tools: ToolGrant[];
  errors: FieldErrors;
  disabled?: boolean;
  onChange: (tools: ToolGrant[]) => void;
}) {
  const connections = useQuery<unknown>(project ? "/api/v1/agent-api-connections" : null);
  const options = items<ProjectResource>(connections.data).filter((item) => inProject(item, project));
  const update = (index: number, patch: { repository?: string; connection?: string }) =>
    onChange(
      tools.map((tool, i) => (i === index && tool.kind === "git" ? { ...tool, ...patch } : tool)),
    );
  return (
    <fieldset className="team-tool-group team-tool-wide" data-tool-kind="git">
      <legend>Git repositories</legend>
      <small>Pushes need a person's approval and go only to a kionga/&lt;run&gt;/ branch.</small>
      {tools.map((tool, index) => {
        if (tool.kind !== "git") return null;
        const inputId = `definition-tools-${index}-repository`,
          error = errors[`tools.${index}.repository`];
        const known = !tool.connection || options.some((item) => item.id === tool.connection);
        return (
          <div className="team-git-row" key={index}>
            <div className="field">
              <label htmlFor={inputId}>Repository URL</label>
              <input
                id={inputId}
                type="url"
                inputMode="url"
                autoComplete="off"
                placeholder="https://github.com/org/repository"
                disabled={disabled}
                value={tool.repository}
                aria-invalid={!!error}
                aria-describedby={error ? `${inputId}-error` : undefined}
                onChange={(event) => update(index, { repository: event.target.value })}
              />
              {error && <small className="team-field-error" id={`${inputId}-error`}>{error}</small>}
            </div>
            <div className="field">
              <label htmlFor={`definition-tools-${index}-connection`}>Credential connection</label>
              <Select
                id={`definition-tools-${index}-connection`}
                disabled={disabled}
                value={tool.connection || ""}
                onChange={(event) => update(index, { connection: event.target.value })}
              >
                <option value="">No credential</option>
                {!known && <option value={tool.connection}>{tool.connection} (not available)</option>}
                {options.map((item) => (
                  <option key={item.id} value={item.id}>{item.name || item.id}</option>
                ))}
              </Select>
            </div>
            <button
              type="button"
              className="quiet"
              disabled={disabled}
              aria-label={`Remove repository ${index + 1}`}
              onClick={() => {
                onChange(tools.filter((_, i) => i !== index));
                document.getElementById("definition-add-repository")?.focus();
              }}
            >
              <Icon name="close" />
              <span>Remove</span>
            </button>
          </div>
        );
      })}
      <div>
        <button
          type="button"
          id="definition-add-repository"
          disabled={disabled}
          onClick={() => onChange([...tools, { kind: "git", repository: "" }])}
        >
          <Icon name="plus" />
          <span>Add repository</span>
        </button>
      </div>
    </fieldset>
  );
}
/** Grants come only from real project resources. A worker can use nothing beyond this list. */
export function ToolPicker({
  project,
  identity,
  tools,
  errors,
  disabled,
  onChange,
}: {
  project: string;
  identity: Identity;
  tools: ToolGrant[];
  errors: FieldErrors;
  disabled?: boolean;
  onChange: (tools: ToolGrant[]) => void;
}) {
  const sandbox = tools.some((tool) => tool.kind === "sandbox");
  return (
    <div className="field-wide team-tools" id="definition-tools" role="group" aria-labelledby="definition-tools-title">
      <h3 id="definition-tools-title">Tools</h3>
      <div className="team-tool-grid">
        <fieldset className="team-tool-group" data-tool-kind="sandbox">
          <legend>Code workspace</legend>
          <small>Runs commands and edits files in an isolated sandbox with no network.</small>
          <div className="team-tool-options">
            <label>
              <input
                id="definition-tool-sandbox"
                type="checkbox"
                disabled={disabled}
                checked={sandbox}
                onChange={(event) =>
                  onChange(
                    event.target.checked
                      ? [...tools, { kind: "sandbox" }]
                      : tools.filter((tool) => tool.kind !== "sandbox"),
                  )
                }
              />
              <span>Code sandbox</span>
            </label>
          </div>
        </fieldset>
        {SOURCES.map((source) => (
          <ResourceGroup
            key={source.kind}
            source={source}
            project={project}
            identity={identity}
            tools={tools}
            disabled={disabled}
            onChange={onChange}
          />
        ))}
        <GitGrants project={project} tools={tools} errors={errors} disabled={disabled} onChange={onChange} />
      </div>
    </div>
  );
}
