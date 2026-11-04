import "./setup-dom";
import { test, afterEach, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import React from "react";
import { act, cleanup, fireEvent, render, waitFor, within } from "@testing-library/react";
import { Icon } from "../components/ui/Icon";
import { AgentDefinitionsView } from "../features/teams/AgentDefinitionsView";
import { RunView } from "../features/teams/RunView";
import { activeTime, canResume, mergeEvents, sandboxNotice, tokens } from "../features/teams/runState";
import { TeamsView } from "../features/teams/TeamsView";
import { TeamRunsView } from "../features/teams/TeamRunsView";
import { blankDefinition, blankTeam, definitionPayload, validateDefinition, validateTeam } from "../features/teams/validation";

// A failed assert.ok without a message makes node re-parse the transpiled source, which hangs
// inside a waitFor retry. Always pass a message.
const ok = (value: unknown, message = "expected a truthy value") => assert.ok(value, message);
const writer: any = { subject: "ada", roles: ["user"], services: ["agents", "catalog"], permissions: { agents_write: true }, mode: "local" };
const viewer: any = { ...writer, subject: "grace", permissions: {} };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
type Call = { path: string; method: string; body: any; headers: any };
type Route = (call: Call) => Response | Promise<Response> | undefined;
let calls: Call[] = [];
/** Fake gateway: the first route returning a response wins; anything else is a loud 404. */
function gateway(...routes: Route[]) {
  calls = [];
  globalThis.fetch = (async (input: any, init: any = {}) => {
    const call = { path: String(input), method: init.method || "GET", body: init.body ? JSON.parse(init.body) : undefined, headers: init.headers || {} };
    calls.push(call);
    for (const route of routes) {
      const response = await route(call);
      if (response) return response;
    }
    return json({ message: `no route for ${call.method} ${call.path}` }, 404);
  }) as any;
}
const on = (method: string, path: string | RegExp, reply: any): Route => (call) => {
  if (call.method !== method) return;
  if (path instanceof RegExp ? !path.test(call.path) : call.path !== path) return;
  return typeof reply === "function" ? reply(call) : json(reply);
};
const sent = (method: string, path: string) => calls.filter((call) => call.method === method && call.path === path);
const resources: Route[] = [
  on("GET", "/api/v1/project-options", { items: [{ id: "p1", name: "Fraud" }, { id: "p2", name: "Support" }] }),
  on("GET", "/api/v1/tools", { items: [{ name: "lookup" }, { name: "other-project-tool", project_id: "p2" }] }),
  on("GET", "/api/v1/agent-api-connections", { items: [{ id: "api-1", name: "CRM", project_id: "p1" }, { id: "api-2", name: "Elsewhere", project_id: "p2" }] }),
  on("GET", "/api/v1/knowledge-bases", { items: [{ id: "kb-1", name: "Runbooks", project_id: "p1" }] }),
  on("GET", "/api/v1/datasets", { items: [{ id: "ds-1", name: "Claims", project_id: "p1" }] }),
];
class FakeEventSource {
  static last: FakeEventSource | null = null;
  static opened: string[] = [];
  listeners: Record<string, Array<(event: any) => void>> = {};
  onerror: (() => void) | null = null;
  onopen: (() => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeEventSource.last = this;
    FakeEventSource.opened.push(url);
  }
  addEventListener(name: string, listener: (event: any) => void) {
    (this.listeners[name] ||= []).push(listener);
  }
  close() {
    this.closed = true;
  }
  emit(name: string, data?: unknown) {
    for (const listener of this.listeners[name] || []) listener({ data: JSON.stringify(data) });
  }
}
beforeEach(() => {
  window.HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  window.HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
  FakeEventSource.last = null;
  FakeEventSource.opened = [];
  (globalThis as any).EventSource = FakeEventSource;
});
afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "/");
});
const fill = (view: any, label: string | RegExp, value: string) => fireEvent.change(view.getByLabelText(label), { target: { value } });
const validDefinition = () => ({ ...blankDefinition("p1"), name: "bug-fixer", description: "Fixes bugs", instructions: "Fix it" });

// ---------- Editor validation

