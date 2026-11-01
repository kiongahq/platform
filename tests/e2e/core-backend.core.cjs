const { test, expect } = require('@playwright/test');
const { signIn } = require('./fixtures.cjs');
const admin = { username: 'admin', password: 'core-backend-test-password' };

test('Langflow opens the selected instance through the gateway and loads upstream assets', async ({page}) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await signIn(page, admin, '/console/langflow');
  if (test.info().project.name === "narrow") await page.locator("#sidebar-toggle").click();
  await expect(page.locator('[data-view="langflow"]')).toBeVisible();
  await expect(page.locator('[data-view="langflow"]')).toHaveAttribute('aria-current', 'page');
  if (test.info().project.name === "narrow") await page.locator("#sidebar").getByRole("button", {name:"Close navigation"}).click();
  await expect(page.locator('#langflow-list')).toContainText('Local Langflow');
  const launch = page.waitForResponse(response => response.url().endsWith('/api/v1/langflow-workspaces/local/launch') && response.request().method() === 'POST');
  await page.getByRole('button', { name: 'Open canvas', exact: true }).click();
  await expect(page).toHaveURL(/\/workspace\?tool=langflow&instance=local/);
  expect((await launch).status()).toBe(200);
  await expect(page.locator('#workspace-tool')).toHaveValue('langflow');
  await expect(page.locator('#workspace-frame')).toHaveAttribute('src', '/workspaces/langflow/');
  const canvas = page.frameLocator('#workspace-frame');
  await expect(canvas.getByRole('heading', { name: 'Langflow workspace test upstream' })).toBeVisible();
  await expect(canvas.locator('#upstream-status')).toHaveText('Workspace assets loaded through the gateway');
  expect(errors).toEqual([]);
});

test('a Langflow link without an instance opens the chooser without launching another tool', async ({page}) => {
  await signIn(page, admin);
  const launches = [];
  page.on('request', request => { if (new URL(request.url()).pathname.endsWith('/launch')) launches.push(request.url()); });
  await page.goto('/workspace?tool=langflow&project=prj-demo');
  await expect(page).toHaveURL(/\/console\/langflow\?project=prj-demo/);
  await expect(page.locator('#langflow-list')).toContainText('Local Langflow');
  expect(launches).toEqual([]);
});

test('core backend APIs remain JSON endpoints after frontend routing changes', async ({page}) => {
  await signIn(page, admin);
  for (const path of ['/api/v1/health', '/api/v1/me', '/api/v1/projects', '/api/v1/project-options', '/api/v1/pipelines/runs', '/api/v1/pipelines/definitions', '/api/v1/models', '/api/v1/agents', '/api/v1/workspaces', '/api/v1/langflow-workspaces', '/api/v1/langflow-snapshots', '/api/openapi.json']) {
    const response = await page.request.get(path);
    expect(response.status(), path).toBe(200);
    expect(response.headers()['content-type'], path).toContain('application/json');
    await response.json();
  }
});

test('anonymous callers cannot launch or proxy a Langflow workspace', async ({request}) => {
  for (const [method, path] of [['post', '/api/v1/langflow-workspaces/local/launch'], ['get', '/workspaces/langflow/']]) {
    const response = await request[method](path, { maxRedirects: 0 });
    expect([401, 302], path).toContain(response.status());
    expect(await response.text(), path).not.toContain('Langflow workspace test upstream');
  }
});

test('client navigation retains project context and browser history without replacing the document', async ({page}) => {
  await signIn(page, admin, '/console/langflow?project=prj-demo');
  await expect(page.locator('#page-title')).toHaveText('Langflow');
  await page.evaluate(() => { window.__navigationMarker = 'same-document'; });
  if (test.info().project.name === 'narrow') await page.locator('#sidebar-toggle').click();
  await page.locator('[data-view="projects"]').click();
  await expect(page).toHaveURL(/\/console\/projects\?project=prj-demo$/);
  await expect(page.locator('#page-title')).toHaveText('Projects');
  expect(await page.evaluate(() => window.__navigationMarker)).toBe('same-document');
  await page.goBack();
  await expect(page).toHaveURL(/\/console\/langflow\?project=prj-demo$/);
  await expect(page.locator('#page-title')).toHaveText('Langflow');
  expect(await page.evaluate(() => window.__navigationMarker)).toBe('same-document');
});
