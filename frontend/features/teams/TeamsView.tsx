"use client";
import { useRef, useState } from "react";
import { useQuery } from "@/hooks/useQuery";
import { useUnsaved } from "@/hooks/useUnsaved";
import { errorMessage } from "@/lib/api";
import { Icon } from "@/components/ui/Icon";
import { Select } from "@/components/ui/Select";
import { DataState, Feedback, Modal, ResourceTable, type ViewProps } from "@/features/shared/View";
import { definitionsApi, DEFINITIONS_PATH, runsApi, teamsApi, TEAMS_PATH } from "./api";
import { RevisionHistory } from "./RevisionHistory";
import {
  ConfirmDialog,
  ErrorSummary,
  ProjectField,
  SelectField,
  TextField,
  conflictFrom,
  failureText,
  setSearchParam,
  type SaveConflict,
} from "./shared";
import { applyTemplate, TEAM_TEMPLATES } from "./templates";
import type { AgentDefinition, List, Team } from "./types";
import { blankTeam, BUDGET_NOTE, MODEL_OPTIONS, TEAM_LIMITS, teamPayload, TEXT_LIMITS, validateTeam, type FieldErrors } from "./validation";

const VIEW = "agent-teams";
const fieldId = (key: string) => `team-${key.replaceAll(".", "-")}`;
const comparable = (draft: Team) => JSON.stringify(teamPayload(draft));

export function TeamsView({ identity, project, resource, onNavigate }: ViewProps) {
  const writable = identity.permissions.agents_write === true;
  const list = useQuery<List<Team>>(resource ? null : TEAMS_PATH);
  const [notice, setNotice] = useState(""),
    [seed, setSeed] = useState<Team | null>(null),
    [templateOpen, setTemplateOpen] = useState(false),
    [starting, setStarting] = useState<Team | null>(null),
    [removing, setRemoving] = useState<Team | null>(null);
  if (resource)
    return (
      <TeamEditor
        key={resource}
        id={resource === "new" ? "" : resource}
        project={project}
        seed={seed}
        writable={writable}
        notice={notice}
        onNotice={setNotice}
        onBack={() => {
          setNotice("");
          setSeed(null);
          onNavigate(VIEW);
        }}
        onStarted={(id) => onNavigate("team-runs", id)}
      />
    );
  const rows = (list.data?.items || []).filter((item) => !project || item.project_id === project);
  const runsLink = (team: Team) => {
    const query = new URLSearchParams({ team: team.id! });
    if (project) query.set("project", project);
    return `/console/team-runs?${query}`;
  };
  return (
    <section aria-label="Agent teams" id="agent-teams">
      <div className="toolbar">
        <p>A coordinator and the agents it can start, with shared rules and limits.</p>
        {writable && (
          <div className="button-row">
            <button id="use-team-template" onClick={() => setTemplateOpen(true)}>Use template</button>
            <button
              className="primary"
              id="new-agent-team"
              onClick={() => {
                setSeed(null);
                onNavigate(VIEW, "new");
              }}
            >
              <Icon name="plus" />
              <span>New team</span>
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
              { key: "description", label: "Description" },
              { key: "members", label: "Members", render: (row) => (row.members || []).length },
              { key: "revision", label: "Revision" },
            ]}
            actions={(row) => (
              <>
                {writable && (
                  <button className="primary" aria-label={`Start a run of ${row.name}`} onClick={() => setStarting(row as Team)}>
                    Start run
                  </button>
                )}
                <a className="button" href={runsLink(row as Team)} aria-label={`Runs of ${row.name}`}>Runs</a>
                <button aria-label={`${writable ? "Edit" : "View"} ${row.name}`} onClick={() => onNavigate(VIEW, row.id)}>
                  {writable ? "Edit" : "View"}
                </button>
                {writable && (
                  <button className="danger" aria-label={`Delete ${row.name}`} onClick={() => setRemoving(row as Team)}>
                    Delete
                  </button>
                )}
              </>
            )}
          />
        ) : (
          <div className="empty-state">
            <h2>No teams yet</h2>
            <p>{writable ? "Create a team from scratch with your own agents and rules, or optionally use a starter template." : "Nobody has defined a team in this project."}</p>
          </div>
        )}
      </DataState>
      {templateOpen && (
        <TemplateDialog
          project={project}
          onClose={() => setTemplateOpen(false)}
          onApplied={(team) => {
            setSeed(team);
            setTemplateOpen(false);
            setNotice("Agents are ready. Review this team, then save it. Add any grants the template lists on the Agent definitions page.");
            onNavigate(VIEW, "new");
          }}
        />
      )}
      {starting && (
        <StartRunDialog team={starting} onClose={() => setStarting(null)} onStarted={(id) => onNavigate("team-runs", id)} />
      )}
      {removing && (
        <ConfirmDialog
          title={`Delete ${removing.name}`}
          confirm="Delete team"
          danger
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            await teamsApi.remove(removing.id!);
            setNotice(`Deleted ${removing.name}.`);
            list.reload();
          }}
        >
          <p>Runs already started keep their snapshot of this team and are not stopped.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}
