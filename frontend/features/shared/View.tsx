"use client";
import { Select } from "@/components/ui/Select";
import { Icon } from "@/components/ui/Icon";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { CodeEditor } from "@/components/ui/CodeEditor";
import {
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
  type FormEvent,
} from "react";
import { APIError, errorMessage } from "@/lib/api";
import type { Entity, Identity } from "@/lib/types";
export type ViewProps = {
  view: string;
  identity: Identity;
  project: string;
  resource: string;
  onNavigate: (view: string, resource?: string) => void;
};
export function Feedback({
  error,
  message,
}: {
  error?: string;
  message?: string;
}) {
  return error || message ? (
    <p
      className={`feedback${error ? " error" : ""}`}
      role={error ? "alert" : "status"}
    >
      {error || message}
    </p>
  ) : null;
}
export function DataState({
  loading,
  error,
  empty,
  onRetry,
  children,
}: {
  loading: boolean;
  error?: string;
  empty?: boolean;
  onRetry?: () => void;
  children: ReactNode;
}) {
  if (error)
    return (
      <div className="feedback error" role="alert">
        <p>{error}</p>
        {onRetry && <button onClick={onRetry}>Retry</button>}
      </div>
    );
  if (loading)
    return (
      <div className="loading-state" role="status" aria-label="Loading">
        <span>Loading…</span>
      </div>
    );
  if (empty) return <p className="empty-state">No items to show.</p>;
  return <>{children}</>;
}
export function Status({ value }: { value?: unknown }) {
  return (
    <span className="status" data-state={String(value || "unknown")}>
      {String(value || "Unknown").replaceAll("_", " ")}
    </span>
  );
}
export function Detail({ value }: { value: unknown }) {
  return (
    <details className="resource-detail">
      <summary>Details</summary>
      <CodeBlock language="json">
        {JSON.stringify(value, null, 2) ?? "null"}
      </CodeBlock>
    </details>
  );
}
export type Field = {
  name: string;
  label: string;
  type?:
    | "text"
    | "number"
    | "password"
    | "textarea"
    | "select"
    | "checkbox"
    | "json"
    | "datetime-local"
    | "url";
  required?: boolean;
  value?: any;
  placeholder?: string;
  help?: string;
  min?: number;
  max?: number;
  step?: string;
  options?: Array<string | { value: string; label: string }>;
  disabled?: boolean;
};
export function ActionForm({
  title,
  fields,
  initial = {},
  submit = "Save",
  onSubmit,
  onCancel,
  id,
  children,
}: {
  title?: string;
  fields: Field[];
  initial?: Entity;
  submit?: string;
  onSubmit: (value: Entity) => Promise<unknown>;
  onCancel?: () => void;
  id?: string;
  children?: ReactNode;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [success, setSuccess] = useState("");
  const [errors, setErrors] = useState<Entity>({});
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const value: Entity = {};
    try {
      for (const field of fields) {
        const raw = data.get(field.name);
        if (field.disabled) {
          value[field.name] = initial[field.name] ?? field.value;
          continue;
        }
        value[field.name] =
          field.type === "number"
            ? raw === ""
              ? 0
              : Number(raw)
            : field.type === "checkbox"
              ? data.has(field.name)
              : field.type === "json"
                ? JSON.parse(String(raw || "{}"))
                : String(raw ?? "");
      }
      setBusy(true);
      setError("");
      setSuccess("");
      setErrors({});
      await onSubmit(value);
      setSuccess("Saved.");
    } catch (failure) {
      setError(errorMessage(failure));
      if (failure instanceof APIError)
        setErrors(failure.body.fields || failure.body.details || {});
    } finally {
      setBusy(false);
    }
  }
  return (
    <form id={id} className="action-form" onSubmit={save} aria-busy={busy}>
      {title && <h2>{title}</h2>}
      <div className="form-grid">
        {fields.map((field) => {
          const value = initial[field.name] ?? field.value ?? "";
          const common = {
            name: field.name,
            id: `${id || "form"}-${field.name}`,
            required: field.required,
            disabled: busy || field.disabled,
            "aria-invalid": !!errors[field.name],
            "aria-describedby": field.help
              ? `${id || "form"}-${field.name}-help`
              : undefined,
          };
          if (field.type === "json")
            return (
              <div className="field field-wide" key={field.name}>
                <label htmlFor={common.id}>{field.label}</label>
                <CodeEditor
                  {...common}
                  label={field.label}
                  language="json"
                  rows={8}
                  defaultValue={
                    typeof value === "object"
                      ? JSON.stringify(value, null, 2)
                      : String(value)
                  }
                  placeholder={field.placeholder}
                />
                {field.help && (
                  <small id={common["aria-describedby"]}>{field.help}</small>
                )}
                {errors[field.name] && (
                  <small role="alert">{String(errors[field.name])}</small>
                )}
              </div>
            );
          return (
            <label
              className={`field${field.type === "textarea" ? " field-wide" : ""}`}
              key={field.name}
              htmlFor={common.id}
            >
              <span>{field.label}</span>
              {field.type === "select" ? (
                <Select {...common} defaultValue={String(value)}>
                  {field.options?.map((option) => {
                    const item =
                      typeof option === "string"
                        ? { value: option, label: option }
                        : option;
                    return (
                      <option key={item.value} value={item.value}>
                        {item.label}
                      </option>
                    );
                  })}
                </Select>
              ) : field.type === "textarea" ? (
                <textarea
                  {...common}
                  rows={3}
                  defaultValue={
                    typeof value === "object"
                      ? JSON.stringify(value, null, 2)
                      : String(value)
                  }
                  placeholder={field.placeholder}
                  spellCheck={true}
                />
              ) : field.type === "checkbox" ? (
                <input {...common} type="checkbox" defaultChecked={!!value} />
              ) : (
                <input
                  {...common}
                  type={field.type || "text"}
                  defaultValue={String(value)}
                  min={field.min}
                  max={field.max}
                  step={field.step}
                  placeholder={field.placeholder}
                />
              )}
              {field.help && (
                <small id={common["aria-describedby"]}>{field.help}</small>
              )}
              {errors[field.name] && (
                <small role="alert">{String(errors[field.name])}</small>
              )}
            </label>
          );
        })}
      </div>
      {children}
      <Feedback error={error} message={success} />
      <div className="button-row">
        {onCancel && (
          <button type="button" onClick={onCancel}>
            Cancel
          </button>
        )}
        <button
          className="primary"
          type="submit"
          disabled={busy || fields.every((field) => field.disabled)}
        >
          {busy ? "Saving…" : submit}
        </button>
      </div>
    </form>
  );
}
export function Modal({
  title,
  children,
  onClose,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
}) {
  const titleId = useId();
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    const previous = document.activeElement as HTMLElement;
    dialog?.showModal();
    return () => {
      dialog?.close();
      previous?.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      className="dialog"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <header className="toolbar">
        <h2 id={titleId}>{title}</h2>
        <button
          type="button"
          className="icon-button"
          onClick={onClose}
          aria-label={`Close ${title}`}
          title="Close"
        >
          <Icon name="close" />
        </button>
      </header>
      {children}
    </dialog>
  );
}
export function ResourceTable({
  rows,
  columns,
  actions,
}: {
  rows: Entity[];
  columns: Array<{
    key: string;
    label: string;
    render?: (row: Entity) => ReactNode;
  }>;
  actions?: (row: Entity) => ReactNode;
}) {
  return (
    <div
      className="table-scroll"
      tabIndex={0}
      role="region"
      aria-label="Scrollable table"
    >
      <table className="data-table">
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.key} scope="col">
                {column.label}
              </th>
            ))}
            {actions && (
              <th className="actions-column" scope="col">
                Actions
              </th>
            )}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={row.id || row.name || i}>
              {columns.map((column) => (
                <td key={column.key}>
                  {column.render
                    ? column.render(row)
                    : typeof row[column.key] === "object"
                      ? JSON.stringify(row[column.key])
                      : String(row[column.key] ?? "—")}
                </td>
              ))}
              {actions && (
                <td>
                  <div className="button-row">{actions(row)}</div>
                </td>
              )}
            </tr>
          ))}
          {!rows.length && (
            <tr>
              <td
                colSpan={columns.length + (actions ? 1 : 0)}
                className="empty-state"
              >
                No items to show.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
