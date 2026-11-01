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
import { request } from "@/lib/api";
import type { Identity } from "@/lib/types";
export type RecordData = Record<string, any>;
export type OperationsProps = {
  view: string;
  identity: Identity;
  project: string;
  resource: string;
  onNavigate: (view: string, resource?: string) => void;
};
export const encode = encodeURIComponent;
export const services = [
  "overview",
  "projects",
  "pipelines",
  "functions",
  "models",
  "agents",
  "langflow",
  "features",
  "storage",
  "realtime",
  "catalog",
  "platform",
  "git",
  "workbench",
  "ide",
];
export const can = (identity: Identity, permission: string) =>
  identity.permissions?.[permission] === true;
export const admin = (identity: Identity) =>
  identity.roles?.some((role) => ["admin", "operator"].includes(role));
export const has = (identity: Identity, service: string) =>
  admin(identity) ||
  (identity.roles?.length > 0 && !identity.roles?.includes("user")) ||
  identity.services?.includes(service);
export const csv = (value: unknown) =>
  String(value || "")
    .split(/[\n,]/)
    .map((value) => value.trim())
    .filter(Boolean);
export const date = (value: unknown) =>
  value && !String(value).startsWith("0001")
    ? new Date(String(value)).toLocaleString()
    : "Not recorded";
export const items = (data: any): RecordData[] =>
  Array.isArray(data) ? data : data?.items || [];
export const scoped = (data: RecordData[], project: string) =>
  project
    ? data.filter((item) => item.project_id === project || item.id === project)
    : data;
