"use client";
import { useMemo } from "react";
import { GraphCanvas } from "@/components/ui/GraphCanvas";
import { Icon } from "@/components/ui/Icon";
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
    <div className="execution-graph">
      <GraphCanvas
        label="Pipeline dependency graph"
        onSelect={(ids) => {
          if (ids[0]) onSelect?.(ids[0]);
        }}
        nodes={jobs.map((job) => ({
          id: job.name,
          x: layout.positions.get(job.name)!.x,
          y: layout.positions.get(job.name)!.y,
          selected: selected === job.name,
          inputs: !!job.depends_on?.length,
          outputs: jobs.some((item) => item.depends_on?.includes(job.name))
            ? ["out"]
            : [],
          content: (
            <button
              type="button"
              className={`graph-node execution-node state-${job.status || "draft"}${selected === job.name ? " selected" : ""}`}
              aria-pressed={selected === job.name}
              aria-label={`${job.name}${job.status ? `, ${job.status}` : ""}`}
              onClick={() => onSelect?.(job.name)}
            >
              <span className="execution-node-status">
                <Icon
                  name={
                    job.status === "succeeded"
                      ? "check"
                      : job.status === "failed"
                        ? "close"
                        : "pipelines"
                  }
                />
                {job.status || job.kind || "Container job"}
              </span>
              <strong>{job.name}</strong>
              {job.depends_on?.length > 0 && (
                <small>After {job.depends_on.join(", ")}</small>
              )}
              {job.attempt > 0 && <small>Attempt {job.attempt}</small>}
            </button>
          ),
        }))}
        edges={jobs.flatMap((job) =>
          (job.depends_on || [])
            .filter((name: string) => layout.positions.has(name))
            .map((name: string) => ({
              id: `${name}-${job.name}`,
              from: name,
              to: job.name,
              port: "out",
              status: job.status,
            })),
        )}
      />
      <p className="graph-help">
        Select a node to inspect it. Drag the background to pan; use the
        controls to zoom.
      </p>
    </div>
  );
}
