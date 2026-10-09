// Save-status, local recovery and undo logic of the editorial workspace.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM} = require('jsdom');

function load() {
  const dom = new JSDOM('<!doctype html><body></body>', {url: 'http://localhost/editorial.html', runScripts: 'outside-only'});
  dom.window.eval(fs.readFileSync('go/cmd/gateway/web/editorial/state.js', 'utf8'));
  return dom;
}

const snapshot = (title, extra = {}) => ({title, summary: 'S', slug: 'slug', tags: [], blocks: [{id: 'a', type: 'paragraph', data: {text: title}}], cover: null, seo: {}, ...extra});

function harness(dom, overrides = {}) {
  const {EditorialState} = dom.window;
  const statuses = [];
  const saves = [];
  let online = true;
  const timers = [];
  const controller = EditorialState.createSaveController({
    key: 'post-1', revision: 1, storage: dom.window.localStorage, initial: snapshot('Saved'),
    now: () => new Date(2026, 9, 9, 14, 5),
    save: async (snap, base) => { saves.push({snap, base}); return (overrides.save || (async () => ({revision: base + 1})))(snap, base); },
    onStatus: (state, label) => statuses.push([state, label]),
    onConflict: (latest, mine) => { controller.conflict = {latest, mine}; },
    isOnline: () => online,
    setTimer: fn => { timers.push(fn); return timers.length; }, clearTimer: () => {},
    ...overrides.options,
  });
  return {controller, statuses, saves, timers, setOnline: value => { online = value; }, EditorialState};
}

test('autosave shows Saving… then Saved hh:mm and advances the base revision', async () => {
  const dom = load();
  const {controller, statuses, saves} = harness(dom);
  controller.changed(snapshot('Draft one'));
  assert.equal(controller.state, 'dirty');
  await controller.flush();
  assert.deepEqual(statuses.map(item => item[1]), ['Unsaved changes', 'Saving…', 'Saved 14:05']);
  assert.equal(saves[0].base, 1);
  assert.equal(controller.revision, 2);
  controller.changed(snapshot('Draft two'));
  await controller.flush();
  assert.equal(saves[1].base, 2, 'second save is based on the new revision');
  assert.equal(dom.window.localStorage.getItem('kionga.editorial.recovery.post-1'), null, 'recovery copy cleared after a clean save');
});

test('unchanged content does not trigger a save', async () => {
  const dom = load();
  const {controller, saves} = harness(dom);
  controller.changed(snapshot('Saved'));
  await controller.flush();
  assert.equal(saves.length, 0);
});

test('offline keeps the edit locally and saves when back online', async () => {
  const dom = load();
  const {controller, statuses, saves, setOnline} = harness(dom);
  setOnline(false);
  controller.changed(snapshot('Written on a train'));
  await controller.flush();
  assert.equal(saves.length, 0);
  assert.equal(statuses.at(-1)[1], 'Offline — kept locally');
  const stored = JSON.parse(dom.window.localStorage.getItem('kionga.editorial.recovery.post-1'));
  assert.equal(stored.snapshot.title, 'Written on a train');
  assert.equal(stored.base_revision, 1);
  setOnline(true);
  await controller.online();
  assert.equal(saves.length, 1);
  assert.equal(statuses.at(-1)[0], 'saved');
});

test('network failure during save is reported as offline and retried', async () => {
  const dom = load();
  let fail = true;
  const {controller, statuses, saves} = harness(dom, {save: async (snap, base) => { if (fail) throw new TypeError('Failed to fetch'); return {revision: base + 1}; }});
  controller.changed(snapshot('Edit'));
  await controller.flush();
  assert.equal(statuses.at(-1)[1], 'Offline — kept locally');
  assert.ok(dom.window.localStorage.getItem('kionga.editorial.recovery.post-1'));
  fail = false;
  await controller.online();
  assert.equal(saves.length, 2);
  assert.equal(controller.state, 'saved');
});

