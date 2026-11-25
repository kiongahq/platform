"use client";
import { useRef } from "react";
import { request } from "@/lib/api";
import { encode, Form, Modal, option, type RecordData } from "./shared";

const COLLECTION = "/api/v1/connections";
const SERVICES = [
  "kubernetes",
  "mlflow",
  "rustfs",
  "s3",
  "kafka",
  "langfuse",
  "prefect",
  "openfaas",
  "redis",
];

/** Form values for a stored connection. The credential itself is never
 * stored or returned by the API: secret_ref is "none" or the name of the
 * gateway variable that holds it. */
export function connectionFormValues(connection: RecordData): RecordData {
  return {
    type: connection.type ?? "",
    name: connection.name ?? "",
    endpoint: connection.endpoint ?? "",
    secret_ref: connection.secret_ref ?? "",
  };
}

/** Request body for POST (create) and PUT (replace) of a connection. */
export function connectionBody(value: RecordData): RecordData {
  return {
    type: value.type,
    name: value.name,
    endpoint: value.endpoint,
    secret_ref: String(value.secret_ref ?? "").trim(),
  };
}

/**
 * Connect or edit infrastructure. With a connection it saves through PUT,
 * otherwise through POST, then runs the connection test. onSaved receives the
 * tested connection. A connection activated as a runtime keeps its service:
 * the gateway refuses to change it, and verifies a new address or credential
 * before saving it.
 */
export function InfrastructureConnectionDialog({
  connection,
  initial,
  onClose,
  onSaved,
}: {
  connection?: RecordData | null;
  /** Starting values when creating, such as a component preset. */
  initial?: RecordData;
  onClose: () => void;
  onSaved: (checked: RecordData) => void;
}) {
  // After a create succeeds but the test call fails, a retry must update the
  // saved record instead of creating a second one with the same name.
  const savedId = useRef<string>(connection?.id ?? "");
  const editing = Boolean(connection);
  const activated = Boolean(connection?.activated_at);
  return (
    <Modal
      title={editing ? "Edit connection" : "Connect infrastructure"}
      onClose={onClose}
    >
      <Form
        initial={connection ? connectionFormValues(connection) : initial}
        fields={[
          {
            name: "type",
            label: "Service",
            type: "select",
            options: SERVICES.map(option),
            readOnly: activated,
            help: activated
              ? "This connection is an active runtime, so its service cannot change. A new address or credential is verified before it is saved."
              : undefined,
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
        onSubmit={async (value) => {
          const body = connectionBody(value);
          const saved = savedId.current
            ? await request<RecordData>(
                `${COLLECTION}/${encode(savedId.current)}`,
                { method: "PUT", body },
              )
            : await request<RecordData>(COLLECTION, { method: "POST", body });
          savedId.current = saved.id;
          const checked = await request<RecordData>(
            `${COLLECTION}/${encode(saved.id)}/test`,
            { method: "POST", body: {} },
          );
          onSaved(checked);
        }}
      />
    </Modal>
  );
}