test("definition rules match the published limits at each boundary", () => {
  assert.deepEqual(validateDefinition(validDefinition()), {});
  for (const name of ["", "Bug", "1bug", "bug_fixer", `a${"b".repeat(63)}`]) ok(validateDefinition({ ...validDefinition(), name }).name, name);
  assert.equal(validateDefinition({ ...validDefinition(), name: `a${"b".repeat(62)}` }).name, undefined);
  const limits = (patch: object) => validateDefinition({ ...validDefinition(), limits: { ...validDefinition().limits, ...patch } });
  ok(limits({ max_turns: 0 })["limits.max_turns"]);
  ok(limits({ max_turns: 501 })["limits.max_turns"]);
  ok(limits({ max_turns: 1.5 })["limits.max_turns"]);
  ok(limits({ max_turns: Number.NaN })["limits.max_turns"]);
  assert.equal(limits({ max_turns: 500 })["limits.max_turns"], undefined);
  ok(limits({ max_tokens: 5_000_001 })["limits.max_tokens"]);
  ok(limits({ max_seconds: 59 })["limits.max_seconds"]);
  ok(limits({ max_seconds: 14_401 })["limits.max_seconds"]);
  ok(validateDefinition({ ...validDefinition(), description: "x".repeat(1001) }).description);
  ok(validateDefinition({ ...validDefinition(), instructions: "x".repeat(32_001) }).instructions);
});
test("sandbox and git grants require the sandbox workspace, and git needs a clean https URL", () => {
  const withTools = (tools: any[], workspace: any = "none") => validateDefinition({ ...validDefinition(), tools, workspace });
  ok(withTools([{ kind: "sandbox" }]).workspace);
  ok(withTools([{ kind: "git", repository: "https://github.com/o/r" }]).workspace);
  assert.deepEqual(withTools([{ kind: "sandbox" }], "sandbox"), {});
  assert.deepEqual(withTools([{ kind: "tool", ref: "lookup" }]), {});
  for (const repository of ["", "http://github.com/o/r", "git@github.com:o/r.git", "ssh://github.com/o/r", "https://user:pw@github.com/o/r", "https://github.com/o/r?x=1", "https://github.com/o/r#x", "https://github.com"])
    ok(withTools([{ kind: "git", repository }], "sandbox")["tools.0.repository"], repository);
});
test("team rules cover members, brief and every limit", () => {
  const team = { ...blankTeam("p1"), name: "code-change", coordinator: { instructions: "Coordinate", model: "llm" }, members: ["adf-1"] };
  assert.deepEqual(validateTeam(team), {});
  ok(validateTeam({ ...team, members: [] }).members);
  ok(validateTeam({ ...team, members: Array.from({ length: 17 }, (_, i) => `adf-${i}`) }).members);
  ok(validateTeam({ ...team, brief: "x".repeat(16_001) }).brief);
  ok(validateTeam({ ...team, coordinator: { instructions: " ", model: "llm" } })["coordinator.instructions"]);
  const limits = (patch: object) => validateTeam({ ...team, limits: { ...team.limits, ...patch } });
  ok(limits({ max_workers_total: 51 })["limits.max_workers_total"]);
  ok(limits({ max_concurrent_workers: 17 })["limits.max_concurrent_workers"]);
  ok(limits({ max_tokens: 20_000_001 })["limits.max_tokens"]);
  ok(limits({ max_seconds: 86_401 })["limits.max_seconds"]);
  ok(limits({ max_workers_total: 3, max_concurrent_workers: 5 })["limits.max_concurrent_workers"], "concurrent above total is refused");
  assert.equal(limits({ max_workers_total: 5, max_concurrent_workers: 5 })["limits.max_concurrent_workers"], undefined);
  assert.equal(limits({ max_seconds: 86_400 })["limits.max_seconds"], undefined);
});
test("the agent editor refuses invalid input without calling the gateway, focuses the summary and names each field", async () => {
  gateway(...resources);
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="new" onNavigate={() => {}} />);
  fill(view, "Name", "Not Valid");
  fireEvent.click(view.getByRole("button", { name: "Create agent" }));
  const summary = await view.findByRole("alert");
  assert.equal(document.activeElement, summary);
  assert.match(summary.textContent || "", /problems/);
  assert.equal((view.getByLabelText("Name") as HTMLInputElement).getAttribute("aria-invalid"), "true");
  assert.equal(sent("POST", "/api/v1/agent-definitions").length, 0);
  fireEvent.click(within(summary).getByText(/lowercase letter first/));
  assert.equal(document.activeElement, view.getByLabelText("Name"));
});
test("a valid agent posts exactly the documented body", async () => {
  gateway(...resources, on("POST", "/api/v1/agent-definitions", (call: Call) => json({ ...call.body, id: "adf-1", revision: 1 }, 201)));
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="new" onNavigate={() => {}} />);
  await waitFor(() => ok(view.getByRole("option", { name: "Fraud" })));
  fill(view, "Name", "bug-fixer");
  fill(view, "When to use", "Fixes bugs");
  fill(view, "Instructions", "Fix it");
  fireEvent.click(view.getByRole("button", { name: "Create agent" }));
  await view.findByText(/Saved bug-fixer, revision 1/);
  assert.deepEqual(sent("POST", "/api/v1/agent-definitions")[0].body, {
    project_id: "p1", name: "bug-fixer", description: "Fixes bugs", instructions: "Fix it", model: "llm", tools: [], workspace: "none",
    limits: { max_turns: 100, max_tokens: 1000000, max_seconds: 3600 },
  });
  assert.equal(window.location.search, "?resource=adf-1");
  ok(view.getByRole("button", { name: "Save revision" }));
});
test("saving sends the base revision, and a conflict keeps the draft until the person chooses the latest", async () => {
  const doc = { ...validDefinition(), id: "adf-1", revision: 3 };
  gateway(
    ...resources,
    on("GET", "/api/v1/agent-definitions/adf-1", doc),
    on("PUT", "/api/v1/agent-definitions/adf-1", () => json({ error: "conflict", message: "stale", latest: { ...doc, name: "theirs", revision: 4 } }, 409)),
  );
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="adf-1" onNavigate={() => {}} />);
  await view.findByText("Revision 3");
  fill(view, "Name", "mine");
  fireEvent.click(view.getByRole("button", { name: "Save revision" }));
  await view.findByText(/Someone else saved/);
  const put = sent("PUT", "/api/v1/agent-definitions/adf-1")[0];
  assert.deepEqual(Object.keys(put.body).sort(), ["description", "instructions", "limits", "model", "name", "revision", "tools", "workspace"], "the only server field an update sends is its base revision");
  assert.equal(put.body.revision, 3);
  assert.equal((view.getByLabelText("Name") as HTMLInputElement).value, "mine");
  fireEvent.click(view.getByRole("button", { name: /load the latest revision/ }));
  assert.equal((view.getByLabelText("Name") as HTMLInputElement).value, "theirs");
  view.getByText("Revision 4");
});
test("loading a revision fills the editor against the current base and writes nothing", async () => {
  const doc = { ...validDefinition(), id: "adf-1", revision: 2, instructions: "new" };
  gateway(...resources, on("GET", "/api/v1/agent-definitions/adf-1", doc), on("GET", "/api/v1/agent-definitions/adf-1/revisions", { items: [{ revision: 2, saved_at: "2026-11-04T10:00:00Z", content: { ...validDefinition(), instructions: "new" } }, { revision: 1, saved_at: "2026-11-03T10:00:00Z", content: { ...validDefinition(), instructions: "old" } }] }), on("PUT", "/api/v1/agent-definitions/adf-1", (call: Call) => json({ ...call.body, id: "adf-1", revision: 3 })));
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="adf-1" onNavigate={() => {}} />);
  await view.findByText("Revision 2");
  const details = view.getByText("Revision history").closest("details")!;
  details.open = true;
  fireEvent(details, new window.Event("toggle"));
  fireEvent.click(await view.findByRole("button", { name: "Load revision 1" }));
  assert.equal((view.getByLabelText("Instructions") as HTMLTextAreaElement).value, "old");
  assert.equal(calls.filter((call) => call.method !== "GET").length, 0);
  fireEvent.click(view.getByRole("button", { name: "Save revision" }));
  await view.findByText(/revision 3/);
  assert.equal(sent("PUT", "/api/v1/agent-definitions/adf-1")[0].body.revision, 2);
  assert.equal(sent("PUT", "/api/v1/agent-definitions/adf-1")[0].body.instructions, "old");
});
test("every problem in a 422 is shown, not only the first", async () => {
  gateway(...resources, on("POST", "/api/v1/agent-definitions", () => json({ error: "validation_error", message: "first", issues: ["first problem.", "tools[0]: tool x is not available in this project"] }, 422)));
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="new" onNavigate={() => {}} />);
  await view.findByRole("option", { name: "Fraud" });
  fill(view, "Name", "bug-fixer");
  fill(view, "When to use", "Fixes bugs");
  fill(view, "Instructions", "Fix it");
  fireEvent.click(view.getByRole("button", { name: "Create agent" }));
  await view.findByText(/first problem\. tools\[0\]: tool x is not available/);
});
test("viewers get a read-only agent editor with no create, save or delete", async () => {
  const doc = { ...validDefinition(), id: "adf-1", revision: 1 };
  gateway(...resources, on("GET", "/api/v1/agent-definitions", { items: [doc] }), on("GET", "/api/v1/agent-definitions/adf-1", doc));
  const list = render(<AgentDefinitionsView identity={viewer} view="agent-definitions" project="p1" resource="" onNavigate={() => {}} />);
  await list.findByRole("button", { name: "View bug-fixer" });
  assert.equal(list.queryByRole("button", { name: "New agent" }), null);
  assert.equal(list.queryByRole("button", { name: "Delete bug-fixer" }), null);
  list.unmount();
  const form = render(<AgentDefinitionsView identity={viewer} view="agent-definitions" project="p1" resource="adf-1" onNavigate={() => {}} />);
  await form.findByText(/read-only access/);
  assert.equal(form.queryByRole("button", { name: "Save revision" }), null);
  assert.equal((form.getByLabelText("Name") as HTMLInputElement).disabled, true);
});

