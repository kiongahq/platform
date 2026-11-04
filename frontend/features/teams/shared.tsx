"use client";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Select } from "@/components/ui/Select";
import { APIError, errorMessage } from "@/lib/api";
import { items, type Entity } from "@/lib/types";
import { useQuery } from "@/hooks/useQuery";
import { Feedback, Modal } from "@/features/shared/View";
import type { FieldErrors } from "./validation";

export const formatTime = (value?: string) => {
  const parsed = value ? new Date(value) : null;
  return parsed && !Number.isNaN(parsed.getTime()) ? parsed.toLocaleString() : "Not reported";
};
export const NOT_REPORTED = "Not reported";
export const searchParam = (key: string) =>
  new URLSearchParams(window.location.search).get(key) || "";
/** Keeps a selection in the address without a navigation, so it survives a reload. */
export function setSearchParam(key: string, value: string) {
  const url = new URL(window.location.href);
  if (value) url.searchParams.set(key, value);
  else url.searchParams.delete(key);
  window.history.replaceState(window.history.state, "", url);
}
type FieldProps = {
  id: string;
  label: string;
  value: string | number;
  onChange: (value: string) => void;
  error?: string;
  help?: string;
  required?: boolean;
  disabled?: boolean;
  wide?: boolean;
};
export function TextField({
  multiline,
  rows = 4,
  mono,
  type = "text",
  min,
  max,
  ...props
}: FieldProps & {
  multiline?: boolean;
  rows?: number;
  mono?: boolean;
  type?: string;
  min?: number;
  max?: number;
}) {
  const { id, label, value, onChange, error, help, required, disabled, wide } = props;
  const described =
    [help ? `${id}-help` : "", error ? `${id}-error` : ""].filter(Boolean).join(" ") || undefined;
  const common = {
    id,
    required,
    disabled,
    value,
    "aria-invalid": !!error,
    "aria-describedby": described,
    className: mono ? "team-mono" : undefined,
  };
  return (
    <div className={`field${wide || multiline ? " field-wide" : ""}`}>
      <label htmlFor={id}>{label}</label>
      {multiline ? (
        <textarea {...common} rows={rows} spellCheck onChange={(event) => onChange(event.target.value)} />
      ) : (
        <input
          {...common}
          type={type}
          min={min}
          max={max}
          autoComplete="off"
          onChange={(event) => onChange(event.target.value)}
        />
      )}
      {help && <small id={`${id}-help`}>{help}</small>}
      {error && (
        <small className="team-field-error" id={`${id}-error`}>
          {error}
        </small>
      )}
    </div>
  );
}
export function SelectField({
  options,
  ...props
}: FieldProps & { options: Array<{ value: string; label: string }> }) {
  const { id, label, value, onChange, error, help, required, disabled } = props;
  const described =
    [help ? `${id}-help` : "", error ? `${id}-error` : ""].filter(Boolean).join(" ") || undefined;
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <Select
        id={id}
        value={String(value)}
        required={required}
        disabled={disabled}
        aria-invalid={!!error}
        aria-describedby={described}
        onChange={(event) => onChange(event.target.value)}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </Select>
      {help && <small id={`${id}-help`}>{help}</small>}
      {error && (
        <small className="team-field-error" id={`${id}-error`}>
          {error}
        </small>
      )}
    </div>
  );
}
export function ProjectField({
  id,
  value,
  onChange,
  error,
  disabled,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  error?: string;
  disabled?: boolean;
}) {
  const projects = useQuery<Entity>("/api/v1/project-options");
  return (
    <SelectField
      id={id}
      label="Project"
      value={value}
      required
      error={error}
      disabled={disabled}
      onChange={onChange}
      options={[
        { value: "", label: projects.loading ? "Loading projects…" : "Select a project" },
        ...items(projects.data).map((item) => ({ value: item.id, label: item.name })),
      ]}
    />
  );
}
/** Lists every problem and moves focus to it, so a keyboard user lands on the summary. */
export function ErrorSummary({
  errors,
  idFor,
  focusToken,
}: {
  errors: FieldErrors;
  idFor: (key: string) => string;
  focusToken: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const entries = Object.entries(errors);
  useEffect(() => {
    if (focusToken && entries.length) ref.current?.focus();
  }, [focusToken]);
  if (!entries.length) return null;
  return (
    <div className="feedback error team-error-summary" role="alert" tabIndex={-1} ref={ref}>
      <p>Fix {entries.length === 1 ? "this problem" : `these ${entries.length} problems`} before saving.</p>
      <ul>
        {entries.map(([key, text]) => (
          <li key={key}>
            <a
              href={`#${idFor(key)}`}
              onClick={(event) => {
                event.preventDefault();
                document.getElementById(idFor(key))?.focus();
              }}
            >
              {text}
            </a>
          </li>
        ))}
      </ul>
    </div>
  );
}
export function ConfirmDialog({
  title,
  children,
  confirm,
  danger,
  onConfirm,
  onClose,
}: {
  title: string;
  children: ReactNode;
  confirm: string;
  danger?: boolean;
  onConfirm: () => Promise<unknown>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal title={title} onClose={onClose}>
      {children}
      <Feedback error={error} />
      <div className="button-row">
        <button type="button" onClick={onClose} disabled={busy}>
          Keep
        </button>
        <button
          type="button"
          className={danger ? "danger" : "primary"}
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            setError("");
            try {
              await onConfirm();
              onClose();
            } catch (failure) {
              setError(errorMessage(failure));
              setBusy(false);
            }
          }}
        >
          {busy ? "Working…" : confirm}
        </button>
      </div>
    </Modal>
  );
}
/** Untrusted or free-form text. React renders it as text, with line breaks kept. */
export function PlainText({ children, label }: { children: string; label?: string }) {
  const id = useId();
  return (
    <div className="team-text" id={id} aria-label={label} role={label ? "group" : undefined}>
      {children}
    </div>
  );
}
export type SaveConflict = { latest?: Entity } | null;
export function conflictFrom(failure: unknown): SaveConflict {
  return failure instanceof APIError && failure.status === 409
    ? { latest: failure.latest }
    : null;
}
export const serverFieldErrors = (failure: unknown): FieldErrors =>
  failure instanceof APIError ? failure.body.fields || failure.body.details || {} : {};
