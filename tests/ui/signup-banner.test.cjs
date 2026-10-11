// Someone who signed up with single sign-on lands in the console before an
// administrator grants anything: the banner says what happens next.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {JSDOM, VirtualConsole} = require('jsdom');
const {html, consoleScripts} = require('./harness.cjs');
const script = consoleScripts();
const tick = (ms = 20) => new Promise(resolve => setTimeout(resolve, ms));

function app(me) {
  const errors = [];
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', error => errors.push(error.message));
  const dom = new JSDOM(html, {url: 'http://localhost:8080/console.html?view=profile', runScripts: 'outside-only', pretendToBeVisual: true, virtualConsole});
  const w = dom.window;
  w.matchMedia = () => ({matches: false});
  w.KiongaPipelineGraph = {render: () => '<svg></svg>', enhance: () => {}};
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; };
  w.EventSource = class { close() {} };
  w.localStorage.setItem('kionga.console.preferences', JSON.stringify({live: false}));
  const identity = {subject: 'CgNuZXc', email: 'new@example.com', services: [], mode: 'oidc', permissions: {}, entitlements: null, connector: 'github', ...me};
  const requests = identity.access_request ? [{id: 'r1', status: identity.access_request.status, requested_services: ['projects'], created_at: new Date().toISOString()}] : [];
  w.fetch = async path => {
    const body = path === '/api/v1/me' ? identity : path === '/api/v1/access-requests' ? {items: requests} : {items: []};
    return {ok: true, status: 200, headers: {get: () => 'application/json'}, json: async () => body};
  };
  w.eval(script);
  return {dom, w, errors};
}

const banner = doc => ({hidden: doc.querySelector('#signup-banner').hidden, text: doc.querySelector('#signup-banner-message').textContent});

test('a pending sign-up sees that approval is on its way', async t => {
  const {dom, w, errors} = app({roles: ['user'], provisioned: false, access_request: {status: 'pending'}});
  t.after(() => dom.window.close()); await tick(60);
  const shown = banner(w.document);
  assert.equal(shown.hidden, false);
  assert.match(shown.text, /waiting for an administrator to approve/);
  assert.equal(w.document.querySelector('#signup-banner-link').getAttribute('href'), '?view=profile');
  assert.equal(w.document.querySelector('#request-access').disabled, true, 'a second request cannot be filed while one is pending');
  assert.deepEqual(errors, []);
});

test('a declined sign-up is told so, and how to ask again', async t => {
  const {dom, w} = app({roles: ['user'], provisioned: false, access_request: {status: 'rejected'}});
  t.after(() => dom.window.close()); await tick(60);
  assert.match(banner(w.document).text, /declined/);
});

test('a signed-in user with no request is pointed at My access', async t => {
  const {dom, w} = app({roles: ['user'], provisioned: false, access_request: null});
  t.after(() => dom.window.close()); await tick(60);
  assert.match(banner(w.document).text, /no services are assigned/);
});

for (const [name, me] of [
  ['a provisioned user', {roles: ['user'], provisioned: true, services: ['projects']}],
  ['a directory admin', {roles: ['admin'], provisioned: false}],
  ['a directory engineer', {roles: ['engineer'], provisioned: false}],
]) {
  test(`${name} sees no sign-up banner`, async t => {
    const {dom, w} = app(me);
    t.after(() => dom.window.close()); await tick(60);
    assert.equal(banner(w.document).hidden, true);
  });
}
