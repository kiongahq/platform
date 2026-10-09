// Scaffold confirmation/progress UI, feature cards and data connections.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {JSDOM, VirtualConsole} = require('jsdom');
const {html, consoleScripts} = require('./harness.cjs');
const script = consoleScripts();
const tick = (ms = 20) => new Promise(resolve => setTimeout(resolve, ms));

const project = {id: 'p1', name: 'Churn model', namespace: 'churn-model', template: 'production-ml', template_version: '1.0.0', framework: 'xgboost', accelerator: 'cpu', requested_profile: 'team', status: 'ready',
  scaffold_command: 'kionga scaffold churn-model --template production-ml --template-version 1.0.0 --framework xgboost --accelerator cpu --profile team'};
const plan = {project_id: 'p1', namespace: 'churn-model', workspace: 'workbench', argv: project.scaffold_command.split(' '), command: project.scaffold_command, working_dir: '/workspace/projects', output_dir: '/workspace/projects/churn-model', available: true};

function app(view, {identity = {}, fixtures = {}} = {}) {
  const errors = [];
  const virtualConsole = new VirtualConsole(); virtualConsole.on('jsdomError', error => errors.push(error.message));
  const dom = new JSDOM(html, {url: `http://localhost:8080/console.html?view=${view}`, runScripts: 'outside-only', pretendToBeVisual: true, virtualConsole});
  const w = dom.window, requests = [];
  w.matchMedia = () => ({matches: false});
  w.KiongaPipelineGraph = {render: () => '<svg></svg>', enhance: () => {}};
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new w.Event('close')); };
  w.HTMLCanvasElement.prototype.getContext = () => ({scale() {}, clearRect() {}, fillText() {}, beginPath() {}, roundRect() {}, fill() {}});
  w.EventSource = class { close() {} };
  w.confirm = () => true;
  const me = {subject: 'admin', roles: ['admin'], services: [], mode: 'local', permissions: {projects_write: true, connections_write: true}, provisioned: true, entitlements: null, ...identity};
  const routes = {
    'GET /api/v1/me': me,
    'GET /api/v1/project-options': {items: [{id: 'p1', name: 'Churn model', namespace: 'churn-model'}, {id: 'p2', name: 'Fraud', namespace: 'fraud'}]},
    'GET /api/v1/workspaces': {items: [{id: 'workbench', name: 'JupyterLab', available: true, state: 'ready', message: 'Ready'}, {id: 'ide', name: 'Browser IDE', available: true, state: 'ready', message: 'Ready'}]},
    'GET /api/v1/projects': [project],
    'GET /api/v1/pipelines/runs': [], 'GET /api/v1/functions': {items: [], configured: false}, 'GET /api/v1/models': {items: []}, 'GET /api/v1/agents': {items: []},
    'GET /api/v1/projects/p1/scaffold-jobs': {items: [], total: 0},
    'GET /api/v1/projects/p1/scaffold-plan': plan,
    'GET /api/v1/components': [], 'GET /api/v1/connections': {items: []}, 'GET /api/v1/onboarding/readiness': {percent: 0, items: []},
    'GET /api/v1/admin/feature-stores': {items: []}, 'GET /api/v1/admin/storage-connections': {items: []},
    'GET /api/v1/features/views': {items: []}, 'GET /api/v1/features/stores': {items: []},
    ...fixtures,
  };
  w.fetch = async (path, options = {}) => {
    const method = options.method || 'GET';
    requests.push({method, path, body: options.body});
    let fixture = routes[`${method} ${path}`];
    if (typeof fixture === 'function') fixture = fixture(options);
    if (fixture === undefined) throw new Error(`Unexpected request: ${method} ${path}`);
    const failed = fixture instanceof Error;
    return {ok: !failed, status: failed ? fixture.status || 500 : 200, headers: {get: () => 'application/json'}, json: async () => failed ? {message: fixture.message} : fixture};
  };
  w.eval(script);
  return {dom, w, doc: w.document, requests, errors, routes};
}

