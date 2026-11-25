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
  featureStoreBody,
  featureStoreFormValues,
} from "../features/operations/FeatureStoreDialog";

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
  permissions: { connections_write: true },
  services: [],
};
const operator: any = { ...admin, subject: "ops", roles: ["operator"] };
const engineer: any = {
  subject: "eng",
  roles: ["engineer"],
  permissions: {},
  services: [],
};
const internal = {
  id: "internal",
  provider: "internal",
  name: "kionga-internal",
  kind: "internal",
  config: { online_url: "http://feature-gateway:8083", offline_uri: "s3://kionga-features" },
  secret_present: true,
  allowed_projects: [],
  health: { state: "configured", detail: "Not checked yet." },
};
const stored = {
  id: "fs-1",
  provider: "feast",
  name: "feast-prod",
  kind: "external",
  config: { url: "https://feast.example.test", feature_services: "fraud,churn" },
  secret_ref: "env:KIONGA_FEATURE_SECRET_FEAST",
  secret_present: true,
  allowed_projects: ["p1", "p2"],
  health: { state: "healthy", detail: "ok" },
};
const providers = {
  items: [
    { name: "internal", title: "Kionga internal", kind: "internal", adapter: true },
    { name: "feast", title: "Feast", kind: "external", adapter: true },
    { name: "tecton", title: "Tecton", kind: "external", adapter: false },
  ],
};

const shell = (identity: any) => ({ identity, view: "platform", project: "", resource: "", onNavigate: () => {} });
type Call = { url: string; method: string; body: any };
function api(routes: Record<string, any> = {}, failures: Record<string, number> = {}) {
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
        status: 422,
        text: async () =>
          JSON.stringify({ error: "validation_error", message: "secret_ref: not permitted", details: ["secret_ref: not permitted"] }),
      } as Response;
    }
    const value = routes[key] ?? routes[call.url];
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify(value ?? { items: [] }),
    } as Response;
  }) as typeof fetch;
  return calls;
}
const list = {
  "/api/v1/admin/feature-stores": { items: [internal, stored] },
  "/api/v1/admin/feature-store-providers": providers,
};
const writes = (calls: Call[]) => calls.filter((c) => c.method !== "GET");

test("featureStoreFormValues reads only public settings and the reference name", () => {
  assert.deepEqual(featureStoreFormValues(null), {});
  assert.deepEqual(featureStoreFormValues(stored), {
    provider: "feast",
    name: "feast-prod",
    url: "https://feast.example.test",
    feature_services: "fraud,churn",
    secret_ref: "env:KIONGA_FEATURE_SECRET_FEAST",
    allowed_projects: "p1, p2",
  });
});

test("featureStoreBody keeps settings the form does not show and drops a cleared one", () => {
  const withExtra = { ...stored, config: { ...stored.config, region: "eu" } };
  const values = featureStoreFormValues(withExtra);
  assert.deepEqual(featureStoreBody(values, withExtra), {
    provider: "feast",
    name: "feast-prod",
    config: { url: "https://feast.example.test", feature_services: "fraud,churn", region: "eu" },
    secret_ref: "env:KIONGA_FEATURE_SECRET_FEAST",
    allowed_projects: ["p1", "p2"],
  });
  assert.deepEqual(
    featureStoreBody({ ...values, feature_services: " ", secret_ref: "  " }, withExtra).config,
    { url: "https://feast.example.test", region: "eu" },
  );
  assert.equal(featureStoreBody({ ...values, secret_ref: "  " }, withExtra).secret_ref, "");
  // Settings of another provider are not carried into a changed provider.
  assert.deepEqual(featureStoreBody({ ...values, provider: "other" }, withExtra).config, {
    url: "https://feast.example.test",
    feature_services: "fraud,churn",
  });
});

