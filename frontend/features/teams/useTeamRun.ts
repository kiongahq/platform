"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage } from "@/lib/api";
import { approvalsApi, runEventsStreamUrl, runsApi } from "./api";
import { lastSeq, mergeEvents } from "./runState";
import type { Approval, RunEvent, TeamRun, TeamWorker } from "./types";

const REFRESH_DEBOUNCE_MS = 250;
const SAFETY_REFRESH_MS = 10_000;
// The gateway ends a stream after 5 minutes. A stream that lived this long reconnects; one that
// fails sooner means streaming does not work here, so the view polls instead.
const STREAM_HEALTHY_MS = 30_000;
export type RunConnection = "connecting" | "live" | "polling" | "finished";

/**
 * Live state of one run. The event log is the change signal; the run, worker and approval
 * documents stay the source of truth, so a lost event cannot leave the view wrong.
 * Reads the log until the gateway says it is complete (`done`, or the stream's `end`). That comes
 * after the run settles, so the last events of a run that just finished are not lost. A resume
 * changes `resume_count`, which starts the subscription again.
 */
export function useTeamRun(id: string, pollMs = 2000) {
  const [run, setRun] = useState<TeamRun | null>(null),
    [workers, setWorkers] = useState<TeamWorker[]>([]),
    [events, setEvents] = useState<RunEvent[]>([]),
    [approvals, setApprovals] = useState<Approval[]>([]),
    [approvalsError, setApprovalsError] = useState(""),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [connection, setConnection] = useState<RunConnection>("connecting");
  const after = useRef(-1),
    latestRefresh = useRef(0),
    alive = useRef(true),
    catchUp = useRef<() => Promise<unknown>>(() => Promise.resolve());
  const refresh = useCallback(async () => {
    const ticket = ++latestRefresh.current;
    const [runResult, workersResult, approvalsResult] = await Promise.allSettled([
      runsApi.get(id),
      runsApi.workers(id),
      approvalsApi.pending(id),
    ]);
    // An older, slower response never overwrites a newer one.
    if (!alive.current || ticket !== latestRefresh.current) return;
    if (runResult.status === "fulfilled") setRun(runResult.value);
    if (workersResult.status === "fulfilled") setWorkers(workersResult.value);
    if (approvalsResult.status === "fulfilled") {
      setApprovals(approvalsResult.value);
      setApprovalsError("");
    } else setApprovalsError(errorMessage(approvalsResult.reason));
    const failed = [runResult, workersResult].find((result) => result.status === "rejected");
    setError(failed ? errorMessage((failed as PromiseRejectedResult).reason) : "");
    setLoading(false);
  }, [id]);
  useEffect(() => {
    alive.current = true;
    after.current = -1;
    setRun(null);
    setWorkers([]);
    setEvents([]);
    setApprovals([]);
    setLoading(true);
    void refresh();
    window.addEventListener("kionga:refresh", refresh);
    return () => {
      alive.current = false;
      window.removeEventListener("kionga:refresh", refresh);
    };
  }, [id, refresh]);
  const known = !!run;
  const resumes = run?.resume_count ?? 0;
  useEffect(() => {
    if (!known) return;
    const controller = new AbortController();
    let source: EventSource | null = null,
      pollTimer: ReturnType<typeof setTimeout> | undefined,
      debounce: ReturnType<typeof setTimeout> | undefined,
      safety: ReturnType<typeof setInterval> | undefined,
      stopped = false;
    const ingest = (incoming: RunEvent[]) => {
      if (!incoming.length) return;
      after.current = lastSeq(incoming, after.current);
      setEvents((old) => mergeEvents(old, incoming));
      clearTimeout(debounce);
      debounce = setTimeout(() => void refresh(), REFRESH_DEBOUNCE_MS);
    };
    const finish = () => {
      stopped = true;
      source?.close();
      clearTimeout(pollTimer);
      clearInterval(safety);
      setConnection("finished");
      void refresh();
    };
    /** Reads pages until none is left. Returns true when the log says it is complete. */
    const readPages = async () => {
      for (;;) {
        const page = await runsApi.events(id, after.current, controller.signal);
        ingest(page.events || []);
        if (page.done) return true;
        if (!page.events?.length) return false;
      }
    };
    const poll = async () => {
      if (stopped) return;
      try {
        if (await readPages()) return finish();
      } catch (failure) {
        if (controller.signal.aborted) return;
        setError(errorMessage(failure));
      }
      if (!stopped) pollTimer = setTimeout(poll, pollMs);
    };
    const connect = () => {
      if (typeof EventSource !== "function") {
        setConnection("polling");
        void poll();
        return;
      }
      let openedAt = 0;
      source = new EventSource(runEventsStreamUrl(id, after.current));
      source.onopen = () => {
        openedAt = Date.now();
        setConnection("live");
      };
      source.addEventListener("run", (event) => {
        try {
          ingest([JSON.parse((event as MessageEvent).data)]);
        } catch {
          /* A malformed event is ignored; the documents remain authoritative. */
        }
        setConnection("live");
      });
      source.addEventListener("end", finish);
      source.onerror = () => {
        if (stopped) return;
        source?.close();
        source = null;
        if (openedAt && Date.now() - openedAt >= STREAM_HEALTHY_MS) return connect();
        setConnection("polling");
        void poll();
      };
    };
    const start = async () => {
      setConnection("connecting");
      // Catch up from the log first, so the stream starts after what is already shown.
      try {
        if (await readPages()) return finish();
      } catch (failure) {
        if (controller.signal.aborted) return;
        setError(errorMessage(failure));
      }
      if (!controller.signal.aborted) connect();
    };
    // A proxy can hold a stream open and deliver nothing, so the log is also read directly.
    catchUp.current = () => readPages().catch(() => undefined);
    void start();
    safety = setInterval(() => {
      void catchUp.current();
      void refresh();
    }, SAFETY_REFRESH_MS);
    return () => {
      stopped = true;
      controller.abort();
      source?.close();
      clearTimeout(pollTimer);
      clearTimeout(debounce);
      clearInterval(safety);
    };
  }, [id, known, resumes, pollMs, refresh]);
  /** After an action: shows its effect now rather than when the next event arrives. */
  const sync = useCallback(async () => {
    await catchUp.current();
    await refresh();
  }, [refresh]);
  return { run, workers, events, approvals, approvalsError, error, loading, connection, refresh: sync };
}
