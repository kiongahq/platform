"use client";
import { useRef } from "react";
import { request } from "@/lib/api";
import { csv, encode, Form, Modal, type RecordData } from "./shared";

const COLLECTION = "/api/v1/admin/feature-stores";

/** Form values for a stored feature store connection. Only public settings
 * and the reference name are read: the credential is never stored or returned
 * by the API, and secret_ref is the name of the gateway variable holding it. */
export function featureStoreFormValues(
  connection?: RecordData | null,
): RecordData {
  if (!connection) return {};
  return {
    provider: connection.provider ?? "",
    name: connection.name ?? "",
    url: connection.config?.url ?? "",
    feature_services: connection.config?.feature_services ?? "",
    secret_ref: connection.secret_ref ?? "",
    allowed_projects: (connection.allowed_projects || []).join(", "),
  };
}

/** Request body for POST (create) and PUT (replace). PUT replaces the whole
 * record, so settings of the same provider that this form does not show are
 * carried over from the stored connection. */
export function featureStoreBody(
  value: RecordData,
  connection?: RecordData | null,
): RecordData {
  const config: Record<string, string> = {
    ...(connection && connection.provider === value.provider
      ? connection.config
      : {}),
    url: value.url,
  };
  const featureServices = String(value.feature_services ?? "").trim();
  if (featureServices) config.feature_services = featureServices;
  else delete config.feature_services;
  return {
    provider: value.provider,
    name: value.name,
    config,
    secret_ref: String(value.secret_ref ?? "").trim(),
    allowed_projects: csv(value.allowed_projects),
  };
}

/**
 * Connect or edit a feature store. With a connection it saves through PUT,
 * otherwise through POST, then runs the connection test. onSaved receives the
 * tested connection. The built-in internal store is configured by the
 * platform environment and is never passed here.
 */
export function FeatureStoreDialog({
  connection,
  providers,
  onClose,
  onSaved,
}: {
  connection?: RecordData | null;
  /** Entries of /api/v1/admin/feature-store-providers. */
  providers: RecordData[];
  onClose: () => void;
  onSaved: (checked: RecordData) => void;
}) {
  // After a create succeeds but the test call fails, a retry must update the
  // saved record instead of creating a second one with the same name.
  const savedId = useRef<string>(connection?.id ?? "");
  const editing = Boolean(connection);
  return (
    <Modal
      title={editing ? "Edit feature store" : "Connect feature store"}
      onClose={onClose}
    >
      <Form
        initial={featureStoreFormValues(connection)}
        fields={[
          {
            name: "provider",
            label: "Provider",
            type: "select",
            required: true,
            options: providers
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
          {
            name: "secret_ref",
            label: "Secret reference",
            help: "The name of a gateway variable, such as env:KIONGA_FEATURE_SECRET_NAME. Do not paste credentials.",
          },
          {
            name: "allowed_projects",
            label: "Allowed project IDs",
            help: "Comma-separated; empty shares with all projects.",
          },
        ]}
        submit="Save and test"
        onSubmit={async (value) => {
          const body = featureStoreBody(value, connection);
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
