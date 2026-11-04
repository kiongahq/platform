"use client";
import { useState } from "react";
import { useQuery } from "@/hooks/useQuery";
import { useUnsaved } from "@/hooks/useUnsaved";
import { errorMessage } from "@/lib/api";
import { DataState, Feedback, ResourceTable, type ViewProps } from "@/features/shared/View";
import { Icon } from "@/components/ui/Icon";
import { definitionsApi, DEFINITIONS_PATH } from "./api";
import { RevisionHistory } from "./RevisionHistory";
import {
  ConfirmDialog,
  ErrorSummary,
  ProjectField,
  SelectField,
  TextField,
  conflictFrom,
  serverFieldErrors,
  setSearchParam,
  type SaveConflict,
} from "./shared";
import { ToolPicker } from "./ToolPicker";
import type { AgentDefinition, List } from "./types";
import {
  blankDefinition,
  DEFINITION_LIMITS,
  definitionPayload,
  TEXT_LIMITS,
  validateDefinition,
  type FieldErrors,
} from "./validation";

const VIEW = "agent-definitions";
const fieldId = (key: string) => `definition-${key.replaceAll(".", "-")}`;
const comparable = (draft: AgentDefinition) => JSON.stringify(definitionPayload(draft));

export function AgentDefinitionsView({ identity, project, resource, onNavigate }: ViewProps) {
  const writable = identity.permissions.agents_write === true;
  const [notice, setNotice] = useState("");
  const list = useQuery<List<AgentDefinition>>(resource ? null : DEFINITIONS_PATH);
  const [removing, setRemoving] = useState<AgentDefinition | null>(null);
  if (resource)
    return (
      <DefinitionEditor
        key={resource}
        id={resource === "new" ? "" : resource}
        project={project}
        identity={identity}
        writable={writable}
        notice={notice}
        onNotice={setNotice}
        onBack={() => {
          setNotice("");
          onNavigate(VIEW);
        }}
      />
    );
  const rows = (list.data?.items || []).filter((item) => !project || item.project_id === project);
  return (
    <section aria-label="Agent definitions" id="agent-definitions">
      <div className="toolbar">
        <p>Reusable agents that a team's coordinator can start as workers.</p>
        {writable && (
          <div className="button-row">
            <button className="primary" id="new-agent-definition" onClick={() => onNavigate(VIEW, "new")}>
              <Icon name="plus" />
              <span>New agent</span>
            </button>
          </div>
        )}
      </div>
      <Feedback message={notice} />
      <DataState loading={list.loading && !list.data} error={list.error} onRetry={list.reload}>
        {rows.length ? (
          <ResourceTable
            rows={rows}
            columns={[
              { key: "name", label: "Name" },
              { key: "description", label: "When to use" },
              { key: "workspace", label: "Workspace" },
              { key: "tools", label: "Tools", render: (row) => (row.tools || []).length },
              { key: "revision", label: "Revision" },
            ]}
            actions={(row) => (
              <>
                <button onClick={() => onNavigate(VIEW, row.id)} aria-label={`${writable ? "Edit" : "View"} ${row.name}`}>
                  {writable ? "Edit" : "View"}
                </button>
                {writable && (
                  <button className="danger" aria-label={`Delete ${row.name}`} onClick={() => setRemoving(row as AgentDefinition)}>
                    Delete
                  </button>
                )}
              </>
            )}
          />
        ) : (
          <div className="empty-state">
            <h2>No agents yet</h2>
            <p>{writable ? "Create an agent, or start from a team template on the Agent teams page." : "Nobody has defined an agent in this project."}</p>
          </div>
        )}
      </DataState>
      {removing && (
        <ConfirmDialog
          title={`Delete ${removing.name}`}
          confirm="Delete agent"
          danger
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            await definitionsApi.remove(removing.id!);
            setNotice(`Deleted ${removing.name}.`);
            list.reload();
          }}
        >
          <p>Running teams keep their snapshot. Teams that list this agent need a new member before their next run.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}
function DefinitionEditor({
  id,
  project,
  identity,
  writable,
  notice,
  onNotice,
  onBack,
}: {
  id: string;
  project: string;
  identity: ViewProps["identity"];
  writable: boolean;
  notice: string;
  onNotice: (value: string) => void;
  onBack: () => void;
}) {
  const query = useQuery<AgentDefinition>(id ? `${DEFINITIONS_PATH}/${encodeURIComponent(id)}` : null);
  if (id)
    return (
      <DataState loading={query.loading && !query.data} error={query.error} onRetry={query.reload}>
        {query.data && (
          <DefinitionForm initial={query.data} identity={identity} writable={writable} notice={notice} onNotice={onNotice} onBack={onBack} />
        )}
      </DataState>
    );
  return (
    <DefinitionForm initial={blankDefinition(project)} identity={identity} writable={writable} notice={notice} onNotice={onNotice} onBack={onBack} />
  );
}
export function DefinitionForm({
  initial,
  identity,
  writable,
  notice,
  onNotice,
  onBack,
}: {
  initial: AgentDefinition;
  identity: ViewProps["identity"];
  writable: boolean;
  notice: string;
  onNotice: (value: string) => void;
  onBack: () => void;
}) {
  const [draft, setDraft] = useState<AgentDefinition>(initial),
    [baseline, setBaseline] = useState(comparable(initial)),
    [saved, setSaved] = useState<AgentDefinition>(initial),
    [errors, setErrors] = useState<FieldErrors>({}),
    [attempt, setAttempt] = useState(0),
    [error, setError] = useState(""),
    [conflict, setConflict] = useState<SaveConflict>(null),
    [busy, setBusy] = useState(false);
  const dirty = comparable(draft) !== baseline;
  useUnsaved(dirty);
  const change = (patch: Partial<AgentDefinition>) => setDraft((old) => ({ ...old, ...patch }));
  const limit = (key: keyof AgentDefinition["limits"], value: string) =>
    setDraft((old) => ({ ...old, limits: { ...old.limits, [key]: value === "" ? Number.NaN : Number(value) } }));
  async function save(event: React.FormEvent) {
    event.preventDefault();
    const found = validateDefinition(draft);
    setErrors(found);
    setError("");
    setConflict(null);
    onNotice("");
    if (Object.keys(found).length) {
      setAttempt((value) => value + 1);
      return;
    }
    setBusy(true);
    try {
      const body = definitionPayload(draft, !!saved.id);
      const result = saved.id ? await definitionsApi.update(saved.id, body, saved.revision) : await definitionsApi.create(body);
      // Fields typed while the request was pending stay in the editor as unsaved changes.
      setSaved(result);
      setBaseline(comparable(draft));
      setDraft((old) => ({ ...old, id: result.id, revision: result.revision }));
      onNotice(`Saved ${result.name}, revision ${result.revision}.`);
      if (!saved.id && result.id) setSearchParam("resource", result.id);
    } catch (failure) {
      const clash = conflictFrom(failure);
      setConflict(clash);
      setErrors(serverFieldErrors(failure));
      setError(clash ? "Someone else saved this agent after you opened it." : errorMessage(failure));
    } finally {
      setBusy(false);
    }
  }
  const disabled = !writable || busy;
  return (
    <form className="action-form team-editor" id="agent-definition-form" onSubmit={save} noValidate aria-busy={busy}>
      <div className="toolbar">
        <button type="button" onClick={onBack}>Back to agents</button>
        <p id="definition-revision" className="team-muted">
          {saved.revision ? `Revision ${saved.revision}` : "Not saved yet"}
          {dirty ? ", unsaved changes" : ""}
        </p>
      </div>
      {!writable && <p className="feedback">You have read-only access to agents.</p>}
      <ErrorSummary errors={errors} idFor={fieldId} focusToken={attempt} />
      <Feedback error={error} message={notice} />
      {conflict?.latest && (
        <button
          type="button"
          onClick={() => {
            const latest = conflict.latest as AgentDefinition;
            setDraft(latest);
            setSaved(latest);
            setBaseline(comparable(latest));
            setConflict(null);
            setError("");
          }}
        >
          Discard my changes and load the latest revision
        </button>
      )}
      <div className="form-grid">
        <ProjectField id="definition-project_id" value={draft.project_id || ""} error={errors.project_id} disabled={disabled || !!saved.id} onChange={(value) => change({ project_id: value })} />
        <TextField id="definition-name" label="Name" required mono value={draft.name} error={errors.name} disabled={disabled} help="Lowercase letters, digits and hyphens. The coordinator starts this agent by name." onChange={(value) => change({ name: value })} />
        <TextField id="definition-description" label="When to use" required multiline rows={3} value={draft.description} error={errors.description} disabled={disabled} help={`Shown to the coordinator. At most ${TEXT_LIMITS.description.toLocaleString("en")} characters.`} onChange={(value) => change({ description: value })} />
        <TextField id="definition-instructions" label="Instructions" required multiline rows={10} value={draft.instructions} error={errors.instructions} disabled={disabled} help={`This agent's system prompt. At most ${TEXT_LIMITS.instructions.toLocaleString("en")} characters.`} onChange={(value) => change({ instructions: value })} />
        <TextField id="definition-model" label="Model" required mono value={draft.model} error={errors.model} disabled={disabled} onChange={(value) => change({ model: value })} />
        <SelectField id="definition-workspace" label="Workspace" value={draft.workspace} error={errors.workspace} disabled={disabled} help="Code sandbox and git access need the sandbox workspace." options={[{ value: "none", label: "None" }, { value: "sandbox", label: "Sandbox" }]} onChange={(value) => change({ workspace: value as AgentDefinition["workspace"] })} />
        <ToolPicker project={draft.project_id || ""} identity={identity} tools={draft.tools} errors={errors} disabled={disabled} onChange={(tools) => change({ tools })} />
        <fieldset className="field-wide team-limits">
          <legend>Limits for each worker</legend>
          <div className="form-grid">
            {(Object.keys(DEFINITION_LIMITS) as Array<keyof typeof DEFINITION_LIMITS>).map((key) => {
              const rule = DEFINITION_LIMITS[key];
              return (
                <TextField key={key} id={`definition-limits-${key}`} type="number" label={rule.label} min={rule.min} max={rule.max} value={Number.isNaN(draft.limits[key]) ? "" : draft.limits[key]} error={errors[`limits.${key}`]} disabled={disabled} help={`${rule.min.toLocaleString("en")} to ${rule.max.toLocaleString("en")}`} onChange={(value) => limit(key, value)} />
              );
            })}
          </div>
        </fieldset>
      </div>
      {writable && (
        <div className="button-row">
          <button className="primary" type="submit" id="definition-save" disabled={busy}>
            {busy ? "Saving…" : saved.id ? "Save revision" : "Create agent"}
          </button>
        </div>
      )}
      {saved.id && (
        <RevisionHistory<AgentDefinition>
          load={(signal) => definitionsApi.revisions(saved.id!, signal)}
          revision={saved.revision}
          disabled={!writable || busy}
          onLoad={(value) => {
            setDraft({ ...value, id: saved.id, revision: saved.revision });
            setErrors({});
            onNotice(`Loaded revision ${value.revision}. Save to make it current.`);
          }}
        />
      )}
    </form>
  );
}
