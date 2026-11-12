import { dom } from "./setup-dom";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { render, cleanup, waitFor, fireEvent } from "@testing-library/react";
import { OperationsView } from "../features/operations/OperationsView";
import type { Identity } from "../lib/types";
const identity: Identity = {
  subject: "admin",
  email: "admin@example.com",
  roles: ["admin"],
  services: [],
  mode: "local",
  permissions: {
    functions_write: true,
    pipelines_write: true,
    models_write: true,
  },
  provisioned: true,
};
const calls: { path: string; method: string; body: any }[] = [];
function setup(
  fixtures: Record<string, any>,
  url = "http://localhost/console/functions",
) {
  history.replaceState(null, "", url);
  calls.length = 0;
  Object.assign(globalThis, { localStorage: dom.window.localStorage });
  localStorage.clear();
  dom.window.HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  dom.window.HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
  globalThis.fetch = async (input, options) => {
    const path = String(input),
      method = options?.method || "GET",
      body = options?.body ? JSON.parse(String(options.body)) : undefined;
    calls.push({ path, method, body });
    const canonical = (path: string) => {
      const url = new URL(path, "http://localhost");
      url.searchParams.sort();
      return url.pathname + url.search;
    };
    const fixture =
      fixtures[`${method} ${path}`] ??
      fixtures[path] ??
      (method === "GET"
        ? Object.entries(fixtures).find(
            ([key]) =>
              key.startsWith("/") && canonical(key) === canonical(path),
          )?.[1]
        : undefined);
    if (fixture === undefined)
      throw new Error(`Unexpected request ${method} ${path}`);
    const value = typeof fixture === "function" ? await fixture(body) : fixture;
    return new Response(JSON.stringify(value?.__status ? value.body : value), {
      status: value?.__status || 200,
      headers: { "Content-Type": "application/json" },
    });
  };
}
afterEach(cleanup);
test("projects can start without templates even when the catalog is unavailable", async () => {
  setup({
    "/api/v1/projects": { items: [] },
    "/api/v1/project-templates": { __status: 503, body: { message: "Catalog unavailable" } },
    "/api/v1/workspaces": { items: [] },
    "POST /api/v1/projects": { id: "custom" },
  }, "http://localhost/console/projects");
  const view = app("projects", { identity: { ...identity, permissions: { projects_write: true } } });
  fireEvent.click(view.getByRole("button", { name: "New project" }));
  fireEvent.change(view.getByLabelText(/Project name/), { target: { value: "My custom service" } });
  fireEvent.change(view.getByLabelText(/Framework or language/), { target: { value: "Rust" } });
  fireEvent.click(view.getByRole("button", { name: "Create project" }));
  await waitFor(() => assert.ok(calls.some(call => call.method === "POST" && call.path === "/api/v1/projects")));
  const request = calls.find(call => call.method === "POST" && call.path === "/api/v1/projects")!;
  assert.equal(request.body.template, "none");
  assert.equal(request.body.framework, "Rust");
  assert.equal(request.body.template_version, undefined);
});
function app(view: string, extra = {}) {
  return render(
    <OperationsView
      view={view}
      identity={identity}
      project="p1"
      resource=""
      onNavigate={() => {}}
      {...extra}
    />,
  );
}
test("function deployment maps Kafka trigger and environment into the existing API request", async () => {
  setup({
    "/api/v1/functions": { configured: true, items: [] },
    "/api/v1/project-options": { items: [{ id: "p1", name: "Project one" }] },
    "POST /api/v1/functions": { name: "score" },
  });
  const view = app("functions");
  await waitFor(() =>
    assert.equal(
      (
        view.getByRole("button", {
          name: "Deploy function",
        }) as HTMLButtonElement
      ).disabled,
      false,
    ),
  );
  fireEvent.click(view.getByRole("button", { name: "Deploy function" }));
  fireEvent.change(view.getByLabelText("Name"), { target: { value: "score" } });
  fireEvent.change(view.getByLabelText("OCI image"), {
    target: { value: "registry.example/score:1" },
  });
  fireEvent.change(view.getByLabelText("Trigger"), {
    target: { value: "kafka" },
  });
  fireEvent.change(view.getByLabelText(/Cron expression or Kafka topic/), {
    target: { value: "events.scored" },
  });
  fireEvent.change(view.getByLabelText("Environment variables"), {
    target: { value: '{"MODEL":"score-v1"}' },
  });
  fireEvent.submit(view.getByLabelText("OCI image").closest("form")!);
  await waitFor(() => assert.ok(calls.find((call) => call.method === "POST")));
  const body = calls.find((call) => call.method === "POST")!.body;
  assert.equal(body.project_id, "p1");
  assert.deepEqual(body.env_vars, { MODEL: "score-v1" });
  assert.deepEqual(body.annotations, {
    "io.kionga.invocation": "sync",
    topic: "events.scored",
  });
  assert.equal("trigger_type" in body, false);
  assert.equal("trigger_source" in body, false);
});
test("read-only identity cannot deploy or invoke functions", async () => {
  setup({
    "/api/v1/functions": {
      configured: true,
      items: [
        {
          name: "score",
          project_id: "p1",
          image: "score:1",
          status: "deployed",
          replicas: 1,
        },
      ],
    },
    "/api/v1/project-options": { items: [] },
  });
  const view = app("functions", {
    identity: { ...identity, roles: ["viewer"], permissions: {} },
  });
  await waitFor(() => assert.ok(view.getByText("score")));
  assert.equal(
    (view.getByRole("button", { name: "Deploy function" }) as HTMLButtonElement)
      .disabled,
    true,
  );
  assert.equal(
    (view.getByRole("button", { name: "Invoke" }) as HTMLButtonElement)
      .disabled,
    true,
  );
});
test("pipeline submission remains disabled when preflight rejects the run", async () => {
  setup({
    "/api/v1/pipelines/runs": [],
    "/api/v1/pipelines/definitions": { items: [] },
    "/api/v1/project-options": { items: [{ id: "p1", name: "Project one" }] },
    "POST /api/v1/pipelines/preflight": {
      allowed: false,
      checks: [{ status: "fail", message: "Project capacity exceeded" }],
      fields: [{field: "parameters.window", default: "daily", effective: "weekly", overridden: true, locked: false, reason: "Parameter override is permitted"}],
      policy_decision: "pipeline:OverrideParameters=allow",
    },
  });
  const view = app("pipelines");
  fireEvent.click(view.getByRole("button", { name: "Run pipeline" }));
  fireEvent.change(view.getByLabelText("Run name"), {
    target: { value: "Capacity test" },
  });
  fireEvent.click(view.getByRole("button", { name: "Check run" }));
  await waitFor(() => assert.ok(view.getByText("Project capacity exceeded")));
  assert.ok(view.getByRole("region", {name: "Effective run fields"}));
  assert.ok(view.getByText("parameters.window"));
  assert.ok(view.getByText("weekly"));
  assert.ok(view.getByText("Parameter override is permitted"));
  assert.ok(view.getByText("pipeline:OverrideParameters=allow"));
  assert.equal(
    (view.getByRole("button", { name: "Submit run" }) as HTMLButtonElement)
      .disabled,
    true,
  );
  assert.equal(
    calls.some((call) => call.path === "/api/v1/pipelines/submit"),
    false,
  );
  assert.equal(
    calls.find((call) => call.path === "/api/v1/pipelines/preflight")!.body
      .name,
    "Capacity test",
  );
});
test("Hub token is cleared before the account request and is never persisted in browser storage", async () => {
  let finish!: (value: unknown) => void;
  setup(
    {
      "/api/v1/settings/tokens": { items: [] },
      "/api/v1/settings/huggingface": { configured: true, connected: false },
      "/api/v1/workspaces": { items: [] },
      "PUT /api/v1/settings/huggingface": () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    },
    "http://localhost/console/settings",
  );
  const view = app("settings");
  await waitFor(() =>
    assert.equal(
      (
        view.getByRole("button", {
          name: "Connect account",
        }) as HTMLButtonElement
      ).disabled,
      false,
    ),
  );
  fireEvent.change(view.getByLabelText("Access token"), {
    target: { value: "hf_private_token" },
  });
  fireEvent.submit(view.getByLabelText("Access token").closest("form")!);
  await waitFor(() => assert.ok(finish));
  assert.equal(
    (view.getByLabelText("Access token") as HTMLInputElement).value,
    "",
  );
  assert.equal(
    JSON.stringify({ ...localStorage }).includes("hf_private_token"),
    false,
  );
  assert.equal(
    calls.find((call) => call.method === "PUT")!.body.token,
    "hf_private_token",
  );
  finish({ connected: true, username: "test" });
});

