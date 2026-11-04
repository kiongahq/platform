"use client";
import { useEffect, useRef, useState } from "react";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { Select } from "@/components/ui/Select";
import { APIError, errorMessage } from "@/lib/api";
import type { Identity } from "@/lib/types";
import { DataState, Feedback, ResourceTable, Status } from "@/features/shared/View";
import { approvalsApi, runsApi } from "./api";
import { useTeamRun } from "./useTeamRun";
import {
  activeTime,
  approvalOrigin,
  canCancel,
  canMessage,
  canResume,
  codeView,
  eventPreview,
  finalReport,
  issueText,
  messageFeed,
  messageTargets,
  sandboxNotice,
  tokens,
  transcriptEntries,
  unframe,
  workerIssue,
} from "./runState";
import { ConfirmDialog, NOT_REPORTED, PlainText, formatTime, searchParam, setSearchParam } from "./shared";
import type { Approval, RunEvent, TeamRun, TeamWorker } from "./types";

// Everything a run returns comes from model or tool output, so it is untrusted. This file
// renders it only as React text or through CodeBlock, and never as markup.
const CONNECTION_LABEL = {
  connecting: "Connecting to live updates",
  live: "Live",
  polling: "Live stream unavailable, checking every few seconds",
  finished: "Run finished",
};
export function RunView({ id, identity, onBack, pollMs }: { id: string; identity: Identity; onBack: () => void; pollMs?: number }) {
  const live = useTeamRun(id, pollMs);
  const { run, workers, events, approvals } = live;
  const writable = identity.permissions.agents_write === true;
  const [dialog, setDialog] = useState<"cancel" | "resume" | null>(null),
    [notice, setNotice] = useState(""),
    [selected, setSelected] = useState(() => searchParam("worker"));
  const chosen = workers.find((worker) => worker.id === selected) || workers.find((worker) => worker.role === "coordinator") || workers[0];
  const banner = sandboxNotice(run, workers, events);
  const report = finalReport(run, workers);
  // Cancel and Resume remove their own trigger, so focus would otherwise fall to the page body.
  useEffect(() => {
    if (!dialog && document.activeElement === document.body) document.getElementById("team-run-status")?.focus({ preventScroll: true });
  }, [dialog]);
  return (
    <section aria-label="Team run" id="team-run" data-run-status={run?.status}>
      <div className="toolbar">
        <button type="button" onClick={onBack}>Back to runs</button>
        <span className="team-mono team-muted">{id}</span>
        <span className="team-muted" id="team-run-connection" role="status">{run ? CONNECTION_LABEL[live.connection] : ""}</span>
      </div>
      <DataState loading={live.loading && !run} error={!run ? live.error : ""} onRetry={live.refresh}>
        {run && (
          <>
            {banner && (
              <div className={`team-banner${banner.kind === "unavailable" ? " error" : ""}`} role={banner.kind === "unavailable" ? "alert" : "status"} id="team-sandbox-banner" data-kind={banner.kind}>
                <h2>{banner.title}</h2>
                <p>{banner.detail}</p>
              </div>
            )}
            {/* Keeps the last good data on screen while a refresh is failing. */}
            <Feedback error={live.error} message={notice} />
            <RunSummary run={run} workers={workers} />
            {writable && (canCancel(run) || canResume(run, workers)) && (
              <div className="button-row" id="team-run-actions">
                {canCancel(run) && <button type="button" className="danger" id="team-run-cancel" onClick={() => setDialog("cancel")}>Cancel run</button>}
                {canResume(run, workers) && <button type="button" className="primary" id="team-run-resume" onClick={() => setDialog("resume")}>Resume failed workers</button>}
              </div>
            )}
            <Approvals approvals={approvals} error={live.approvalsError} workers={workers} events={events} writable={writable} onDecided={live.refresh} />
            {report && (
              <section className="panel team-report" id="team-run-report" aria-labelledby="team-run-report-title">
                <h2 id="team-run-report-title">Final report</h2>
                <PlainText>{report}</PlainText>
              </section>
            )}
            <h2>Workers</h2>
            <WorkerTable
              workers={workers}
              selected={chosen?.id}
              onSelect={(value) => {
                setSelected(value);
                setSearchParam("worker", value);
              }}
            />
            <div className="team-run-grid">
              {chosen ? <Transcript runId={id} worker={chosen} events={events} /> : <p className="empty-state">No workers have started yet.</p>}
              <div className="team-run-side">
                <MessageFeed events={events} workers={workers} />
                <SendMessage run={run} workers={workers} writable={writable} onSent={live.refresh} />
              </div>
            </div>
            <details className="team-event-log" id="team-run-events">
              <summary>Event log ({events.length})</summary>
              <ResourceTable
                rows={events.map((event) => ({ ...event, id: `event-${event.seq}` }))}
                columns={[
                  { key: "seq", label: "Sequence" },
                  { key: "type", label: "Event" },
                  { key: "agent", label: "Worker", render: (row) => workerName(workers, row.worker_id) || row.agent || "—" },
                  { key: "tool", label: "Tool", render: (row) => row.tool || "—" },
                  { key: "preview", label: "Preview", render: (row) => <span className="team-text">{eventPreview(row as RunEvent) || "—"}</span> },
                  { key: "ts", label: "Time", render: (row) => formatTime(row.ts) },
                ]}
              />
            </details>
          </>
        )}
      </DataState>
      {dialog === "cancel" && (
        <ConfirmDialog
          title="Cancel this run"
          confirm="Cancel run"
          danger
          onClose={() => setDialog(null)}
          onConfirm={async () => {
            await runsApi.cancel(id);
            setNotice("Cancelling. Live workers stop after their current step.");
            await live.refresh();
          }}
        >
          <p>All live workers stop. A tool call already in progress may still finish, and a cancelled run cannot be resumed.</p>
        </ConfirmDialog>
      )}
      {dialog === "resume" && (
        <ConfirmDialog
          title="Resume failed workers"
          confirm="Resume"
          onClose={() => setDialog(null)}
          onConfirm={async () => {
            await runsApi.resume(id);
            setNotice("Failed workers were requeued.");
            await live.refresh();
          }}
        >
          <p>Use this after the cause is fixed, for example a restored provider or sandbox. Spent budgets stay spent. A call that may already have happened is not run again; the worker is told its outcome is unknown.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}
const workerName = (workers: TeamWorker[], id?: string) => workers.find((worker) => worker.id === id)?.agent || "";
function RunSummary({ run, workers }: { run: TeamRun; workers: TeamWorker[] }) {
  const used = tokens(run.usage);
  return (
    <dl className="stat-row team-run-stats">
      <div><dt>Status</dt><dd id="team-run-status" tabIndex={-1}><Status value={run.status} /></dd></div>
      <div><dt>Workers</dt><dd>{workers.length}</dd></div>
      <div><dt>Tokens</dt><dd>{used === null ? NOT_REPORTED : used.toLocaleString("en")}</dd></div>
      <div><dt>Resumes</dt><dd>{run.resume_count ?? 0}</dd></div>
      <div><dt>Side effects</dt><dd>{run.allow_side_effects ? "Allowed with approval" : "Not allowed"}</dd></div>
      <div><dt>Started by</dt><dd>{run.subject || NOT_REPORTED}</dd></div>
      {run.error && <div className="team-run-error"><dt>Error</dt><dd className="team-text">{issueText(run.error, run.error_message)}</dd></div>}
    </dl>
  );
}
/** At phone width the metric columns collapse into this line, so nothing needs sideways scrolling. */
function WorkerCompact({ worker }: { worker: TeamWorker }) {
  const issue = workerIssue(worker);
  return (
    <small className="team-worker-compact">
      Turns {worker.turns ?? NOT_REPORTED}, tokens {tokens(worker.usage)?.toLocaleString("en") ?? NOT_REPORTED}, time {activeTime(worker) ?? NOT_REPORTED}
      {issue && <span className="team-text">{issue}</span>}
    </small>
  );
}
function WorkerTable({ workers, selected, onSelect }: { workers: TeamWorker[]; selected?: string; onSelect: (id: string) => void }) {
  return (
    <div id="team-workers">
      <ResourceTable
        rows={workers}
        columns={[
          { key: "agent", label: "Worker", render: (row) => <><span className="team-mono">{row.agent}</span>{row.role === "coordinator" && <small>Coordinator</small>}</> },
          { key: "status", label: "Status", render: (row) => <><Status value={row.status} />{row.status === "waiting_retry" && row.next_attempt_at && <small>Next attempt {formatTime(row.next_attempt_at)}</small>}<WorkerCompact worker={row as TeamWorker} /></> },
          { key: "turns", label: "Turns", render: (row) => row.turns ?? NOT_REPORTED },
          { key: "tokens", label: "Tokens", render: (row) => tokens(row.usage)?.toLocaleString("en") ?? NOT_REPORTED },
          { key: "time", label: "Time", render: (row) => activeTime(row as TeamWorker) ?? NOT_REPORTED },
          { key: "last_error", label: "Last error", render: (row) => <span className="team-text">{workerIssue(row as TeamWorker) || "—"}</span> },
        ]}
        actions={(row) => (
          <button type="button" aria-pressed={row.id === selected} aria-label={`Show transcript of ${row.agent}`} onClick={() => onSelect(row.id)}>
            Transcript
          </button>
        )}
      />
    </div>
  );
}
function Approvals({ approvals, error, workers, events, writable, onDecided }: { approvals: Approval[]; error: string; workers: TeamWorker[]; events: RunEvent[]; writable: boolean; onDecided: () => Promise<void> }) {
  const heading = useRef<HTMLHeadingElement>(null);
  // The heading stays once a request has appeared, so focus has somewhere to land after a decision removes the card.
  const seen = useRef(false);
  if (approvals.length) seen.current = true;
  if (!seen.current && !error) return null;
  return (
    <section className="team-approvals" id="team-approvals" aria-labelledby="team-approvals-title">
      <h2 id="team-approvals-title" ref={heading} tabIndex={-1}>Pending approvals ({approvals.length})</h2>
      {error && <Feedback error={`Approvals could not be loaded: ${error}`} />}
      {!approvals.length && !error && <p className="team-muted">Nothing is waiting for a decision.</p>}
      {!writable && approvals.length > 0 && <p className="feedback">Deciding an approval needs agents write access in this project. You can see what is waiting, but a project member with write access must decide.</p>}
      {approvals.map((approval) => (
        <ApprovalCard key={approval.id} approval={approval} origin={approvalOrigin(approval, events)} worker={workerName(workers, approvalOrigin(approval, events).workerId)} writable={writable} onDecided={async () => { heading.current?.focus(); await onDecided(); }} />
      ))}
    </section>
  );
}
function ApprovalCard({ approval, origin, worker, writable, onDecided }: { approval: Approval; origin: ReturnType<typeof approvalOrigin>; worker: string; writable: boolean; onDecided: () => Promise<void> }) {
  const [comment, setComment] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function decide(decision: "approve" | "reject") {
    setBusy(true);
    setError("");
    try {
      await approvalsApi.decide(approval.id, { decision, comment });
      await onDecided();
    } catch (failure) {
      if (failure instanceof APIError && failure.code === "approval_closed") {
        setError("Someone already decided this request, or it has expired.");
        await onDecided();
      } else setError(errorMessage(failure));
    } finally {
      setBusy(false);
    }
  }
  const inputId = `approval-comment-${approval.id}`;
  return (
    <article className="panel team-approval" data-approval={approval.id}>
      <p className="team-muted">
        {worker && <>Worker <span className="team-mono">{worker}</span>. </>}
        {origin.tool && <>Tool <span className="team-mono">{origin.tool}</span>. </>}
        Requested {formatTime(approval.created_at)}.
      </p>
      <PlainText>{approval.message || "No description was provided."}</PlainText>
      {origin.input && <CodeBlock language={codeView(origin.input).language} format={false} filename="Call input (redacted)">{codeView(origin.input).source}</CodeBlock>}
      <Feedback error={error} />
      {writable && (
        <>
          <div className="field">
            <label htmlFor={inputId}>Comment</label>
            <input id={inputId} maxLength={1000} value={comment} disabled={busy} onChange={(event) => setComment(event.target.value)} />
          </div>
          <div className="button-row">
            <button type="button" className="primary" disabled={busy} aria-label={`Approve ${origin.tool || "request"} ${approval.id}`} onClick={() => void decide("approve")}>Approve</button>
            <button type="button" className="danger" disabled={busy} aria-label={`Reject ${origin.tool || "request"} ${approval.id}`} onClick={() => void decide("reject")}>Reject</button>
          </div>
        </>
      )}
    </article>
  );
}
/** The sender comes from the gateway's wrapper, which a model cannot set. */
const deliveredFrom = (source?: string) =>
  source === "person"
    ? "From a person"
    : source === "coordinator"
      ? "From the coordinator"
      : source?.startsWith("worker:")
        ? `From ${source.slice("worker:".length)}`
        : "Delivered to worker";
function Transcript({ runId, worker, events }: { runId: string; worker: TeamWorker; events: RunEvent[] }) {
  const [detail, setDetail] = useState<TeamWorker | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  // New activity for this worker changes the key, which reloads its transcript.
  const activity = `${worker.id}:${worker.status}:${worker.turns ?? 0}:${events.filter((event) => event.worker_id === worker.id).length}`;
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    runsApi
      .worker(runId, worker.id, controller.signal)
      .then((value) => {
        if (controller.signal.aborted) return;
        setDetail(value);
        setError("");
        setLoading(false);
      })
      .catch((failure) => {
        if (controller.signal.aborted) return;
        setError(errorMessage(failure));
        setLoading(false);
      });
    return () => controller.abort();
  }, [runId, activity]);
  const current = detail?.id === worker.id ? detail : null;
  const entries = transcriptEntries(current?.transcript);
  return (
    <section className="team-transcript" id="team-transcript" aria-labelledby="team-transcript-title">
      <h2 id="team-transcript-title">Transcript of <span className="team-mono">{worker.agent}</span></h2>
      {(current?.task || worker.task) && (
        <details className="team-task">
          <summary>Assigned task</summary>
          <PlainText>{current?.task || worker.task || ""}</PlainText>
        </details>
      )}
      <Feedback error={error} />
      {!current && loading && <p role="status" className="team-muted">Loading transcript…</p>}
      {current && !entries.length && <p className="team-muted">No messages yet.</p>}
      <ol className="team-transcript-list">
        {entries.map((entry, index) =>
          entry.type === "message" ? (
            <li key={index} data-role={entry.role}>
              <b>{entry.role === "assistant" ? worker.agent : entry.role === "user" ? deliveredFrom(unframe(entry.content).source) : entry.role}</b>
              <PlainText>{entry.role === "user" ? unframe(entry.content).body : entry.content}</PlainText>
            </li>
          ) : (
            <li key={index} data-role="tool" data-tool-call={entry.call.id}>
              <b>Tool call <span className="team-mono">{entry.call.name}</span></b>
              {entry.call.status && <Status value={entry.call.status} />}
              {entry.call.arguments !== undefined && (
                <CodeBlock language="json" format={false} filename="Arguments">{codeView(entry.call.arguments).source}</CodeBlock>
              )}
              {entry.result === undefined ? (
                <p className="team-muted">No result yet.</p>
              ) : (
                <CodeBlock language={codeView(entry.result).language} format={false} filename="Result (redacted)">{codeView(entry.result).source}</CodeBlock>
              )}
            </li>
          ),
        )}
      </ol>
      {current?.report && (
        <>
          <h3>Report from this worker</h3>
          <PlainText>{current.report}</PlainText>
        </>
      )}
      {current?.last_error && <p className="team-muted">Stopped with <span className="team-mono">{current.last_error}</span>.</p>}
    </section>
  );
}
function MessageFeed({ events, workers }: { events: RunEvent[]; workers: TeamWorker[] }) {
  const feed = messageFeed(events);
  const label = (value?: string) => (value === "person" ? "Person" : workerName(workers, value) || value || "Unknown");
  return (
    <section id="team-messages" aria-labelledby="team-messages-title">
      <h2 id="team-messages-title">Messages</h2>
      {feed.length ? (
        <ol className="team-feed" role="log" aria-live="polite" aria-label="Message feed">
          {feed.map((event) => (
            <li key={event.seq}>
              <small><span className="team-mono">{label(event.from || event.worker_id)}</span> to <span className="team-mono">{label(event.to)}</span>, {formatTime(event.ts)}</small>
              <PlainText>{event.message_preview || ""}</PlainText>
            </li>
          ))}
        </ol>
      ) : (
        <p className="team-muted">No messages yet.</p>
      )}
    </section>
  );
}
function SendMessage({ run, workers, writable, onSent }: { run: TeamRun; workers: TeamWorker[]; writable: boolean; onSent: () => Promise<void> }) {
  const targets = messageTargets([...workers]);
  const [to, setTo] = useState(""),
    [text, setText] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [sent, setSent] = useState("");
  if (!writable) return null;
  const target = targets.some((worker) => worker.id === to) ? to : targets[0]?.id || "";
  const open = canMessage(run) && !!target;
  return (
    <form
      className="action-form team-send"
      id="team-send-message"
      onSubmit={async (event) => {
        event.preventDefault();
        if (!text.trim()) return setError("Enter a message.");
        setBusy(true);
        setError("");
        setSent("");
        try {
          await runsApi.message(run.id, { to: target, text });
          // The text is cleared only after the gateway accepted it.
          setText("");
          setSent("Message sent.");
          await onSent();
        } catch (failure) {
          setError(errorMessage(failure));
        } finally {
          setBusy(false);
        }
      }}
    >
      <h2>Send a message</h2>
      {!open && <p className="team-muted">{run.status === "running" ? "No worker can receive a message." : "This run is not accepting messages."}</p>}
      <div className="field">
        <label htmlFor="team-message-to">To</label>
        <Select id="team-message-to" value={target} disabled={!open || busy} onChange={(event) => setTo(event.target.value)}>
          {targets.map((worker) => (
            <option key={worker.id} value={worker.id}>
              {worker.agent}{worker.role === "coordinator" ? " (coordinator)" : worker.status === "completed" ? " (completed, resumes it)" : ""}
            </option>
          ))}
        </Select>
      </div>
      <div className="field">
        <label htmlFor="team-message-text">Message</label>
        <textarea id="team-message-text" rows={3} value={text} disabled={!open || busy} onChange={(event) => setText(event.target.value)} />
      </div>
      <Feedback error={error} message={sent} />
      <div className="button-row">
        <button type="submit" className="primary" disabled={!open || busy}>{busy ? "Sending…" : "Send message"}</button>
      </div>
    </form>
  );
}