test('run in workspace shows the exact argv, then polls progress to files, git status and errors', async t => {
  const jobs = [
    {id: 'scf-1', project_id: 'p1', status: 'running', workspace: 'workbench', output_dir: '/workspace/projects/churn-model', requested_by: 'admin', files: [], git_status: []},
    {id: 'scf-1', project_id: 'p1', status: 'succeeded', workspace: 'workbench', output_dir: '/workspace/projects/churn-model', requested_by: 'admin', ended_at: new Date().toISOString(), exit_code: 0, files: ['README.md', 'src/churn_model/__init__.py'], git_status: ['?? README.md'], output_tail: 'Created /workspace/projects/churn-model'},
  ];
  let polls = 0;
  const a = app('projects&resource=p1', {identity: {services: ['projects', 'workbench']}, fixtures: {
    'POST /api/v1/projects/p1/scaffold-jobs': {...jobs[0], status: 'queued'},
    'GET /api/v1/projects/p1/scaffold-jobs/scf-1': () => jobs[Math.min(polls++, 1)],
  }});
  t.after(() => a.dom.window.close());
  await tick(); await tick();
  const panel = a.doc.querySelector('#scaffold-panel');
  assert.match(panel.textContent, /\/workspace\/projects\/churn-model/);
  assert.ok(panel.querySelector('[data-copy]'), 'Copy stays available');
  for (const tool of ['workbench', 'ide']) {
    const link = a.doc.querySelector(`#project-detail-panel a[href*="tool=${tool}&project=p1"]`);
    assert.ok(link, `${tool} link carries the project`);
    assert.match(link.title, /\/workspace\/projects\/churn-model/);
  }
  panel.querySelector('[data-scaffold-run]').click(); await tick();
  const dialog = a.doc.querySelector('#scaffold-confirm-dialog');
  assert.equal(dialog.open, true, 'nothing runs before confirmation');
  assert.ok(!a.requests.some(r => r.method === 'POST'), 'no job is created by opening the dialog');
  assert.equal(a.doc.querySelector('#scaffold-confirm-command').textContent, project.scaffold_command);
  assert.deepEqual([...dialog.querySelectorAll('.argv-list li')].map(li => li.textContent), plan.argv);
  assert.match(dialog.textContent, /Writes to\s*\/workspace\/projects\/churn-model/);
  a.doc.querySelector('#scaffold-confirm-form').dispatchEvent(new a.w.Event('submit', {cancelable: true})); await tick();
  const post = a.requests.find(r => r.method === 'POST');
  assert.deepEqual(JSON.parse(post.body), {options: {workspace: 'workbench'}}, 'the browser sends no command text');
  assert.equal(dialog.open, false);
  const progress = a.doc.querySelector('#scaffold-run');
  assert.equal(progress.getAttribute('aria-live'), 'polite');
  assert.match(progress.textContent, /running/i);
  await tick(1100);
  assert.match(progress.textContent, /succeeded/i);
  assert.match(progress.textContent, /Created 2 files/);
  assert.match(progress.textContent, /src\/churn_model\/__init__.py/);
  assert.match(progress.textContent, /\?\? README.md/);
  assert.deepEqual(a.errors, []);
});

test('failed jobs and unavailable workspaces explain themselves', async t => {
  const failed = {id: 'scf-2', project_id: 'p1', status: 'failed', workspace: 'workbench', output_dir: '/workspace/projects/churn-model', ended_at: new Date().toISOString(), exit_code: 1, error: 'kionga scaffold exited with code 1. See the output below.', output_tail: 'kionga: target already exists and is not empty', files: [], git_status: []};
  const a = app('projects&resource=p1', {identity: {services: ['projects', 'workbench']}, fixtures: {
    'GET /api/v1/projects/p1/scaffold-jobs': {items: [failed], total: 1},
    'GET /api/v1/projects/p1/scaffold-plan': {...plan, workspace: '', available: false, reason: 'Workspace is offline. Ask your administrator to start it.'},
  }});
  t.after(() => a.dom.window.close());
  await tick(); await tick();
  const progress = a.doc.querySelector('#scaffold-run');
  assert.equal(progress.hidden, false);
  assert.match(progress.querySelector('[role=alert]').textContent, /exited with code 1/);
  assert.match(progress.textContent, /not empty/);
  a.doc.querySelector('[data-scaffold-run]').click(); await tick();
  assert.equal(a.doc.querySelector('#scaffold-confirm-run').disabled, true);
  assert.match(a.doc.querySelector('#scaffold-confirm-error').textContent, /offline/);
});

