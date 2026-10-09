const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const web = path.resolve(__dirname, '../../go/cmd/gateway/web');

test('bundled object-store preset targets RustFS, not a legacy hostname', () => {
  const view = fs.readFileSync(path.join(web, 'js/views/platform.js'), 'utf8');
  const html = fs.readFileSync(path.join(web, 'console.html'), 'utf8');
  assert.match(view, /"Object Store": \{type: "rustfs", endpoint: "http:\/\/objectstore:9000\/health"\}/);
  assert.match(html, /<option value="rustfs">RustFS \(local\)<\/option>/);
  assert.doesNotMatch(view, /http:\/\/minio:/);
});