// ---------- Tool picker

test("the tool picker offers only this project's real resources and saves the documented grant shapes", async () => {
  gateway(...resources, on("POST", "/api/v1/agent-definitions", (call: Call) => json({ ...call.body, id: "adf-1", revision: 1 }, 201)));
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="new" onNavigate={() => {}} />);
  await view.findByRole("checkbox", { name: "lookup" });
  await view.findByRole("checkbox", { name: /Runbooks/ });
  await view.findByRole("checkbox", { name: /Claims/ });
  const crm = await view.findByRole("checkbox", { name: /CRM/ });
  assert.equal(view.queryByRole("checkbox", { name: "other-project-tool" }), null);
  assert.equal(view.queryByRole("checkbox", { name: /Elsewhere/ }), null);
  fireEvent.click(view.getByRole("checkbox", { name: "lookup" }));
  fireEvent.click(crm);
  fireEvent.click(view.getByRole("checkbox", { name: /Runbooks/ }));
  fireEvent.click(view.getByRole("checkbox", { name: /Claims/ }));
  fireEvent.click(view.getByRole("checkbox", { name: "Code sandbox" }));
  fireEvent.click(view.getByRole("button", { name: "Add repository" }));
  fill(view, "Repository URL", "https://github.com/o/r");
  fill(view, "Credential connection", "api-1");
  fill(view, "Name", "bug-fixer");
  fill(view, "When to use", "Fixes bugs");
  fill(view, "Instructions", "Fix it");
  fireEvent.click(view.getByRole("button", { name: "Create agent" }));
  const summary = await view.findByRole("alert");
  assert.match(summary.textContent || "", /need the sandbox workspace/);
  assert.equal(sent("POST", "/api/v1/agent-definitions").length, 0);
  fill(view, "Workspace", "sandbox");
  fireEvent.click(view.getByRole("button", { name: "Create agent" }));
  await view.findByText(/Saved bug-fixer/);
  assert.deepEqual(sent("POST", "/api/v1/agent-definitions")[0].body.tools, [
    { kind: "tool", ref: "lookup" },
    { kind: "connection", ref: "api-1" },
    { kind: "knowledge", ref: "kb-1" },
    { kind: "dataset", ref: "ds-1" },
    { kind: "sandbox" },
    { kind: "git", repository: "https://github.com/o/r", connection: "api-1" },
  ]);
});
test("a saved grant whose resource is gone stays visible and removable, and a failed source says so", async () => {
  const doc = { ...validDefinition(), id: "adf-1", revision: 1, tools: [{ kind: "knowledge", ref: "kb-deleted" }] };
  gateway(
    on("GET", "/api/v1/agent-definitions/adf-1", doc),
    on("GET", "/api/v1/datasets", () => json({ message: "datasets are down" }, 503)),
    ...resources,
  );
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="adf-1" onNavigate={() => {}} />);
  const missing = await view.findByRole("checkbox", { name: /kb-deleted/ });
  view.getByText("Not available in this project");
  await view.findByText(/Datasets could not be loaded: datasets are down/);
  fireEvent.click(missing);
  assert.equal(view.queryByRole("checkbox", { name: /kb-deleted/ }), null);
});
test("the picker lists nothing until a project is chosen, and respects missing catalog access", async () => {
  gateway(...resources);
  const noCatalog = { ...writer, services: ["agents"] };
  const view = render(<AgentDefinitionsView identity={noCatalog} view="agent-definitions" project="" resource="new" onNavigate={() => {}} />);
  assert.equal(calls.some((call) => call.path === "/api/v1/knowledge-bases"), false);
  ok(view.getAllByText("Select a project to list its resources.").length >= 1);
  await view.findByRole("option", { name: "Fraud" });
  fill(view, "Project", "p1");
  await view.findByText(/does not have access to the catalog/);
  assert.equal(calls.some((call) => call.path === "/api/v1/tools"), false);
});
test("budget defaults match the gateway and the editors say budgets count re-sent context", async () => {
  assert.equal(blankDefinition().limits.max_tokens, 1_000_000);
  assert.equal(blankTeam().limits.max_tokens, 5_000_000);
  gateway(...resources, on("GET", "/api/v1/agent-definitions", { items: [] }));
  const agent = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="new" onNavigate={() => {}} />);
  assert.equal((agent.getByLabelText("Maximum tokens") as HTMLInputElement).value, "1000000");
  agent.getByText(/Default 1,000,000/);
  agent.getByText(/count re-sent context/);
  agent.unmount();
  const team = render(<TeamsView identity={writer} view="agent-teams" project="p1" resource="new" onNavigate={() => {}} />);
  assert.equal((team.getByLabelText("Maximum tokens") as HTMLInputElement).value, "5000000");
  team.getByText(/Default 5,000,000/);
  team.getByText(/count re-sent context/);
});
test("the picker describes what registered tools and knowledge bases do now", async () => {
  gateway(...resources);
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="new" onNavigate={() => {}} />);
  view.getByText(/tool_no_function/);
  view.getByText(/search with a text query/);
  assert.doesNotMatch(view.container.textContent || "", /tool_not_executable|vector/i);
});
test("a tool registered in several versions is one choice", async () => {
  gateway(on("GET", "/api/v1/tools", { items: [{ name: "lookup", version: "1" }, { name: "lookup", version: "2" }] }), ...resources);
  const view = render(<AgentDefinitionsView identity={writer} view="agent-definitions" project="p1" resource="new" onNavigate={() => {}} />);
  await view.findByRole("checkbox", { name: "lookup" });
  assert.equal(view.getAllByRole("checkbox", { name: "lookup" }).length, 1);
});
test("duplicate grants are removed from the request", () => {
  const payload = definitionPayload({ ...validDefinition(), workspace: "sandbox", tools: [{ kind: "sandbox" }, { kind: "sandbox" }, { kind: "tool", ref: "a" }, { kind: "tool", ref: "a" }] });
  assert.deepEqual(payload.tools, [{ kind: "sandbox" }, { kind: "tool", ref: "a" }]);
});

// ---------- Teams and templates

