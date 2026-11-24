"use client";
import { useState } from "react";
import { request } from "@/lib/api";
import {
  admin,
  can,
  Detail,
  encode,
  Facts,
  has,
  items,
  Modal,
  State,
  Status,
  Table,
  useAction,
  useResource,
  type OperationsProps,
  type RecordData,
} from "./shared";
import { StorageConnectionDialog } from "./StorageConnectionDialog";
import { FeatureStoreDialog } from "./FeatureStoreDialog";
import { InfrastructureConnectionDialog } from "./InfrastructureConnectionDialog";
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
  const [connectionDialog, setConnectionDialog] = useState<{
      connection: RecordData | null;
      initial?: RecordData;
    } | null>(null),
    [detail, setDetail] = useState<RecordData | null>(null),
    [storageDialog, setStorageDialog] = useState<{
      connection: RecordData | null;
    } | null>(null),
    [featureDialog, setFeatureDialog] = useState<{
      connection: RecordData | null;
    } | null>(null);
  const action = useAction(() => {
    components.reload();
    connections.reload();
    stores.reload();
    storage.reload();
  });
  return (
    <>
      {action.feedback}
      <section className="panel">
        <header className="page-header">
          <h2>Components</h2>
          <button
            disabled={!can(identity, "connections_write")}
            onClick={() =>
              setConnectionDialog({
                connection: null,
                initial: { type: "prefect", secret_ref: "none" },
              })
            }
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
                  onClick={() => setConnectionDialog({ connection: row })}
                >
                  Edit
                </button>
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
              <button onClick={() => setFeatureDialog({ connection: null })}>
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
                    {/* The internal store is configured by the platform
                        environment; the API refuses to replace it. */}
                    {row.id !== "internal" && (
                      <button
                        disabled={action.busy}
                        onClick={() => setFeatureDialog({ connection: row })}
                      >
                        Edit
                      </button>
                    )}
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
              <button onClick={() => setStorageDialog({ connection: null })}>
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
                      onClick={() => setStorageDialog({ connection: row })}
                    >
                      Edit
                    </button>
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
      {connectionDialog && (
        <InfrastructureConnectionDialog
          connection={connectionDialog.connection}
          initial={connectionDialog.initial}
          onClose={() => setConnectionDialog(null)}
          onSaved={(checked) => {
            setConnectionDialog(null);
            action
              .run(async () => checked, checked.message || "Connection checked.")
              .catch(() => {});
          }}
        />
      )}
      {featureDialog && (
        <FeatureStoreDialog
          connection={featureDialog.connection}
          providers={items(providers.data)}
          onClose={() => setFeatureDialog(null)}
          onSaved={(checked) => {
            setFeatureDialog(null);
            action
              .run(
                async () => checked,
                checked.health?.detail || "Store checked.",
              )
              .catch(() => {});
          }}
        />
      )}
      {storageDialog && (
        <StorageConnectionDialog
          connection={storageDialog.connection}
          onClose={() => setStorageDialog(null)}
          onSaved={(checked) => {
            setStorageDialog(null);
            action
              .run(
                async () => checked,
                checked.health?.detail || checked.message || "Connection checked.",
              )
              .catch(() => {});
          }}
        />
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
                  setConnectionDialog({
                    connection: null,
                    initial: {
                      type: "kubernetes",
                      endpoint: "",
                      ...presets[detail.name],
                      name: detail.name.toLowerCase().replaceAll(" ", "-"),
                      secret_ref: "none",
                    },
                  });
                  setDetail(null);
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
