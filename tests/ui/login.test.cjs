const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM} = require('jsdom');
const tick = () => new Promise(resolve => setTimeout(resolve, 30));

async function loginPage(url, providersResponse) {
  const dom = new JSDOM(fs.readFileSync('go/cmd/gateway/web/login.html', 'utf8'), {url, runScripts: 'outside-only'});
  const w = dom.window;
  w.fetch = async () => {
    if (providersResponse instanceof Error) throw providersResponse;
    return {ok: true, json: async () => providersResponse};
  };
  w.eval(fs.readFileSync('go/cmd/gateway/web/login.js', 'utf8'));
  await tick();
  return w;
}

test('login lists each single sign-on provider next to the local form', async t => {
  const w = await loginPage('http://localhost/login.html?return_to=%2Fconsole.html%3Fview%3Dmodels',
    {local: true, providers: [{id: 'ldap', name: 'Company directory', type: 'ldap'}, {id: 'github', name: 'GitHub', type: 'github'}]});
  t.after(() => w.close());
  const links = [...w.document.querySelectorAll('#sso-options a')];
  assert.deepEqual(links.map(link => link.textContent), ['Sign in with Company directory', 'Sign in with GitHub']);
  const first = new URL(links[0].href);
  assert.equal(first.pathname, '/auth/sso/login');
  assert.equal(first.searchParams.get('connector'), 'ldap');
  assert.equal(first.searchParams.get('return_to'), '/console.html?view=models');
  assert.equal(w.document.querySelector('#local-login').hidden, false);
  assert.equal(w.document.querySelector('#login-divider').hidden, false);
});

test('local form is hidden when only single sign-on is enabled', async t => {
  const w = await loginPage('http://localhost/login.html', {local: false, providers: [{id: '', name: 'Single sign-on'}]});
  t.after(() => w.close());
  assert.equal(w.document.querySelector('#local-login').hidden, true);
  assert.equal(w.document.querySelectorAll('#sso-options a').length, 1);
  assert.equal(w.document.querySelector('#login-hint').textContent, 'Use your organization account.');
});

test('provider names are rendered as text, never markup', async t => {
  const w = await loginPage('http://localhost/login.html', {local: true, providers: [{id: 'x', name: '<img src=x onerror="window.pwned=1">'}]});
  t.after(() => w.close());
  assert.equal(w.document.querySelector('#sso-options img'), null);
  assert.equal(w.pwned, undefined);
});

test('if the providers call fails the local form remains and unsafe return paths are ignored', async t => {
  const w = await loginPage('http://localhost/login.html?return_to=%2F%2Fevil.example', new Error('offline'));
  t.after(() => w.close());
  assert.equal(w.document.querySelector('#local-login').hidden, false);
  assert.equal(w.document.querySelector('#sso-options').hidden, true);
  assert.equal(w.document.querySelector('#return-to').value, '/console.html');
});
