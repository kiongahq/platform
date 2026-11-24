import { dom } from "./setup-dom";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { render, cleanup, waitFor, fireEvent } from "@testing-library/react";
import { OperationsView } from "../features/operations/OperationsView";
import { USER_ROLES, DEFAULT_USER_ROLE } from "../features/operations/userRoles";
import type { Identity } from "../lib/types";

// The roles the gateway accepts for a person (go/internal/store UserRoles). Spelled
// out on purpose: a Go test keeps userRoles.ts equal to the gateway list, and this
// literal keeps the form equal to userRoles.ts, so drift anywhere fails a test.
const GATEWAY_ROLES = ["user", "viewer", "engineer", "operator", "admin"];
const identity: Identity = {
  subject: "admin",
  email: "admin@example.com",
  roles: ["admin"],
  services: [],
  mode: "local",
  permissions: {},
  provisioned: true,
};
const calls: { path: string; method: string; body: any }[] = [];
function setup(users: any[]) {
  history.replaceState(null, "", "http://localhost/console/access");
  calls.length = 0;
  Object.assign(globalThis, { localStorage: dom.window.localStorage });
  dom.window.HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  dom.window.HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
  const fixtures: Record<string, any> = {
    "/api/v1/admin/users": { items: users },
    "/api/v1/admin/access-requests": { items: [] },
    "/api/v1/admin/resource-profiles": { items: [] },
    "/api/v1/admin/iam/groups": { items: [] },
    "/api/v1/admin/iam/policies": { items: [] },
    "/api/v1/admin/iam/attachments": { items: [] },
    "/api/v1/iam/catalog": { actions: [], profiles: [] },
  };
  globalThis.fetch = async (input, options) => {
    const path = String(input),
      method = options?.method || "GET",
      body = options?.body ? JSON.parse(String(options.body)) : undefined;
    calls.push({ path, method, body });
    const value = method === "GET" ? fixtures[path] : {};
    if (value === undefined) throw new Error(`Unexpected request ${method} ${path}`);
    return new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } });
  };
}
function app() {
  return render(
    <OperationsView view="access" identity={identity} project="p1" resource="" onNavigate={() => {}} />,
  );
}
afterEach(cleanup);

test("role list equals the gateway roles, least to most privileged, without service", () => {
  assert.deepEqual(USER_ROLES.map((item) => item.value), GATEWAY_ROLES);
  for (const item of USER_ROLES) assert.equal(item.label, item.value);
  assert.equal(DEFAULT_USER_ROLE, "user");
  assert.equal(GATEWAY_ROLES.includes("service"), false);
});

test("regression: Provision user offers engineer and exactly the gateway roles", async () => {
  setup([]);
  const view = app();
  await waitFor(() => assert.ok(view.getByRole("button", { name: "Provision user" })));
  fireEvent.click(view.getByRole("button", { name: "Provision user" }));
  const select = view.getByLabelText(/^Role/) as HTMLSelectElement;
  assert.deepEqual(Array.from(select.options).map((option) => option.value), GATEWAY_ROLES);
  assert.equal(select.value, "user");
  assert.notDeepEqual(Array.from(select.options).map((option) => option.value), ["user", "viewer", "operator", "admin"]);
});

test("role help describes every offered role", async () => {
  setup([]);
  const view = app();
  await waitFor(() => assert.ok(view.getByRole("button", { name: "Provision user" })));
  fireEvent.click(view.getByRole("button", { name: "Provision user" }));
  const help = view.container.ownerDocument.body.textContent || "";
  for (const role of GATEWAY_ROLES) assert.ok(help.includes(`${role}:`), `help names ${role}`);
});

test("choosing engineer submits role engineer unchanged", async () => {
  setup([]);
  const view = app();
  await waitFor(() => assert.ok(view.getByRole("button", { name: "Provision user" })));
  fireEvent.click(view.getByRole("button", { name: "Provision user" }));
  fireEvent.change(view.getByLabelText(/Account subject/), { target: { value: "eve" } });
  fireEvent.change(view.getByLabelText(/^Role/), { target: { value: "engineer" } });
  fireEvent.click(view.getByRole("button", { name: "Save access" }));
  await waitFor(() => assert.ok(calls.find((call) => call.method === "PUT")));
  const put = calls.find((call) => call.method === "PUT")!;
  assert.equal(put.path, "/api/v1/admin/users/eve");
  assert.equal(put.body.role, "engineer");
});

test("editing an engineer keeps the role selected", async () => {
  setup([{ subject: "eve", role: "engineer", services: [], project_ids: [], storage: {}, compute: {} }]);
  const view = app();
  await waitFor(() => assert.ok(view.getAllByText("eve").length));
  fireEvent.click(view.getByRole("button", { name: /Edit/ }));
  assert.equal((view.getByLabelText(/^Role/) as HTMLSelectElement).value, "engineer");
});
