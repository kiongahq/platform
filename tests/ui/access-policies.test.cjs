// Users & access IAM tabs: simulator, policy editor round-trip and
// validation details, and the "Why?" explanation on denial toasts.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {JSDOM, VirtualConsole} = require('jsdom');
const {html, consoleScripts} = require('./harness.cjs');
const script = consoleScripts();
const tick = (ms = 20) => new Promise(resolve => setTimeout(resolve, ms));

const allow = {allowed: true, action: 'pipeline:Read', resource: 'kionga:project/p1/pipeline/train', reason: 'Allowed by statement ReadP1 in policy "Read p1" (group:analysts).', decided_by: 'read-p1@v2#ReadP1',
  matched_statements: [{policy_id: 'read-p1', policy_name: 'Read p1', version: 2, source: 'group:analysts', sid: 'ReadP1', effect: 'allow'}],
  evaluated_policies: [{id: 'kionga-user', name: 'Role baseline: user', version: 1, source: 'role:user', managed: true}, {id: 'read-p1', name: 'Read p1', version: 2, source: 'group:analysts'}]};
const deny = {allowed: false, action: 'pipeline:Run', resource: 'kionga:project/p1/pipeline/train', reason: 'Explicit deny: statement Freeze in policy "Freeze" (user:alice) denies pipeline:Run on kionga:project/p1/pipeline/train. An explicit deny overrides every allow.', decided_by: 'freeze@v1#Freeze',
  matched_statements: [{policy_id: 'kionga-user', policy_name: 'Role baseline: user', version: 1, source: 'role:user', sid: 'ServicePipelines', effect: 'allow'}, {policy_id: 'freeze', policy_name: 'Freeze', version: 1, source: 'user:alice', sid: 'Freeze', effect: 'deny'}],
  evaluated_policies: [{id: 'kionga-user', name: 'Role baseline: user', version: 1, source: 'role:user', managed: true}, {id: 'freeze', name: 'Freeze', version: 1, source: 'user:alice'}]};

function adminApp(overrides = {}) {
  const errors = [];
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', error => errors.push(error.message));
  const dom = new JSDOM(html, {url: 'http://localhost:8080/console.html?view=access', runScripts: 'outside-only', pretendToBeVisual: true, virtualConsole});
  const w = dom.window, requests = [];
  w.matchMedia = () => ({matches: false});
  w.KiongaPipelineGraph = {render: () => '<svg></svg>', enhance: () => {}};
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new w.Event('close')); };
  w.EventSource = class { close() {} };
  w.confirm = () => true;
  w.localStorage.setItem('kionga.console.preferences', JSON.stringify({live: false}));
  const fixtures = {
    '/api/v1/me': {subject: 'root', roles: ['admin'], services: [], mode: 'local', permissions: {access_manage: true, iam_manage: true}, provisioned: false, entitlements: null},
    '/api/v1/project-options': {items: [{id: 'p1', name: 'Project one', namespace: 'p1'}]},
    '/api/v1/workspaces': {items: []},
    '/api/v1/admin/users': {items: [{subject: 'alice', email: 'alice@example.com', role: 'user', services: ['pipelines'], project_ids: ['p1'], storage: {size_gb: 10, buckets: []}, compute: {profile: 'starter', vcpus: 2, memory_gb: 4, max_vms: 1, max_projects: 2, max_concurrent_runs: 1}, disabled: false}]},
    '/api/v1/admin/access-requests': {items: []},
    '/api/v1/admin/resource-profiles': {items: [{name: 'starter', label: 'Starter', compute: {vcpus: 2}, storage_gb: 25}]},
    '/api/v1/admin/iam/groups': {items: [{id: 'analysts', name: 'Analysts', members: ['alice'], updated_at: new Date().toISOString()}]},
    '/api/v1/admin/iam/policies': {items: [
      {id: 'kionga-user', name: 'Role baseline: user', version: 1, managed: true, statements: [{sid: 'ServicePipelines', effect: 'allow', actions: ['pipeline:Read'], resources: ['kionga:project/*']}]},
      {id: 'read-p1', name: 'Read p1', version: 2, statements: [{sid: 'ReadP1', effect: 'allow', actions: ['pipeline:Read'], resources: ['kionga:project/p1/*'], conditions: {ip_cidr: ['10.0.0.0/8']}}], updated_at: new Date().toISOString()},
    ]},
    '/api/v1/admin/iam/attachments': {items: [{id: 'read-p1:group:analysts', policy_id: 'read-p1', principal_type: 'group', principal_id: 'analysts', created_at: new Date().toISOString(), created_by: 'root'}]},
    '/api/v1/iam/catalog': {actions: [{action: 'pipeline:Read', description: 'Read'}, {action: 'pipeline:Run', description: 'Run'}], namespaces: ['pipeline'], profiles: ['starter', 'team']},
    ...overrides,
  };
  const respond = (status, body) => ({ok: status < 400, status, headers: {get: () => 'application/json'}, json: async () => body});
  w.fetch = async (path, options = {}) => {
    requests.push({path, method: options.method || 'GET', body: options.body});
    const fixture = fixtures[path];
    if (typeof fixture === 'function') return fixture(options);
    if (fixture === undefined) throw new Error(`Unexpected request: ${path}`);
    return respond(200, fixture);
  };
  w.eval(script);
  return {dom, w, requests, errors, fixtures, respond};
}

