"use client";
import { useEffect, useState } from "react";
import { errorMessage } from "@/lib/api";
import { DataState, ResourceTable } from "@/features/shared/View";
import { formatTime } from "./shared";
import type { Revision } from "./types";

/** Saved revisions, newest first. Loading one fills the editor; only Save changes the record. */
export function RevisionHistory<T>({
  load,
  revision,
  disabled,
  onLoad,
}: {
  load: (signal: AbortSignal) => Promise<Revision<T>[]>;
  revision?: number;
  disabled?: boolean;
  onLoad: (value: Revision<T>) => void;
}) {
  const [open, setOpen] = useState(false),
    [rows, setRows] = useState<Revision<T>[]>([]),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(""),
    [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    load(controller.signal)
      .then((values) => {
        setRows([...values].sort((a, b) => b.revision - a.revision));
        setLoading(false);
      })
      .catch((failure) => {
        if (controller.signal.aborted) return;
        setError(errorMessage(failure));
        setLoading(false);
      });
    return () => controller.abort();
  }, [open, revision, attempt]);
  return (
    <details
      className="team-revisions"
      onToggle={(event) => setOpen((event.currentTarget as HTMLDetailsElement).open)}
    >
      <summary>Revision history</summary>
      {open && (
        <DataState loading={loading && !rows.length} error={error} onRetry={() => setAttempt((v) => v + 1)}>
          <ResourceTable
            rows={rows.map((row) => ({ ...row, id: `revision-${row.revision}` }))}
            columns={[
              { key: "revision", label: "Revision" },
              { key: "saved", label: "Saved", render: (row) => formatTime(row.saved_at) },
            ]}
            actions={(row) =>
              row.revision === revision ? (
                <span className="team-muted">Current</span>
              ) : (
                <button
                  type="button"
                  disabled={disabled}
                  onClick={() => onLoad(rows.find((item) => item.revision === row.revision)!)}
                >
                  Load revision {row.revision}
                </button>
              )
            }
          />
        </DataState>
      )}
    </details>
  );
}