export function useResource<T = any>(path: string | null) {
  const [revision, refresh] = useState(0);
  const [state, setState] = useState<{
    data?: T;
    error: string;
    loading: boolean;
  }>({ error: "", loading: !!path });
  useEffect(() => {
    if (!path) {
      setState({ error: "", loading: false });
      return;
    }
    const controller = new AbortController();
    setState((old) => ({ ...old, error: "", loading: true }));
    request<T>(path, { signal: controller.signal })
      .then((data) => setState({ data, error: "", loading: false }))
      .catch((error) => {
        if (!controller.signal.aborted)
          setState((old) => ({ ...old, error: error.message, loading: false }));
      });
    return () => controller.abort();
  }, [path, revision]);
  useEffect(() => {
    const update = () => refresh((value) => value + 1);
    window.addEventListener("kionga:refresh", update);
    return () => window.removeEventListener("kionga:refresh", update);
  }, []);
  return { ...state, reload: () => refresh((value) => value + 1) };
}
export function State({
  loading,
  error,
  empty,
  children,
  retry,
}: {
  loading?: boolean;
  error?: string;
  empty?: boolean;
  children: ReactNode;
  retry?: () => void;
}) {
  if (error)
    return (
      <div className="feedback error" role="alert">
        <p>{error}</p>
        {retry && <button onClick={retry}>Retry</button>}
      </div>
    );
  if (loading)
    return (
      <p className="loading" role="status">
        Loading…
      </p>
    );
  if (empty) return <p className="empty-state">No records found.</p>;
  return <>{children}</>;
}
export function Status({ value }: { value: unknown }) {
  return (
    <span
      className={`status status-${String(value || "unknown").replace(/[^a-z-]/gi, "")}`}
    >
      {String(value || "Unknown")}
    </span>
  );
}
export function Detail({ value }: { value: unknown }) {
  return (
    <CodeBlock language="json">
      {JSON.stringify(value, null, 2) ?? "null"}
    </CodeBlock>
  );
}
export function Facts({ value }: { value: RecordData }) {
  return (
    <dl className="facts">
      {Object.entries(value).map(([label, v]) => (
        <div key={label}>
          <dt>{label.replaceAll("_", " ")}</dt>
          <dd>
            {typeof v === "object" ? <Detail value={v} /> : String(v ?? "—")}
          </dd>
        </div>
      ))}
    </dl>
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
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement;
    dialog.current?.showModal();
    return () => {
      dialog.current?.close();
      previous?.focus?.();
    };
  }, []);
  return (
    <dialog
      ref={dialog}
      className="dialog"
      aria-label={title}
      onCancel={onClose}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <header className="page-header">
        <h2>{title}</h2>
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
export type Field = {
  name: string;
  label: string;
  type?:
    | "text"
    | "number"
    | "password"
    | "url"
    | "textarea"
    | "select"
    | "checkbox"
    | "json"
    | "multiselect";
  required?: boolean;
  options?: { value: string; label: string; disabled?: boolean }[];
  help?: string;
  min?: number;
  max?: number;
  readOnly?: boolean;
  placeholder?: string;
  rows?: number;
};
export function Form({
  fields,
  initial = {},
  submit = "Save",
  onSubmit,
  onChange,
  children,
}: {
  fields: Field[];
  initial?: RecordData;
  submit?: string;
  onSubmit: (value: RecordData) => Promise<unknown>;
  onChange?: (value: RecordData) => void;
  children?: ReactNode;
}) {
  const formId = useId();
  const [values, setValues] = useState<RecordData>(initial),
    [error, setError] = useState(""),
    [details, setDetails] = useState<RecordData[]>([]),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    setValues((previous) => {
      let next = previous;
      for (const field of fields) {
        if (field.type === "select" && previous[field.name] === undefined) {
          const first = field.options?.find((option) => !option.disabled);
          if (first) next = { ...next, [field.name]: first.value };
        }
      }
      return next;
    });
  }, [fields]);
  const update = (name: string, value: unknown) => {
    const next = { ...values, [name]: value };
    setValues(next);
    onChange?.(next);
  };
  async function save(event: FormEvent) {
    event.preventDefault();
    setError("");
    setDetails([]);
    setBusy(true);
    try {
      const parsed = { ...values };
      for (const field of fields) {
        if (field.type === "select" && parsed[field.name] === undefined)
          parsed[field.name] =
            field.options?.find((option) => !option.disabled)?.value ?? "";
        if (field.type === "json")
          parsed[field.name] = JSON.parse(String(values[field.name] || "{}"));
        if (field.type === "number")
          parsed[field.name] = Number(values[field.name] || 0);
      }
      await onSubmit(parsed);
    } catch (e) {
      const failure = e as any;
      setError(failure.message);
      setDetails(failure.body?.details || failure.details || []);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={save} className="stack">
      <div className="form-grid">
        {fields.map((field) =>
          field.type === "json" ? (
            <div className="field full" key={field.name}>
              <label htmlFor={`${formId}-${field.name}`}>{field.label}</label>
              <CodeEditor
                id={`${formId}-${field.name}`}
                label={field.label}
                name={field.name}
                language="json"
                rows={field.rows || 8}
                value={values[field.name] ?? ""}
                required={field.required}
                readOnly={field.readOnly}
                placeholder={field.placeholder}
                onChange={(event) => update(field.name, event.target.value)}
              />
              {field.help && <small>{field.help}</small>}
            </div>
          ) : field.type === "multiselect" ? (
            <fieldset key={field.name} className="field full">
              <legend>{field.label}</legend>
              <div className="checkbox-list">
                {field.options?.map((option) => (
                  <label key={option.value}>
                    <input
                      type="checkbox"
                      name={field.name}
                      value={option.value}
                      checked={(values[field.name] || []).includes(
                        option.value,
                      )}
                      disabled={field.readOnly || option.disabled}
                      onChange={(event) =>
                        update(
                          field.name,
                          event.target.checked
                            ? [...(values[field.name] || []), option.value]
                            : (values[field.name] || []).filter(
                                (value: string) => value !== option.value,
                              ),
                        )
                      }
                    />
                    {option.label}
                  </label>
                ))}
              </div>
              {field.help && <small>{field.help}</small>}
            </fieldset>
          ) : (
            <label
              className={field.type === "textarea" ? "field full" : "field"}
              key={field.name}
            >
              <span>{field.label}</span>
              {field.type === "select" ? (
                <Select
                  name={field.name}
                  value={values[field.name] ?? ""}
                  required={field.required}
                  disabled={field.readOnly}
                  onChange={(event) => update(field.name, event.target.value)}
                >
                  {field.options?.map((option) => (
                    <option
                      key={option.value}
                      value={option.value}
                      disabled={option.disabled}
                    >
                      {option.label}
                    </option>
                  ))}
                </Select>
              ) : field.type === "textarea" ? (
                <textarea
                  name={field.name}
                  rows={field.rows || 5}
                  value={values[field.name] ?? ""}
                  required={field.required}
                  readOnly={field.readOnly}
                  spellCheck={true}
                  placeholder={field.placeholder}
                  onChange={(event) => update(field.name, event.target.value)}
                />
              ) : field.type === "checkbox" ? (
                <input
                  type="checkbox"
                  name={field.name}
                  checked={!!values[field.name]}
                  onChange={(event) => update(field.name, event.target.checked)}
                />
              ) : (
                <input
                  type={field.type || "text"}
                  name={field.name}
                  value={values[field.name] ?? ""}
                  required={field.required}
                  readOnly={field.readOnly}
                  min={field.min}
                  max={field.max}
                  placeholder={field.placeholder}
                  autoComplete={
                    field.type === "password" ? "new-password" : undefined
                  }
                  onChange={(event) => update(field.name, event.target.value)}
                />
              )}{" "}
              {field.help && <small>{field.help}</small>}
            </label>
          ),
        )}
      </div>
      {children}
      {error && (
        <div className="feedback error" role="alert">
          <p>{error}</p>
          {details.length > 0 && (
            <ul>
              {details.map((detail, index) => (
                <li key={index}>
                  <code>{detail.field}</code> {detail.message}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      <div className="button-row">
        <button className="primary" disabled={busy} type="submit">
          {busy ? "Saving…" : submit}
        </button>
      </div>
    </form>
  );
}
export function Table({
  rows,
  columns,
  actions,
}: {
  rows: RecordData[];
  columns: {
    key: string;
    label: string;
    render?: (row: RecordData) => ReactNode;
  }[];
  actions?: (row: RecordData) => ReactNode;
}) {
  return (
    <div
      className="table-wrap"
      tabIndex={0}
      role="region"
      aria-label="Scrollable table"
    >
      <table className="data-table">
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.key}>{column.label}</th>
            ))}
            {actions && <th className="actions-column">Actions</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={row.id || row.name || index}>
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
                No records found.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
export function useAction(reload?: () => void) {
  const [error, setError] = useState(""),
    [message, setMessage] = useState(""),
    [busy, setBusy] = useState(false);
  async function run(action: () => Promise<any>, success = "Saved.") {
    setError("");
    setMessage("");
    setBusy(true);
    try {
      const result = await action();
      setMessage(success);
      reload?.();
      return result;
    } catch (error) {
      setError((error as Error).message);
      throw error;
    } finally {
      setBusy(false);
    }
  }
  return {
    run,
    busy,
    error,
    message,
    feedback: (
      <>
        {error && (
          <p className="feedback error" role="alert">
            {error}
          </p>
        )}
        {message && (
          <p className="feedback" role="status">
            {message}
          </p>
        )}
      </>
    ),
  };
}
export const option = (value: string) => ({
  value,
  label: value.replaceAll("_", " "),
});
export const projectField = (projects: RecordData[]): Field => ({
  name: "project_id",
  label: "Project",
  type: "select",
  required: true,
  options: [
    { value: "", label: "Choose a project" },
    ...projects.map((project) => ({ value: project.id, label: project.name })),
  ],
});