test('409 conflict stops autosave, reports the latest revision and keeps mine', async () => {
  const dom = load();
  const latest = {revision: 5, title: 'Someone else', summary: 'S', slug: 'slug', tags: [], blocks: []};
  const {controller, statuses, saves} = harness(dom, {save: async () => { throw {status: 409, latest}; }});
  controller.changed(snapshot('Mine'));
  await controller.flush();
  assert.equal(statuses.at(-1)[1], 'Newer version on server');
  assert.equal(controller.conflict.latest.revision, 5);
  assert.equal(controller.conflict.mine.title, 'Mine');
  controller.changed(snapshot('Mine, more'));
  await controller.flush();
  assert.equal(saves.length, 1, 'no blind overwrite while the conflict is open');
});

test('resolving a conflict by keeping mine saves on top of the latest revision', async () => {
  const dom = load();
  let conflict = true;
  const {controller, saves} = harness(dom, {save: async (snap, base) => { if (conflict) { conflict = false; throw {status: 409, latest: {revision: 7, title: 'Theirs', blocks: []}}; } return {revision: base + 1}; }});
  controller.changed(snapshot('Mine'));
  await controller.flush();
  controller.resolve(controller.conflict.latest, true);
  await controller.flush();
  assert.equal(saves.at(-1).base, 7);
  assert.equal(saves.at(-1).snap.title, 'Mine');
  assert.equal(controller.revision, 8);
});

test('recovery is offered after an interruption and flagged stale when the server moved on', () => {
  const dom = load();
  const {EditorialState} = dom.window;
  const storage = dom.window.localStorage;
  storage.setItem('kionga.editorial.recovery.post-9', JSON.stringify({snapshot: snapshot('Unsaved words'), base_revision: 3, saved_at: '2026-10-09T10:00:00Z'}));
  const offer = EditorialState.recoveryFor(storage, 'post-9', {revision: 3, ...snapshot('Server words')});
  assert.equal(offer.snapshot.title, 'Unsaved words');
  assert.equal(offer.stale, false);
  assert.equal(EditorialState.recoveryFor(storage, 'post-9', {revision: 4, ...snapshot('Server words')}).stale, true);
  assert.equal(EditorialState.recoveryFor(storage, 'post-9', {revision: 3, ...snapshot('Unsaved words')}), null, 'identical copy is dropped');
  assert.equal(storage.getItem('kionga.editorial.recovery.post-9'), null);
  storage.setItem('kionga.editorial.recovery.post-9', '{not json');
  assert.equal(EditorialState.recoveryFor(storage, 'post-9', null), null, 'corrupt storage is ignored');
});

test('a new post keeps its recovery copy when it receives an id', async () => {
  const dom = load();
  const {controller} = harness(dom, {options: {key: 'new', revision: 0, initial: null}});
  controller.changed(snapshot('Brand new'));
  controller.rekey('post-42', 1);
  assert.ok(dom.window.localStorage.getItem('kionga.editorial.recovery.post-42'));
  assert.equal(dom.window.localStorage.getItem('kionga.editorial.recovery.new'), null);
});

test('history undo and redo walk block snapshots', () => {
  const {EditorialState} = load().window;
  const history = EditorialState.createHistory(3);
  for (const value of ['a', 'b', 'c', 'd']) history.record({value});
  assert.equal(history.record({value: 'd'}), false, 'duplicates are ignored');
  assert.equal(history.undo().value, 'c');
  assert.equal(history.undo().value, 'b');
  assert.equal(history.redo().value, 'c');
  history.record({value: 'x'});
  assert.equal(history.canRedo, false, 'a new edit clears redo');
});

test('slugify, alt-text check and word count', () => {
  const {EditorialState} = load().window;
  assert.equal(EditorialState.slugify('  Déjà Vu: S3 — FUSE!  '), 'deja-vu-s3-fuse');
  assert.deepEqual(EditorialState.missingAlt([{id: 'i1', type: 'image', data: {alt: ' '}}, {id: 'i2', type: 'image', data: {alt: 'ok'}}, {id: 'p', type: 'paragraph', data: {}}]), ['i1']);
  assert.equal(EditorialState.wordCount([{type: 'paragraph', data: {text: 'one <b>two</b>'}}, {type: 'list', data: {items: [{content: 'three', items: [{content: 'four', items: []}]}]}}]), 4);
});