test("policy editor preserves UTC condition semantics across form and JSON modes", async () => {
  setup(
    {
      "/api/v1/admin/users": { items: [] },
      "/api/v1/admin/access-requests": { items: [] },
      "/api/v1/admin/resource-profiles": { items: [] },
      "/api/v1/admin/iam/groups": { items: [] },
      "/api/v1/admin/iam/policies": { items: [] },
      "/api/v1/admin/iam/attachments": { items: [] },
      "/api/v1/iam/catalog": { actions: [], profiles: [] },
      "POST /api/v1/admin/iam/policies": { id: "office-hours", version: 1 },
    },
    "http://localhost/console/access?tab=policies",
  );
  const view = app("access");
  await waitFor(() =>
    assert.ok(view.getByRole("button", { name: "New policy" })),
  );
  fireEvent.click(view.getByRole("button", { name: "New policy" }));
  fireEvent.change(view.getByLabelText("Policy ID"), {
    target: { value: "office-hours" },
  });
  fireEvent.change(view.getByLabelText("Name"), {
    target: { value: "Office hours" },
  });
  fireEvent.click(view.getByRole("button", { name: "Add statement" }));
  fireEvent.change(view.getByLabelText(/Actions/), {
    target: { value: "pipeline:Run" },
  });
  fireEvent.change(view.getByLabelText(/Resources/), {
    target: { value: "kionga:project/p1/pipeline/*" },
  });
  fireEvent.click(view.getByText("Conditions"));
  fireEvent.change(view.getByLabelText("UTC start hour"), {
    target: { value: "9" },
  });
  fireEvent.change(view.getByLabelText("UTC end hour"), {
    target: { value: "17" },
  });
  fireEvent.click(view.getByRole("button", { name: "JSON" }));
  const draft = JSON.parse(
    (view.getByLabelText("Policy JSON") as HTMLTextAreaElement).value,
  );
  assert.deepEqual(draft.statements[0].conditions.utc_hours, {
    start: 9,
    end: 17,
  });
  fireEvent.click(view.getByRole("button", { name: "Form" }));
  fireEvent.submit(view.getByLabelText("Policy ID").closest("form")!);
  await waitFor(() =>
    assert.ok(
      calls.find(
        (call) =>
          call.path === "/api/v1/admin/iam/policies" && call.method === "POST",
      ),
    ),
  );
  const body = calls.find(
    (call) =>
      call.path === "/api/v1/admin/iam/policies" && call.method === "POST",
  )!.body;
  assert.equal(body.id, "office-hours");
  assert.deepEqual(body.statements[0].actions, ["pipeline:Run"]);
  assert.deepEqual(body.statements[0].conditions, {
    utc_hours: { start: 9, end: 17 },
  });
});

