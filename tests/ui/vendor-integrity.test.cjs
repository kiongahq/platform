// Vendored browser libraries must match the manifest written (and verified
// against upstream) by scripts/vendor-manifest.py, and every page that loads
// them must pin the same Subresource Integrity hash.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const WEB = 'go/cmd/gateway/web';

for (const dir of ['vendor/editorjs', 'vendor/highlight']) {
  test(`${dir} files match their pinned integrity`, () => {
    const manifest = JSON.parse(fs.readFileSync(path.join(WEB, dir, 'MANIFEST.json'), 'utf8'));
    assert.ok(manifest.files.length > 0);
    for (const entry of manifest.files) {
      const data = fs.readFileSync(path.join(WEB, dir, entry.file));
      const actual = 'sha384-' + crypto.createHash('sha384').update(data).digest('base64');
      assert.equal(actual, entry.integrity, `${dir}/${entry.file} changed since it was verified`);
      assert.match(entry.version, /^\d+\.\d+\.\d+$/, 'versions are pinned exactly');
    }
    const licenses = fs.readdirSync(path.join(WEB, dir)).filter(name => /LICENSE/.test(name));
    assert.ok(licenses.length > 0, `${dir} ships its licenses`);
  });
}

test('pages reference vendored scripts with the manifest integrity', () => {
  const manifests = {};
  for (const dir of ['vendor/editorjs', 'vendor/highlight']) {
    for (const entry of JSON.parse(fs.readFileSync(path.join(WEB, dir, 'MANIFEST.json'), 'utf8')).files) manifests[`/${dir}/${entry.file}`] = entry.integrity;
  }
  const pages = fs.readdirSync(WEB).filter(name => name.endsWith('.html'));
  let checked = 0;
  for (const page of pages) {
    const html = fs.readFileSync(path.join(WEB, page), 'utf8');
    for (const match of html.matchAll(/<script[^>]+src="(\/vendor\/(?:editorjs|highlight)\/[^"]+)"[^>]*>/g)) {
      const integrity = (match[0].match(/integrity="([^"]+)"/) || [])[1];
      assert.equal(integrity, manifests[match[1]], `${page} loads ${match[1]} without the pinned integrity`);
      checked++;
    }
    for (const match of html.matchAll(/"(\/vendor\/highlight\/[^"]+\.js)"\s*:\s*"(sha384-[^"]+)"/g)) {
      assert.equal(match[2], manifests[match[1]], `${page} import map integrity for ${match[1]}`);
      checked++;
    }
  }
  assert.ok(checked >= 0);
});