for (const identity of [admin, operator])
  test(`${identity.roles[0]}: Edit opens the dialog prefilled, saves through PUT and then runs the test`, async () => {
    const calls = api({
      ...list,
      "PUT /api/v1/admin/feature-stores/fs-1": { ...stored, name: "feast-eu" },
      "POST /api/v1/admin/feature-stores/fs-1/test": { ...stored, name: "feast-eu", health: { state: "healthy", detail: "Registry reachable." } },
    });
    const view = render(<Platform {...shell(identity)} />);
    await waitFor(() => assert.ok(view.getByText("feast-prod")));
    // Only the external store is editable: the API refuses PUT on the internal one.
    assert.equal(view.getAllByRole("button", { name: "Edit" }).length, 1);
    fireEvent.click(view.getByRole("button", { name: "Edit" }));
    const dialog = await view.findByRole("dialog", { name: "Edit feature store", hidden: true });
    const form = within(dialog);
    assert.equal((form.getByLabelText("Provider") as HTMLSelectElement).value, "feast");
    assert.equal((form.getByLabelText("Name") as HTMLInputElement).value, "feast-prod");
    assert.equal((form.getByLabelText("Provider URL") as HTMLInputElement).value, "https://feast.example.test");
    assert.equal((form.getByLabelText("Feature services") as HTMLInputElement).value, "fraud,churn");
    assert.equal((form.getByLabelText(/^Secret reference/) as HTMLInputElement).value, "env:KIONGA_FEATURE_SECRET_FEAST");
    assert.equal((form.getByLabelText(/^Allowed project IDs/) as HTMLInputElement).value, "p1, p2");
    assert.equal(dialog.querySelector('input[type="password"]'), null);
    fireEvent.change(form.getByLabelText("Name"), { target: { value: "feast-eu" } });
    fireEvent.click(form.getByRole("button", { name: "Save and test" }));
    await waitFor(() => assert.ok(calls.some((c) => c.url.endsWith("/fs-1/test"))));
    const sent = writes(calls);
    assert.deepEqual(sent.map((c) => `${c.method} ${c.url}`), [
      "PUT /api/v1/admin/feature-stores/fs-1",
      "POST /api/v1/admin/feature-stores/fs-1/test",
    ]);
    assert.deepEqual(sent[0].body, {
      provider: "feast",
      name: "feast-eu",
      config: { url: "https://feast.example.test", feature_services: "fraud,churn" },
      secret_ref: "env:KIONGA_FEATURE_SECRET_FEAST",
      allowed_projects: ["p1", "p2"],
    });
    await waitFor(() => assert.ok(view.getByText("Registry reachable.")));
    assert.equal(view.queryByRole("dialog", { name: "Edit feature store", hidden: true }), null);
  });

test("Add feature store still creates through POST and tests", async () => {
  const calls = api({
    ...list,
    "POST /api/v1/admin/feature-stores": { ...stored, id: "fs-2" },
    "POST /api/v1/admin/feature-stores/fs-2/test": { ...stored, id: "fs-2", health: { detail: "fine" } },
  });
  const view = render(<Platform {...shell(admin)} />);
  await waitFor(() => assert.ok(view.getByText("feast-prod")));
  fireEvent.click(view.getByRole("button", { name: "Add feature store" }));
  const form = within(await view.findByRole("dialog", { name: "Connect feature store", hidden: true }));
  fireEvent.change(form.getByLabelText("Name"), { target: { value: "feast-new" } });
  fireEvent.change(form.getByLabelText("Provider URL"), { target: { value: "https://feast.new.test" } });
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(calls.some((c) => c.url.endsWith("/fs-2/test"))));
  const sent = writes(calls);
  assert.equal(`${sent[0].method} ${sent[0].url}`, "POST /api/v1/admin/feature-stores");
  assert.deepEqual(sent[0].body, {
    provider: "feast",
    name: "feast-new",
    config: { url: "https://feast.new.test" },
    secret_ref: "",
    allowed_projects: [],
  });
});

test("a validation error from PUT is shown, the dialog stays open and no test runs", async () => {
  const calls = api(list, { "PUT /api/v1/admin/feature-stores/fs-1": 1 });
  const view = render(<Platform {...shell(admin)} />);
  await waitFor(() => assert.ok(view.getByText("feast-prod")));
  fireEvent.click(view.getByRole("button", { name: "Edit" }));
  const form = within(await view.findByRole("dialog", { name: "Edit feature store", hidden: true }));
  fireEvent.click(form.getByRole("button", { name: "Save and test" }));
  await waitFor(() => assert.ok(form.getAllByText(/secret_ref: not permitted/).length));
  assert.equal(calls.filter((c) => c.url.endsWith("/test")).length, 0);
});

test("people who cannot create feature stores get no section and no Edit action", async () => {
  const calls = api(list);
  const view = render(<Platform {...shell(engineer)} />);
  await waitFor(() => assert.ok(calls.length > 0));
  assert.equal(view.queryByText("Feature store connections"), null);
  assert.equal(view.queryByRole("button", { name: "Edit" }), null);
  assert.equal(calls.some((c) => c.url.includes("feature-stores")), false);
});
