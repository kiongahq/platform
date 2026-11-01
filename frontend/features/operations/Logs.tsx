"use client";
import {Select} from "@/components/ui/Select";
import { useEffect, useRef, useState } from "react";
import { request } from "@/lib/api";
import { State, Table, type RecordData } from "./shared";
export function Logs({
  project = "",
  run = "",
  node = "",
}: {
  project?: string;
  run?: string;
  node?: string;
}) {
  const [filters, setFilters] = useState({
      project_id: project,
      run_id: run,
      node,
      severity: "",
      source: "",
      q: "",
      window: "1h",
    }),
    [lines, setLines] = useState<RecordData[]>([]),
    [before, setBefore] = useState(0),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(""),
    [live, setLive] = useState(false),
    [revision, setRevision] = useState(0);
  const newest = useRef(0);
  function params(extra: RecordData = {}) {
    const { window: time, ...values } = filters;
    const query = new URLSearchParams(
      Object.entries({ ...values, ...extra })
        .filter(([, value]) => value !== "" && value !== undefined)
        .map(([key, value]) => [key, String(value)]),
    );
    const duration: Record<string, number> = {
      "15m": 900000,
      "1h": 3600000,
      "24h": 86400000,
      "7d": 604800000,
    };
    if (duration[time])
      query.set("since", new Date(Date.now() - duration[time]).toISOString());
    return query;
  }
  async function load(earlier = false, signal?: AbortSignal) {
    setLoading(true);
    setError("");
    try {
      const page = await request<RecordData>(
        `/api/v1/logs?${params({ order: "desc", limit: 200, ...(earlier ? { before } : {}) })}`,
        { signal },
      );
      const next = [...(page.items || [])].reverse();
      setLines((old) => (earlier ? [...next, ...old] : next));
      setBefore(page.next_before || 0);
      newest.current = Math.max(newest.current, page.items?.[0]?.sequence || 0);
    } catch (error) {
      if (!signal?.aborted) setError((error as Error).message);
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }
  useEffect(() => {
    setFilters((old) => ({ ...old, project_id: project, run_id: run, node }));
  }, [project, run, node]);
  useEffect(() => {
    const read = () => {
      const query = new URLSearchParams(location.search);
      setFilters((old) => ({
        ...old,
        ...Object.fromEntries(
          ["run_id", "node", "severity", "source", "q", "window"]
            .filter((key) => query.has(key))
            .map((key) => [key, query.get(key) || ""]),
        ),
      }));
    };
    read();
    window.addEventListener("popstate", read);
    return () => window.removeEventListener("popstate", read);
  }, []);
  const key = JSON.stringify(filters);
  useEffect(() => {
    const controller = new AbortController();
    void load(false, controller.signal);
    return () => controller.abort();
  }, [key, revision]);
  useEffect(() => {
    if (!live) return;
    const stream = new EventSource(
      `/api/v1/logs/stream?${params({ after: newest.current })}`,
    );
    stream.addEventListener("logs", (event) => {
      try {
        const next = JSON.parse((event as MessageEvent).data);
        setLines((old) => {
          const seen = new Set(old.map((item) => item.sequence));
          return [
            ...old,
            ...next.filter((entry: RecordData) => !seen.has(entry.sequence)),
          ];
        });
        newest.current = next.at(-1)?.sequence || newest.current;
      } catch {
        setError("A log update could not be read.");
      }
    });
    stream.onerror = () => setError("Live tail is reconnecting.");
    stream.onopen = () => setError("");
    return () => stream.close();
  }, [live, key]);
  const change = (field: string, value: string) => {
    setFilters((old) => ({ ...old, [field]: value }));
    const url = new URL(location.href);
    value ? url.searchParams.set(field, value) : url.searchParams.delete(field);
    history.replaceState(null, "", url);
  };
  return (
    <section className="panel">
      <div className="toolbar">
        <label>
          Run
          <input
            value={filters.run_id}
            onChange={(event) => change("run_id", event.target.value)}
          />
        </label>
        <label>
          Node
          <input
            value={filters.node}
            onChange={(event) => change("node", event.target.value)}
          />
        </label>
        <label>
          Time window
          <Select
            value={filters.window}
            onChange={(event) => change("window", event.target.value)}
          >
            {[
              ["15m", "Last 15 minutes"],
              ["1h", "Last hour"],
              ["24h", "Last day"],
              ["7d", "Last week"],
              ["", "All time"],
            ].map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </Select>
        </label>
        <label>
          Severity
          <Select
            value={filters.severity}
            onChange={(event) => change("severity", event.target.value)}
          >
            <option value="">All</option>
            <option value="error">Errors</option>
            <option value="warn,error">Warnings and errors</option>
            <option value="info,warn,error">Info and above</option>
          </Select>
        </label>
        <label>
          Source
          <Select
            value={filters.source}
            onChange={(event) => change("source", event.target.value)}
          >
            {["", "runner", "platform", "engine", "k8s"].map((value) => (
              <option key={value} value={value}>
                {value || "All sources"}
              </option>
            ))}
          </Select>
        </label>
        <label>
          Search
          <input
            type="search"
            value={filters.q}
            onChange={(event) => change("q", event.target.value)}
          />
        </label>
        <label>
          <input
            type="checkbox"
            checked={live}
            onChange={(event) => setLive(event.target.checked)}
          />
          Live tail
        </label>
        <button onClick={() => setRevision((value) => value + 1)}>
          Refresh
        </button>
      </div>
      <State
        error={error}
        loading={loading && !lines.length}
        retry={() => setRevision((value) => value + 1)}
      >
        <div className="logs" role="log" aria-live="polite" tabIndex={0}>
          <Table
            rows={lines}
            columns={[
              {
                key: "ts",
                label: "Time",
                render: (row) => new Date(row.ts).toLocaleTimeString(),
              },
              { key: "run_id", label: "Run" },
              { key: "node", label: "Node" },
              { key: "severity", label: "Severity" },
              { key: "source", label: "Source" },
              { key: "message", label: "Message" },
            ]}
          />
        </div>
      </State>
      <div className="button-row">
        {before > 0 && (
          <button disabled={loading} onClick={() => void load(true)}>
            Load earlier lines
          </button>
        )}
        <p role="status">
          {lines.length} lines shown. Live tail {live ? "on" : "off"}.
        </p>
      </div>
    </section>
  );
}