test('run in workspace is disabled with a reason when no workspace is assigned', async t => {
  const a = app('projects&resource=p1', {identity: {roles: ['user'], services: ['projects'], permissions: {projects_write: true}}});
  t.after(() => a.dom.window.close());
  await tick(); await tick();
  const button = a.doc.querySelector('#scaffold-panel button[disabled]');
  assert.ok(button && /JupyterLab or the IDE/.test(button.title));
});

test('feature cards show store, location, freshness, failures and accessible projects', async t => {
  const now = Date.now();
  const internal = {id: 'internal', name: 'kionga-internal', provider: 'internal', kind: 'internal', location: 'online http://feature-gateway:8083 · offline s3://mlaiops-features', all_projects: true, allowed_projects: [], health: {state: 'healthy', detail: 'Online store reachable'}};
  const feast = {id: 'fs-1', name: 'team-feast', provider: 'feast', kind: 'external', location: 'Feast http://feast:6566', all_projects: false, allowed_projects: ['p1'], health: {state: 'unavailable', detail: 'Feast feature server is unreachable: connection refused'}};
  const a = app('features', {identity: {roles: ['user'], services: ['features']}, fixtures: {
    'GET /api/v1/features/stores': {items: [internal, feast]},
    'GET /api/v1/features/views': {items: [
      {id: 'fv1', name: 'customer_profile', entity: 'user_id', fields: [{name: 'plan', type: 'string'}], tags: [], ttl_seconds: 3600, status: 'materialized', online_entity_count: 3, version: 2, store: internal, freshness: {state: 'fresh', age_seconds: 60, ttl_seconds: 3600}, failure_count: 0},
      {id: 'fv2', name: 'txn_stats', entity: 'user_id', fields: [], tags: [], ttl_seconds: 300, status: 'materialized', online_entity_count: 3, version: 1, store: internal, freshness: {state: 'stale', age_seconds: 7200, ttl_seconds: 300}, failure_count: 2, latest_failure: 'source unreachable'},
      {id: 'fv3', name: 'new_view', entity: 'user_id', fields: [], tags: [], ttl_seconds: 0, status: 'registered', online_entity_count: 0, store: internal, freshness: {state: 'never', ttl_seconds: 0}},
    ]},
    'GET /api/v1/features/customer_profile/versions': {items: [{version: 2}, {version: 1}]},
    'GET /api/v1/features/customer_profile/lineage': {items: [{run_id: 'mat-1', status: 'succeeded', view_version: 2, source_dataset: 's3://raw/customers', offline_uri: 's3://mlaiops-features/customer_profile/snapshot.parquet', entity_count: 3, created_at: new Date(now).toISOString()}]},
  }});
  t.after(() => a.dom.window.close());
  await tick(); await tick();
  const stores = a.doc.querySelectorAll('#feature-store-list .store-item');
  assert.equal(stores.length, 2);
  assert.match(stores[0].textContent, /Internal/); assert.match(stores[0].textContent, /All projects/);
  assert.match(stores[1].textContent, /External/); assert.match(stores[1].textContent, /Churn model/);
  assert.match(stores[1].textContent, /connection refused/);
  assert.ok(stores[1].querySelector('.status.unavailable'));
  const cards = a.doc.querySelectorAll('#feature-grid .feature-card');
  assert.equal(cards.length, 3);
  assert.ok(cards[0].querySelector('.status.fresh')); assert.match(cards[0].textContent, /v2/); assert.match(cards[0].textContent, /s3:\/\/mlaiops-features/);
  assert.ok(cards[1].querySelector('.status.stale')); assert.match(cards[1].textContent, /source unreachable/);
  assert.ok(cards[2].querySelector('.status.never')); assert.match(cards[2].textContent, /Never materialized/);
  cards[0].click(); await tick();
  const detail = a.doc.querySelector('#metadata-detail');
  assert.match(detail.textContent, /mat-1/); assert.match(detail.textContent, /v2 of 2/);
  assert.deepEqual(a.errors, []);
});

