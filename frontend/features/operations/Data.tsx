"use client";
import {CodeBlock} from "@/components/ui/CodeBlock";
import { useEffect, useState } from "react";
import { request } from "@/lib/api";
import {
  date,
  Detail,
  encode,
  Facts,
  has,
  items,
  Modal,
  State,
  Status,
  Table,
  useResource,
  type OperationsProps,
  type RecordData,
} from "./shared";

export function Features() {
  const views = useResource("/api/v1/features/views"),
    stores = useResource("/api/v1/features/stores");
  const [search, setSearch] = useState(""),
    [selected, setSelected] = useState<RecordData | null>(null),
    [history, setHistory] = useState<RecordData>({});
  useEffect(() => {
    const read = () =>
      setSearch(new URLSearchParams(location.search).get("q") || "");
    read();
    window.addEventListener("popstate", read);
    return () => window.removeEventListener("popstate", read);
  }, []);
  async function open(view: RecordData) {
    setSelected(view);
    setHistory({});
    const [versions, lineage] = await Promise.allSettled([
      request(`/api/v1/features/${encode(view.name)}/versions`),
      request(`/api/v1/features/${encode(view.name)}/lineage`),
    ]);
    setHistory({
      versions:
        versions.status === "fulfilled"
          ? versions.value
          : { error: versions.reason.message },
      lineage:
        lineage.status === "fulfilled"
          ? lineage.value
          : { error: lineage.reason.message },
    });
  }
  return (
    <>
      <label className="field">
        Search feature views
        <input
          type="search"
          value={search}
          onChange={(event) => {
            setSearch(event.target.value);
            const url = new URL(location.href);
            url.searchParams.set("q", event.target.value);
            historyReplace(url);
          }}
        />
      </label>
      <section className="panel">
        <h2>Feature views</h2>
        <State {...views} retry={views.reload}>
          <Table
            rows={items(views.data).filter((view) =>
              [view.name, ...(view.tags || [])]
                .join(" ")
                .toLowerCase()
                .includes(search.toLowerCase()),
            )}
            columns={[
              { key: "name", label: "Feature view" },
              { key: "entity", label: "Entity" },
              { key: "version", label: "Version" },
              {
                key: "store",
                label: "Store",
                render: (row) => (
                  <>
                    {row.store?.name || row.store?.provider || "Internal"}
                    <small>{row.store?.location}</small>
                  </>
                ),
              },
              {
                key: "freshness",
                label: "Freshness",
                render: (row) => (
                  <>
                    <Status
                      value={
                        row.freshness?.state ||
                        (row.materialized_at ? "fresh" : "never")
                      }
                    />
                    {row.freshness?.age_seconds !== undefined && (
                      <small>Updated {row.freshness.age_seconds}s ago</small>
                    )}
                  </>
                ),
              },
              { key: "online_entity_count", label: "Online entities" },
              {
                key: "failure_count",
                label: "Failures",
                render: (row) => (
                  <>
                    {row.failure_count || 0}
                    <small>{row.latest_failure}</small>
                  </>
                ),
              },
            ]}
            actions={(row) => (
              <button onClick={() => void open(row)}>Schema and lineage</button>
            )}
          />
        </State>
      </section>
      <section className="panel">
        <h2>Available stores</h2>
        <State {...stores} retry={stores.reload}>
          <Table
            rows={items(stores.data)}
            columns={[
              { key: "name", label: "Store" },
              { key: "provider", label: "Provider" },
              { key: "location", label: "Location" },
              {
                key: "health",
                label: "Health",
                render: (row) => (
                  <>
                    <Status value={row.health?.state} />
                    <small>{row.health?.detail}</small>
                  </>
                ),
              },
              {
                key: "allowed_projects",
                label: "Projects",
                render: (row) =>
                  row.all_projects
                    ? "All projects"
                    : (row.allowed_projects || []).join(", "),
              },
            ]}
          />
        </State>
      </section>
      {selected && (
        <Modal title={selected.name} onClose={() => setSelected(null)}>
          <Facts
            value={{
              entity: selected.entity,
              version: selected.version,
              ttl_seconds: selected.ttl_seconds,
              store: selected.store,
              freshness: selected.freshness,
            }}
          />
          <h3>Schema</h3>
          <Table
            rows={selected.fields || []}
            columns={[
              { key: "name", label: "Field" },
              { key: "type", label: "Type" },
            ]}
          />
          <h3>Versions</h3>
          <Detail value={history.versions || "Loading…"} />
          <h3>Lineage</h3>
          <Detail value={history.lineage || "Loading…"} />
        </Modal>
      )}
    </>
  );
}
function historyReplace(url: URL) {
  window.history.replaceState(null, "", url);
}

