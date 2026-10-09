const {test} = require('node:test');
const assert = require('node:assert/strict');
const {JSDOM} = require('jsdom');
const fs = require('node:fs');
const graph = require('../../go/cmd/gateway/web/pipeline-graph.js');

function edgesInMarkup(markup) {
  return [...markup.matchAll(/data-edge="([^"]+)"/g)].map(match => match[1].replace('&gt;', '>')).sort();
}

function randomDAG(seed, size) {
  let state = seed;
  const random = () => (state = (state * 1103515245 + 12345) % 2147483648) / 2147483648;
  return Array.from({length: size}, (_, index) => ({
    name: `n${index}`, kind: 'container', image: 'busybox',
    depends_on: Array.from({length: index}, (_, parent) => `n${parent}`).filter(() => random() < 0.3),
  }));
}

test('rendered edges exactly equal the persisted dependencies (property test)', () => {
  for (let seed = 1; seed <= 40; seed++) {
    const jobs = randomDAG(seed, 2 + (seed % 25));
    const markup = graph.render(jobs);
    assert.deepEqual(edgesInMarkup(markup), graph.edgesOf(jobs), `seed ${seed}`);
  }
});

test('with Dagre the edges are identical to the fallback layout', () => {
  const dom = new JSDOM('<!doctype html><body></body>', {runScripts: 'outside-only'});
  dom.window.eval(fs.readFileSync('go/cmd/gateway/web/vendor/dagre.min.js', 'utf8'));
  dom.window.eval(fs.readFileSync('go/cmd/gateway/web/pipeline-graph.js', 'utf8'));
  const jobs = randomDAG(7, 30);
  const markup = dom.window.KiongaPipelineGraph.render(jobs);
  assert.match(markup, /data-layout="dagre"/);
  assert.deepEqual(edgesInMarkup(markup), graph.edgesOf(jobs));
  dom.window.close();
});

test('status is conveyed by symbol and text, not color alone, with a legend', () => {
  const markup = graph.render([
    {name: 'a', kind: 'container', image: 'x', status: 'succeeded'},
    {name: 'b', kind: 'container', image: 'x', status: 'failed', depends_on: ['a']},
  ]);
  assert.match(markup, /aria-label="a: Succeeded\./);
  assert.match(markup, /aria-label="b: Failed\. x\. Depends on a\./);
  assert.match(markup, /class="dag-legend"/);
  assert.match(markup, /Succeeded/);
  assert.match(markup, />✕</);
});

test('invalid definitions show every issue and an empty definition explains itself', () => {
  const markup = graph.render([
    {name: 'a', kind: 'container', image: 'x', depends_on: ['b']},
    {name: 'b', kind: 'container', image: 'x', depends_on: ['a']},
    {name: 'c', kind: 'container', image: 'x', depends_on: ['missing']},
  ]);
  assert.match(markup, /Graph needs attention/);
  assert.match(markup, /missing job “missing”/);
  assert.match(markup, /cycle/);
  assert.match(graph.render([]), /at least one job/);
});

test('enhance adds zoom controls and keyboard navigation that selects nodes', () => {
  const dom = new JSDOM('<!doctype html><body><div id="g"></div></body>', {pretendToBeVisual: true});
  const {document} = dom.window;
  const host = document.querySelector('#g');
  host.innerHTML = graph.render([{name: 'a', kind: 'container', image: 'x'}, {name: 'b', kind: 'container', image: 'x', depends_on: ['a']}]);
  const selected = [];
  graph.enhance(host, {onSelect: id => selected.push(id)});
  const svg = host.querySelector('svg');
  const before = svg.getAttribute('viewBox');
  host.querySelector('[data-dag-zoom="in"]').click();
  assert.notEqual(svg.getAttribute('viewBox'), before);
  host.querySelector('[data-dag-zoom="fit"]').click();
  assert.equal(svg.getAttribute('viewBox'), before);
  const nodes = host.querySelectorAll('.dag-node-group');
  assert.equal(nodes[0].getAttribute('tabindex'), '0');
  nodes[0].dispatchEvent(new dom.window.KeyboardEvent('keydown', {key: 'ArrowRight', bubbles: true}));
  assert.equal(nodes[1].getAttribute('tabindex'), '0');
  nodes[1].dispatchEvent(new dom.window.KeyboardEvent('keydown', {key: 'Enter', bubbles: true}));
  assert.deepEqual(selected, ['b']);
  assert.equal(nodes[1].getAttribute('aria-selected'), 'true');
  dom.window.close();
});