test("project deep links preserve the resource and scaffold sends only server-approved workspace options", async () => {
  const project = {
    id: "p1",
    name: "Churn project",
    namespace: "churn",
    template: "production-ml",
    template_version: "1.0.0",
    scaffold_command:
      "kionga scaffold churn --template production-ml --framework xgboost --accelerator cpu --profile team",
  };
  const plan = {
    project_id: "p1",
    workspace: "workbench",
    working_dir: "/workspace/projects",
    output_dir: "/workspace/projects/churn",
    command: project.scaffold_command,
    argv: project.scaffold_command.split(" "),
    available: true,
  };
  setup(
    {
      "/api/v1/projects": [project],
      "/api/v1/projects/p1": project,
      "/api/v1/project-templates": { items: [] },
      "/api/v1/workspaces": {
        items: [{ id: "workbench", available: true, name: "JupyterLab" }],
      },
      "/api/v1/projects/p1/scaffold-jobs": { items: [] },
      "/api/v1/projects/p1/scaffold-plan": plan,
      "POST /api/v1/projects/p1/scaffold-jobs": {
        id: "scf-1",
        project_id: "p1",
        workspace: "workbench",
        status: "succeeded",
        files: ["main.py"],
        output_dir: plan.output_dir,
      },
    },
    "http://localhost/console/projects?resource=p1",
  );
  const navigate: { view: string; resource?: string }[] = [];
  const view = render(
    <OperationsView
      view="projects"
      identity={{
        ...identity,
        permissions: { projects_write: true, git_write: true },
      }}
      project=""
      resource="p1"
      onNavigate={(view, resource) => navigate.push({ view, resource })}
    />,
  );
  await waitFor(() =>
    assert.ok(view.getByRole("heading", { name: "Churn project" })),
  );
  assert.equal(
    view.getByRole("link", { name: "Open JupyterLab" }).getAttribute("href"),
    "/workspace?tool=workbench&project=p1",
  );
  assert.equal(
    view.getByRole("link", { name: "Pipelines" }).getAttribute("href"),
    "/console/pipelines?project=p1",
  );
  fireEvent.click(view.getByRole("button", { name: "Run in workspace" }));
  await waitFor(() =>
    assert.ok(view.getByRole("heading", { name: "Run scaffold in workspace" })),
  );
  assert.ok(view.getAllByText(project.scaffold_command).length >= 1);
  fireEvent.click(view.getByRole("button", { name: "Run command" }));
  await waitFor(() =>
    assert.ok(
      calls.find(
        (call) =>
          call.method === "POST" && call.path.endsWith("/scaffold-jobs"),
      ),
    ),
  );
  const body = calls.find(
    (call) => call.method === "POST" && call.path.endsWith("/scaffold-jobs"),
  )!.body;
  assert.deepEqual(body, { options: { workspace: "workbench" } });
  assert.equal("command" in body, false);
  assert.equal("argv" in body, false);
  await waitFor(() => assert.ok(view.getByText("main.py")));
  fireEvent.click(view.getByRole("button", { name: "All projects" }));
  assert.deepEqual(navigate.at(-1), { view: "projects", resource: "" });
});

