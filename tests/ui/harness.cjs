// Builds the console script bundle exactly as console.html loads it. Classic
// scripts share one global lexical scope in the browser; concatenating them
// reproduces that in a single jsdom eval (separate evals would not).
const fs = require('node:fs');
const path = require('node:path');

const WEB = 'go/cmd/gateway/web';
const html = fs.readFileSync(path.join(WEB, 'console.html'), 'utf8');

// Third-party and separately-tested scripts are excluded; tests stub them.
const EXCLUDED = new Set(['/vendor/dagre.min.js', '/pipeline-graph.js', '/huggingface.js']);

function consoleScripts() {
  const sources = [...html.matchAll(/<script src="([^"]+)"/g)].map(match => match[1]).filter(src => !EXCLUDED.has(src));
  if (!sources.length) throw new Error('console.html lists no scripts');
  return sources.map(src => `// ${src}\n${fs.readFileSync(path.join(WEB, src), 'utf8')}`).join('\n;\n');
}

module.exports = {html, consoleScripts};
