"use client";
import { useState } from "react";
import { request } from "@/lib/api";
import {
  admin,
  can,
  csv,
  Detail,
  encode,
  Facts,
  Form,
  has,
  items,
  Modal,
  option,
  State,
  Status,
  Table,
  useAction,
  useResource,
  type OperationsProps,
  type RecordData,
} from "./shared";
export function Platform({ identity }: OperationsProps) {
  const components = useResource("/api/v1/components"),
    connections = useResource("/api/v1/connections"),
    readiness = useResource(
      has(identity, "overview") ? "/api/v1/onboarding/readiness" : null,
    ),
    exporter = useResource("/api/v1/observability/log-export"),
    stores = useResource(
      admin(identity) ? "/api/v1/admin/feature-stores" : null,
    ),
    storage = useResource(
      admin(identity) ? "/api/v1/admin/storage-connections" : null,
    ),
    providers = useResource(
      admin(identity) ? "/api/v1/admin/feature-store-providers" : null,
    );
  const [modal, setModal] = useState<"" | "connection" | "feature" | "storage">(
      "",
    ),
    [detail, setDetail] = useState<RecordData | null>(null);
  const [connectionInitial, setConnectionInitial] = useState<RecordData>({
    type: "prefect",
    secret_ref: "none",
  });
  const action = useAction(() => {
    components.reload();
    connections.reload();
    stores.reload();
    storage.reload();
  });
  async function saveAndTest(path: string, body: RecordData) {
    const saved = await request<RecordData>(path, { method: "POST", body });
    const checked = await request<RecordData>(
      `${path}/${encode(saved.id)}/test`,
      { method: "POST", body: {} },
    );
    setModal("");
    action
      .run(
        async () => checked,
        checked.health?.detail || checked.message || "Connection checked.",
      )
      .catch(() => {});
  }
  return (
    <>
      {action.feedback}
      <section className="panel">
        <header className="page-header">
          <h2>Components</h2>
          <button
            disabled={!can(identity, "connections_write")}
            onClick={() => {
              setConnectionInitial({ type: "prefect", secret_ref: "none" });
              setModal("connection");
            }}
          >
            Add connection
          </button>
        </header>
        <State {...components} retry={components.reload}>
          <Table
            rows={items(components.data)}
            columns={[
              { key: "name", label: "Component" },
              { key: "category", label: "Category" },
              {
                key: "status",
                label: "Status",
                render: (row) => <Status value={row.status} />,
              },
              { key: "description", label: "Description" },
            ]}
            actions={(row) => (
              <button onClick={() => setDetail(row)}>Details</button>
            )}
          />
        </State>
      </section>
      <section className="panel">
        <h2>Connected infrastructure</h2>
        <State {...connections} retry={connections.reload}>
          <Table
            rows={items(connections.data)}
            columns={[
              { key: "name", label: "Name" },
              { key: "type", label: "Service" },
              { key: "endpoint", label: "Endpoint" },
              {
                key: "status",
                label: "Status",
                render: (row) => <Status value={row.status} />,
              },
              { key: "message", label: "Last check" },
              {
                key: "activated_at",
                label: "Active runtime",
                render: (row) => (row.activated_at ? "Yes" : "No"),
              },
            ]}
            actions={(row) => (
              <>
                <button
                  disabled={!can(identity, "connections_write") || action.busy}
                  onClick={() =>
                    void action
                      .run(
                        () =>
                          request(
                            `/api/v1/connections/${encode(row.id)}/test`,
                            { method: "POST", body: {} },
                          ),
                        "Connection checked.",
                      )
                      .then(setDetail)
                      .catch(() => {})
                  }
                >
                  Test
                </button>
                {["prefect", "openfaas"].includes(row.type) && (
                  <button
                    disabled={
                      !can(identity, "connections_write") || action.busy
                    }
                    onClick={() =>
                      void action
                        .run(
                          () =>
                            request(
                              `/api/v1/connections/${encode(row.id)}/activate`,
                              { method: "POST", body: {} },
                            ),
                          "Runtime activated for new operations.",
                        )
                        .catch(() => {})
                    }
                  >
                    Use for new operations
                  </button>
                )}
              </>
            )}
          />
        </State>
      </section>
      <section className="panel">
        <h2>Production readiness</h2>
        <State {...readiness} retry={readiness.reload}>
          <p>{readiness.data?.percent ?? 0}% of checks ready</p>
          <Table
            rows={items(readiness.data)}
            columns={[
              { key: "label", label: "Check" },
              {
                key: "status",
                label: "Status",
                render: (row) => <Status value={row.status} />,
              },
              { key: "description", label: "Detail" },
            ]}
          />
        </State>
      </section>
      <section className="panel">
        <h2>Log export</h2>
        <State {...exporter} retry={exporter.reload}>
          {exporter.data && <Facts value={exporter.data} />}
        </State>
      </section>
      {admin(identity) && (
        <>
          <section className="panel">
            <header className="page-header">
              <h2>Feature store connections</h2>
              <button onClick={() => setModal("feature")}>
                Add feature store
              </button>
            </header>
            <State {...stores} retry={stores.reload}>
              <Table
                rows={items(stores.data)}
                columns={[
                  { key: "name", label: "Store" },
                  { key: "provider", label: "Provider" },
                  { key: "config", label: "Configuration" },
                  {
                    key: "allowed_projects",
                    label: "Projects",
                    render: (row) =>
                      (row.allowed_projects || []).join(", ") || "All projects",
                  },
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
                ]}
                actions={(row) => (
                  <>
                    <button
                      disabled={action.busy}
                      onClick={() =>
                        void action
                          .run(
                            () =>
                              request(
                                `/api/v1/admin/feature-stores/${encode(row.id)}/test`,
                                { method: "POST", body: {} },
                              ),
                            "Store checked.",
                          )
                          .then(setDetail)
                          .catch(() => {})
                      }
                    >
                      Test
                    </button>
                    {row.id !== "internal" && (
                      <button
                        className="danger"
                        disabled={action.busy}
                        onClick={() => {
                          if (
                            window.confirm(
                              "Remove this store connection? Projects using it will lose access.",
                            )
                          )
                            void action
                              .run(
                                () =>
                                  request(
                                    `/api/v1/admin/feature-stores/${encode(row.id)}`,
                                    { method: "DELETE" },
                                  ),
                                "Feature store removed.",
                              )
                              .catch(() => {});
                        }}
                      >
                        Remove
                      </button>
                    )}
                  </>
                )}
              />
            </State>
          </section>
          <section className="panel">
            <header className="page-header">
              <h2>Object storage connections</h2>
              <button onClick={() => setModal("storage")}>
                Add object storage
              </button>
            </header>
            <State {...storage} retry={storage.reload}>
              <Table
                rows={items(storage.data)}
                columns={[
                  { key: "name", label: "Connection" },
                  { key: "endpoint", label: "Endpoint" },
                  { key: "bucket", label: "Bucket" },
                  { key: "region", label: "Region" },
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
                ]}
                actions={(row) => (
                  <>
                    <button
                      disabled={action.busy}
                      onClick={() =>
                        void action
                          .run(
                            () =>
                              request(
                                `/api/v1/admin/storage-connections/${encode(row.id)}/test`,
                                { method: "POST", body: {} },
                              ),
                            "Storage checked.",
                          )
                          .then(setDetail)
                          .catch(() => {})
                      }
                    >
                      Test
                    </button>
                    <button
                      className="danger"
                      disabled={action.busy}
                      onClick={() => {
                        if (
                          window.confirm(
                            "Remove this storage connection? Projects using it will lose access.",
                          )
                        )
                          void action
                            .run(
                              () =>
                                request(
                                  `/api/v1/admin/storage-connections/${encode(row.id)}`,
                                  { method: "DELETE" },
                                ),
                              "Storage connection removed.",
                            )
                            .catch(() => {});
                      }}
                    >
                      Remove
                    </button>
                  </>
                )}
              />
            </State>
          </section>
        </>
      )}
      {modal && (
        <Modal
          title={
            modal === "connection"
              ? "Connect infrastructure"
              : modal === "feature"
                ? "Connect feature store"
                : "Connect object storage"
          }
          onClose={() => setModal("")}
        >
          {modal === "connection" ? (
            <Form
              initial={connectionInitial}
              fields={[
                {
                  name: "type",
                  label: "Service",
                  type: "select",
                  options: [
                    "kubernetes",
                    "mlflow",
                    "rustfs",
                    "s3",
                    "kafka",
                    "langfuse",
                    "prefect",
                    "openfaas",
                    "redis",
                  ].map(option),
                },
                { name: "name", label: "Connection name", required: true },
                {
                  name: "endpoint",
                  label: "Service address",
                  type: "url",
                  required: true,
                },
                {
                  name: "secret_ref",
                  label: "Credential reference",
                  required: true,
                  help: "Use none or env:VARIABLE. Do not paste credentials.",
                },
              ]}
              submit="Save and test"
              onSubmit={(value) => saveAndTest("/api/v1/connections", value)}
            />
          ) : modal === "feature" ? (
            <Form
              fields={[
                {
                  name: "provider",
                  label: "Provider",
                  type: "select",
                  required: true,
                  options: items(providers.data)
                    .filter((item) => item.kind === "external")
                    .map((item) => ({
                      value: item.name,
                      label: item.title + (item.adapter ? "" : " (no adapter)"),
                      disabled: !item.adapter,
                    })),
                },
                { name: "name", label: "Name", required: true },
                {
                  name: "url",
                  label: "Provider URL",
                  required: true,
                  type: "url",
                },
                { name: "feature_services", label: "Feature services" },
                { name: "secret_ref", label: "Secret reference" },
                {
                  name: "allowed_projects",
                  label: "Allowed project IDs",
                  help: "Comma-separated; empty shares with all projects.",
                },
              ]}
              submit="Save and test"
              onSubmit={(value) =>
                saveAndTest("/api/v1/admin/feature-stores", {
                  provider: value.provider,
                  name: value.name,
                  config: {
                    url: value.url,
                    ...(value.feature_services
                      ? { feature_services: value.feature_services }
                      : {}),
                  },
                  secret_ref: value.secret_ref?.trim() || "",
                  allowed_projects: csv(value.allowed_projects),
                })
              }
            />
          ) : (
            <Form
              initial={{ path_style: true }}
              fields={[
                { name: "name", label: "Name", required: true },
                {
                  name: "endpoint",
                  label: "S3 endpoint",
                  type: "url",
                  required: true,
                },
                { name: "region", label: "Region" },
                { name: "bucket", label: "Bucket", required: true },
                {
                  name: "path_style",
                  label: "Path-style addressing",
                  type: "checkbox",
                },
                {
                  name: "ca_bundle",
                  label: "Custom CA bundle",
                  type: "textarea",
                },
                {
                  name: "secret_ref",
                  label: "Secret reference",
                  required: true,
                },
                { name: "allowed_projects", label: "Allowed project IDs" },
              ]}
              submit="Save and test"
              onSubmit={(value) =>
                saveAndTest("/api/v1/admin/storage-connections", {
                  ...value,
                  secret_ref: value.secret_ref.trim(),
                  allowed_projects: csv(value.allowed_projects),
                })
              }
            />
          )}
        </Modal>
      )}
      {detail && (
        <Modal
          title={detail.name || "Connection check"}
          onClose={() => setDetail(null)}
        >
          <Facts value={detail} />
          {detail.name &&
            detail.category &&
            can(identity, "connections_write") && (
              <button
                onClick={() => {
                  const presets: Record<string, RecordData> = {
                    "API Gateway": {
                      type: "kubernetes",
                      endpoint: `${location.origin}/api/v1/health`,
                    },
                    "Pipeline Engine": {
                      type: "prefect",
                      endpoint: "http://prefect-server:4200/api",
                    },
                    "Experiment Tracker": {
                      type: "mlflow",
                      endpoint: "http://mlflow:5000/health",
                    },
                    "Feature Store": {
                      type: "redis",
                      endpoint: "http://feature-gateway:8083/healthz",
                    },
                    "Object Store": {
                      type: "rustfs",
                      endpoint: "http://objectstore:9000/health",
                    },
                    "Inference Engine": {
                      type: "kubernetes",
                      endpoint: "http://serving-manager:8085/healthz",
                    },
                    "Agent Observability": {
                      type: "langfuse",
                      endpoint: "http://langfuse:3000/api/public/health",
                    },
                    "Streaming Broker": {
                      type: "kafka",
                      endpoint: "http://kafka-rest:8082",
                    },
                    Kubernetes: {
                      type: "kubernetes",
                      endpoint: "https://kubernetes.default.svc/version",
                    },
                  };
                  setConnectionInitial({
                    type: "kubernetes",
                    endpoint: "",
                    ...presets[detail.name],
                    name: detail.name.toLowerCase().replaceAll(" ", "-"),
                    secret_ref: "none",
                  });
                  setDetail(null);
                  setModal("connection");
                }}
              >
                Configure connection
              </button>
            )}
        </Modal>
      )}
    </>
  );
}
