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
import { LangflowView } from "../features/langflow/LangflowView";
import {
  storageBody,
  storageFormValues,
} from "../features/operations/StorageConnectionDialog";

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

const admin: any = {
  subject: "root",
  roles: ["admin"],
  permissions: { connections_write: true, agents_write: true },
  services: ["agents", "catalog"],
};
const engineer: any = {
  subject: "eng",
  roles: ["engineer"],
  permissions: { agents_write: true },
  services: ["agents", "catalog"],
};
const stored = {
  id: "st-1",
  name: "warehouse",
  endpoint: "https://s3.example.test",
  region: "eu-west-1",
  bucket: "raw",
  path_style: false,
  ca_bundle: "-----BEGIN CERTIFICATE-----\nPUBLIC\n-----END CERTIFICATE-----",
  secret_ref: "env:KIONGA_STORAGE_SECRET_WAREHOUSE",
  secret_present: true,
  allowed_projects: ["p1", "p2"],
  health: { state: "healthy", detail: "ok" },
};

const shell = (identity: any) => ({ identity, view: "platform", project: "", resource: "", onNavigate: () => {} });
type Call = { url: string; method: string; body: any };
function api(
  routes: Record<string, any | ((call: Call) => any)> = {},
  failures: Record<string, number> = {},
) {
  const calls: Call[] = [];
  globalThis.fetch = (async (input: any, init: any) => {
    const call = {
      url: String(input),
      method: init?.method || "GET",
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    };
    calls.push(call);
    const key = `${call.method} ${call.url}`;
    if (failures[key]) {
      failures[key]--;
      return {
        ok: false,
        status: 500,
        text: async () =>
          JSON.stringify({ error: "boom", message: "storage is down" }),
      } as Response;
    }
    const route = routes[key] ?? routes[call.url];
    const value = typeof route === "function" ? route(call) : route;
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify(value ?? { items: [] }),
    } as Response;
  }) as typeof fetch;
  return calls;
}
const list = { "/api/v1/admin/storage-connections": { items: [stored] } };
const writes = (calls: Call[]) => calls.filter((c) => c.method !== "GET");

test("storageFormValues shows the stored reference name and never invents credentials", () => {
  assert.deepEqual(storageFormValues(null), { path_style: true, allowed_projects: "" });
  assert.equal(storageFormValues(null, ["p1"]).allowed_projects, "p1");
  const values = storageFormValues(stored);
  assert.equal(values.secret_ref, "env:KIONGA_STORAGE_SECRET_WAREHOUSE");
  assert.equal(values.allowed_projects, "p1, p2");
  assert.equal(values.path_style, false);
  assert.equal("secret_present" in values, false);
  assert.equal("id" in values, false);
  assert.deepEqual(storageBody({ ...values, secret_ref: "  env:X  " }).allowed_projects, ["p1", "p2"]);
  assert.equal(storageBody({ ...values, secret_ref: "  env:X  " }).secret_ref, "env:X");
});

test("Platform Edit opens the dialog prefilled, saves through PUT and then runs the test", async () => {
  const calls = api({
    ...list,
    "PUT /api/v1/admin/storage-connections/st-1": { ...stored, region: "eu-central-1" },
    "POST /api/v1/admin/storage-connections/st-1/test": { ...stored, health: { state: "healthy", detail: "Bucket reachable." } },
  });
  const view = render(<Platform {...shell(admin)} />);
  await waitFor(() => assert.ok(view.getByText("warehouse")));
  fireEvent.click(view.getByRole("button", { name: "Edit" }));
  const dialog = await view.findByRole("dialog", { name: "Edit object storage", hidden: true });
  const form = within(dialog);
  assert.equal((form.getByLabelText("Name") as HTMLInputElement).value, "warehouse");
  assert.equal((form.getByLabelText("S3 endpoint") as HTMLInputElement).value, "https://s3.example.test");
  assert.equal((form.getByLabelText("Secret reference") as HTMLInputElement).value, "env:KIONGA_STORAGE_SECRET_WAREHOUSE");
  assert.equal((form.getByLabelText("Allowed project IDs") as HTMLInputElement).value, "p1, p2");
  assert.equal((form.getByLabelText("Path-style addressing") as HTMLInputElement).checked, false);
  assert.match((form.getByLabelText("Custom CA bundle") as HTMLTextAreaElement).value, /PUBLIC/);
  fireEvent.change(form.getByLabelText("Region"), { target: { value: "eu-central-1" } });
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(calls.some((c) => c.url.endsWith("/st-1/test"))));
  const sent = writes(calls);
  assert.deepEqual(sent.map((c) => `${c.method} ${c.url}`), [
    "PUT /api/v1/admin/storage-connections/st-1",
    "POST /api/v1/admin/storage-connections/st-1/test",
  ]);
  assert.deepEqual(sent[0].body, {
    name: "warehouse",
    endpoint: "https://s3.example.test",
    region: "eu-central-1",
    bucket: "raw",
    path_style: false,
    ca_bundle: stored.ca_bundle,
    secret_ref: "env:KIONGA_STORAGE_SECRET_WAREHOUSE",
    allowed_projects: ["p1", "p2"],
  });
  await waitFor(() => assert.ok(view.getByText("Bucket reachable.")));
});

