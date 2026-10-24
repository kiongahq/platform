const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM} = require('jsdom');
const formatter = require('../../go/cmd/gateway/web/js/code-format.js');

test('numbered YAML and JSON views escape input and distinguish keys and values', () => {
  const yaml = formatter.render('name: sample\nenabled: true\n# <script>alert(1)</script>');
  const json = formatter.render('{\n  "count": 2\n}', 'json');
  assert.match(yaml, /code-line-number[^>]*>3</);
  assert.match(yaml, /class="code-key">name</);
  assert.match(yaml, /class="code-literal">true</);
  assert.doesNotMatch(yaml, /<script>/);
  assert.match(yaml, /&lt;script&gt;/);
  assert.match(json, /class="code-key">&quot;count&quot;</);
});

test('editable code gutter tracks programmatic and typed lines', () => {
  const dom = new JSDOM('<!doctype html><body><label>YAML<textarea class="yaml-editor">a: 1</textarea></label></body>', {runScripts: 'outside-only'});
  const {window} = dom;
  window.eval(fs.readFileSync('go/cmd/gateway/web/js/code-format.js', 'utf8'));
  const textarea = window.document.querySelector('textarea');
  assert.equal(window.document.querySelectorAll('.editor-gutter span').length, 1);
  textarea.value = 'a: 1\nb: <unsafe>';
  window.KiongaCodeFormat.sync(textarea);
  assert.equal(window.document.querySelectorAll('.editor-gutter span').length, 2);
  assert.match(window.document.querySelector('.editor-highlight').innerHTML, /&lt;unsafe&gt;/);
  textarea.value += '\nc: true';
  textarea.dispatchEvent(new window.Event('input', {bubbles: true}));
  assert.equal(window.document.querySelectorAll('.editor-gutter span').length, 3);
  dom.window.close();
});