function Routed({ Component, identity = writer, initial = "" }: any) {
  const [state, setState] = React.useState({ view: "agent-teams", resource: initial });
  return <div data-location={`${state.view}:${state.resource}`}><Component identity={identity} view={state.view} project="p1" resource={state.resource} onNavigate={(view: string, resource = "") => setState({ view, resource })} /></div>;
}
test("a template reuses existing agents, creates only the missing ones and opens an unsaved team", async () => {
  const existing = { ...validDefinition(), id: "adf-existing", project_id: "p1", name: "bug-fixer" };
  let list = [existing];
  gateway(
    ...resources,
    on("GET", "/api/v1/agent-teams", { items: [] }),
    on("GET", "/api/v1/agent-definitions", () => json({ items: list })),
    on("POST", "/api/v1/agent-definitions", (call: Call) => {
      const created = { ...call.body, id: "adf-new", revision: 1 };
      list = [...list, created];
      return json(created, 201);
    }),
  );
  const view = render(<Routed Component={TeamsView} />);
  fireEvent.click(await view.findByRole("button", { name: "Use template" }));
  await view.findByRole("option", { name: "Fraud" });
  fireEvent.click(view.getByRole("button", { name: "Create agents and open team" }));
  await view.findByText(/Review this team/);
  const posted = sent("POST", "/api/v1/agent-definitions");
  assert.deepEqual(posted.map((call) => call.body.name), ["reviewer"]);
  assert.equal(posted[0].body.workspace, "sandbox");
  assert.deepEqual(posted[0].body.tools, [{ kind: "sandbox" }]);
  assert.deepEqual(posted[0].body.limits, { max_turns: 60, max_tokens: 200000, max_seconds: 1800 });
  assert.equal((view.getByLabelText("Name") as HTMLInputElement).value, "code-change");
  assert.equal(sent("POST", "/api/v1/agent-teams").length, 0);
  assert.equal((await view.findByRole("checkbox", { name: /bug-fixer/ }) as HTMLInputElement).checked, true);
  assert.equal((view.getByRole("checkbox", { name: /reviewer/ }) as HTMLInputElement).checked, true);
  view.getByText(/Not saved yet, unsaved changes/);
});
test("both documented templates exist with their members", async () => {
  const { TEAM_TEMPLATES } = await import("../features/teams/templates");
  assert.deepEqual(
    TEAM_TEMPLATES.map((item) => [item.id, item.team.members]),
    [["code-change", ["bug-fixer", "reviewer"]], ["research", ["researcher", "writer"]]],
  );
  for (const template of TEAM_TEMPLATES)
    for (const agent of template.agents)
      assert.deepEqual(validateDefinition({ ...blankDefinition("p1"), ...agent } as any), {}, agent.name);
  // The embedded copies must stay identical to the contract once the submodule carries them.
  for (const id of ["code-change", "research"]) {
    const upstream = join(import.meta.dirname, `../../contracts/agent-team/templates/${id}.json`);
    if (existsSync(upstream)) assert.equal(readFileSync(join(import.meta.dirname, `../features/teams/templates/${id}.json`), "utf8"), readFileSync(upstream, "utf8"), id);
  }
});
test("starting a run posts the documented body, side effects default off, and a retry reuses the idempotency key", async () => {
  const team = { ...blankTeam("p1"), id: "atm-1", name: "code-change", revision: 1, members: ["adf-1"] };
  let attempts = 0;
  gateway(
    on("GET", "/api/v1/agent-teams", { items: [team] }),
    on("POST", "/api/v1/team-runs", () => (++attempts === 1 ? json({ message: "gateway busy" }, 503) : json({ id: "atr-1", status: "running" }, 201))),
    on("GET", /team-runs\/atr-1/, { items: [] }),
    on("GET", /agent-approvals/, { items: [] }),
  );
  const view = render(<Routed Component={TeamsView} />);
  fireEvent.click(await view.findByRole("button", { name: "Start a run of code-change" }));
  fireEvent.click(view.getByRole("button", { name: "Start run" }));
  await view.findByText("Enter the task for this team.");
  assert.equal(attempts, 0);
  fill(view, "Task", "Fix the login bug");
  fireEvent.click(view.getByRole("button", { name: "Start run" }));
  await view.findByText("gateway busy");
  fireEvent.click(view.getByRole("button", { name: "Start run" }));
  await waitFor(() => assert.equal(view.container.querySelector("[data-location]")?.getAttribute("data-location"), "team-runs:atr-1"));
  const [first, second] = sent("POST", "/api/v1/team-runs").map((call) => call.body);
  assert.equal(first.idempotency_key, second.idempotency_key);
  ok(first.idempotency_key);
  assert.deepEqual({ ...first, idempotency_key: "" }, { team_id: "atm-1", input: "Fix the login bug", allow_side_effects: false, idempotency_key: "" });
});
test("the run list filters by team through the documented query", async () => {
  const team = { ...blankTeam("p1"), id: "atm-1", name: "code-change" };
  gateway(on("GET", "/api/v1/agent-teams", { items: [team] }), on("GET", /^\/api\/v1\/team-runs/, { items: [{ id: "atr-1", team_id: "atm-1", status: "running" }] }));
  const view = render(<TeamRunsView identity={writer} view="team-runs" project="p1" resource="" onNavigate={() => {}} />);
  await view.findByText("atr-1");
  await view.findByRole("option", { name: "code-change" });
  fill(view, "Team", "atm-1");
  await waitFor(() => ok(calls.some((call) => call.path === "/api/v1/team-runs?team_id=atm-1")));
  assert.match(window.location.search, /team=atm-1/);
});

// ---------- Live run

const RUN = "/api/v1/team-runs/atr-1";
const coordinator = { id: "atw-c", role: "coordinator", agent: "coordinator", status: "running", turns: 2, usage: { input_tokens: 100, output_tokens: 20 }, active_millis: 65000, created_at: "2026-11-04T10:00:00Z" };
const fixer = { id: "atw-f", role: "worker", agent: "bug-fixer", status: "queued", turns: 0 };
function liveRun(initial: { run?: any; workers?: any[]; approvals?: any[]; events?: any[]; details?: Record<string, any> } = {}) {
  const state = {
    run: { id: "atr-1", team_id: "atm-1", status: "running", subject: "ada", allow_side_effects: true, resume_count: 0, ...initial.run },
    workers: initial.workers || [coordinator, fixer],
    approvals: initial.approvals || [],
    events: initial.events || [],
    done: undefined as boolean | undefined,
    details: initial.details || {},
  };
  gateway(
    on("GET", RUN, () => json(state.run)),
    on("GET", `${RUN}/workers`, () => json({ items: state.workers })),
    on("GET", /team-runs\/atr-1\/workers\/atw-/, (call: Call) => {
      const id = call.path.split("/").pop()!;
      return json({ ...state.workers.find((worker) => worker.id === id), transcript: [], ...state.details[id] });
    }),
    on("GET", /team-runs\/atr-1\/events\?after=/, (call: Call) => {
      const after = Number(call.path.split("after=")[1]);
      return json({ events: state.events.filter((event) => event.seq > after), done: state.done ?? ["succeeded", "failed", "cancelled"].includes(state.run.status) });
    }),
    on("GET", /agent-approvals\?run_id=atr-1&status=pending/, () => json({ items: state.approvals })),
  );
  return state;
}
const openRun = (identity = writer, props: any = {}) => render(<RunView id="atr-1" identity={identity} onBack={() => {}} {...props} />);
const statusOf = (view: any) => view.container.querySelector("#team-run-status")?.textContent;
const row = (view: any, name: string) => within(view.container.querySelector("#team-workers")).getByText(name).closest("tr")!;
const rowLoaded = (view: any, name: string) =>
  waitFor(() => {
    const table = view.container.querySelector("#team-workers");
    ok(table);
    within(table).getByText(name);
  });
