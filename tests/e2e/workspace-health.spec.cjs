const {test, expect, ADMIN, signIn} = require('./fixtures.cjs');

test('browser IDE opens shared files over its remote WebSocket', async ({page}) => {
  const socketErrors = [];
  page.on('console', message => {
    if (message.type() === 'error' && /WebSocket close with status code 1006|No file system provider found/.test(message.text())) {
      socketErrors.push(message.text());
    }
  });
  await signIn(page, ADMIN, '/workspace.html?tool=ide');
  await page.goto('/workspace.html?tool=ide');
  const frame = page.frameLocator('#workspace-frame');
  await expect(frame.getByText('quickstart.ipynb', {exact: true})).toBeVisible({timeout: 45_000});
  expect(socketErrors).toEqual([]);
});

test('quickstart notebook receives a file ID and opens without an error dialog', async ({page}) => {
  await signIn(page, ADMIN);
  const fileIDs = [];
  page.on('response', async response => {
    if (response.url().includes('/api/fileid/index?path=quickstart.ipynb')) {
      fileIDs.push({status: response.status(), body: await response.json()});
    }
  });
  await page.goto('/workspaces/workbench/lab/tree/quickstart.ipynb');
  await expect(page.getByRole('tabpanel', {name: 'quickstart.ipynb'})).toBeVisible({timeout: 45_000});
  await expect.poll(() => fileIDs.length).toBeGreaterThan(0);
  expect(fileIDs[0].status).toBe(200);
  expect(fileIDs[0].body.id).toMatch(/^[a-f0-9-]{36}$/);
  await expect(page.getByText('File ID error')).toHaveCount(0);
});

test('Jupyter extension manager lists installed extensions without PyPI search errors', async ({page}) => {
  await signIn(page, ADMIN);
  await page.goto('/workspaces/workbench/lab/');
  await page.locator('[title="Extension Manager"][role="tab"]').click();
  await expect(page.getByText('READ-ONLY MANAGER')).toBeVisible();
  const response = await page.request.get('/workspaces/workbench/lab/api/extensions?refresh=1');
  expect(response.status()).toBe(200);
  expect(Array.isArray(await response.json())).toBe(true);
  await expect(page.getByText('Error searching for extensions')).toHaveCount(0);
});
