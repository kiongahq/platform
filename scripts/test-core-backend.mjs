// Isolated browser-test backend. Only the Langflow upstream is a test double;
// authentication, workspace launch, proxying and platform APIs run in the Go gateway.
import http from 'node:http';
import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const state = await mkdtemp(join(tmpdir(), 'kionga-core-'));
const upstream = http.createServer((request, response) => {
  if (request.url === '/health_check' || request.url === '/healthz') {
    response.setHeader('Content-Type', 'application/json');
    response.end('{"status":"ok"}');
  } else if (request.url === '/') {
    // Verify that the gateway strips platform credentials before proxying.
    if (request.headers.cookie || request.headers.authorization) {
      response.writeHead(500).end('Platform credentials reached the upstream');
      return;
    }
    response.setHeader('Content-Type', 'text/html');
    response.end('<!doctype html><title>Workspace test upstream</title><h1>Langflow workspace test upstream</h1><p id="upstream-status">Loading</p><script src="/workspaces/langflow/assets/workspace.js"></script>');
  } else if (request.url === '/assets/workspace.js') {
    response.setHeader('Content-Type', 'text/javascript');
    response.end('document.querySelector("#upstream-status").textContent = "Workspace assets loaded through the gateway";');
  } else {
    response.writeHead(404).end();
  }
});
await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
// Use a restricted environment so tests cannot connect to a developer's database,
// cluster, identity provider or external integrations.
const environment = {};
for (const key of ['PATH', 'HOME', 'TMPDIR', 'GOCACHE', 'GOPATH', 'GOMODCACHE', 'GOTOOLCHAIN', 'GOROOT', 'GOPROXY', 'GOSUMDB']) {
  if (process.env[key]) environment[key] = process.env[key];
}
Object.assign(environment, {
  PORT: process.env.KIONGA_CORE_TEST_PORT || '18195',
  MLAIOPS_DATA_PATH: join(state, 'platform.json'),
  KIONGA_LANGFLOW_UPSTREAM: `http://127.0.0.1:${upstream.address().port}`,
  MLAIOPS_LOCAL_USERNAME: 'admin',
  MLAIOPS_LOCAL_PASSWORD: 'core-backend-test-password',
});
const gateway = spawn('go', ['run', './cmd/gateway'], {
  cwd: resolve('go'), env: environment, stdio: ['ignore', 'inherit', 'inherit'], detached: true,
});
let stopping = false;
async function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  try { process.kill(-gateway.pid, 'SIGTERM'); } catch { /* already stopped */ }
  await new Promise(resolve => upstream.close(resolve));
  await rm(state, { recursive: true, force: true });
  process.exit(code);
}
process.on('SIGINT', () => void stop());
process.on('SIGTERM', () => void stop());
gateway.on('error', error => { console.error(error.message); void stop(1); });
gateway.on('exit', code => { if (!stopping) void stop(code || 1); });
