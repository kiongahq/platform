"use client";
import { useMemo } from "react";
import type { RecordData } from "./shared";
/** A dependency graph of actual pipeline nodes. Layout is derived from the DAG. */
export function ExecutionGraph({
  jobs,
  onSelect,
  selected = "",
}: {
  jobs: RecordData[];
  onSelect?: (name: string) => void;
  selected?: string;
}) {
  const layout = useMemo(() => {
    const ranks = new Map<string, number>();
    const pending = new Map(jobs.map((job) => [job.name, job]));
    for (let pass = 0; pass <= jobs.length && pending.size; pass++) {
      let progressed = false;
      for (const [name, job] of pending) {
        const dependencies = (job.depends_on || []) as string[];
        if (
          dependencies.every(
            (dep) => ranks.has(dep) || !jobs.some((item) => item.name === dep),
          )
        ) {
          ranks.set(
            name,
            dependencies.length
              ? Math.max(
                  0,
                  ...dependencies.map((dep) => (ranks.get(dep) ?? -1) + 1),
                )
              : 0,
          );
          pending.delete(name);
          progressed = true;
        }
      }
      if (!progressed) break;
    }
    for (const name of pending.keys()) ranks.set(name, 0);
    const rows = new Map<number, number>();
    const positions = new Map<string, { x: number; y: number }>();
    for (const job of jobs) {
      const rank = ranks.get(job.name) || 0,
        row = rows.get(rank) || 0;
      positions.set(job.name, { x: 24 + rank * 250, y: 24 + row * 116 });
      rows.set(rank, row + 1);
    }
    return {
      positions,
      width: Math.max(280, (Math.max(0, ...ranks.values()) + 1) * 250 + 24),
      height: Math.max(140, Math.max(1, ...rows.values()) * 116 + 24),
    };
  }, [jobs]);
  if (!jobs.length)
    return <p className="empty-state">No execution nodes recorded.</p>;
  return (
    <div
      className="execution-graph"
      style={{ overflow: "auto" }}
      aria-label="Pipeline dependency graph"
    >
      <div
        style={{
          position: "relative",
          width: layout.width,
          height: layout.height,
        }}
      >
        <svg
          width={layout.width}
          height={layout.height}
          style={{ position: "absolute", inset: 0, pointerEvents: "none" }}
          aria-hidden="true"
        >
          {jobs.flatMap((job) =>
            (job.depends_on || []).map((dependency: string) => {
              const from = layout.positions.get(dependency),
                to = layout.positions.get(job.name);
              if (!from || !to) return null;
              const start = from.x + 210,
                end = to.x;
              return (
                <path
                  key={`${dependency}-${job.name}`}
                  d={`M ${start} ${from.y + 40} C ${(start + end) / 2} ${from.y + 40}, ${(start + end) / 2} ${to.y + 40}, ${end} ${to.y + 40}`}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={1.5}
                />
              );
            }),
          )}
        </svg>
        {jobs.map((job) => {
          const position = layout.positions.get(job.name)!;
          return (
            <button
              type="button"
              key={job.name}
              className={
                selected === job.name ? "graph-node selected" : "graph-node"
              }
              aria-pressed={selected === job.name}
              aria-label={`${job.name}${job.status ? `, ${job.status}` : ""}`}
              onClick={() => onSelect?.(job.name)}
              style={{
                position: "absolute",
                left: position.x,
                top: position.y,
                width: 210,
                minHeight: 80,
                textAlign: "left",
              }}
            >
              <strong>{job.name}</strong>
              <small>{job.status || job.kind || "Container job"}</small>
              {job.depends_on?.length > 0 && (
                <small>After {job.depends_on.join(", ")}</small>
              )}
            </button>
          );
        })}
      </div>
    </div>
  );
}
