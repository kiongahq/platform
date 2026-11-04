"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage } from "@/lib/api";
import { approvalsApi, runEventsStreamUrl, runsApi } from "./api";
import { isTerminalRun, lastSeq, mergeEvents } from "./runState";
import type { Approval, RunEvent, TeamRun, TeamWorker } from "./types";

const REFRESH_DEBOUNCE_MS = 250;
const SAFETY_REFRESH_MS = 10_000;
export type RunConnection = "connecting" | "live" | "polling" | "finished";

/**
 * Live state of one run. The event log is the change signal; the run, worker and approval
 * documents stay the source of truth, so a lost event cannot leave the view wrong.
 * Uses the SSE stream, drops to polling `events?after=` on any stream failure, and
 * reconnects when a resume brings a finished run back to `running`.
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
    alive = useRef(true);
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
  const terminal = isTerminalRun(run);
  const known = !!run;
  useEffect(() => {
    if (!known) return;
    const controller = new AbortController();
    let source: EventSource | null = null,
      pollTimer: ReturnType<typeof setTimeout> | undefined,
      debounce: ReturnType<typeof setTimeout> | undefined,
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
      setConnection("finished");
      void refresh();
    };
    /** Reads every available page. Returns true when the log says the run has ended. */
    const readPages = async () => {
      for (;;) {
        const page = await runsApi.events(id, after.current, controller.signal);
        ingest(page.events || []);
        if (page.done) return true;
        if (!page.has_more || !page.events?.length) return false;
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
      source = new EventSource(runEventsStreamUrl(id, after.current));
      source.onopen = () => setConnection("live");
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
        setConnection("polling");
        void poll();
      };
    };
    const start = async () => {
      if (terminal) {
        try {
          await readPages();
        } catch (failure) {
          if (!controller.signal.aborted) setError(errorMessage(failure));
        }
        if (!controller.signal.aborted) setConnection("finished");
        return;
      }
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
    void start();
    const safety = terminal ? undefined : setInterval(() => void refresh(), SAFETY_REFRESH_MS);
    return () => {
      stopped = true;
      controller.abort();
      source?.close();
      clearTimeout(pollTimer);
      clearTimeout(debounce);
      clearInterval(safety);
    };
  }, [id, known, terminal, pollMs, refresh]);
  return { run, workers, events, approvals, approvalsError, error, loading, connection, refresh };
}