const stream = async () => {
  await waitFor(() => ok(FakeEventSource.last));
  return FakeEventSource.last!;
};

test("the worker table follows stream events through retry, approval and completion", async () => {
  const state = liveRun();
  const view = openRun();
  await rowLoaded(view, "bug-fixer");
  const source = await stream();
  assert.match(source.url, /\/api\/v1\/team-runs\/atr-1\/events\/stream\?after=-1$/);
  assert.match(row(view, "bug-fixer").textContent || "", /queued/);
  assert.match(row(view, "coordinator").textContent || "", /120.*1m 5s/);

  const step = async (seq: number, type: string, worker: any, pattern: RegExp) => {
    state.workers = [coordinator, { ...fixer, ...worker }];
    act(() => source.emit("run", { seq, type, worker_id: "atw-f" }));
    await waitFor(() => assert.match(row(view, "bug-fixer").textContent || "", pattern));
  };
  await step(1, "worker.turn", { status: "running", turns: 1, usage: { total_tokens: 50 } }, /running.*1.*50/);
  await step(2, "worker.retrying", { status: "waiting_retry", next_attempt_at: "2026-11-04T10:05:00Z" }, /waiting retry.*Next attempt/);
  await step(3, "worker.waiting_approval", { status: "waiting_approval" }, /waiting approval/);
  await step(4, "worker.finished", { status: "completed", report: "done" }, /completed/);

  state.run = { ...state.run, status: "succeeded", report: "All items verified." };
  state.workers = [{ ...coordinator, status: "completed" }, { ...fixer, status: "completed" }];
  act(() => source.emit("end"));
  await waitFor(() => assert.match(statusOf(view) || "", /succeeded/));
  await view.findByText("All items verified.");
  assert.equal(source.closed, true);
  assert.equal(view.queryByRole("button", { name: "Cancel run" }), null);
  assert.match(view.container.querySelector("#team-run-connection")?.textContent || "", /finished/);
});
test("a redelivered event is ignored and a malformed one does not break the view", async () => {
  assert.deepEqual(mergeEvents([{ seq: 2, type: "b" }], [{ seq: 1, type: "a" }, { seq: 2, type: "dup" }]).map((event) => event.type), ["a", "b"]);
  liveRun();
  const view = openRun();
  await rowLoaded(view, "bug-fixer");
  const source = await stream();
  act(() => source.emit("run", { seq: 7, type: "worker.turn" }));
  act(() => source.emit("run", { seq: 7, type: "worker.turn" }));
  act(() => {
    for (const listener of source.listeners.run) listener({ data: "{not json" });
  });
  await view.findByText("Event log (1)");
});
test("a stream failure falls back to polling from the last sequence seen", async () => {
  const state = liveRun({ events: [{ seq: 4, type: "run.started" }] });
  const view = openRun(writer, { pollMs: 20 });
  await rowLoaded(view, "bug-fixer");
  const source = await stream();
  assert.match(source.url, /after=4$/);
  act(() => source.onerror!());
  assert.equal(source.closed, true);
  await waitFor(() => assert.match(view.container.querySelector("#team-run-connection")?.textContent || "", /every few seconds/));
  await waitFor(() => ok(calls.some((call) => call.path === `${RUN}/events?after=4`)));
  state.events = [...state.events, { seq: 5, type: "worker.message", message_preview: "polled message", from: "atw-f", to: "atw-c" }];
  await view.findByText("polled message", { selector: ".team-feed .team-text" });
  state.run = { ...state.run, status: "failed" };
  await waitFor(() => assert.match(statusOf(view) || "", /failed/));
  await waitFor(() => assert.match(view.container.querySelector("#team-run-connection")?.textContent || "", /finished/));
});
test("a stream the gateway closes after its time limit reconnects from the last sequence", async () => {
  liveRun();
  const view = openRun();
  await rowLoaded(view, "bug-fixer");
  const source = await stream();
  act(() => source.onopen!());
  act(() => source.emit("run", { seq: 3, type: "worker.turn" }));
  const realNow = Date.now;
  Date.now = () => realNow() + 5 * 60_000;
  try {
    act(() => source.onerror!());
  } finally {
    Date.now = realNow;
  }
  assert.equal(source.closed, true);
  assert.equal(FakeEventSource.opened.length, 2);
  assert.match(FakeEventSource.opened[1], /after=3$/);
});
test("a run that just finished keeps reading until the log is complete, so run.finished is not lost", async () => {
  const state = liveRun();
  const view = openRun();
  await rowLoaded(view, "bug-fixer");
  const source = await stream();
  state.done = false;
  state.run = { ...state.run, status: "cancelled" };
  act(() => source.emit("run", { seq: 1, type: "worker.finished", worker_id: "atw-f", status: "cancelled" }));
  await waitFor(() => assert.match(statusOf(view) || "", /cancelled/));
  assert.equal(source.closed, false, "the stream stays open until the gateway sends end");
  act(() => source.emit("run", { seq: 2, type: "run.finished", status: "cancelled" }));
  await view.findByText("Event log (2)");
  act(() => source.emit("end", { status: "cancelled" }));
  assert.equal(source.closed, true);
});
test("catching up reads every page of a long log", async () => {
  const events = Array.from({ length: 1200 }, (_, seq) => ({ seq: seq + 1, type: "worker.turn" }));
  const state = liveRun({ run: { status: "succeeded" } });
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: any, init: any) => {
    const match = String(input).match(/\/events\?after=(-?\d+)/);
    if (!match) return original(input, init);
    const page = events.filter((event) => event.seq > Number(match[1])).slice(0, 500);
    return json({ events: page, done: page.length < 500 });
  }) as any;
  const view = openRun();
  await view.findByText("Event log (1200)");
  assert.equal(FakeEventSource.opened.length, 0);
  assert.ok(state);
});
test("polling is used when the browser has no EventSource", async () => {
  delete (globalThis as any).EventSource;
  const state = liveRun({ workers: [coordinator] });
  const view = openRun(writer, { pollMs: 20 });
  await rowLoaded(view, "coordinator");
  state.events = [{ seq: 1, type: "worker.message", message_preview: "hello", from: "atw-c", to: "person" }];
  await view.findByText("hello", { selector: ".team-feed .team-text" });
});
test("an older, slower refresh never overwrites a newer one", async () => {
  liveRun();
  let release!: () => void;
  const fetchOld = globalThis.fetch;
  let delayed = 0;
  globalThis.fetch = (async (input: any, init: any) => {
    // The second read of the run is the slow one; it carries the stale status.
    if (String(input) === RUN && ++delayed === 2) {
      await new Promise<void>((resolve) => (release = resolve));
      return json({ id: "atr-1", team_id: "atm-1", status: "cancelling" });
    }
    return fetchOld(input, init);
  }) as any;
  const view = openRun();
  await rowLoaded(view, "bug-fixer");
  const source = await stream();
  act(() => source.emit("run", { seq: 1, type: "worker.turn" }));
  await waitFor(() => ok(release));
  act(() => source.emit("run", { seq: 2, type: "worker.turn" }));
  await waitFor(() => ok(delayed >= 3));
  await act(async () => {
    release();
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
  assert.match(statusOf(view) || "", /running/);
});
test("metrics the gateway did not report read as not reported, never as an estimate", () => {
  assert.equal(tokens(undefined), null);
  assert.equal(tokens({}), null);
  assert.equal(tokens({ input_tokens: 3 }), 3);
  assert.equal(tokens({ total_tokens: 9, input_tokens: 1 }), 9);
  assert.equal(activeTime({ id: "w", agent: "a", status: "running" } as any), null);
  assert.equal(activeTime({ id: "w", agent: "a", status: "running", active_millis: 5000 } as any), "5s");
});
test("the transcript shows tool calls with their paired results as code views, and reloads on new activity", async () => {
  const state = liveRun({
    details: {
      "atw-c": {
        transcript: [
          { role: "user", content: "Fix the bug" },
          { role: "assistant", content: "Running tests", tool_calls: [{ id: "c1", name: "exec", arguments: { command: "npm test" }, status: "completed" }, { id: "c2", name: "read_file", arguments: { path: "a.ts" }, status: "pending" }] },
          { role: "tool", tool_call_id: "c1", content: '{"exit_code":0,"stdout":"[REDACTED] ok"}' },
        ],
      },
    },
  });
  const view = openRun();
  const call = await waitFor(() => {
    const node = view.container.querySelector('[data-tool-call="c1"]');
    ok(node);
    return node as HTMLElement;
  });
  assert.equal(call.querySelector('[data-language="json"]') !== null, true);
  assert.match(call.textContent || "", /npm test/);
  assert.match(call.textContent || "", /Result \(redacted\)/);
  assert.match(call.textContent || "", /\[REDACTED\] ok/);
  assert.match(view.container.querySelector('[data-tool-call="c2"]')?.textContent || "", /No result yet/);
  const source = await stream();
  state.details["atw-c"].transcript.push({ role: "assistant", content: "All green" });
  act(() => source.emit("run", { seq: 1, type: "worker.turn", worker_id: "atw-c" }));
  await view.findByText("All green");
  fireEvent.click(view.getByRole("button", { name: "Show transcript of bug-fixer" }));
  await view.findByText("No messages yet.", { selector: ".team-transcript p" });
  assert.match(window.location.search, /worker=atw-f/);
  ok(calls.some((item) => item.path === `${RUN}/workers/atw-f`));
});

// ---------- Approvals

const approval = { id: "apr-1", run_id: "atr-1", message: "Call lookup with id 7", status: "pending", created_at: "2026-11-04T10:00:00Z" };
const waiting = { seq: 8, type: "worker.waiting_approval", worker_id: "atw-f", tool: "lookup", tool_call_id: "c3", approval_id: "apr-1", status: "waiting_approval", input_preview: '{"id":7}' };
test("approving posts the decision to the existing approvals API, clears the card and keeps focus on the section", async () => {
  const state = liveRun({ approvals: [approval], events: [waiting] });
  const original = globalThis.fetch;
  const decisions: any[] = [];
  globalThis.fetch = (async (input: any, init: any) => {
    if (String(input) === "/api/v1/agent-approvals/apr-1/decision") {
      decisions.push(JSON.parse(init.body));
      state.approvals = [];
      return json({ ...approval, status: "approved" });
    }
    return original(input, init);
  }) as any;
  const view = openRun();
  await view.findByText("Call lookup with id 7");
  await view.findByRole("button", { name: "Approve lookup apr-1" });
  await waitFor(() => assert.match(view.container.querySelector("[data-approval]")?.textContent || "", /Worker bug-fixer\. Tool lookup\./));
  fireEvent.change(view.getByLabelText("Comment"), { target: { value: "looks fine" } });
  fireEvent.click(view.getByRole("button", { name: "Approve lookup apr-1" }));
  await view.findByText("Nothing is waiting for a decision.");
  assert.deepEqual(decisions, [{ decision: "approve", comment: "looks fine" }]);
  assert.equal(document.activeElement, view.getByRole("heading", { name: /Pending approvals \(0\)/ }));
  ok(calls.some((call) => call.path === "/api/v1/agent-approvals?run_id=atr-1&status=pending"));
});
test("rejecting posts reject, and a request someone else decided is explained and refreshed", async () => {
  const state = liveRun({ approvals: [approval], events: [waiting] });
  const original = globalThis.fetch;
  const decisions: any[] = [];
  globalThis.fetch = (async (input: any, init: any) => {
    if (String(input).endsWith("/decision")) {
      decisions.push(JSON.parse(init.body).decision);
      return json({ error: "approval_closed", message: "closed" }, 409);
    }
    return original(input, init);
  }) as any;
  const view = openRun();
  fireEvent.click(await view.findByRole("button", { name: "Reject lookup apr-1" }));
  await view.findByText(/already decided this request/);
  assert.deepEqual(decisions, ["reject"]);
  assert.equal((view.getByRole("button", { name: "Approve lookup apr-1" }) as HTMLButtonElement).disabled, false);
  ok(state);
});
test("without agents write an approval is read-only with an explanation, even for the run's starter", async () => {
  liveRun({ approvals: [approval], run: { subject: "grace" } });
  const view = openRun(viewer);
  await view.findByText("Call lookup with id 7");
  view.getByText(/needs agents write access/);
  assert.equal(view.queryByRole("button", { name: /Approve/ }), null);
  assert.equal(view.queryByRole("button", { name: /Reject/ }), null);
  assert.equal(view.queryByLabelText("Comment"), null);
});

// ---------- Messages, cancel, resume

test("after sending, the feed shows the message at once even if the stream delivers nothing", async () => {
  const state = liveRun();
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: any, init: any) => {
    if (String(input) === `${RUN}/messages`) {
      const body = JSON.parse(init.body);
      state.events = [...state.events, { seq: 1, type: "worker.message", from: "person", to: body.to, message_preview: body.text }];
      return json({ to: body.to, id: "msg-1" }, 202);
    }
    return original(input, init);
  }) as any;
  const view = openRun();
  await view.findByRole("option", { name: /bug-fixer/ });
  fireEvent.change(view.getByLabelText("Message"), { target: { value: "Check logout" } });
  fireEvent.click(view.getByRole("button", { name: "Send message" }));
  await view.findByText("Check logout", { selector: ".team-feed .team-text" });
});
test("sending a message posts {to, text} with the worker id, and clears the box only after success", async () => {
  liveRun();
  const original = globalThis.fetch;
  const bodies: any[] = [];
  let fail = true;
  globalThis.fetch = (async (input: any, init: any) => {
    if (String(input) === `${RUN}/messages`) {
      bodies.push(JSON.parse(init.body));
      return fail ? json({ message: "gateway busy" }, 503) : json({}, 201);
    }
    return original(input, init);
  }) as any;
  const view = openRun();
  await view.findByRole("option", { name: /bug-fixer/ });
  const box = view.getByLabelText("Message") as HTMLTextAreaElement;
  fireEvent.click(view.getByRole("button", { name: "Send message" }));
  await view.findByText("Enter a message.");
  assert.equal(bodies.length, 0);
  fireEvent.change(view.getByLabelText("To"), { target: { value: "atw-f" } });
  fireEvent.change(box, { target: { value: "Please also check the logout path" } });
  fireEvent.click(view.getByRole("button", { name: "Send message" }));
  await view.findByText("gateway busy");
  assert.equal(box.value, "Please also check the logout path");
  fail = false;
  fireEvent.click(view.getByRole("button", { name: "Send message" }));
  await view.findByText("Message sent.");
  assert.equal(box.value, "");
  assert.deepEqual(bodies[1], { to: "atw-f", text: "Please also check the logout path" });
});
test("a person cannot message failed or cancelled workers, or any worker of a finished run", async () => {
  liveRun({ workers: [coordinator, { ...fixer, status: "failed" }, { ...fixer, id: "atw-x", agent: "reviewer", status: "cancelled" }, { ...fixer, id: "atw-d", agent: "writer", status: "completed" }] });
  const running = openRun();
  await running.findByRole("option", { name: /writer \(completed, resumes it\)/ });
  const to = running.getByLabelText("To") as HTMLSelectElement;
  assert.deepEqual([...to.options].map((option) => option.value), ["atw-c", "atw-d"]);
  const { messageTargets } = await import("../features/teams/runState");
  assert.deepEqual(messageTargets([{ ...coordinator, status: "completed" } as any]), [], "a finished coordinator cannot be reopened");
  running.unmount();
  liveRun({ run: { status: "succeeded" } });
  const finished = openRun();
  await finished.findByText("This run is not accepting messages.");
  assert.equal((finished.getByRole("button", { name: "Send message" }) as HTMLButtonElement).disabled, true);
});
test("cancel asks first, posts a JSON body, then shows cancelling with no second cancel", async () => {
  const state = liveRun();
  const original = globalThis.fetch;
  const posted: any[] = [];
  globalThis.fetch = (async (input: any, init: any) => {
    if (String(input) === `${RUN}/cancel`) {
      posted.push(init);
      state.run = { ...state.run, status: "cancelling" };
      return json(state.run);
    }
    return original(input, init);
  }) as any;
  const view = openRun();
  fireEvent.click(await view.findByRole("button", { name: "Cancel run" }));
  assert.equal(posted.length, 0);
  const dialog = view.getByRole("dialog", { hidden: true });
  fireEvent.click(within(dialog).getByRole("button", { name: "Keep", hidden: true }));
  assert.equal(posted.length, 0);
  fireEvent.click(view.getByRole("button", { name: "Cancel run" }));
  fireEvent.click(within(view.getByRole("dialog", { hidden: true })).getByRole("button", { name: "Cancel run", hidden: true }));
  await waitFor(() => assert.match(statusOf(view) || "", /cancelling/));
  assert.equal(posted[0].method, "POST");
  assert.equal(posted[0].body, "{}");
  assert.equal(posted[0].headers["Content-Type"], "application/json");
  assert.equal(view.queryByRole("button", { name: "Cancel run" }), null);
  assert.equal(document.activeElement, view.container.querySelector("#team-run-status"));
});
test("a refused cancel stays in the dialog with the gateway's reason", async () => {
  liveRun();
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: any, init: any) => (String(input) === `${RUN}/cancel` ? json({ message: "already finished" }, 409) : original(input, init))) as any;
  const view = openRun();
  fireEvent.click(await view.findByRole("button", { name: "Cancel run" }));
  fireEvent.click(within(view.getByRole("dialog", { hidden: true })).getByRole("button", { name: "Cancel run", hidden: true }));
  await within(view.getByRole("dialog", { hidden: true })).findByText("already finished");
});
test("resume appears only with failed workers, and reconnects the live stream when the run comes back", async () => {
  const state = liveRun({ run: { status: "failed", error: "owner_access_lost", error_message: "Access was restored" }, workers: [coordinator, { ...fixer, status: "failed", last_error: "provider_unavailable", last_error_message: "30 minutes of failure", last_error_resumable: true }] });
  const original = globalThis.fetch;
  let resumes = 0;
  globalThis.fetch = (async (input: any, init: any) => {
    if (String(input) === `${RUN}/resume`) {
      resumes++;
      state.run = { ...state.run, status: "running", resume_count: 1, error: undefined, error_message: undefined };
      state.workers = [coordinator, { ...fixer, status: "queued", last_error: undefined, last_error_message: undefined }];
      return json(state.run);
    }
    return original(input, init);
  }) as any;
  const view = openRun();
  await view.findAllByText(/provider_unavailable: 30 minutes of failure/);
  view.getByText(/owner_access_lost: Access was restored/);
  assert.equal(FakeEventSource.opened.length, 0, "a finished run opens no stream");
  assert.equal(view.queryByRole("button", { name: "Cancel run" }), null);
  fireEvent.click(view.getByRole("button", { name: "Resume failed workers" }));
  fireEvent.click(within(view.getByRole("dialog", { hidden: true })).getByRole("button", { name: "Resume", hidden: true }));
  await waitFor(() => assert.match(statusOf(view) || "", /running/));
  assert.equal(resumes, 1);
  await waitFor(() => assert.equal(FakeEventSource.opened.length, 1));
  assert.equal(view.queryByRole("button", { name: "Resume failed workers" }), null);
  view.getByRole("button", { name: "Cancel run" });
});
test("resume follows the gateway's rule: budgets stay spent and only resumable workers come back", () => {
  const run: any = { id: "a", team_id: "t", status: "running" };
  const failed: any = { id: "w", agent: "a", status: "failed" };
  assert.equal(canResume({ ...run, status: "failed" }, []), true);
  assert.equal(canResume({ ...run, status: "failed", error: "budget_exceeded" }, []), false);
  assert.equal(canResume(run, [failed]), false, "a worker stopped by its own budget is not resumable");
  assert.equal(canResume(run, [{ ...failed, last_error_resumable: true }]), true);
  assert.equal(canResume({ ...run, status: "cancelling" }, [{ ...failed, last_error_resumable: true }]), false);
});
test("cancelled and succeeded runs offer neither cancel nor resume, and viewers get no actions at all", async () => {
  for (const status of ["cancelled", "succeeded"]) {
    liveRun({ run: { status }, workers: [{ ...fixer, status: "failed" }] });
    const view = openRun();
    await rowLoaded(view, "bug-fixer");
    assert.equal(view.queryByRole("button", { name: "Resume failed workers" }), null, status);
    assert.equal(view.queryByRole("button", { name: "Cancel run" }), null, status);
    view.unmount();
  }
  liveRun({ workers: [{ ...fixer, status: "failed", last_error_resumable: true }] });
  const read = openRun(viewer);
  await rowLoaded(read, "bug-fixer");
  assert.equal(read.queryByRole("button", { name: "Cancel run" }), null);
  assert.equal(read.queryByRole("button", { name: "Resume failed workers" }), null);
  assert.equal(read.queryByRole("button", { name: "Send message" }), null);
});