test('access view shows IAM tabs with groups, policies and attachments', async t => {
  const app = adminApp(); t.after(() => app.dom.window.close()); await tick(60);
  const doc = app.w.document;
  assert.equal(doc.querySelector('.view.active').id, 'access');
  assert.equal(doc.querySelector('#view-feedback').hidden, true, doc.querySelector('#view-feedback-message').textContent);
  assert.equal(doc.querySelector('#access-panel-users').hidden, false);
  assert.ok(doc.querySelector('#access-table').textContent.includes('alice@example.com'), 'existing users table still renders');
  doc.querySelector('#access-tab-policies').click(); await tick();
  assert.equal(doc.querySelector('#access-panel-policies').hidden, false);
  assert.equal(doc.querySelector('#access-panel-users').hidden, true);
  assert.equal(doc.querySelector('#access-tab-policies').getAttribute('aria-selected'), 'true');
  const rows = doc.querySelectorAll('#iam-policy-table tr');
  assert.equal(rows.length, 2);
  assert.match(rows[0].textContent, /Built-in role baseline/);
  assert.ok(rows[0].querySelector('[data-iam-policy-view]') && !rows[0].querySelector('[data-iam-policy-edit]'), 'built-ins are view-only');
  assert.match(rows[1].textContent, /Group · analysts/);
  doc.querySelector('#access-tab-groups').click(); await tick();
  assert.match(doc.querySelector('#iam-group-table').textContent, /alice/);
  assert.match(doc.querySelector('#iam-group-table').textContent, /read-p1/);
  doc.querySelector('#access-tab-attachments').click(); await tick();
  assert.equal(doc.querySelector('#iam-attach-policy').value, 'read-p1');
  assert.equal(doc.querySelectorAll('#iam-attachment-table tr').length, 1);
  assert.deepEqual(app.errors, []);
});

