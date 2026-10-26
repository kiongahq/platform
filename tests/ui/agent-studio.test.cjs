const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM, VirtualConsole} = require('jsdom');
const {html, consoleScripts} = require('./harness.cjs');
const script = consoleScripts();
const tick = (ms = 30) => new Promise(resolve => setTimeout(resolve, ms));
const registry = JSON.parse(fs.readFileSync('go/internal/agentgraph/components.v1.json', 'utf8'));
const template = JSON.parse(fs.readFileSync('go/internal/agentgraph/templates/customer-support.json', 'utf8'));

const saved = {id: 'agd-aaaaaaaaaaaaaaaaaaaaaaaa', project_id: 'p1', owner_subject: 'user-1', schema_version: 2, name: 'Support', revision: 3, history: [],
  nodes: [{id: 'input', kind: 'input', name: 'Input', x: 40, y: 100}, {id: 'model', kind: 'model', name: 'Model', x: 260, y: 100, config: {model: 'llm'}}, {id: 'output', kind: 'output', name: 'Output', x: 480, y: 100}],
  edges: [{from: 'input', to: 'model', from_port: 'out', to_port: 'in'}, {from: 'model', to: 'output', from_port: 'out', to_port: 'in'}]};

// Same harness as navigation.test.cjs, with method-aware fixtures
// ("POST /path") and a record of request bodies.
function studioApp(overrides = {}, view = 'agent-designer') {
  const errors = [];
  const virtualConsole = new VirtualConsole(); virtualConsole.on('jsdomError', error => errors.push(error.message));
  const dom = new JSDOM(html, {url: `http://localhost:8080/console.html?view=${view}`, runScripts: 'outside-only', pretendToBeVisual: true, virtualConsole});
  const w = dom.window, requests = [];
  w.matchMedia = () => ({matches: false});
  w.CSS = {escape: value => value};
  w.KiongaPipelineGraph = {render: () => '<svg></svg>', enhance: () => {}};
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; };
  w.HTMLCanvasElement.prototype.getContext = () => ({scale() {}, clearRect() {}, fillText() {}, beginPath() {}, roundRect() {}, fill() {}});
  w.EventSource = class { close() {} };
  w.confirm = () => true;
  w.structuredClone ||= value => JSON.parse(JSON.stringify(value));
  w.localStorage.setItem('kionga.console.preferences', JSON.stringify({live: false}));
  const fixtures = {
    '/api/v1/me': {subject: 'user-1', roles: ['user'], services: ['agents'], mode: 'local', permissions: {agents_write: true}, project_ids: ['p1'], provisioned: true},
    '/api/v1/project-options': {items: [{id: 'p1', name: 'Project one', namespace: 'project-one'}]},
    '/api/v1/workspaces': {items: []},
    '/api/v1/agent-designs': {items: []},
    '/api/v1/agent-designs/components': registry,
    '/api/v1/agent-designs/templates': {items: [template]},
    '/api/v1/agent-designs/overview?project_id=p1': {window_hours: 24, designs: 1, versions: 0, deployments: 0, runs: 0, success_rate: null, p95_latency_ms: null, cost_usd: 0},
    '/api/v1/knowledge-bases': {items: [{id: 'kb-1', project_id: 'p1', name: 'Docs', backend: 'qdrant', dimensions: 64}, {id: 'kb-2', project_id: 'p2', name: 'Other', backend: 'qdrant', dimensions: 64}]},
    '/api/v1/agent-api-connections': {items: []},
    '/api/v1/datasets': {items: []},
    ...overrides,
  };
  w.fetch = async (path, options = {}) => {
    const method = (options.method || 'GET').toUpperCase();
    requests.push(`${method} ${path}`);
    if (options.body) (w.__bodies = w.__bodies || []).push({path, method, body: options.body});
    let fixture = fixtures[`${method} ${path}`] ?? (method === 'GET' ? fixtures[path] : undefined);
    if (typeof fixture === 'function') fixture = fixture(options);
    if (fixture === undefined) throw new Error(`Unexpected request: ${method} ${path}`);
    const status = fixture?.__status || 200;
    return {ok: status < 400, status, headers: {get: () => 'application/json'}, json: async () => fixture};
  };
  w.eval(script);
  return {dom, w, d: w.document, requests, errors, fixtures};
}