test("Platform Add object storage still creates through POST and tests", async () => {
  const calls = api({
    ...list,
    "POST /api/v1/admin/storage-connections": { ...stored, id: "st-2" },
    "POST /api/v1/admin/storage-connections/st-2/test": { ...stored, id: "st-2", health: { detail: "fine" } },
  });
  const view = render(<Platform {...shell(admin)} />);
  await waitFor(() => assert.ok(view.getByText("warehouse")));
  fireEvent.click(view.getByRole("button", { name: "Add object storage" }));
  const form = within(await view.findByRole("dialog", { name: "Connect object storage", hidden: true }));
  assert.equal((form.getByLabelText("Path-style addressing") as HTMLInputElement).checked, true);
  fireEvent.change(form.getByLabelText("Name"), { target: { value: "lake" } });
  fireEvent.change(form.getByLabelText("S3 endpoint"), { target: { value: "https://s3.lake.test" } });
  fireEvent.change(form.getByLabelText("Bucket"), { target: { value: "b" } });
  fireEvent.change(form.getByLabelText("Secret reference"), { target: { value: " env:KIONGA_STORAGE_SECRET_LAKE " } });
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(calls.some((c) => c.url.endsWith("/st-2/test"))));
  const sent = writes(calls);
  assert.equal(sent[0].method, "POST");
  assert.equal(sent[0].url, "/api/v1/admin/storage-connections");
  assert.equal(sent[0].body.secret_ref, "env:KIONGA_STORAGE_SECRET_LAKE");
  assert.deepEqual(sent[0].body.allowed_projects, []);
});

test("a failed test after a successful save retries through PUT, not a second POST", async () => {
  const calls = api(
    {
      "POST /api/v1/admin/storage-connections": { ...stored, id: "st-9" },
      "PUT /api/v1/admin/storage-connections/st-9": { ...stored, id: "st-9" },
      "POST /api/v1/admin/storage-connections/st-9/test": { ...stored, id: "st-9" },
      ...list,
    },
    { "POST /api/v1/admin/storage-connections/st-9/test": 1 },
  );
  const view = render(<Platform {...shell(admin)} />);
  await waitFor(() => assert.ok(view.getByText("warehouse")));
  fireEvent.click(view.getByRole("button", { name: "Add object storage" }));
  const form = within(await view.findByRole("dialog", { name: "Connect object storage", hidden: true }));
  fireEvent.change(form.getByLabelText("Name"), { target: { value: "lake" } });
  fireEvent.change(form.getByLabelText("S3 endpoint"), { target: { value: "https://s3.lake.test" } });
  fireEvent.change(form.getByLabelText("Bucket"), { target: { value: "b" } });
  fireEvent.change(form.getByLabelText("Secret reference"), { target: { value: "env:KIONGA_STORAGE_SECRET_LAKE" } });
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(form.getByText("storage is down")));
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.equal(writes(calls).filter((c) => c.url.endsWith("/st-9/test")).length, 2));
  assert.deepEqual(writes(calls).map((c) => `${c.method} ${c.url}`), [
    "POST /api/v1/admin/storage-connections",
    "POST /api/v1/admin/storage-connections/st-9/test",
    "PUT /api/v1/admin/storage-connections/st-9",
    "POST /api/v1/admin/storage-connections/st-9/test",
  ]);
});

test("a validation error from PUT is shown and the dialog stays open", async () => {
  const calls = api({ ...list });
  globalThis.fetch = ((orig) => async (input: any, init: any) =>
    init?.method === "PUT"
      ? ({ ok: false, status: 422, text: async () => JSON.stringify({ error: "validation_error", message: "secret_ref: not permitted", details: ["secret_ref: not permitted"] }) } as Response)
      : orig(input, init))(globalThis.fetch);
  const view = render(<Platform {...shell(admin)} />);
  await waitFor(() => assert.ok(view.getByText("warehouse")));
  fireEvent.click(view.getByRole("button", { name: "Edit" }));
  const form = within(await view.findByRole("dialog", { name: "Edit object storage", hidden: true }));
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(form.getAllByText(/secret_ref: not permitted/).length));
  assert.equal(calls.filter((c) => c.url.endsWith("/test") && c.method === "POST").length, 0);
});

