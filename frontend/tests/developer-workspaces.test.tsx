import { dom } from "./setup-dom";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { render, cleanup, waitFor, fireEvent } from "@testing-library/react";
import { OperationsView } from "../features/operations/OperationsView";
import type { Identity } from "../lib/types";

const identity: Identity = {
  subject: "alice",
  email: "alice@example.com",
  roles: ["engineer"],
  services: [],
  mode: "oidc",
  permissions: {},
  provisioned: true,
};
const calls: { path: string; method: string }[] = [];
let listing: () => unknown = () => ({ items: [] });

function setup() {
  history.replaceState(null, "", "http://localhost/console/settings");
  calls.length = 0;
  Object.assign(globalThis, { localStorage: dom.window.localStorage });
  globalThis.fetch = async (input, options) => {
    const path = String(input),
      method = options?.method || "GET";
    calls.push({ path, method });
    const json = (value: unknown, status = 200) =>
      new Response(JSON.stringify(value), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    if (path === "/api/v1/settings/tokens") return json({ items: [] });
    if (path === "/api/v1/settings/huggingface")
      return json({ configured: false, connected: false });
    if (path === "/api/v1/workspaces") return json(listing());
    if (method === "POST" && path.startsWith("/api/v1/workspaces/"))
      return json({ changed: true });
    throw new Error(`Unexpected request ${method} ${path}`);
  };
}
function settings(extra: Partial<Identity> = {}) {
  return render(
    <OperationsView
      view="settings"
      identity={{ ...identity, ...extra }}
      project=""
      resource=""
      onNavigate={() => {}}
    />,
  );
}
const managed = (state: string, overrides: Record<string, unknown> = {}) => ({
  managed: true,
  workspace: "alice-ws",
  state,
  ready: state === "ready",
  services: ["workbench", "ide"],
  transitioning: ["starting", "stopping"].includes(state),
  message: `message for ${state}`,
  ...overrides,
});
const entry = (id: string, name: string, state: string, lifecycle?: unknown) => ({
  id,
  name,
  state,
  available: state === "ready",
  message: `list message ${state}`,
  lifecycle,
});
afterEach(cleanup);

test("a stopped workspace offers Start, which starts it and follows it to ready", async () => {
  setup();
  let phase = "stopped";
  listing = () => ({
    items: [
      entry("workbench", "JupyterLab", phase, managed(phase)),
      entry("ide", "Browser IDE", phase, managed(phase)),
    ],
  });
  const view = settings();
  const start = (await view.findByRole("button", {
    name: "Start JupyterLab",
  })) as HTMLButtonElement;
  const stop = view.getByRole("button", { name: "Stop JupyterLab" }) as HTMLButtonElement;
  assert.equal(start.disabled, false);
  assert.equal(stop.disabled, true);
  assert.ok(view.getAllByText(/Files are kept when stopped/).length >= 1);
  phase = "starting";
  fireEvent.click(start);
  await waitFor(() =>
    assert.ok(
      calls.some((call) => call.method === "POST" && call.path === "/api/v1/workspaces/workbench/start"),
    ),
  );
  await waitFor(() =>
    assert.equal(
      (view.getByRole("button", { name: "Start JupyterLab" }) as HTMLButtonElement).disabled,
      true,
    ),
  );
  assert.equal(view.queryByRole("link", { name: "Open JupyterLab" }), null);
  // The console keeps polling until the workspace is ready.
  phase = "ready";
  const open = await view.findByRole("link", { name: "Open JupyterLab" }, { timeout: 6000 });
  assert.equal(open.getAttribute("href"), "/workspace?tool=workbench");
  assert.ok(view.getAllByText("ready").length >= 1);
});

test("Stop posts to the stop endpoint", async () => {
  setup();
  listing = () => ({
    items: [entry("ide", "Browser IDE", "ready", managed("ready"))],
  });
  const view = settings();
  fireEvent.click(await view.findByRole("button", { name: "Stop Browser IDE" }));
  await waitFor(() =>
    assert.ok(
      calls.some((call) => call.method === "POST" && call.path === "/api/v1/workspaces/ide/stop"),
    ),
  );
});

test("Compose installs show an explanation and no start or stop controls", async () => {
  setup();
  const lifecycle = {
    managed: false,
    transitioning: false,
    ready: false,
    message: "On Compose, JupyterLab and the IDE are separate services of the stack. Start and stop them from the stack, not the console.",
  };
  listing = () => ({
    items: [entry("workbench", "JupyterLab", "ready", lifecycle)],
  });
  const view = settings();
  await view.findByText(/separate services of the stack/);
  assert.equal(view.queryByRole("button", { name: /^(Start|Stop) / }), null);
  assert.ok(view.getByRole("link", { name: "Open JupyterLab" }));
});

test("a viewer sees a disabled workspace with no start or stop controls", async () => {
  setup();
  listing = () => ({
    items: [
      { id: "workbench", name: "JupyterLab", state: "forbidden", available: false, message: "Request access to use this workspace" },
    ],
  });
  const view = settings({ roles: ["viewer"] });
  const disabled = (await view.findByText(/JupyterLab: forbidden/)) as HTMLButtonElement;
  assert.equal(disabled.closest("button")!.disabled, true);
  assert.equal(view.queryByRole("button", { name: /^(Start|Stop) / }), null);
});

test("a failed workspace offers Stop but not a Start that would do nothing", async () => {
  setup();
  listing = () => ({
    items: [entry("ide", "Browser IDE", "failed", managed("failed"))],
  });
  const view = settings();
  const start = (await view.findByRole("button", { name: "Start Browser IDE" })) as HTMLButtonElement;
  assert.equal(start.disabled, true);
  assert.equal((view.getByRole("button", { name: "Stop Browser IDE" }) as HTMLButtonElement).disabled, false);
});