test('registry drives the inspector and resource pickers list only project resources', async () => {
  const {d, errors} = studioApp();
  await tick();
  d.querySelector('[data-design-add="knowledge"]').click();
  const inspector = d.querySelector('#design-inspector-content').textContent;
  assert.match(inspector, /Search a project knowledge base/);
  const picker = d.querySelector('select[data-design-config="knowledge_base"]');
  const values = [...picker.options].map(option => option.value);
  assert.deepEqual(values, ['', 'kb-1'], 'knowledge bases from other projects must not be offered');
  assert.equal(d.querySelector('input[data-design-config="limit"]').type, 'number');
  assert.match(d.querySelector('#design-kpis').textContent, /Designs1/);
  assert.match(d.querySelector('#design-kpis').textContent, /Success rate—/);
  assert.deepEqual(errors, []);
});

test('condition nodes expose true and false branches that save as ports', async () => {
  const {d} = studioApp();
  await tick();
  d.querySelector('[data-design-add="condition"]').click();
  const condition = d.querySelector('.design-node.selected').dataset.designNode;
  const ports = d.querySelectorAll(`[data-design-out="${condition}"]`);
  assert.deepEqual([...ports].map(port => port.dataset.designPort), ['true', 'false']);
  d.querySelector(`[data-design-out="${condition}"][data-design-port="false"]`).click();
  d.querySelector('[data-design-in="output"]').click();
  const edge = d.querySelector('.design-edge.branch-false');
  assert.ok(edge, 'false branch drawn distinctly');
  d.querySelector(`[data-design-out="${condition}"][data-design-port="true"]`).click();
  d.querySelector('[data-design-in="output"]').click();
  assert.equal(d.querySelectorAll('.design-edge').length, 3, 'true and false branches may reach the same node');
  d.querySelector(`[data-design-out="${condition}"][data-design-port="true"]`).click();
  d.querySelector('[data-design-in="output"]').click();
  assert.match(d.querySelector('#design-message').textContent, /already connected/);
});

test('undo and redo restore graph edits', async () => {
  const {d, w} = studioApp();
  await tick();
  assert.equal(d.querySelector('#design-undo').disabled, true);
  d.querySelector('[data-design-add="prompt"]').click();
  d.querySelector('[data-design-add="model"]').click();
  assert.equal(d.querySelectorAll('.design-node').length, 4);
  d.querySelector('#design-undo').click();
  assert.equal(d.querySelectorAll('.design-node').length, 3);
  d.querySelector('#agent-designer').dispatchEvent(new w.KeyboardEvent('keydown', {key: 'z', ctrlKey: true, bubbles: true}));
  assert.equal(d.querySelectorAll('.design-node').length, 2);
  d.querySelector('#design-redo').click();
  d.querySelector('#design-redo').click();
  assert.equal(d.querySelectorAll('.design-node').length, 4);
  assert.equal(d.querySelector('#design-redo').disabled, true);
});

test('templates open as unsaved designs with typed branches', async () => {
  const {d, w} = studioApp();
  await tick();
  const select = d.querySelector('#design-template');
  select.value = 'customer-support';
  select.dispatchEvent(new w.Event('change', {bubbles: true}));
  assert.equal(d.querySelectorAll('.design-node').length, template.graph.nodes.length);
  assert.equal(d.querySelector('#design-name').value, 'Customer support');
  assert.match(d.querySelector('#design-revision').textContent, /Unsaved/);
  assert.ok(d.querySelector('.design-edge.branch-false'));
});