test('platform lists feature store and storage connections with Test buttons and honest states', async t => {
  const created = [];
  const a = app('platform', {identity: {services: ['platform']}, fixtures: {
    'GET /api/v1/admin/feature-stores': {items: [
      {id: 'internal', provider: 'internal', name: 'kionga-internal', kind: 'internal', config: {online_url: 'http://feature-gateway:8083', offline_uri: 's3://mlaiops-features'}, allowed_projects: [], capabilities: {online: true, offline: true, point_in_time: true}, health: {state: 'configured', detail: 'Not checked yet.'}},
      {id: 'fs-1', provider: 'feast', name: 'team-feast', kind: 'external', config: {url: 'http://feast:6566'}, secret_ref: 'env:FEAST_TOKEN', secret_present: false, allowed_projects: ['p1'], capabilities: {online: true, ttl: true}, health: {state: 'degraded', detail: 'Feast health check returned 503'}},
    ]},
    'GET /api/v1/admin/storage-connections': {items: [{id: 'st-1', name: 'team-lake', endpoint: 'https://s3.example.com', bucket: 'lake', region: 'eu-west-1', path_style: true, secret_ref: 'env:LAKE', secret_present: true, allowed_projects: [], health: {state: 'unavailable', detail: 'Access denied to bucket lake'}}]},
    'POST /api/v1/admin/feature-stores/fs-1/test': {id: 'fs-1', health: {state: 'healthy', detail: 'Feast feature server is serving'}},
    'GET /api/v1/admin/feature-store-providers': {items: [{name: 'feast', title: 'Feast feature server', kind: 'external', adapter: true}, {name: 'tecton', title: 'Tecton', kind: 'external', adapter: false}]},
    'POST /api/v1/admin/feature-stores': options => { created.push(JSON.parse(options.body)); return {id: 'fs-2'}; },
    'POST /api/v1/admin/feature-stores/fs-2/test': {id: 'fs-2', health: {state: 'unavailable', detail: 'Feast feature server is unreachable'}},
  }});
  t.after(() => a.dom.window.close());
  await tick(); await tick();
  const section = a.doc.querySelector('#data-connections');
  assert.equal(section.hidden, false);
  const stores = section.querySelectorAll('#feature-store-connections .store-item');
  assert.equal(stores.length, 2);
  assert.ok(stores[0].querySelector('.status.configured'));
  assert.ok(!stores[0].querySelector('[data-feature-store-delete]'), 'the internal store cannot be removed');
  assert.match(stores[1].textContent, /env:FEAST_TOKEN missing/);
  assert.match(stores[1].textContent, /returned 503/);
  assert.match(section.querySelector('#storage-connections').textContent, /Access denied/);
  stores[1].querySelector('[data-feature-store-test]').click(); await tick();
  assert.ok(a.requests.some(r => r.method === 'POST' && r.path === '/api/v1/admin/feature-stores/fs-1/test'));
  assert.match(a.doc.querySelector('#toast').textContent, /Healthy/);
  a.doc.querySelector('#add-feature-store').click(); await tick();
  const options = [...a.doc.querySelectorAll('#feature-store-provider option')];
  assert.equal(options.find(o => o.value === 'tecton').disabled, true);
  assert.match(options.find(o => o.value === 'tecton').textContent, /no adapter/);
  const form = a.doc.querySelector('#feature-store-form');
  form.elements.name.value = 'team-feast-2'; form.elements.url.value = 'http://feast:6566'; form.elements.secret_ref.value = 'env:FEAST_TOKEN'; form.elements.allowed_projects.value = 'p1, p2';
  form.dispatchEvent(new a.w.Event('submit', {cancelable: true})); await tick(); await tick();
  assert.deepEqual(created[0], {provider: 'feast', name: 'team-feast-2', config: {url: 'http://feast:6566'}, secret_ref: 'env:FEAST_TOKEN', allowed_projects: ['p1', 'p2']});
  assert.match(a.doc.querySelector('#toast').textContent, /Unavailable: Feast feature server is unreachable/);
  assert.deepEqual(a.errors, []);
});

test('non-administrators never request connection administration', async t => {
  const a = app('platform', {identity: {roles: ['user'], services: ['platform']}});
  t.after(() => a.dom.window.close());
  await tick(); await tick();
  assert.equal(a.doc.querySelector('#data-connections').hidden, true);
  assert.ok(!a.requests.some(r => r.path.startsWith('/api/v1/admin/')));
});