test("successful preflight is invalidated when run inputs change", async () => {
  setup({
    "/api/v1/pipelines/runs": [],
    "/api/v1/pipelines/definitions": { items: [] },
    "/api/v1/project-options": { items: [{ id: "p1", name: "Project one" }] },
    "POST /api/v1/pipelines/preflight": {
      allowed: true,
      checks: [{ status: "pass", message: "Capacity available" }],
    },
  });
  const view = app("pipelines");
  fireEvent.click(view.getByRole("button", { name: "Run pipeline" }));
  fireEvent.change(view.getByLabelText("Run name"), {
    target: { value: "Checked run" },
  });
  fireEvent.click(view.getByRole("button", { name: "Check run" }));
  await waitFor(() =>
    assert.equal(
      (view.getByRole("button", { name: "Submit run" }) as HTMLButtonElement)
        .disabled,
      false,
    ),
  );
  fireEvent.change(view.getByLabelText("Run name"), {
    target: { value: "Changed run" },
  });
  assert.equal(
    (view.getByRole("button", { name: "Submit run" }) as HTMLButtonElement)
      .disabled,
    true,
  );
  assert.equal(
    calls.some((call) => call.path === "/api/v1/pipelines/submit"),
    false,
  );
});

test("access simulator uses catalog actions and shows the deciding policy without changing grants", async () => {
  const explain =
    "/api/v1/iam/explain?" +
    new URLSearchParams({
      action: "pipeline:Run",
      resource: "kionga:project/p1/pipeline/train",
      principal: "alice",
      ip: "10.0.0.5",
      resource_profile: "starter",
    });
  setup(
    {
      "/api/v1/admin/users": { items: [] },
      "/api/v1/admin/access-requests": { items: [] },
      "/api/v1/admin/resource-profiles": { items: [] },
      "/api/v1/admin/iam/groups": { items: [] },
      "/api/v1/admin/iam/policies": { items: [] },
      "/api/v1/admin/iam/attachments": { items: [] },
      "/api/v1/iam/catalog": {
        actions: [{ action: "pipeline:Run" }],
        profiles: ["starter"],
      },
      [explain]: {
        principal: { subject: "alice", roles: ["user"], groups: ["analysts"] },
        decision: {
          allowed: false,
          policy_id: "business-hours",
          sid: "OfficeHours",
          reason: "Request is outside the allowed UTC window",
        },
      },
      "/api/v1/iam/effective?principal=alice": {
        policies: [
          {
            source: "group:analysts",
            policy: {
              id: "business-hours",
              name: "Business hours",
              version: 2,
              statements: [],
            },
          },
        ],
      },
    },
    "http://localhost/console/access?tab=simulator",
  );
  const view = app("access");
  await waitFor(() =>
    assert.ok(view.getByRole("option", { name: "pipeline:Run" })),
  );
  fireEvent.change(view.getByLabelText("Resource"), {
    target: { value: "kionga:project/p1/pipeline/train" },
  });
  fireEvent.change(view.getByLabelText(/Account subject/), {
    target: { value: "alice" },
  });
  fireEvent.change(view.getByLabelText("Source IP address"), {
    target: { value: "10.0.0.5" },
  });
  fireEvent.change(view.getByLabelText("Resource profile"), {
    target: { value: "starter" },
  });
  fireEvent.click(view.getByRole("button", { name: "Explain access" }));
  await waitFor(() => assert.ok(view.getByText("Denied")));
  assert.match(view.container.textContent || "", /business-hours/);
  assert.match(view.container.textContent || "", /OfficeHours/);
  assert.match(view.container.textContent || "", /Business hours/);
  assert.equal(
    calls.some((call) => call.method !== "GET"),
    false,
  );
});


