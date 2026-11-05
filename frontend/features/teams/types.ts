// Typed shapes for the Agent Teams API (build spec section 2). The gateway is the
// authority on every rule; the console mirrors the published limits for early feedback.
export type ToolGrant =
  | { kind: "sandbox" }
  | { kind: "git"; repository: string; connection?: string }
  | { kind: "tool" | "connection" | "knowledge" | "dataset"; ref: string };
export type Workspace = "none" | "sandbox";
export type DefinitionLimits = {
  max_turns: number;
  max_tokens: number;
  max_seconds: number;
};
export type AgentDefinition = {
  id?: string;
  project_id?: string;
  revision?: number;
  name: string;
  description: string;
  instructions: string;
  model: string;
  tools: ToolGrant[];
  workspace: Workspace;
  limits: DefinitionLimits;
  updated_at?: string;
};
export type TeamLimits = {
  max_workers_total: number;
  max_concurrent_workers: number;
  max_tokens: number;
  max_seconds: number;
};
export type Team = {
  id?: string;
  project_id?: string;
  revision?: number;
  name: string;
  description: string;
  coordinator: { instructions: string; model: string };
  members: string[];
  brief: string;
  limits: TeamLimits;
  updated_at?: string;
};
export type Revision<T> = {
  revision: number;
  saved_at?: string;
  content: T;
};
export type Usage = {
  input_tokens?: number;
  output_tokens?: number;
  total_tokens?: number;
};
export type RunStatus =
  | "running"
  | "succeeded"
  | "failed"
  | "cancelling"
  | "cancelled";
export type WorkerStatus =
  | "queued"
  | "running"
  | "waiting_retry"
  | "waiting_approval"
  | "idle"
  | "completed"
  | "failed"
  | "cancelled";
/** Reported by the gateway when the run's team has sandbox members. */
export type SandboxState = {
  available: boolean;
  runtime?: string;
  runtime_verified?: boolean;
  driver?: string;
  unsafe_runtime?: boolean;
  reason?: string;
};
export type TeamRun = {
  id: string;
  project_id?: string;
  team_id: string;
  status: RunStatus;
  input?: string;
  allow_side_effects?: boolean;
  subject?: string;
  roles?: string[];
  error?: string;
  error_message?: string;
  report?: string;
  usage?: Usage;
  resume_count?: number;
  content_sha256?: string;
  coordinator_id?: string;
  worker_ids?: string[];
  sandbox?: SandboxState;
  snapshot?: { team_id?: string; team_revision?: number; team?: Partial<Team>; members?: Partial<AgentDefinition>[] };
  created_at?: string;
  updated_at?: string;
  finished_at?: string;
};
export type ToolCall = {
  id: string;
  name: string;
  arguments?: unknown;
  status?: string;
};
export type TranscriptMessage = {
  role: string;
  content?: string;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
};
export type TeamWorker = {
  id: string;
  run_id?: string;
  project_id?: string;
  role?: string;
  agent: string;
  task?: string;
  status: WorkerStatus;
  transcript?: TranscriptMessage[];
  usage?: Usage;
  turns?: number;
  active_millis?: number;
  next_attempt_at?: string;
  report?: string;
  last_error?: string;
  last_error_message?: string;
  last_error_resumable?: boolean;
  sandbox_id?: string;
  created_at?: string;
  updated_at?: string;
};
/** One log entry, contracts agent-team/team-run-event.v1. Previews are redacted by the gateway. */
export type RunEvent = {
  seq: number;
  type: string;
  run_id?: string;
  ts?: string;
  worker_id?: string;
  agent?: string;
  role?: "coordinator" | "worker";
  turn?: number;
  attempt?: number;
  status?: string;
  tool?: string;
  tool_call_id?: string;
  duration_ms?: number;
  input_preview?: string;
  output_preview?: string;
  message_preview?: string;
  from?: string;
  to?: string;
  next_attempt_at?: string;
  retry_after_seconds?: number;
  approval_id?: string;
  resume_count?: number;
  usage?: Usage;
  error?: { code: string; message: string };
};
export type EventPage = {
  events?: RunEvent[];
  done?: boolean;
};
export type Approval = {
  id: string;
  run_id?: string;
  worker_id?: string;
  tool_name?: string;
  message?: string;
  status: string;
  created_at?: string;
  expires_at?: string;
};
export type ProjectResource = {
  id?: string;
  name?: string;
  project_id?: string;
  description?: string;
};
export type List<T> = { items?: T[] };