test('validation marks nodes and lists issues that select the node', async () => {
  const issues = [{severity: 'error', code: 'missing_setting', node_id: 'model', field: 'model', message: 'model needs model.'}, {severity: 'warning', code: 'dead_end', node_id: 'input', message: 'input is unused.'}];
  const {d} = studioApp({'/api/v1/agent-designs': {items: [saved]}, [`POST /api/v1/agent-designs/${saved.id}/validate`]: {runnable: false, issues, content_sha256: 'f'.repeat(64)}}, 'agent-designer');
  await tick();
  d.querySelector('#design-list').value = saved.id;
  d.querySelector('#design-list').dispatchEvent(new d.defaultView.Event('change', {bubbles: true}));
  d.querySelector('#design-validate').click();
  await tick();
  assert.ok(d.querySelector('[data-design-node="model"]').classList.contains('has-error'));
  assert.ok(d.querySelector('[data-design-node="input"]').classList.contains('has-warning'));
  assert.equal(d.querySelector('[data-design-panel="validation"]').hidden, false);
  assert.match(d.querySelector('#design-validation').textContent, /Not runnable/);
  d.querySelector('[data-design-issue-node="model"]').click();
  assert.ok(d.querySelector('[data-design-node="model"]').classList.contains('selected'));
  assert.match(d.querySelector('#design-message').textContent, /1 error must be fixed/);
});

test('playground runs the saved revision and renders the streamed trace', async () => {
  const run = {id: 'agr-bbbbbbbbbbbbbbbbbbbbbbbb', status: 'running', events: []};
  const events = [
    {seq: 0, type: 'run.started', run_id: run.id, ts: '2026-10-26T00:00:00Z'},
    {seq: 1, type: 'node.completed', run_id: run.id, ts: '2026-10-26T00:00:00Z', node_id: 'model', kind: 'model', duration_ms: 42, output_preview: 'Hello there', usage: {input_tokens: 5, output_tokens: 2, cost_usd: 0}},
    {seq: 2, type: 'run.completed', run_id: run.id, ts: '2026-10-26T00:00:00Z', output: 'Hello there', duration_ms: 50, usage: {input_tokens: 5, output_tokens: 2, cost_usd: 0}},
  ];
  const {d, w} = studioApp({
    '/api/v1/agent-designs': {items: [saved]},
    [`POST /api/v1/agent-designs/${saved.id}/runs`]: run,
    [`/api/v1/agent-designs/${saved.id}/runs/${run.id}/events?after=-1`]: {status: 'succeeded', events, next: 2, done: true},
  });
  await tick();
  d.querySelector('#design-list').value = saved.id;
  d.querySelector('#design-list').dispatchEvent(new w.Event('change', {bubbles: true}));
  d.querySelector('#design-run-input').value = 'hi';
  d.querySelector('#design-run-form').dispatchEvent(new w.Event('submit', {bubbles: true, cancelable: true}));
  await tick(80);
  const body = JSON.parse(w.__bodies.find(item => item.path.endsWith('/runs')).body);
  assert.equal(body.input, 'hi');
  assert.equal(body.allow_side_effects, false, 'side effects need explicit approval');
  assert.equal(body.revision, 3);
  assert.match(d.querySelector('#design-run-timeline').textContent, /Model.*succeeded.*42 ms.*5 \/ 2/s);
  assert.match(d.querySelector('#design-chat-log').textContent, /Hello there/);
  assert.match(d.querySelector('#design-run-summary').textContent, /Succeeded in 50 ms/);
  assert.ok(d.querySelector('[data-design-node="model"]').classList.contains('run-succeeded'));
  assert.equal(d.querySelector('#design-run-save-case').disabled, false);
});

test('runs are refused for unsaved changes without calling the API', async () => {
  const {d, w, requests} = studioApp({'/api/v1/agent-designs': {items: [saved]}});
  await tick();
  d.querySelector('#design-list').value = saved.id;
  d.querySelector('#design-list').dispatchEvent(new w.Event('change', {bubbles: true}));
  d.querySelector('[data-design-add="prompt"]').click();
  d.querySelector('#design-run-input').value = 'hi';
  d.querySelector('#design-run-form').dispatchEvent(new w.Event('submit', {bubbles: true, cancelable: true}));
  await tick();
  assert.match(d.querySelector('#design-message').textContent, /Save your changes first/);
  assert.ok(!requests.some(request => request.startsWith('POST') && request.endsWith('/runs')));
});

