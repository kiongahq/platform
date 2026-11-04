#!/usr/bin/env node
// Development-only stand-in for the gateway's Agent Teams routes, so the console can be seen
// and browser-tested with no stack. It serves canned data and never runs anything.
//   node frontend/tests/fixtures/teams-mock-gateway.mjs 18917
//   KIONGA_GATEWAY_URL=http://127.0.0.1:18917 npm run dev --prefix frontend -- --port 3417
// The task text picks a scenario: "sandbox-down", "unsafe-runtime", "provider-outage", "attack".
import { randomBytes } from "node:crypto";
import { createServer } from "node:http";

const port = Number(process.argv[2] || 18917);
const hex = () => randomBytes(12).toString("hex");
const now = () => new Date().toISOString();
const projects = [{ id: "fraud", name: "Fraud detection" }, { id: "support", name: "Support" }];
const resources = {
  "/api/v1/tools": [{ name: "lookup-customer" }, { name: "issue-refund" }],
  "/api/v1/agent-api-connections": [{ id: `api-${hex()}`, name: "CRM", project_id: "fraud" }, { id: `api-${hex()}`, name: "GitHub", project_id: "fraud" }],
  "/api/v1/knowledge-bases": [{ id: `kb-${hex()}`, name: "Runbooks", project_id: "fraud" }],
  "/api/v1/datasets": [{ id: `ds-${hex()}`, name: "Claims 2026", project_id: "fraud" }],
};
const limits = { max_turns: 100, max_tokens: 200000, max_seconds: 3600 };
const definitions = new Map(), teams = new Map(), runs = new Map(), approvals = new Map(), history = new Map();
const save = (store, doc) => {
  store.set(doc.id, doc);
  const { id, project_id, revision, owner_subject, created_at, updated_at, ...content } = doc;
  history.set(id, [...(history.get(id) || []), { revision, saved_at: updated_at, content: structuredClone(content) }].slice(-20));
  return doc;
};
const fixer = save(definitions, { id: `adf-${hex()}`, project_id: "fraud", revision: 2, name: "bug-fixer", owner_subject: "admin", description: "Use for one defect: reproduces it, fixes the cause and adds a test.", instructions: "Reproduce the problem, then fix it.", model: "llm", tools: [{ kind: "sandbox" }, { kind: "tool", ref: "lookup-customer" }], workspace: "sandbox", limits, updated_at: now() });
const reviewer = save(definitions, { id: `adf-${hex()}`, project_id: "fraud", revision: 1, name: "reviewer", description: "Use after a change exists: reviews a diff independently.", instructions: "Review the diff.", model: "llm", tools: [{ kind: "sandbox" }], workspace: "sandbox", limits, updated_at: now() });
const seeded = save(teams, { id: `atm-${hex()}`, project_id: "fraud", revision: 1, name: "code-change", description: "Makes a code change and reviews it.", coordinator: { instructions: "Split the work and check each report.", model: "llm" }, members: [fixer.id, reviewer.id], brief: "State what you did not verify.", limits: { max_workers_total: 20, max_concurrent_workers: 5, max_tokens: 2000000, max_seconds: 14400 }, updated_at: now() });