// ---------- Sandbox banner

test("the unavailable banner appears from any of the gateway's three signals, and non-sandbox runs have none", async () => {
  const run: any = { id: "atr-1", team_id: "t", status: "running" };
  assert.equal(sandboxNotice(run, [coordinator as any], []), null);
  assert.equal(sandboxNotice({ ...run, sandbox: { available: true, runtime: "runsc", runtime_verified: true } }, [], []), null);
  assert.equal(sandboxNotice({ ...run, sandbox: { available: false } }, [], [])?.kind, "unavailable");
  assert.equal(sandboxNotice(run, [{ ...fixer, last_error: "sandbox_unavailable", last_error_message: "no runtime" } as any], [])?.kind, "unavailable");
  assert.equal(sandboxNotice(run, [], [{ seq: 1, type: "worker.tool_result", error: { code: "sandbox_unavailable", message: "no runtime" } }])?.kind, "unavailable");
  assert.equal(sandboxNotice(run, [], [{ seq: 1, type: "worker.tool_result", output_preview: '{"error":"sandbox_unavailable"}' }])?.kind, "unavailable");
  assert.equal(sandboxNotice(run, [], [{ seq: 1, type: "worker.message", message_preview: "the word sandbox_unavailable in chat" }]), null, "an agent's own words cannot raise the banner");
  assert.equal(sandboxNotice(run, [], [{ seq: 1, type: "worker.tool_result", output_preview: "grep found sandbox_unavailable in a log" }]), null, "command output cannot raise the banner");

  assert.doesNotMatch(sandboxNotice({ ...run, sandbox: { available: false, reason: "sandbox_unavailable" } }, [], [])?.detail || "", /sandbox_unavailable/, "a bare code is not shown as prose");
  const outage = { seq: 1, type: "worker.tool_result", error: { code: "sandbox_unavailable", message: "down" } };
  assert.equal(sandboxNotice(run, [], [outage, { seq: 2, type: "run.resumed", resume_count: 1 }]), null, "a resume clears an old outage");
  assert.equal(sandboxNotice(run, [], [outage, { seq: 2, type: "run.resumed", resume_count: 1 }, { ...outage, seq: 3 }])?.kind, "unavailable", "a new outage after the resume counts");
  liveRun({ run: { sandbox: { available: false, runtime: "runsc", runtime_verified: false, driver: "docker", unsafe_runtime: false, reason: "The runsc runtime is not registered." } } });
  const view = openRun();
  const banner = await view.findByRole("alert");
  assert.equal(banner.id, "team-sandbox-banner");
  assert.match(banner.textContent || "", /Sandbox unavailable/);
  assert.match(banner.textContent || "", /The runsc runtime is not registered/);
  view.unmount();

  liveRun();
  const clean = openRun();
  await rowLoaded(clean, "bug-fixer");
  assert.equal(clean.container.querySelector("#team-sandbox-banner"), null);
});
test("the unsafe-runtime banner is distinct and unavailable outranks it", async () => {
  liveRun({ run: { sandbox: { available: true, runtime: "runc", runtime_verified: false, unsafe_runtime: true } } });
  const view = openRun();
  await rowLoaded(view, "bug-fixer");
  const banner = view.container.querySelector("#team-sandbox-banner")!;
  assert.equal(banner.getAttribute("data-kind"), "unsafe");
  assert.match(banner.textContent || "", /runc runtime/);
  assert.equal(sandboxNotice({ id: "a", team_id: "t", status: "running", sandbox: { available: false, unsafe_runtime: true } }, [], [])?.kind, "unavailable");
});