test('evaluations show gate outcome, failure reasons and a case comparison', async () => {
  const base = {design_id: saved.id, project_id: 'p1', status: 'completed', gate: 0.8, p95_latency_ms: 120, cost_usd: 0.002, input_tokens: 10, output_tokens: 4, revision: 3, content_sha256: 'a'.repeat(64)};
  const older = {...base, id: 'age-000000000000000000000001', created_at: '2026-10-25T00:00:00Z', pass_rate: 0.5, passed: false, case_count: 2, completed: 2, failure_reasons: {missing_text: 1},
    cases: [{id: 'refund', input: 'refund?', expected: {contains: ['refund']}, passed: false, status: 'succeeded', failures: ['missing_text: refund'], output: 'no'}, {id: 'hello', input: 'hi', expected: {}, passed: true, status: 'succeeded', output: 'hi'}]};
  const newer = {...base, id: 'age-000000000000000000000002', created_at: '2026-10-26T00:00:00Z', pass_rate: 1, passed: true, case_count: 2, completed: 2, failure_reasons: {},
    cases: [{id: 'refund', input: 'refund?', expected: {contains: ['refund']}, passed: true, status: 'succeeded', output: 'refund ok'}, {id: 'hello', input: 'hi', expected: {}, passed: true, status: 'succeeded', output: 'hi'}]};
  const {d, w} = studioApp({
    '/api/v1/agent-designs': {items: [saved]},
    [`/api/v1/agent-designs/${saved.id}/evaluations`]: {items: [{...newer, cases: null}, {...older, cases: null}]},
    [`/api/v1/agent-designs/${saved.id}/versions`]: {items: []},
    [`/api/v1/agent-evaluations/${older.id}`]: older,
    [`/api/v1/agent-evaluations/${newer.id}`]: newer,
  });
  await tick();
  d.querySelector('#design-list').value = saved.id;
  d.querySelector('#design-list').dispatchEvent(new w.Event('change', {bubbles: true}));
  d.querySelector('[data-design-tab="evaluations"]').click();
  await tick();
  const table = d.querySelector('#design-evaluations').textContent;
  assert.match(table, /100%.*passed/s);
  assert.match(table, /missing text ×1/);
  assert.equal(d.querySelectorAll('.design-trend span').length, 2);
  for (const id of [older.id, newer.id]) {
    const box = d.querySelector(`[data-eval-compare="${id}"]`);
    box.checked = true; box.dispatchEvent(new w.Event('change', {bubbles: true}));
  }
  d.querySelector('#design-eval-compare').click();
  await tick();
  assert.match(d.querySelector('#design-eval-detail').textContent, /50% → 100% \(\+50 points\)/);
  assert.match(d.querySelector('#design-eval-detail').textContent, /refund.*fixed/s);
});

test('palette search filters components by name and purpose', async () => {
  const {d, w} = studioApp();
  await tick();
  const search = d.querySelector('#design-palette-search');
  const visible = () => [...d.querySelectorAll('[data-design-add]')].filter(button => !button.hidden).map(button => button.dataset.designAdd);
  search.value = 'knowledge base'; search.dispatchEvent(new w.Event('input', {bubbles: true}));
  assert.deepEqual(visible(), ['knowledge']);
  search.value = 'RustFS'; search.dispatchEvent(new w.Event('input', {bubbles: true}));
  assert.deepEqual(visible(), ['storage'], 'registry descriptions are searchable');
  search.value = 'zzz'; search.dispatchEvent(new w.Event('input', {bubbles: true}));
  assert.equal(d.querySelector('#design-palette-empty').hidden, false);
  search.value = ''; search.dispatchEvent(new w.Event('input', {bubbles: true}));
  assert.equal(visible().length, 8);
});