function TemplateDialog({
  project,
  onClose,
  onApplied,
}: {
  project: string;
  onClose: () => void;
  onApplied: (team: Team) => void;
}) {
  const [template, setTemplate] = useState(TEAM_TEMPLATES[0].id),
    [target, setTarget] = useState(project),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const chosen = TEAM_TEMPLATES.find((item) => item.id === template)!;
  return (
    <Modal title="Use a team template" onClose={onClose}>
      <form
        className="action-form"
        onSubmit={async (event) => {
          event.preventDefault();
          if (!target) {
            setError("Select a project.");
            return;
          }
          setBusy(true);
          setError("");
          try {
            onApplied(await applyTemplate(chosen, target, await definitionsApi.list()));
          } catch (failure) {
            setError(`${errorMessage(failure)} Agents already created are reused when you retry.`);
            setBusy(false);
          }
        }}
      >
        <div className="field">
          <label htmlFor="team-template">Template</label>
          <Select id="team-template" value={template} onChange={(event) => setTemplate(event.target.value)}>
            {TEAM_TEMPLATES.map((item) => (
              <option key={item.id} value={item.id}>{item.team.name}</option>
            ))}
          </Select>
          <small>{chosen.description}</small>
          <small>Creates or reuses these agents: {chosen.agents.map((agent) => agent.name).join(", ")}.</small>
        </div>
        <div className="team-setup-notes">
          <h3>What you add afterwards</h3>
          <ul>{chosen.setup_notes.map((note) => <li key={note}>{note}</li>)}</ul>
        </div>
        <ProjectField id="team-template-project" value={target} onChange={setTarget} disabled={busy} />
        <Feedback error={error} />
        <div className="button-row">
          <button type="button" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="primary" type="submit" disabled={busy}>{busy ? "Creating agents…" : "Create agents and open team"}</button>
        </div>
      </form>
    </Modal>
  );
}
export function StartRunDialog({
  team,
  teams,
  onClose,
  onStarted,
}: {
  team?: Team;
  teams?: Team[];
  onClose: () => void;
  onStarted: (id: string) => void;
}) {
  const [teamId, setTeamId] = useState(team?.id || ""),
    [input, setInput] = useState(""),
    [sideEffects, setSideEffects] = useState(false),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  // One key per dialog: a retry after a lost response cannot start a second run.
  const key = useRef(`ui-${crypto.randomUUID()}`);
  return (
    <Modal title={team ? `Start a run of ${team.name}` : "Start a run"} onClose={onClose}>
      <form
        className="action-form"
        id="start-team-run"
        noValidate
        onSubmit={async (event) => {
          event.preventDefault();
          if (!teamId) return setError("Select a team.");
          if (!input.trim()) return setError("Enter the task for this team.");
          setBusy(true);
          setError("");
          try {
            const run = await runsApi.start({ team_id: teamId, input, allow_side_effects: sideEffects, idempotency_key: key.current });
            onStarted(run.id);
          } catch (failure) {
            setError(errorMessage(failure));
            setBusy(false);
          }
        }}
      >
        {!team && (
          <div className="field">
            <label htmlFor="team-run-team">Team</label>
            <Select id="team-run-team" value={teamId} onChange={(event) => setTeamId(event.target.value)}>
              <option value="">Select a team</option>
              {(teams || []).map((item) => (
                <option key={item.id} value={item.id}>{item.name}</option>
              ))}
            </Select>
          </div>
        )}
        <div className="field">
          <label htmlFor="team-run-input">Task</label>
          <textarea id="team-run-input" rows={6} required value={input} disabled={busy} onChange={(event) => setInput(event.target.value)} />
        </div>
        <label>
          <input id="team-run-side-effects" type="checkbox" checked={sideEffects} disabled={busy} onChange={(event) => setSideEffects(event.target.checked)} />
          <span>Allow side effects</span>
        </label>
        <p className="team-muted">
          {sideEffects
            ? "Registered tools, API connections and git pushes can run, and each call waits for a person's approval."
            : "Registered tools, API connections and git pushes are refused for this run. Sandbox commands still run."}
        </p>
        <Feedback error={error} />
        <div className="button-row">
          <button type="button" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="primary" type="submit" disabled={busy}>{busy ? "Starting…" : "Start run"}</button>
        </div>
      </form>
    </Modal>
  );
}
function TeamEditor({
  id,
  project,
  seed,
  ...props
}: {
  id: string;
  project: string;
  seed: Team | null;
  writable: boolean;
  notice: string;
  onNotice: (value: string) => void;
  onBack: () => void;
  onStarted: (id: string) => void;
}) {
  const query = useQuery<Team>(id ? `${TEAMS_PATH}/${encodeURIComponent(id)}` : null);
  if (id)
    return (
      <DataState loading={query.loading && !query.data} error={query.error} onRetry={query.reload}>
        {query.data && <TeamForm initial={query.data} {...props} />}
      </DataState>
    );
  return <TeamForm initial={seed || blankTeam(project)} {...props} />;
}
export function TeamForm({
  initial,
  writable,
  notice,
  onNotice,
  onBack,
  onStarted,
}: {
  initial: Team;
  writable: boolean;
  notice: string;
  onNotice: (value: string) => void;
  onBack: () => void;
  onStarted: (id: string) => void;
}) {
  const definitions = useQuery<List<AgentDefinition>>(DEFINITIONS_PATH);
  const [draft, setDraft] = useState<Team>(initial),
    // A template fills an unsaved team, which counts as unsaved until it is saved.
    [baseline, setBaseline] = useState(comparable(initial.id ? initial : blankTeam(initial.project_id || ""))),
    [saved, setSaved] = useState<Team>(initial),
    [errors, setErrors] = useState<FieldErrors>({}),
    [attempt, setAttempt] = useState(0),
    [error, setError] = useState(""),
    [conflict, setConflict] = useState<SaveConflict>(null),
    [busy, setBusy] = useState(false),
    [starting, setStarting] = useState(false);
  const dirty = comparable(draft) !== baseline;
  useUnsaved(dirty);
  const change = (patch: Partial<Team>) => setDraft((old) => ({ ...old, ...patch }));
  const candidates = (definitions.data?.items || []).filter((item) => item.project_id === draft.project_id);
  const missing = draft.members.filter((id) => definitions.data && !candidates.some((item) => item.id === id));
  async function save(event: React.FormEvent) {
    event.preventDefault();
    const found = validateTeam(draft);
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
      const body = teamPayload(draft, saved.id ? saved.revision : undefined);
      const result = saved.id ? await teamsApi.update(saved.id, body) : await teamsApi.create(body);
      setSaved(result);
      setBaseline(comparable(draft));
      setDraft((old) => ({ ...old, id: result.id, revision: result.revision }));
      onNotice(`Saved ${result.name}, revision ${result.revision}.`);
      if (!saved.id && result.id) setSearchParam("resource", result.id);
    } catch (failure) {
      const clash = conflictFrom(failure);
      setConflict(clash);
      setError(clash ? "Someone else saved this team after you opened it." : failureText(failure));
    } finally {
      setBusy(false);
    }
  }
  const disabled = !writable || busy;
  return (
    <form className="action-form team-editor" id="agent-team-form" onSubmit={save} noValidate aria-busy={busy}>
      <div className="toolbar">
        <button type="button" onClick={onBack}>Back to teams</button>
        <p id="team-revision" className="team-muted">
          {saved.revision ? `Revision ${saved.revision}` : "Not saved yet"}
          {dirty ? ", unsaved changes" : ""}
        </p>
      </div>
      {!writable && <p className="feedback">You have read-only access to teams.</p>}
      <ErrorSummary errors={errors} idFor={fieldId} focusToken={attempt} />
      <Feedback error={error} message={notice} />
      {conflict?.latest && (
        <button
          type="button"
          onClick={() => {
            const latest = conflict.latest as Team;
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
        <ProjectField id="team-project_id" value={draft.project_id || ""} error={errors.project_id} disabled={disabled || !!saved.id} onChange={(value) => change({ project_id: value, members: [] })} />
        <TextField id="team-name" label="Name" required mono value={draft.name} error={errors.name} disabled={disabled} onChange={(value) => change({ name: value })} />
        <TextField id="team-description" label="Description" multiline rows={2} value={draft.description} error={errors.description} disabled={disabled} onChange={(value) => change({ description: value })} />
        <TextField id="team-coordinator-instructions" label="Coordinator instructions" required multiline rows={8} value={draft.coordinator.instructions} error={errors["coordinator.instructions"]} disabled={disabled} help="How the coordinator splits work, starts members and checks their reports." onChange={(value) => change({ coordinator: { ...draft.coordinator, instructions: value } })} />
        <SelectField id="team-coordinator-model" label="Coordinator model" value={draft.coordinator.model} error={errors["coordinator.model"]} disabled={disabled} options={MODEL_OPTIONS} onChange={(value) => change({ coordinator: { ...draft.coordinator, model: value } })} />
        <fieldset className="field-wide team-members" id="team-members" tabIndex={-1} aria-describedby={errors.members ? "team-members-error" : undefined}>
          <legend>Members</legend>
          <small>The coordinator can start only these agents. Select from 1 to 16.</small>
          {errors.members && <small className="team-field-error" id="team-members-error">{errors.members}</small>}
          <DataState loading={definitions.loading && !definitions.data} error={definitions.error} onRetry={definitions.reload}>
            {!draft.project_id ? (
              <p className="team-muted">Select a project to list its agents.</p>
            ) : !candidates.length && !missing.length ? (
              <p className="team-muted">This project has no agents yet. Create one on the Agent definitions page.</p>
            ) : (
              <div className="team-tool-options">
                {candidates.map((item) => (
                  <label key={item.id}>
                    <input
                      type="checkbox"
                      disabled={disabled}
                      checked={draft.members.includes(item.id!)}
                      onChange={(event) =>
                        change({
                          members: event.target.checked
                            ? [...draft.members, item.id!]
                            : draft.members.filter((id) => id !== item.id),
                        })
                      }
                    />
                    <span>
                      <span className="team-mono">{item.name}</span>
                      <small>{item.description}</small>
                    </span>
                  </label>
                ))}
                {missing.map((id) => (
                  <label key={id} className="team-tool-missing">
                    <input type="checkbox" disabled={disabled} checked onChange={() => change({ members: draft.members.filter((value) => value !== id) })} />
                    <span>
                      <span className="team-mono">{id}</span>
                      <small>This agent no longer exists in this project</small>
                    </span>
                  </label>
                ))}
              </div>
            )}
          </DataState>
        </fieldset>
        <TextField id="team-brief" label="Shared brief" multiline rows={5} value={draft.brief} error={errors.brief} disabled={disabled} help={`Rules appended to every agent's system prompt. At most ${TEXT_LIMITS.brief.toLocaleString("en")} characters.`} onChange={(value) => change({ brief: value })} />
        <fieldset className="field-wide team-limits">
          <legend>Limits for the whole run</legend>
          <small id="team-budget-note">{BUDGET_NOTE}</small>
          <div className="form-grid">
            {(Object.keys(TEAM_LIMITS) as Array<keyof typeof TEAM_LIMITS>).map((key) => {
              const rule = TEAM_LIMITS[key];
              return (
                <TextField
                  key={key}
                  id={`team-limits-${key}`}
                  type="number"
                  label={rule.label}
                  min={rule.min}
                  max={rule.max}
                  value={Number.isNaN(draft.limits[key]) ? "" : draft.limits[key]}
                  error={errors[`limits.${key}`]}
                  disabled={disabled}
                  help={`${rule.min.toLocaleString("en")} to ${rule.max.toLocaleString("en")}${key === "max_tokens" ? ". Default 5,000,000." : ""}`}
                  onChange={(value) => change({ limits: { ...draft.limits, [key]: value === "" ? Number.NaN : Number(value) } })}
                />
              );
            })}
          </div>
        </fieldset>
      </div>
      {writable && (
        <div className="button-row">
          <button className="primary" type="submit" id="team-save" disabled={busy}>
            {busy ? "Saving…" : saved.id ? "Save revision" : "Create team"}
          </button>
          {saved.id && (
            <button type="button" id="team-start-run" disabled={dirty || busy} onClick={() => setStarting(true)}>
              <Icon name="play" />
              <span>Start run</span>
            </button>
          )}
          {saved.id && dirty && <small className="team-muted">Save to start a run of the current team.</small>}
        </div>
      )}
      {saved.id && (
        <RevisionHistory<Team>
          load={(signal) => teamsApi.revisions(saved.id!, signal)}
          revision={saved.revision}
          disabled={!writable || busy}
          onLoad={(value) => {
            setDraft({ ...value.content, id: saved.id, project_id: saved.project_id, revision: saved.revision });
            setErrors({});
            onNotice(`Loaded revision ${value.revision}. Save to make it current.`);
          }}
        />
      )}
      {starting && <StartRunDialog team={saved} onClose={() => setStarting(false)} onStarted={onStarted} />}
    </form>
  );
}
