"use client";
import { useRef } from "react";
import { request } from "@/lib/api";
import { csv, encode, Form, Modal, type RecordData } from "./shared";

const COLLECTION = "/api/v1/admin/storage-connections";

/** Form values for a stored connection. Only reference names and public
 * settings are read: the credential itself is never stored or returned by the
 * API, secret_ref is the name of the gateway variable that holds it, and the
 * API rejects a ca_bundle that contains a private key. */
export function storageFormValues(
  connection?: RecordData | null,
  defaultProjects: string[] = [],
): RecordData {
  if (!connection)
    return { path_style: true, allowed_projects: defaultProjects.join(", ") };
  return {
    name: connection.name ?? "",
    endpoint: connection.endpoint ?? "",
    region: connection.region ?? "",
    bucket: connection.bucket ?? "",
    path_style: connection.path_style === true,
    ca_bundle: connection.ca_bundle ?? "",
    secret_ref: connection.secret_ref ?? "",
    allowed_projects: (connection.allowed_projects || []).join(", "),
  };
}

/** Request body for POST (create) and PUT (replace) of a storage connection. */
export function storageBody(value: RecordData): RecordData {
  return {
    ...value,
    secret_ref: String(value.secret_ref ?? "").trim(),
    allowed_projects: csv(value.allowed_projects),
  };
}

/**
 * Connect or edit object storage. With a connection it saves through PUT,
 * otherwise through POST, then runs the connection test. onSaved receives the
 * tested connection. Shared by Platform and the Langflow registered storage
 * section.
 */
export function StorageConnectionDialog({
  connection,
  defaultProjects,
  onClose,
  onSaved,
}: {
  connection?: RecordData | null;
  /** Project IDs granted access when creating (the gateway lists storage only
   * for projects named here, so an empty grant is visible to nobody). */
  defaultProjects?: string[];
  onClose: () => void;
  onSaved: (checked: RecordData) => void;
}) {
  // After a create succeeds but the test call fails, a retry must update the
  // saved record instead of creating a second one with the same name.
  const savedId = useRef<string>(connection?.id ?? "");
  const editing = Boolean(connection);
  return (
    <Modal
      title={editing ? "Edit object storage" : "Connect object storage"}
      onClose={onClose}
    >
      <Form
        initial={storageFormValues(connection, defaultProjects)}
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
        onSubmit={async (value) => {
          const body = storageBody(value);
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