test("non-administrators get no object storage section or Edit action", async () => {
  const calls = api(list);
  const view = render(<Platform {...shell(engineer)} />);
  await waitFor(() => assert.ok(calls.length > 0));
  assert.equal(view.queryByRole("button", { name: "Edit" }), null);
  assert.equal(view.queryByText("Object storage connections"), null);
  assert.equal(calls.some((c) => c.url.includes("storage-connections")), false);
});

const projects = [{ id: "p1", name: "Project one" }];
const langflowRoutes = {
  "/api/v1/langflow-workspaces": { mode: "local", items: [{ id: "local", name: "Local canvas", state: "configured" }] },
  "/api/v1/project-options": { items: projects },
  "/api/v1/registered-storage": { items: [{ id: "st-5", name: "lake", bucket: "b", project_ids: ["p1"] }] },
};

test("Langflow administrators register storage from the section and it refreshes", async () => {
  const calls = api({
    ...langflowRoutes,
    "POST /api/v1/admin/storage-connections": { ...stored, id: "st-5" },
    "POST /api/v1/admin/storage-connections/st-5/test": { ...stored, id: "st-5" },
  });
  const view = render(<LangflowView view="langflow" identity={admin} project="p1" resource="" onNavigate={() => {}} />);
  await waitFor(() => assert.ok(view.getByText("Local canvas")));
  await waitFor(() => assert.ok(view.getByText("lake")));
  const before = calls.filter((c) => c.url === "/api/v1/registered-storage").length;
  fireEvent.click(view.getByRole("button", { name: "Register storage" }));
  const form = within(await view.findByRole("dialog", { name: "Connect object storage", hidden: true }));
  fireEvent.change(form.getByLabelText("Name"), { target: { value: "lake" } });
  fireEvent.change(form.getByLabelText("S3 endpoint"), { target: { value: "https://s3.lake.test" } });
  fireEvent.change(form.getByLabelText("Bucket"), { target: { value: "b" } });
  fireEvent.change(form.getByLabelText("Secret reference"), { target: { value: "env:KIONGA_STORAGE_SECRET_LAKE" } });
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(calls.some((c) => c.url.endsWith("/st-5/test"))));
  assert.equal(writes(calls)[0].url, "/api/v1/admin/storage-connections");
  assert.equal(writes(calls)[0].method, "POST");
  assert.deepEqual(writes(calls)[0].body.allowed_projects, ["p1"]);
  await waitFor(() => assert.ok(calls.filter((c) => c.url === "/api/v1/registered-storage").length > before));
  await waitFor(() => assert.equal(view.queryByRole("dialog", { hidden: true, name: "Connect object storage" }), null));
});

test("Langflow hides Register storage from non-administrators and keeps the other Register buttons", async () => {
  api(langflowRoutes);
  const user = render(<LangflowView view="langflow" identity={engineer} project="p1" resource="" onNavigate={() => {}} />);
  await waitFor(() => assert.ok(user.getByText("Local canvas")));
  assert.equal(user.queryByRole("button", { name: "Register storage" }), null);
  cleanup();
  api(langflowRoutes);
  const view = render(<LangflowView view="langflow" identity={admin} project="p1" resource="" onNavigate={() => {}} />);
  await waitFor(() => assert.ok(view.getByText("Local canvas")));
  for (const name of ["Register project datasets", "Register knowledge bases", "Register approved api connections", "Register storage"])
    assert.ok(view.getByRole("button", { name }), name);
});

test("Langflow registered storage follows the selected project", async () => {
  const routes = {
    ...langflowRoutes,
    "/api/v1/registered-storage": {
      items: [
        { id: "st-1", name: "only-one", bucket: "a", project_ids: ["p1"] },
        { id: "st-2", name: "only-two", bucket: "b", project_ids: ["p2"] },
        { id: "st-3", name: "both", bucket: "c", project_ids: ["p1", "p2"] },
      ],
    },
  };
  const shown = (view: ReturnType<typeof render>) =>
    ["only-one", "only-two", "both"].filter((name) => view.queryByText(name));
  api(routes);
  const one = render(<LangflowView view="langflow" identity={engineer} project="p1" resource="" onNavigate={() => {}} />);
  await waitFor(() => assert.ok(one.getByText("both")));
  assert.deepEqual(shown(one), ["only-one", "both"]);
  cleanup();
  api(routes);
  const two = render(<LangflowView view="langflow" identity={engineer} project="p2" resource="" onNavigate={() => {}} />);
  await waitFor(() => assert.ok(two.getByText("both")));
  assert.deepEqual(shown(two), ["only-two", "both"]);
  cleanup();
  api(routes);
  const all = render(<LangflowView view="langflow" identity={engineer} project="" resource="" onNavigate={() => {}} />);
  await waitFor(() => assert.ok(all.getByText("both")));
  assert.deepEqual(shown(all), ["only-one", "only-two", "both"]);
});