test("model resource deep links open the registered model and clear the resource on close", async () => {
  setup({
    "/api/v1/models": [{id: "model-1", project_id: "p1", name: "Classifier", version: "2", stage: "staging"}],
    "/api/v1/project-options": {items: [{id: "p1", name: "Project one"}]},
  });
  const navigations: string[][] = [];
  const view = app("models", {resource: "model-1", onNavigate: (...args: string[]) => navigations.push(args)});
  await waitFor(() => assert.ok(view.getByRole("dialog", {name: "Classifier v2"})));
  fireEvent.click(view.getByRole("button", {name: "Close Classifier v2"}));
  assert.deepEqual(navigations.at(-1), ["models", ""]);
});

test("schedule changes refresh revision history while retaining the open definition", async () => {
  let revision = 1;
  const definition = {id: "flow-1", project_id: "p1", name: "Daily training", jobs: [], triggers: [{type: "schedule", cron: "30 2 * * *", timezone: "Africa/Nairobi", paused: false}]};
  setup({
    "/api/v1/pipelines/runs": [],
    "/api/v1/pipelines/definitions": [definition],
    "/api/v1/project-options": {items: [{id: "p1", name: "Project one"}]},
    "/api/v1/pipelines/definitions/flow-1/revisions": () => ({items: Array.from({length: revision}, (_, index) => ({revision: index + 1, message: `Revision ${index + 1}`}))}),
    "POST /api/v1/pipelines/definitions/flow-1/triggers/0/pause": () => { revision += 1; return {...definition, triggers: [{...definition.triggers[0], paused: true}]}; },
  });
  const view = app("pipelines");
  await waitFor(() => assert.ok(view.getByRole("button", {name: "Details"})));
  fireEvent.click(view.getByRole("button", {name: "Details"}));
  await waitFor(() => assert.ok(view.getByText("Revision 1")));
  fireEvent.click(view.getByRole("button", {name: "Pause"}));
  await waitFor(() => assert.ok(view.getByText("Revision 2")));
  assert.ok(view.getByRole("dialog", {name: "Daily training"}));
  assert.ok(view.getByRole("button", {name: "Resume"}));
});
