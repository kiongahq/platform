// Vendored browser libraries must match the manifest written (and verified
// against upstream) by scripts/vendor-manifest.py.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const WEB = 'frontend/public';

for (const dir of ['vendor/highlight']) {
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
