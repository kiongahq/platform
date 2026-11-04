// Pure derivations for the run view. No I/O, so each state transition is directly testable.
import type {
  Approval,
  RunEvent,
  TeamRun,
  TeamWorker,
  ToolCall,
  TranscriptMessage,
  Usage,
  WorkerError,
} from "./types";

const TERMINAL_RUN = new Set(["succeeded", "failed", "cancelled"]);
const MAX_EVENTS = 2000;
export const isTerminalRun = (run?: Pick<TeamRun, "status"> | null) =>
  !!run && TERMINAL_RUN.has(run.status);
export const canCancel = (run?: TeamRun | null) => run?.status === "running";
/** Resume requeues failed workers, so it applies only while something has failed. */
export const canResume = (run: TeamRun | null | undefined, workers: TeamWorker[]) =>
  !!run &&
  run.status !== "cancelling" &&
  run.status !== "cancelled" &&
  run.status !== "succeeded" &&
  (run.status === "failed" || workers.some((worker) => worker.status === "failed"));
export const canMessage = (run?: TeamRun | null) => run?.status === "running";
/** A completed worker can be messaged (it resumes); failed and cancelled ones cannot. */
export const messageTargets = (workers: TeamWorker[]) =>
  workers
    .filter((worker) => worker.status !== "failed" && worker.status !== "cancelled")
    .sort((a, b) => Number(b.role === "coordinator") - Number(a.role === "coordinator"));
/** Events keep their sequence order; a redelivered seq after a reconnect is ignored. */
export function mergeEvents(current: RunEvent[], incoming: RunEvent[]) {
  const known = new Set(current.map((event) => event.seq));
  const fresh = incoming.filter((event) => {
    if (typeof event.seq !== "number" || known.has(event.seq)) return false;
    known.add(event.seq);
    return true;
  });
  if (!fresh.length) return current;
  return [...current, ...fresh].sort((a, b) => a.seq - b.seq).slice(-MAX_EVENTS);
}
export const lastSeq = (events: RunEvent[], fallback = -1) =>
  events.reduce((max, event) => Math.max(max, event.seq), fallback);
/** Null means not reported. The console never shows an estimate in place of a measurement. */
export function tokens(usage?: Usage | null): number | null {
  if (!usage) return null;
  if (typeof usage.total_tokens === "number") return usage.total_tokens;
  if (typeof usage.input_tokens !== "number" && typeof usage.output_tokens !== "number")
    return null;
  return Number(usage.input_tokens || 0) + Number(usage.output_tokens || 0);
}
export function formatDuration(milliseconds: number) {
  const total = Math.max(0, Math.floor(milliseconds / 1000));
  const hours = Math.floor(total / 3600),
    minutes = Math.floor((total % 3600) / 60),
    seconds = total % 60;
  return hours
    ? `${hours}h ${minutes}m ${seconds}s`
    : minutes
      ? `${minutes}m ${seconds}s`
      : `${seconds}s`;
}
const LIVE_WORKER = new Set(["queued", "running", "waiting_retry", "waiting_approval", "idle"]);
export function workerElapsed(worker: TeamWorker, now: number): number | null {
  const start = Date.parse(worker.started_at || worker.created_at || "");
  if (Number.isNaN(start)) return null;
  if (LIVE_WORKER.has(worker.status)) return now - start;
  const end = Date.parse(worker.finished_at || worker.updated_at || "");
  return Number.isNaN(end) ? null : end - start;
}
export const errorCode = (error?: WorkerError) =>
  typeof error === "object" && error ? error.code || "" : "";
export const errorText = (error?: WorkerError) =>
  !error
    ? ""
    : typeof error === "string"
      ? error
      : [error.code, error.message].filter(Boolean).join(": ");
