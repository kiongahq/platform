import "./setup-dom";
import assert from "node:assert/strict";
import { afterEach, beforeEach, test } from "node:test";
import React from "react";
import {
  render,
  fireEvent,
  waitFor,
  cleanup,
  within,
} from "@testing-library/react";
import { Platform } from "../features/operations/Platform";
import {
  connectionBody,
  connectionFormValues,
} from "../features/operations/InfrastructureConnectionDialog";

Object.assign(globalThis, { FormData: window.FormData, self: window });
beforeEach(() => {
  Object.assign(window.HTMLDialogElement.prototype, {
    showModal(this: HTMLDialogElement) {
      this.setAttribute("open", "");
    },
    close(this: HTMLDialogElement) {
      this.removeAttribute("open");
    },
  });
});
afterEach(cleanup);

const operator: any = {
  subject: "ops",
  roles: ["operator"],
  permissions: { connections_write: true },
  services: [],
};
const engineer: any = {
  subject: "eng",
  roles: ["engineer"],
  permissions: { connections_write: false },
  services: [],
};
const tracking = {
  id: "conn-1",
  name: "tracking",
  type: "mlflow",
  endpoint: "http://mlflow:5000/health",
  secret_ref: "env:KIONGA_PLATFORM_SECRET_MLFLOW",
  status: "healthy",
  message: "Connection succeeded",
  created_at: "2026-11-01T00:00:00Z",
  checked_at: "2026-11-02T00:00:00Z",
};
const runtime = {
  id: "conn-2",
  name: "pipelines",
  type: "prefect",
  endpoint: "http://prefect-server:4200/api",
  secret_ref: "none",
  status: "healthy",
  activated_at: "2026-11-03T00:00:00Z",
  created_at: "2026-11-01T00:00:00Z",
};

const shell = (identity: any) => ({ identity, view: "platform", project: "", resource: "", onNavigate: () => {} });
type Call = { url: string; method: string; body: any };
type Failure = { status: number; error: string; message: string };
function api(routes: Record<string, any> = {}, failures: Record<string, Failure[]> = {}) {
  const calls: Call[] = [];
  globalThis.fetch = (async (input: any, init: any) => {
    const call = {
      url: String(input),
      method: init?.method || "GET",
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    };
    calls.push(call);
    const key = `${call.method} ${call.url}`;
    const failure = failures[key]?.shift();
    if (failure)
      return {
        ok: false,
        status: failure.status,
        text: async () => JSON.stringify(failure),
      } as Response;
    const value = routes[key] ?? routes[call.url];
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify(value ?? { items: [] }),
    } as Response;
  }) as typeof fetch;
  return calls;
}
const list = { "/api/v1/connections": { items: [tracking, runtime] } };
const writes = (calls: Call[]) => calls.filter((c) => c.method !== "GET");
const row = (view: ReturnType<typeof render>, name: string) =>
  within(view.getByText(name).closest("tr") as HTMLElement);

test("connectionFormValues and connectionBody carry the reference name and nothing the gateway owns", () => {
  const values = connectionFormValues(runtime);
  assert.deepEqual(values, {
    type: "prefect",
    name: "pipelines",
    endpoint: "http://prefect-server:4200/api",
    secret_ref: "none",
  });
  // PUT rejects unknown fields: status, activation and timestamps are not sent.
  assert.deepEqual(connectionBody({ ...runtime, secret_ref: "  env:X  " }), {
    type: "prefect",
    name: "pipelines",
    endpoint: "http://prefect-server:4200/api",
    secret_ref: "env:X",
  });
});

test("Edit opens the dialog prefilled, saves through PUT and then re-runs the test", async () => {
  const calls = api({
    ...list,
    "PUT /api/v1/connections/conn-1": { ...tracking, endpoint: "http://mlflow-2:5000/health", status: "pending" },
    "POST /api/v1/connections/conn-1/test": { ...tracking, endpoint: "http://mlflow-2:5000/health", message: "Connection succeeded again" },
  });
  const view = render(<Platform {...shell(operator)} />);
  await waitFor(() => assert.ok(view.getByText("tracking")));
  fireEvent.click(row(view, "tracking").getByRole("button", { name: "Edit" }));
  const dialog = await view.findByRole("dialog", { name: "Edit connection", hidden: true });
  const form = within(dialog);
  const service = form.getByLabelText(/^Service(?! address)/) as HTMLSelectElement;
  assert.equal(service.value, "mlflow");
  assert.equal(service.disabled, false);
  assert.equal((form.getByLabelText("Connection name") as HTMLInputElement).value, "tracking");
  assert.equal((form.getByLabelText("Service address") as HTMLInputElement).value, "http://mlflow:5000/health");
  const reference = form.getByLabelText(/^Credential reference/) as HTMLInputElement;
  assert.equal(reference.value, "env:KIONGA_PLATFORM_SECRET_MLFLOW");
  assert.equal(reference.type, "text");
  assert.equal(dialog.querySelector('input[type="password"]'), null);
  fireEvent.change(form.getByLabelText("Service address"), { target: { value: "http://mlflow-2:5000/health" } });
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(calls.some((c) => c.url.endsWith("/conn-1/test"))));
  const sent = writes(calls);
  assert.deepEqual(sent.map((c) => `${c.method} ${c.url}`), [
    "PUT /api/v1/connections/conn-1",
    "POST /api/v1/connections/conn-1/test",
  ]);
  assert.deepEqual(sent[0].body, {
    type: "mlflow",
    name: "tracking",
    endpoint: "http://mlflow-2:5000/health",
    secret_ref: "env:KIONGA_PLATFORM_SECRET_MLFLOW",
  });
  await waitFor(() => assert.ok(view.getByText("Connection succeeded again")));
  assert.equal(view.queryByRole("dialog", { name: "Edit connection", hidden: true }), null);
  // The table is read again after the save.
  assert.ok(calls.filter((c) => c.method === "GET" && c.url === "/api/v1/connections").length > 1);
});