export function Storage({ identity, project, onNavigate }: OperationsProps) {
  const [bucket, setBucket] = useState(""),
    [prefix, setPrefix] = useState(""),
    [preview, setPreview] = useState<RecordData | null>(null),
    [failure, setFailure] = useState("");
  const query = useResource(
    bucket
      ? `/api/v1/storage/objects?${new URLSearchParams({ bucket, prefix })}`
      : "/api/v1/storage/buckets",
  );
  const models = useResource(has(identity, "models") ? "/api/v1/models" : null),
    functions = useResource(
      has(identity, "functions") ? "/api/v1/functions" : null,
    );
  useEffect(() => {
    const q = new URLSearchParams(location.search);
    setBucket(q.get("bucket") || "");
    setPrefix(q.get("prefix") || "");
  }, []);
  function browse(nextBucket: string, nextPrefix: string) {
    setBucket(nextBucket);
    setPrefix(nextPrefix);
    const url = new URL(location.href);
    for (const [key, value] of Object.entries({
      bucket: nextBucket,
      prefix: nextPrefix,
    }))
      value ? url.searchParams.set(key, value) : url.searchParams.delete(key);
    historyReplace(url);
  }
  return (
    <>
      <section className="panel">
        <header className="page-header">
          <h2>{bucket ? `${bucket}/${prefix}` : "Buckets"}</h2>
          {bucket && (
            <button
              onClick={() => {
                const parts = prefix.replace(/\/$/, "").split("/");
                parts.pop();
                browse(
                  prefix ? bucket : "",
                  parts.length ? parts.join("/") + "/" : "",
                );
              }}
            >
              Parent folder
            </button>
          )}
        </header>
        {failure && (
          <p role="alert" className="feedback error">
            {failure}
          </p>
        )}
        <State {...query} retry={query.reload}>
          {!bucket ? (
            <ul className="resource-list">
              {(query.data?.buckets || []).map((item: RecordData) => (
                <li key={item.name}>
                  <button onClick={() => browse(item.name, "")}>
                    {item.name}
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <>
              <ul className="resource-list">
                {(query.data?.prefixes || []).map((value: string) => (
                  <li key={value}>
                    <button onClick={() => browse(bucket, value)}>
                      Folder {value.slice(prefix.length)}
                    </button>
                  </li>
                ))}
              </ul>
              <Table
                rows={query.data?.objects || []}
                columns={[
                  {
                    key: "key",
                    label: "Object",
                    render: (row) => row.key.slice(prefix.length),
                  },
                  { key: "size", label: "Bytes" },
                ]}
                actions={(row) => (
                  <button
                    onClick={() => {
                      setFailure("");
                      request<RecordData>(
                        `/api/v1/storage/object?${new URLSearchParams({ bucket, key: row.key })}`,
                      )
                        .then(setPreview)
                        .catch((error) => setFailure(error.message));
                    }}
                  >
                    Preview
                  </button>
                )}
              />
            </>
          )}
        </State>
      </section>
      {has(identity, "models") && (
        <section className="panel">
          <h2>Model endpoints</h2>
          <State {...models} retry={models.reload}>
            <Table
              rows={items(models.data).filter(
                (row) =>
                  row.endpoint_url?.startsWith("http") &&
                  (!project || row.project_id === project),
              )}
              columns={[
                { key: "name", label: "Model" },
                { key: "endpoint_url", label: "Endpoint" },
                { key: "deployment_status", label: "Status" },
              ]}
              actions={(row) => (
                <button onClick={() => onNavigate("models", row.id)}>
                  Open model
                </button>
              )}
            />
          </State>
        </section>
      )}
      {has(identity, "functions") && (
        <section className="panel">
          <h2>Function endpoints</h2>
          <State {...functions} retry={functions.reload}>
            <Table
              rows={items(functions.data).filter(
                (row) => !project || row.project_id === project,
              )}
              columns={[
                { key: "name", label: "Function" },
                { key: "image", label: "Image" },
                { key: "replicas", label: "Replicas" },
              ]}
              actions={() => (
                <button onClick={() => onNavigate("functions")}>
                  Open functions
                </button>
              )}
            />
          </State>
        </section>
      )}
      {preview && (
        <Modal title={preview.key} onClose={() => setPreview(null)}>
          <p>
            {preview.content_type} {preview.size} bytes{" "}
            {preview.truncated ? "Preview truncated" : ""}
          </p>
          <CodeBlock>{preview.content}</CodeBlock>
        </Modal>
      )}
    </>
  );
}

export function Catalog() {
  const [kind, setKind] = useState(""),
    [selected, setSelected] = useState<RecordData | null>(null);
  const query = useResource(
    `/api/v1/catalog${kind ? `?kind=${encode(kind)}` : ""}`,
  );
  useEffect(() => {
    const read = () =>
      setKind(new URLSearchParams(location.search).get("kind") || "");
    read();
    window.addEventListener("popstate", read);
    return () => window.removeEventListener("popstate", read);
  }, []);
  return (
    <>
      <div className="tabs" aria-label="Catalog kind">
        {["", "model", "feature", "agent", "tool", "prompt"].map((value) => (
          <button
            key={value}
            aria-pressed={kind === value}
            onClick={() => {
              setKind(value);
              const url = new URL(location.href);
              value
                ? url.searchParams.set("kind", value)
                : url.searchParams.delete("kind");
              historyReplace(url);
            }}
          >
            {value || "All kinds"}
          </button>
        ))}
      </div>
      <State {...query} retry={query.reload}>
        <Table
          rows={items(query.data)}
          columns={[
            { key: "name", label: "Name" },
            { key: "kind", label: "Kind" },
            { key: "version", label: "Version" },
            {
              key: "status",
              label: "Status",
              render: (row) => <Status value={row.status} />,
            },
            {
              key: "metadata",
              label: "Metadata",
              render: (row) =>
                Array.isArray(row.metadata)
                  ? row.metadata.join(", ")
                  : JSON.stringify(row.metadata),
            },
          ]}
          actions={(row) => (
            <button onClick={() => setSelected(row)}>Details</button>
          )}
        />
      </State>
      {selected && (
        <Modal title={selected.name} onClose={() => setSelected(null)}>
          <Facts value={selected} />
        </Modal>
      )}
    </>
  );
}
export function Realtime() {
  const query = useResource("/api/v1/realtime");
  return (
    <State {...query} retry={query.reload}>
      <div className="stack">
        {[
          {
            key: "fraud",
            title: "Fraud detection",
            flow: "Transaction → features → model score → alert",
          },
          {
            key: "callcenter",
            title: "Call-center analysis",
            flow: "Transcript → support agent → sentiment and intent",
          },
          {
            key: "recommendations",
            title: "Recommendations",
            flow: "Activity → profile features → ranked items",
          },
        ].map((demo) => {
          const stats = query.data?.demos?.[demo.key];
          return (
            <section key={demo.key} className="panel">
              <h2>{demo.title}</h2>
              <p>{demo.flow}</p>
              {stats ? (
                <Facts
                  value={{
                    events: stats.events,
                    avg_latency_ms: stats.avg_latency_ms,
                    ...(demo.key === "fraud" ? { flagged: stats.flagged } : {}),
                    updated: date(stats.updated_at),
                  }}
                />
              ) : (
                <p className="empty-state">No events processed yet.</p>
              )}
              <p>
                Produce demo events from a workspace terminal using the
                platform’s Kafka event producer.
              </p>
            </section>
          );
        })}
        <section className="panel">
          <h2>Produce demo events</h2>
          <p>Run these commands in a JupyterLab or IDE terminal.</p>
          <CodeBlock>
            {
              "python -m realtime.produce --demo fraud --count 5\npython -m realtime.produce --demo callcenter --count 2\npython -m realtime.produce --demo recommendations --count 2"
            }
          </CodeBlock>
          <button
            onClick={() =>
              void navigator.clipboard.writeText(
                "python -m realtime.produce --demo fraud --count 5",
              )
            }
          >
            Copy fraud command
          </button>
        </section>
      </div>
    </State>
  );
}