const SANDBOX_UNAVAILABLE = "sandbox_unavailable";
/** Only the gateway's structured result counts. Command output that merely contains the word does not. */
function reportsSandboxUnavailable(event: RunEvent) {
  if (event.type !== "worker.tool_result") return false;
  if (event.error?.code === SANDBOX_UNAVAILABLE) return true;
  try {
    return JSON.parse(event.output_preview || "null")?.error === SANDBOX_UNAVAILABLE;
  } catch {
    return false;
  }
}
export type SandboxNotice = {
  kind: "unavailable" | "unsafe";
  title: string;
  detail: string;
};
/** Unavailable outranks unsafe: no sandbox work can run at all. */
export function sandboxNotice(
  run: TeamRun | null | undefined,
  workers: TeamWorker[],
  events: RunEvent[],
): SandboxNotice | null {
  const state = run?.sandbox;
  const reported =
    state?.available === false ||
    workers.some((worker) => errorText(worker.last_error).includes(SANDBOX_UNAVAILABLE)) ||
    events.some(reportsSandboxUnavailable);
  if (reported)
    return {
      kind: "unavailable",
      title: "Sandbox unavailable",
      detail: `${state?.reason ? `${state.reason} ` : ""}Workers that need a code workspace cannot run code. Agents without a sandbox are not affected. Ask an operator to check the sandbox manager and its runtime, then resume the run.`,
    };
  if (state?.unsafe_runtime || state?.runtime_verified === false)
    return {
      kind: "unsafe",
      title: "Sandbox runtime is not isolated as required",
      detail: `This run uses ${state.runtime ? `the ${state.runtime} runtime` : "a runtime"} that has not been verified as the required isolated runtime. Use it for local development only, and do not give it untrusted repositories or tasks.`,
    };
  return null;
}
export const finalReport = (run: TeamRun | null | undefined, workers: TeamWorker[]) =>
  run?.report ||
  workers.find((worker) => worker.role === "coordinator")?.report ||
  "";
/** The redacted text an event carries, for the log. */
export const eventPreview = (event: RunEvent) =>
  event.message_preview ||
  event.output_preview ||
  event.input_preview ||
  (event.error ? `${event.error.code}: ${event.error.message}` : "");
/** Where a pending approval came from. The event log names the worker and tool. */
export function approvalOrigin(approval: Approval, events: RunEvent[]) {
  const event = events.find((item) => item.approval_id === approval.id);
  return {
    workerId: approval.worker_id || event?.worker_id,
    tool: approval.tool || event?.tool,
    input: event?.input_preview,
  };
}
export const messageFeed = (events: RunEvent[]) =>
  events.filter((event) => event.type === "worker.message");
export type TranscriptEntry =
  | { type: "message"; role: string; content: string }
  | { type: "tool"; call: ToolCall; result?: string };
/** Pairs each tool result with its call. A result with no call still appears. */
export function transcriptEntries(transcript: TranscriptMessage[] = []): TranscriptEntry[] {
  const entries: TranscriptEntry[] = [];
  const open = new Map<string, Extract<TranscriptEntry, { type: "tool" }>>();
  for (const message of transcript) {
    if (message.role === "tool") {
      const call = message.tool_call_id ? open.get(message.tool_call_id) : undefined;
      if (call) call.result = message.content || "";
      else
        entries.push({
          type: "tool",
          call: { id: message.tool_call_id || "", name: "Tool result" },
          result: message.content || "",
        });
      continue;
    }
    if (message.content)
      entries.push({ type: "message", role: message.role, content: message.content });
    for (const call of message.tool_calls || []) {
      const entry = { type: "tool" as const, call };
      open.set(call.id, entry);
      entries.push(entry);
    }
  }
  return entries;
}
/** Shows JSON results as indented JSON; everything else stays exactly as received. */
export function codeView(value: unknown): { language: "json" | "text"; source: string } {
  if (typeof value !== "string")
    return { language: "json", source: JSON.stringify(value ?? null, null, 2) ?? "null" };
  try {
    const parsed = JSON.parse(value);
    if (parsed && typeof parsed === "object")
      return { language: "json", source: JSON.stringify(parsed, null, 2) };
  } catch {
    /* plain output */
  }
  return { language: "text", source: value };
}
