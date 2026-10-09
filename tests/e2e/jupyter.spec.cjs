// End-to-end Jupyter acceptance: an authorized signed-in user opens the
// workbench through the gateway, sees shared project files, creates a
// notebook and starts a kernel that reaches the idle state.
const {test, expect, ADMIN, signIn} = require('./fixtures.cjs');

test('workbench readiness, launch, notebook and kernel', async ({page}) => {
  await signIn(page, ADMIN);
  const workspaces = await page.evaluate(async () => (await (await fetch('/api/v1/workspaces')).json()).items);
  const workbench = workspaces.find(item => item.id === 'workbench');
  expect(workbench.state, workbench.message).toBe('ready');

  const projects = await page.evaluate(async () => (await (await fetch('/api/v1/project-options')).json()).items);
  const project = projects[0];
  test.skip(!project, 'No project to open');
  const launch = await page.evaluate(async id => {
    const response = await fetch('/api/v1/workspaces/workbench/launch', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({project_id: id})});
    return {status: response.status, body: await response.json()};
  }, project.id);
  expect(launch.status, JSON.stringify(launch.body)).toBe(200);
  expect(launch.body.url).toContain(`/workspaces/workbench/lab/tree/projects/${project.namespace}`);

  // The workspace page embeds JupyterLab for this project.
  await page.goto(`/workspace.html?tool=workbench&project=${encodeURIComponent(project.id)}`);
  const frame = page.frameLocator('#workspace-frame');
  await expect(frame.locator('#jp-main-dock-panel, .jp-LabShell').first()).toBeVisible({timeout: 60_000});

  const result = await page.evaluate(async namespace => {
    const base = '/workspaces/workbench/api';
    const folder = await fetch(`${base}/contents/projects/${namespace}`);
    const notebookPath = `projects/${namespace}/e2e-${Date.now()}.ipynb`;
    const created = await fetch(`${base}/contents/${notebookPath}`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({type: 'notebook', content: {cells: [], metadata: {}, nbformat: 4, nbformat_minor: 5}})});
    const kernel = await (await fetch(`${base}/kernels`, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({name: 'python3'})})).json();
    // Jupyter Server 2 speaks the v1 binary kernel protocol: a count of
    // offsets, the offsets (uint64 LE), then channel, header, parent header,
    // metadata and content as UTF-8 JSON parts.
    const encode = (channel, parts) => {
      const encoder = new TextEncoder();
      const chunks = [channel, ...parts.map(part => JSON.stringify(part))].map(text => encoder.encode(text));
      const count = chunks.length + 1;
      const header = new DataView(new ArrayBuffer(8 * (count + 1)));
      header.setBigUint64(0, BigInt(count), true);
      let offset = 8 * (count + 1);
      chunks.forEach((chunk, i) => { header.setBigUint64(8 * (i + 1), BigInt(offset), true); offset += chunk.length; });
      header.setBigUint64(8 * count, BigInt(offset), true);
      return new Blob([header.buffer, ...chunks]);
    };
    const decode = buffer => {
      const view = new DataView(buffer);
      const count = Number(view.getBigUint64(0, true));
      const offsets = Array.from({length: count}, (_, i) => Number(view.getBigUint64(8 * (i + 1), true)));
      const text = (start, stop) => new TextDecoder().decode(buffer.slice(start, stop));
      return {channel: text(offsets[0], offsets[1]), header: JSON.parse(text(offsets[1], offsets[2]))};
    };
    const state = await new Promise(resolve => {
      const socket = new WebSocket(`${location.origin.replace('http', 'ws')}${base}/kernels/${kernel.id}/channels`, ['v1.kernel.websocket.jupyter.org']);
      socket.binaryType = 'arraybuffer';
      const timer = setTimeout(() => resolve('timeout'), 30000);
      socket.onopen = () => socket.send(encode('shell', [{msg_id: 'e2e', username: 'e2e', session: 'e2e', msg_type: 'kernel_info_request', version: '5.3', date: new Date().toISOString()}, {}, {}, {}]));
      socket.onmessage = event => {
        if (typeof event.data === 'string') return;
        const message = decode(event.data);
        if (message.header.msg_type === 'kernel_info_reply') { clearTimeout(timer); socket.close(); resolve('kernel_info_reply'); }
      };
      socket.onerror = () => { clearTimeout(timer); resolve('socket error'); };
    });
    await fetch(`${base}/kernels/${kernel.id}`, {method: 'DELETE'});
    await fetch(`${base}/contents/${notebookPath}`, {method: 'DELETE'});
    return {folder: folder.status, created: created.status, kernel: Boolean(kernel.id), state};
  }, project.namespace);
  expect(result).toEqual({folder: 200, created: 201, kernel: true, state: 'kernel_info_reply'});
});
