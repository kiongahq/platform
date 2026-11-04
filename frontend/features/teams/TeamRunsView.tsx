"use client";
import { useEffect, useState } from "react";
import { useQuery } from "@/hooks/useQuery";
import { Icon } from "@/components/ui/Icon";
import { Select } from "@/components/ui/Select";
import { DataState, ResourceTable, Status, type ViewProps } from "@/features/shared/View";
import { runsApi, TEAMS_PATH } from "./api";
import { RunView } from "./RunView";
import { isTerminalRun, tokens } from "./runState";
import { formatTime, NOT_REPORTED, searchParam, setSearchParam } from "./shared";
import { StartRunDialog } from "./TeamsView";
import type { List, Team, TeamRun } from "./types";

const VIEW = "team-runs";
export function TeamRunsView(props: ViewProps) {
  const { identity, project, resource, onNavigate } = props;
  if (resource) return <RunView id={resource} identity={identity} onBack={() => onNavigate(VIEW)} />;
  return <RunList {...props} />;
}
function RunList({ identity, project, onNavigate }: ViewProps) {
  const writable = identity.permissions.agents_write === true;
  const [team, setTeam] = useState(() => searchParam("team")),
    [starting, setStarting] = useState(false);
  const teams = useQuery<List<Team>>(TEAMS_PATH),
    runs = useQuery<List<TeamRun>>(runsApi.listPath(team));
  const teamRows = (teams.data?.items || []).filter((item) => !project || item.project_id === project);
  const rows = (runs.data?.items || []).filter((item) => !project || !item.project_id || item.project_id === project);
  const names = new Map((teams.data?.items || []).map((item) => [item.id, item.name]));
  const active = rows.some((row) => !isTerminalRun(row));
  useEffect(() => {
    if (!active) return;
    const timer = setInterval(runs.reload, 5000);
    return () => clearInterval(timer);
  }, [active, runs.reload]);
  return (
    <section aria-label="Team runs" id="team-runs">
      <div className="toolbar">
        <label className="project-filter">
          Team
          <Select
            id="team-run-filter"
            value={team}
            onChange={(event) => {
              setTeam(event.target.value);
              setSearchParam("team", event.target.value);
            }}
          >
            <option value="">All teams</option>
            {teamRows.map((item) => (
              <option key={item.id} value={item.id}>{item.name}</option>
            ))}
          </Select>
        </label>
        {writable && (
          <div className="button-row">
            <button className="primary" id="new-team-run" onClick={() => setStarting(true)}>
              <Icon name="play" />
              <span>Start run</span>
            </button>
          </div>
        )}
      </div>
      <DataState loading={runs.loading && !runs.data} error={runs.error} onRetry={runs.reload}>
        {rows.length ? (
          <ResourceTable
            rows={rows}
            columns={[
              { key: "id", label: "Run", render: (row) => <span className="team-mono">{row.id}</span> },
              { key: "team", label: "Team", render: (row) => row.snapshot?.team?.name || names.get(row.team_id) || row.team_id },
              { key: "status", label: "Status", render: (row) => <Status value={row.status} /> },
              { key: "subject", label: "Started by", render: (row) => row.subject || NOT_REPORTED },
              { key: "created_at", label: "Started", render: (row) => formatTime(row.created_at) },
              { key: "tokens", label: "Tokens", render: (row) => tokens(row.usage)?.toLocaleString("en") ?? NOT_REPORTED },
              { key: "resume_count", label: "Resumes", render: (row) => row.resume_count ?? 0 },
            ]}
            actions={(row) => (
              <button aria-label={`Open run ${row.id}`} onClick={() => onNavigate(VIEW, row.id)}>Open</button>
            )}
          />
        ) : (
          <div className="empty-state">
            <h2>No runs yet</h2>
            <p>{writable ? "Start a run from a team to see its workers here." : "No team has been run in this project."}</p>
          </div>
        )}
      </DataState>
      {starting && <StartRunDialog teams={teamRows} onClose={() => setStarting(false)} onStarted={(id) => onNavigate(VIEW, id)} />}
    </section>
  );
}