test("an active runtime keeps its service in the dialog and a refusal keeps the dialog open", async () => {
  const calls = api(list, {
    "PUT /api/v1/connections/conn-2": [
      { status: 422, error: "connection_failed", message: "Service could not be reached" },
    ],
  });
  const view = render(<Platform {...shell(operator)} />);
  await waitFor(() => assert.ok(view.getByText("pipelines")));
  fireEvent.click(row(view, "pipelines").getByRole("button", { name: "Edit" }));
  const form = within(await view.findByRole("dialog", { name: "Edit connection", hidden: true }));
  const service = form.getByLabelText(/^Service(?! address)/) as HTMLSelectElement;
  assert.equal(service.value, "prefect");
  assert.equal(service.disabled, true);
  assert.ok(form.getByText(/active runtime, so its service cannot change/));
  fireEvent.change(form.getByLabelText("Service address"), { target: { value: "http://prefect-2:4200/api" } });
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(form.getByText("Service could not be reached")));
  assert.equal(calls.filter((c) => c.url.endsWith("/test")).length, 0);
  assert.equal(writes(calls)[0].body.type, "prefect");
  assert.ok(view.getByRole("dialog", { name: "Edit connection", hidden: true }));
});

test("Add connection still creates through POST, and a failed test retries through PUT", async () => {
  const calls = api(
    {
      ...list,
      "POST /api/v1/connections": { ...tracking, id: "conn-9", name: "events" },
      "PUT /api/v1/connections/conn-9": { ...tracking, id: "conn-9", name: "events" },
      "POST /api/v1/connections/conn-9/test": { ...tracking, id: "conn-9", name: "events" },
    },
    { "POST /api/v1/connections/conn-9/test": [{ status: 500, error: "boom", message: "gateway is down" }] },
  );
  const view = render(<Platform {...shell(operator)} />);
  await waitFor(() => assert.ok(view.getByText("tracking")));
  fireEvent.click(view.getByRole("button", { name: "Add connection" }));
  const form = within(await view.findByRole("dialog", { name: "Connect infrastructure", hidden: true }));
  assert.equal((form.getByLabelText(/^Service(?! address)/) as HTMLSelectElement).value, "prefect");
  assert.equal((form.getByLabelText(/^Credential reference/) as HTMLInputElement).value, "none");
  fireEvent.change(form.getByLabelText("Connection name"), { target: { value: "events" } });
  fireEvent.change(form.getByLabelText("Service address"), { target: { value: "http://prefect:4200/api" } });
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(form.getByText("gateway is down")));
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.equal(writes(calls).length, 4));
  assert.deepEqual(writes(calls).map((c) => `${c.method} ${c.url}`), [
    "POST /api/v1/connections",
    "POST /api/v1/connections/conn-9/test",
    "PUT /api/v1/connections/conn-9",
    "POST /api/v1/connections/conn-9/test",
  ]);
  assert.deepEqual(writes(calls)[0].body, {
    type: "prefect",
    name: "events",
    endpoint: "http://prefect:4200/api",
    secret_ref: "none",
  });
});

test("people who cannot create connections cannot edit them", async () => {
  const calls = api(list);
  const view = render(<Platform {...shell(engineer)} />);
  await waitFor(() => assert.ok(view.getByText("tracking")));
  const edit = row(view, "tracking").getByRole("button", { name: "Edit" }) as HTMLButtonElement;
  assert.equal(edit.disabled, true);
  assert.equal((view.getByRole("button", { name: "Add connection" }) as HTMLButtonElement).disabled, true);
  fireEvent.click(edit);
  assert.equal(view.queryByRole("dialog", { name: "Edit connection", hidden: true }), null);
  assert.equal(writes(calls).length, 0);
});