// ---------- Untrusted output

test("agent output renders as text on every surface and never as markup", async () => {
  const attack = '<img src=x onerror="window.__owned=1"><script>window.__owned=1</script><a href="javascript:alert(1)">click</a>';
  liveRun({
    run: { status: "failed", report: `report ${attack}`, error: "x", error_message: `run error ${attack}` },
    workers: [{ ...coordinator, status: "failed", agent: `agent ${attack}`, last_error: "x", last_error_message: attack }],
    approvals: [{ ...approval, tool_name: attack, message: `approval ${attack}` }],
    events: [{ seq: 1, type: "worker.message", message_preview: `feed ${attack}`, from: attack, to: attack }, { seq: 2, type: "worker.tool_result", output_preview: `event ${attack}` }, { seq: 3, type: "worker.waiting_approval", approval_id: "apr-1", input_preview: attack }],
    details: {
      "atw-c": {
        task: `task ${attack}`,
        report: `worker report ${attack}`,
        transcript: [
          { role: "assistant", content: `assistant ${attack}`, tool_calls: [{ id: "c1", name: attack, arguments: { command: attack }, status: attack }] },
          { role: "tool", tool_call_id: "c1", content: `result ${attack}` },
          { role: "tool", tool_call_id: "c2", content: JSON.stringify({ stdout: attack }) },
        ],
      },
    },
  });
  const view = openRun();
  await waitFor(() => assert.match(view.container.textContent || "", /result <img src=x/));
  const text = view.container.textContent || "";
  for (const label of ["report <img", "run error <img", "approval <img", "feed <img", "assistant <img", "task <img", "worker report <img"]) ok(text.includes(label), label);
  const root = view.container as HTMLElement;
  assert.equal(root.querySelectorAll("img, script, iframe, object, embed").length, 0);
  assert.equal(root.querySelector('a[href^="javascript:"]'), null);
  assert.equal((globalThis as any).window.__owned, undefined);
});
test("no file in the feature uses a raw markup escape hatch", () => {
  const dir = join(import.meta.dirname, "../features/teams");
  for (const file of readdirSync(dir).filter((name) => statSync(join(dir, name)).isFile())) {
    const source = readFileSync(join(dir, file), "utf8");
    assert.doesNotMatch(source, /dangerouslySetInnerHTML|innerHTML|insertAdjacentHTML|document\.write|\beval\(/, file);
  }
});

// ---------- Navigation

test("each new console page has a route and its own icon", () => {
  const glyph = (name: string) => render(<Icon name={name} />).container.querySelector("svg")?.getAttribute("data-testid");
  const views = ["agent-definitions", "agent-teams", "team-runs"];
  const icons = views.map(glyph);
  ok(icons.every(Boolean), String(icons));
  assert.equal(new Set([...icons, glyph("agent-designer"), glyph("langflow")]).size, 5);
  for (const view of views) {
    const page = join(import.meta.dirname, `../app/(app)/console/${view}/page.tsx`);
    ok(existsSync(page) && statSync(page).isFile(), view);
    assert.match(readFileSync(page, "utf8"), new RegExp(`view="${view}"`));
  }
  const shell = readFileSync(join(import.meta.dirname, "../components/pages/ConsolePage.tsx"), "utf8");
  for (const view of views) assert.match(shell, new RegExp(`\\["${view}", "`));
});