const ATTACK = '<img src=x onerror="window.__owned=1"><script>window.__owned=1</script>';
function emit(run, event) {
  const entry = { seq: run.log.length + 1, run_id: run.doc.id, ts: now(), ...event };
  run.log.push(entry);
  for (const client of run.clients) client.write(`id: ${entry.seq}\nevent: run\ndata: ${JSON.stringify(entry)}\n\n`);
}
function finish(run, status, report) {
  run.doc.status = status;
  run.doc.finished_at = now();
  if (report) run.doc.report = report;
  emit(run, { type: "run.finished", status });
  for (const client of run.clients) {
    client.write("event: end\ndata: {}\n\n");
    client.end();
  }
  run.clients.clear();
}
function setWorker(run, worker, patch, usage = 0) {
  Object.assign(worker, patch, { updated_at: now() });
  if (usage) {
    worker.active_millis += usage * 3;
    worker.usage = { input_tokens: worker.usage.input_tokens + usage, output_tokens: worker.usage.output_tokens + Math.round(usage / 4) };
    run.doc.usage = { input_tokens: run.doc.usage.input_tokens + usage, output_tokens: run.doc.usage.output_tokens + Math.round(usage / 4) };
  }
}
function later(run, ms, step) {
  const timer = setTimeout(() => {
    if (!["cancelling", "cancelled"].includes(run.doc.status)) step();
  }, ms);
  run.timers.push(timer);
}
function addWorker(run, role, agent, task) {
  const worker = { id: `atw-${hex()}`, run_id: run.doc.id, role, agent, task, status: "queued", turns: 0, active_millis: 0, usage: { input_tokens: 0, output_tokens: 0 }, transcript: [{ role: "user", content: task }], created_at: now(), updated_at: now() };
  run.workers.push(worker);
  emit(run, { type: "worker.spawned", worker_id: worker.id, agent, role, status: "queued" });
  return worker;
}
function completeFixer(run, coordinator, worker) {
  worker.transcript.push({ role: "assistant", content: "The parser now rejects empty input, and the new test passes." });
  setWorker(run, worker, { status: "completed", report: "Changed parser.ts. npm test exit 0. Not verified: the Windows path handling." }, 400);
  emit(run, { type: "worker.finished", worker_id: worker.id, agent: worker.agent, status: "completed" });
  emit(run, { type: "worker.message", from: worker.id, to: coordinator.id, message_preview: worker.report });
  later(run, 700, () => {
    setWorker(run, coordinator, { status: "completed", turns: coordinator.turns + 1, report: "Item 1: fixed. Evidence: read the diff, npm test exit 0.\nNot verified: Windows path handling.\nFollow-up: add a Windows CI job." }, 300);
    finish(run, "succeeded", coordinator.report);
  });
}
function script(run) {
  const { input, allow_side_effects: sideEffects } = run.doc;
  const coordinator = addWorker(run, "coordinator", "coordinator", input);
  coordinator.transcript = [{ role: "user", content: input.includes("attack") ? `${input} ${ATTACK}` : input }];
  if (input.includes("sandbox-down")) run.doc.sandbox = { available: false, runtime: "runsc", runtime_verified: false, driver: "docker", unsafe_runtime: false, reason: "The runsc runtime is not registered." };
  else if (input.includes("unsafe-runtime")) run.doc.sandbox = { available: true, runtime: "runc", runtime_verified: false, driver: "docker", unsafe_runtime: true };
  else run.doc.sandbox = { available: true, runtime: "runsc", runtime_verified: true, driver: "docker", unsafe_runtime: false };
  later(run, 300, () => {
    setWorker(run, coordinator, { status: "running", turns: 1 }, 200);
    coordinator.transcript.push({ role: "assistant", content: "I will start one bug-fixer.", tool_calls: [{ id: "c1", name: "spawn_worker", arguments: { agent: "bug-fixer" }, status: "completed" }] });
    const worker = addWorker(run, "worker", "bug-fixer", "Fix the parser and add a test.");
    coordinator.transcript.push({ role: "tool", tool_call_id: "c1", content: JSON.stringify({ worker_id: worker.id }) });
    setWorker(run, coordinator, { status: "idle" });
    emit(run, { type: "worker.turn", worker_id: coordinator.id, turn: 1, status: "idle", usage: { input_tokens: 200, output_tokens: 50 } });
    later(run, 300, () => {
      setWorker(run, worker, { status: "running", turns: 1 }, 500);
      const command = "npm test";
      worker.transcript.push({ role: "assistant", content: input.includes("attack") ? ATTACK : "Running the tests first.", tool_calls: [{ id: "c2", name: "exec", arguments: { command }, status: "completed" }] });
      emit(run, { type: "worker.tool_call", worker_id: worker.id, tool: "exec", tool_call_id: "c2", input_preview: JSON.stringify({ command }) });
      if (input.includes("sandbox-down")) {
        worker.transcript.push({ role: "tool", tool_call_id: "c2", content: JSON.stringify({ error: "sandbox_unavailable", message: "The runsc runtime is not registered." }) });
        setWorker(run, worker, { status: "failed", last_error: "sandbox_unavailable", last_error_message: "The sandbox could not start.", last_error_resumable: true });
        emit(run, { type: "worker.tool_result", worker_id: worker.id, tool: "exec", tool_call_id: "c2", error: { code: "sandbox_unavailable", message: "The sandbox could not start." } });
        emit(run, { type: "worker.finished", worker_id: worker.id, agent: worker.agent, status: "failed", error: { code: "sandbox_unavailable", message: "The sandbox could not start." } });
        return;
      }
      worker.transcript.push({ role: "tool", tool_call_id: "c2", content: JSON.stringify({ exit_code: 0, stdout: `12 passing\ntoken=[REDACTED]${input.includes("attack") ? ` ${ATTACK}` : ""}`, stderr: "", truncated: false, timed_out: false, duration_ms: 1840 }) });
      emit(run, { type: "worker.tool_result", worker_id: worker.id, tool: "exec", tool_call_id: "c2", duration_ms: 1840, output_preview: "12 passing" });
      if (input.includes("provider-outage")) {
        setWorker(run, worker, { status: "waiting_retry", next_attempt_at: new Date(Date.now() + 60000).toISOString() });
        emit(run, { type: "worker.retrying", worker_id: worker.id, next_attempt_at: worker.next_attempt_at, retry_after_seconds: 60, attempt: 1, error: { code: "rate_limited", message: "The provider asked to retry." } });
        return;
      }
      later(run, 300, () => {
        if (!sideEffects) return completeFixer(run, coordinator, worker);
        const id = `apr-${hex()}`;
        approvals.set(id, { id, project_id: run.doc.project_id, run_id: run.doc.id, node_id: "c3", worker_id: worker.id, tool_name: "git_push", message: input.includes("attack") ? `Push ${ATTACK}` : "Push branch kionga/run/fix-parser to github.com/org/repo", source: "team_run", requested_by: run.doc.subject, status: "pending", created_at: now() });
        run.pending = { id, worker, coordinator };
        worker.transcript.push({ role: "assistant", content: "Pushing the fix.", tool_calls: [{ id: "c3", name: "git_push", arguments: { branch: "fix-parser" }, status: "pending" }] });
        setWorker(run, worker, { status: "waiting_approval" });
        emit(run, { type: "worker.waiting_approval", worker_id: worker.id, tool: "git_push", tool_call_id: "c3", approval_id: id, status: "waiting_approval", input_preview: JSON.stringify({ branch: "fix-parser" }) });
      });
    });
  });
}
const reply = (response, status, body, headers = {}) => {
  response.writeHead(status, { "Content-Type": "application/json", ...headers });
  response.end(body === undefined ? "" : JSON.stringify(body));
};
const read = (request) => new Promise((resolve) => {
  let raw = "";
  request.on("data", (chunk) => (raw += chunk));
  request.on("end", () => resolve(raw));
});
function crud(store, base, prefix, request, response, path, body) {
  if (path === base && request.method === "GET") return reply(response, 200, { items: [...store.values()] });
  if (path === base && request.method === "POST") {
    if ([...store.values()].some((item) => item.project_id === body.project_id && item.name === body.name)) return reply(response, 409, { error: "already_exists", message: "This name already exists in the project" });
    return reply(response, 201, save(store, { ...body, id: `${prefix}-${hex()}`, revision: 1, updated_at: now() }));
  }
  const match = path.slice(base.length + 1).split("/");
  const doc = store.get(match[0]);
  if (!doc) return reply(response, 404, { error: "not_found", message: "Not found." });
  if (match[1] === "revisions") return reply(response, 200, { items: [...history.get(doc.id)].reverse() });
  if (request.method === "PUT" && body.revision !== doc.revision) return reply(response, 409, { error: "revision_conflict", message: "Changed elsewhere; reload before saving", latest: doc });
  if (request.method === "GET") return reply(response, 200, doc);
  if (request.method === "DELETE") {
    store.delete(doc.id);
    return reply(response, 204);
  }
  if (request.method === "PUT") return reply(response, 200, save(store, { ...doc, ...body, revision: doc.revision + 1, updated_at: now() }));
  return reply(response, 405, {});
}
function runRoutes(request, response, path, query, body) {
  if (path === "/api/v1/team-runs" && request.method === "GET") {
    const team = query.get("team_id");
    return reply(response, 200, { items: [...runs.values()].map((run) => run.doc).filter((doc) => !team || doc.team_id === team).reverse() });
  }
  if (path === "/api/v1/team-runs" && request.method === "POST") {
    const team = teams.get(body.team_id);
    if (!team) return reply(response, 422, { error: "validation_error", message: "Unknown team." });
    const existing = [...runs.values()].find((run) => body.idempotency_key && run.key === body.idempotency_key);
    if (existing) return reply(response, 200, existing.doc);
    const run = { key: body.idempotency_key, log: [], workers: [], clients: new Set(), timers: [], doc: { id: `atr-${hex()}`, project_id: team.project_id, team_id: team.id, snapshot: { team_id: team.id, team_revision: team.revision, team: { name: team.name } }, status: "running", input: body.input, allow_side_effects: !!body.allow_side_effects, subject: "admin", roles: ["admin"], usage: { input_tokens: 0, output_tokens: 0 }, resume_count: 0, content_sha256: hex() + hex(), created_at: now() } };
    runs.set(run.doc.id, run);
    emit(run, { type: "run.started", status: "running" });
    script(run);
    return reply(response, 202, run.doc);
  }
  const [id, section, child] = path.slice("/api/v1/team-runs/".length).split("/");
  const run = runs.get(id);
  if (!run) return reply(response, 404, { error: "not_found", message: "Run not found." });
  const done = ["succeeded", "failed", "cancelled"].includes(run.doc.status);
  if (!section) return reply(response, 200, run.doc);
  if (section === "workers" && !child) return reply(response, 200, { items: run.workers.map(({ transcript, ...worker }) => worker) });
  if (section === "workers") {
    const worker = run.workers.find((item) => item.id === child);
    return worker ? reply(response, 200, worker) : reply(response, 404, { error: "not_found", message: "Worker not found." });
  }
  if (section === "events" && child === "stream") {
    response.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
    response.flushHeaders();
    const after = Number(query.get("after") ?? -1);
    for (const event of run.log) if (event.seq > after) response.write(`id: ${event.seq}\nevent: run\ndata: ${JSON.stringify(event)}\n\n`);
    if (done) {
      response.write("event: end\ndata: {}\n\n");
      return response.end();
    }
    run.clients.add(response);
    response.on("close", () => run.clients.delete(response));
    return;
  }
  if (section === "events") {
    const after = Number(query.get("after") ?? -1);
    return reply(response, 200, { events: run.log.filter((event) => event.seq > after), done });
  }
  if (section === "messages") {
    const worker = run.workers.find((item) => item.id === body.to);
    if (done || !worker) return reply(response, 409, { error: "conflict", message: "This run is not accepting messages." });
    worker.transcript.push({ role: "user", content: body.text });
    emit(run, { type: "worker.message", from: "person", to: worker.id, message_preview: String(body.text).slice(0, 2000) });
    return reply(response, 202, { to: worker.id, id: `msg-${hex()}` });
  }
  if (section === "cancel") {
    run.doc.status = "cancelling";
    for (const timer of run.timers) clearTimeout(timer);
    setTimeout(() => {
      for (const worker of run.workers) if (!["completed", "failed"].includes(worker.status)) worker.status = "cancelled";
      finish(run, "cancelled");
    }, 400);
    return reply(response, 202, run.doc);
  }
  if (section === "resume") {
    run.doc.status = "running";
    run.doc.resume_count += 1;
    run.doc.sandbox = { available: true, runtime: "runsc", runtime_verified: true, driver: "docker", unsafe_runtime: false };
    emit(run, { type: "run.resumed", status: "running", resume_count: run.doc.resume_count });
    const worker = run.workers.find((item) => item.status === "failed");
    const coordinator = run.workers.find((item) => item.role === "coordinator");
    if (worker) {
      setWorker(run, worker, { status: "running", last_error: undefined, last_error_message: undefined, last_error_resumable: undefined });
      later(run, 500, () => completeFixer(run, coordinator, worker));
    }
    return reply(response, 200, run.doc);
  }
  return reply(response, 404, { error: "not_found", message: "Not found." });
}
function decide(id, body, response) {
  const approval = approvals.get(id);
  if (!approval) return reply(response, 404, { error: "not_found", message: "approval not found" });
  if (approval.status !== "pending") return reply(response, 409, { error: "approval_closed", message: "This request was already decided or has expired." });
  approval.status = body.decision === "approve" ? "approved" : "rejected";
  const run = runs.get(approval.run_id);
  const { worker, coordinator } = run.pending;
  const call = worker.transcript.at(-1).tool_calls[0];
  call.status = "completed";
  const result = body.decision === "approve" ? { branch: "kionga/run/fix-parser", pushed: true } : { error: "rejected", message: body.comment || "A person rejected this call." };
  worker.transcript.push({ role: "tool", tool_call_id: "c3", content: JSON.stringify(result) });
  emit(run, { type: "worker.tool_result", worker_id: worker.id, tool: "git_push", tool_call_id: "c3", output_preview: JSON.stringify(result) });
  setTimeout(() => completeFixer(run, coordinator, worker), 300);
  return reply(response, 200, approval);
}
const me = { subject: "admin", email: "admin@example.com", roles: ["admin"], services: ["agents", "catalog", "overview"], permissions: { agents_write: true }, mode: "local" };
const server = createServer(async (request, response) => {
  const url = new URL(request.url, "http://mock");
  const path = url.pathname;
  const raw = request.method === "GET" ? "" : await read(request);
  let body = {};
  if (raw && (request.headers["content-type"] || "").includes("json")) body = JSON.parse(raw);
  if (path === "/auth/providers") return reply(response, 200, { local: true, providers: [] });
  if (path === "/auth/local/login") {
    response.writeHead(303, { Location: new URLSearchParams(raw).get("return_to") || "/console", "Set-Cookie": "mock=1; Path=/; HttpOnly" });
    return response.end();
  }
  if (path === "/api/v1/events") {
    response.writeHead(200, { "Content-Type": "text/event-stream" });
    response.write("data: quiet\n\n");
    return;
  }
  if (path === "/api/v1/me") return reply(response, 200, me);
  if (path === "/api/v1/project-options") return reply(response, 200, { items: projects });
  if (path === "/api/v1/workspaces") return reply(response, 200, { items: [] });
  if (request.method === "GET" && resources[path]) return reply(response, 200, { items: resources[path] });
  if (path.startsWith("/api/v1/agent-definitions")) return crud(definitions, "/api/v1/agent-definitions", "adf", request, response, path, body);
  if (path.startsWith("/api/v1/agent-teams")) return crud(teams, "/api/v1/agent-teams", "atm", request, response, path, body);
  if (path.startsWith("/api/v1/team-runs")) return runRoutes(request, response, path, url.searchParams, body);
  if (path === "/api/v1/agent-approvals") {
    const items = [...approvals.values()].filter((item) => (!url.searchParams.get("run_id") || item.run_id === url.searchParams.get("run_id")) && (!url.searchParams.get("status") || item.status === url.searchParams.get("status")));
    return reply(response, 200, { items, total: items.length });
  }
  const decision = path.match(/^\/api\/v1\/agent-approvals\/([^/]+)\/decision$/);
  if (decision) return decide(decision[1], body, response);
  return reply(response, 404, { error: "not_found", message: `The mock has no route for ${path}.` });
});
server.listen(port, "127.0.0.1", () => process.stdout.write(`teams mock gateway on ${port}, seeded team ${seeded.id}\n`));
