const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM} = require('jsdom');
const tick = () => new Promise(resolve => setTimeout(resolve, 30));

test('landing blog preview escapes every author-controlled field', async t => {
  const dom = new JSDOM(fs.readFileSync('go/cmd/gateway/web/index.html', 'utf8'), {url: 'http://localhost/', runScripts: 'outside-only', pretendToBeVisual: true});
  t.after(() => dom.window.close());
  const w = dom.window;
  const payload = '<img src=x onerror="window.pwned=1">';
  w.fetch = async () => ({ok: true, json: async () => ({items: [{slug: 's', title: payload, summary: payload, tags: [payload], created_at: '2026-01-01T00:00:00Z'}]})});
  w.eval(fs.readFileSync('go/cmd/gateway/web/landing.js', 'utf8'));
  await tick();
  const grid = w.document.querySelector('#landing-blog-grid');
  assert.equal(grid.querySelector('img'), null, 'injected markup rendered');
  assert.match(grid.textContent, /<img src=x/);
  assert.equal(w.pwned, undefined);
});
