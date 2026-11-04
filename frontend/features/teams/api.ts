// Typed client for the Agent Teams routes (build spec section 2). Every POST sends a JSON
// body because the gateway's cross-origin guard accepts JSON only.
import { request } from "@/lib/api";
import { items } from "@/lib/types";
import type {
  AgentDefinition,
  Approval,
  EventPage,
  Revision,
  Team,
  TeamRun,
  TeamWorker,
} from "./types";

export const DEFINITIONS_PATH = "/api/v1/agent-definitions";
export const TEAMS_PATH = "/api/v1/agent-teams";
export const RUNS_PATH = "/api/v1/team-runs";
export const APPROVALS_PATH = "/api/v1/agent-approvals";
const part = encodeURIComponent;

function resource<T extends { id?: string; revision?: number }>(base: string) {
  return {
    list: async (signal?: AbortSignal) =>
      items<T>(await request(base, { signal })),
    get: (id: string, signal?: AbortSignal) =>
      request<T>(`${base}/${part(id)}`, { signal }),
    create: (body: T) => request<T>(base, { method: "POST", body }),
    update: (id: string, body: T) =>
      request<T>(`${base}/${part(id)}`, { method: "PUT", body }),
    remove: (id: string) =>
      request<null>(`${base}/${part(id)}`, { method: "DELETE" }),
    revisions: async (id: string, signal?: AbortSignal) =>
      items<Revision<T>>(
        await request(`${base}/${part(id)}/revisions`, { signal }),
      ),
  };
}
export const definitionsApi = resource<AgentDefinition>(DEFINITIONS_PATH);
export const teamsApi = resource<Team>(TEAMS_PATH);

const runPath = (id: string) => `${RUNS_PATH}/${part(id)}`;
export const runEventsStreamUrl = (id: string, after: number) =>
  `${runPath(id)}/events/stream?after=${after}`;
export const runsApi = {
  listPath: (teamId = "") =>
    teamId ? `${RUNS_PATH}?team_id=${part(teamId)}` : RUNS_PATH,
  start: (body: {
    team_id: string;
    input: string;
    allow_side_effects: boolean;
    idempotency_key?: string;
  }) => request<TeamRun>(RUNS_PATH, { method: "POST", body }),
  get: (id: string, signal?: AbortSignal) =>
    request<TeamRun>(runPath(id), { signal }),
  events: (id: string, after: number, signal?: AbortSignal) =>
    request<EventPage>(`${runPath(id)}/events?after=${after}`, { signal }),
  workers: async (id: string, signal?: AbortSignal) =>
    items<TeamWorker>(await request(`${runPath(id)}/workers`, { signal })),
  worker: (id: string, workerId: string, signal?: AbortSignal) =>
    request<TeamWorker>(`${runPath(id)}/workers/${part(workerId)}`, { signal }),
  message: (id: string, body: { to: string; text: string }) =>
    request(`${runPath(id)}/messages`, { method: "POST", body }),
  cancel: (id: string) =>
    request<TeamRun>(`${runPath(id)}/cancel`, { method: "POST", body: {} }),
  resume: (id: string) =>
    request<TeamRun>(`${runPath(id)}/resume`, { method: "POST", body: {} }),
};
export const approvalsApi = {
  pending: async (runId: string, signal?: AbortSignal) =>
    items<Approval>(
      await request(
        `${APPROVALS_PATH}?run_id=${part(runId)}&status=pending`,
        { signal },
      ),
    ),
  decide: (id: string, body: { decision: "approve" | "reject"; comment: string }) =>
    request<Approval>(`${APPROVALS_PATH}/${part(id)}/decision`, {
      method: "POST",
      body,
    }),
};