test('access simulator renders allow and deny with the deciding statement', async t => {
  const app = adminApp(); t.after(() => app.dom.window.close()); await tick(60);
  const doc = app.w.document;
  const explainAllow = '/api/v1/iam/explain?action=pipeline%3ARead&resource=kionga%3Aproject%2Fp1%2Fpipeline%2Ftrain&principal=alice';
  const explainDeny = '/api/v1/iam/explain?action=pipeline%3ARun&resource=kionga%3Aproject%2Fp1%2Fpipeline%2Ftrain&principal=alice&ip=10.1.2.3';
  app.fixtures[explainAllow] = {principal: {subject: 'alice', roles: ['user'], groups: ['analysts'], source: 'profile'}, decision: allow};
  app.fixtures[explainDeny] = {principal: {subject: 'alice', roles: ['user'], groups: [], source: 'profile'}, decision: deny};
  app.fixtures['/api/v1/iam/effective?principal=alice'] = {policies: [{source: 'role:user', policy: {id: 'kionga-user', name: 'Role baseline: user', version: 1, statements: [{}]}}, {source: 'group:analysts', policy: {id: 'read-p1', name: 'Read p1', version: 2, statements: [{}]}}]};
  doc.querySelector('#access-tab-simulator').click(); await tick();
  const form = doc.querySelector('#access-simulator-form');
  assert.deepEqual([...form.elements.action.options].map(option => option.value), ['pipeline:Read', 'pipeline:Run']);
  form.elements.principal.value = 'alice';
  form.elements.action.value = 'pipeline:Read';
  form.elements.resource.value = 'kionga:project/p1/pipeline/train';
  form.querySelector('button[type=submit]').click(); await tick(60);
  let result = doc.querySelector('#access-simulator-result .decision');
  assert.ok(result, doc.querySelector('#access-simulator-error').textContent);
  assert.equal(result.dataset.decision, 'allow');
  assert.ok(result.classList.contains('allow'));
  assert.match(result.textContent, /read-p1@v2#ReadP1/);
  assert.match(doc.querySelector('#access-simulator-result').textContent, /groups analysts/);
  assert.equal(doc.querySelectorAll('#access-effective .effective-policy').length, 2);

  form.elements.action.value = 'pipeline:Run';
  form.elements.ip.value = '10.1.2.3';
  form.querySelector('button[type=submit]').click(); await tick(60);
  result = doc.querySelector('#access-simulator-result .decision');
  assert.equal(result.dataset.decision, 'deny');
  assert.match(result.textContent, /Explicit deny/);
  const effects = [...result.querySelectorAll('tbody tr')].map(row => row.textContent);
  assert.equal(effects.length, 2);
  assert.match(effects[1], /Freeze.*denied/s);
  assert.ok(app.requests.some(request => request.path === explainDeny), 'simulated context is sent');
});

test('simulator shows 422 field details', async t => {
  const app = adminApp(); t.after(() => app.dom.window.close()); await tick(60);
  const doc = app.w.document;
  app.fixtures['/api/v1/iam/explain?action=pipeline%3ARead&resource=nope'] = () => app.respond(422, {error: 'validation_error', message: 'resource: bad', details: [{field: 'resource', message: 'must be * or start with "kionga:"'}]});
  doc.querySelector('#access-tab-simulator').click();
  const form = doc.querySelector('#access-simulator-form');
  form.elements.resource.value = 'nope';
  form.querySelector('button[type=submit]').click(); await tick(60);
  assert.match(doc.querySelector('#access-simulator-error').textContent, /resource: must be \* or start with "kionga:"/);
});

test('policy editor round-trips form and JSON and maps 422 details to statements', async t => {
  const app = adminApp(); t.after(() => app.dom.window.close()); await tick(60);
  const doc = app.w.document;
  app.fixtures['/api/v1/admin/iam/policies/read-p1'] = () => app.respond(422, {error: 'validation_error', message: 'bad', details: [{field: 'statements[1].actions[0]', message: 'unknown action "pipeline:Fly"'}]});
  doc.querySelector('#access-tab-policies').click();
  doc.querySelector('[data-iam-policy-edit="read-p1"]').click(); await tick();
  assert.equal(doc.querySelector('#iam-policy-dialog').open, true);
  const statements = () => doc.querySelectorAll('#policy-statements .policy-statement');
  assert.equal(statements().length, 1);
  assert.equal(statements()[0].querySelector('[name=ip_cidr]').value, '10.0.0.0/8');
  doc.querySelector('#add-policy-statement').click(); await tick();
  assert.equal(statements().length, 2);
  const second = statements()[1];
  second.querySelector('[name=effect]').value = 'deny';
  second.querySelector('[name=actions]').value = 'pipeline:Fly';
  second.querySelector('[name=resources]').value = 'kionga:project/p1/*';
  second.querySelector('[name=resource_tags]').value = 'template=llm';
  doc.querySelector('#policy-tab-json').click(); await tick();
  assert.equal(doc.querySelector('#policy-json-panel').hidden, false);
  const json = JSON.parse(doc.querySelector('#policy-json').value);
  assert.equal(json.statements.length, 2);
  assert.deepEqual(json.statements[0].conditions, {ip_cidr: ['10.0.0.0/8']});
  assert.deepEqual(json.statements[1], {sid: '', effect: 'deny', actions: ['pipeline:Fly'], resources: ['kionga:project/p1/*'], conditions: {resource_tags: {template: 'llm'}}});
  json.description = 'edited in JSON';
  doc.querySelector('#policy-json').value = JSON.stringify(json);
  doc.querySelector('#policy-tab-form').click(); await tick();
  assert.equal(doc.querySelector('#iam-policy-form [name=description]').value, 'edited in JSON');
  doc.querySelector('#iam-policy-form button[type=submit]').click(); await tick(60);
  const put = app.requests.find(request => request.method === 'PUT');
  assert.equal(JSON.parse(put.body).version, 2, 'optimistic version is sent');
  assert.match(doc.querySelector('#policy-issues').textContent, /statements\[1\]\.actions\[0\].*unknown action/s);
  assert.ok(statements()[1].classList.contains('has-issue'));
  assert.equal(doc.querySelector('#iam-policy-dialog').open, true, 'dialog stays open on validation errors');
});

test('denial toast offers Why? and explains the decision for the current user', async t => {
  const app = adminApp(); t.after(() => app.dom.window.close()); await tick(60);
  const doc = app.w.document;
  app.fixtures['/api/v1/iam/explain?action=pipeline%3ARun&resource=kionga%3Aproject%2Fp1%2Fpipeline%2Ftrain'] = {decision: deny};
  // A real control: detaching is denied by policy and the API returns the decision.
  app.fixtures['/api/v1/admin/iam/attachments/read-p1%3Agroup%3Aanalysts'] = () => app.respond(403, {error: 'access_denied', message: deny.reason, decision: deny});
  doc.querySelector('#access-tab-attachments').click();
  doc.querySelector('[data-iam-detach]').click(); await tick(60);
  const toastNode = doc.querySelector('#toast');
  assert.match(toastNode.textContent, /Explicit deny/);
  const why = toastNode.querySelector('.toast-action');
  assert.ok(why, 'Why? button present');
  why.click(); await tick(40);
  assert.equal(doc.querySelector('#decision-dialog').open, true);
  assert.match(doc.querySelector('#decision-detail').textContent, /freeze@v1#Freeze/);
  assert.ok(app.requests.some(request => request.path.startsWith('/api/v1/iam/explain?action=pipeline%3ARun')), 'explain was called for the current user');
  // Ordinary errors get no Why? button.
  app.w.toast('Something else failed', 'error', new Error('x'));
  assert.equal(toastNode.querySelector('.toast-action'), null);
});
