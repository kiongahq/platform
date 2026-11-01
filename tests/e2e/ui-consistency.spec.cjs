const {test, expect, ADMIN, signIn, assertLayout, screenshotPath} = require('./fixtures.cjs');
const AxeBuilder = require('@axe-core/playwright').default;
async function settings(page) {
  await signIn(page, ADMIN, '/console/settings');
  await expect(page.getByRole('heading', {name: 'Preferences', exact: true})).toBeVisible();
}

test('light is the default regardless of system theme and explicit dark selection persists', async ({page}, testInfo) => {
  await page.emulateMedia({colorScheme: 'dark'});
  await settings(page);
  await expect(page.getByRole('combobox', {name:'Theme',exact:true})).toHaveValue('light');
  await expect(page.locator('html')).toHaveAttribute('data-theme','light');
  await expect(page.getByRole('combobox',{name:'Theme',exact:true}).locator('option')).toHaveCount(2);
  const surface = () => page.evaluate(() => getComputedStyle(document.querySelector('.panel')).backgroundColor);
  const light = await surface();
  await page.getByRole('combobox', {name: 'Theme', exact: true}).selectOption( 'dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect.poll(surface).not.toBe(light);
  await page.reload();
  await expect(page.getByRole('combobox',{name:'Theme',exact:true})).toHaveValue('dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme','dark');
  await page.getByLabel('Compact density').check();
  await expect(page.locator('body')).toHaveClass(/compact-ui/);
  await page.evaluate(() => window.scrollTo(0, 0));
  const header = await page.locator('.app-topbar').boundingBox();
  const title = await page.locator('main > .page-header').boundingBox();
  expect(title.y).toBeGreaterThan(header.y + header.height);
  await assertLayout(page, 'compact dark settings');
  const results = await new AxeBuilder({page}).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(results.violations.filter(v => ['serious', 'critical'].includes(v.impact)).map(v => ({id: v.id, nodes: v.nodes.map(n => n.target)}))).toEqual([]);
  await page.evaluate(() => { document.activeElement?.blur(); window.scrollTo(0, 0); });
  await page.screenshot({path: screenshotPath(testInfo, 'unified-settings-dark'), fullPage: true});
  await page.getByRole('combobox', {name: 'Theme', exact: true}).selectOption( 'light');
  await expect.poll(surface).toBe(light);
  await page.getByLabel('Compact density').uncheck();
  await page.evaluate(() => { document.activeElement?.blur(); window.scrollTo(0, 0); });
  await page.screenshot({path: screenshotPath(testInfo, 'unified-settings-light'), fullPage: true});
});

test('dialogs fit the viewport, scroll the body and restore focus after closing', async ({page}) => {
  await settings(page);
  const trigger = page.getByRole('button', {name: 'Create API key', exact: true});
  await trigger.click();
  const dialog = page.getByRole('dialog', {name: 'Create personal API key'});
  await expect(dialog).toBeVisible();
  await assertLayout(page, 'API key dialog');
  const box = await dialog.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize().width);
  expect(box.y + box.height).toBeLessThanOrEqual(page.viewportSize().height);
  await dialog.getByRole('button', {name: 'Close Create personal API key', exact: true}).click();
  await expect(dialog).not.toBeVisible();
  await expect(trigger).toBeFocused();
});

test('long configuration forms keep their actions reachable in a short viewport', async ({page}, testInfo) => {
  await settings(page);
  await page.setViewportSize({width: page.viewportSize().width, height: 520});
  for (const [view, triggerName, dialogName] of [
    ['projects', 'New project', 'New project'],
    ['agents', 'Deploy agent', 'Deploy agent'],
    ['access', 'Provision user', 'Provision user'],
  ]) {
    await page.goto(view === "overview" ? "/console" : `/console/${view}`);
    const trigger = page.getByRole('button', {name: triggerName, exact: true});
    await trigger.click();
    const dialog = page.getByRole('dialog', {name: dialogName, exact: true});
    await expect(dialog).toBeVisible();
    await assertLayout(page, `${dialogName} short viewport`);
    const box = await dialog.boundingBox();
    expect(box.y).toBeGreaterThanOrEqual(0);
    expect(box.y + box.height).toBeLessThanOrEqual(page.viewportSize().height);
    await dialog.evaluate(node => { node.scrollTop = node.scrollHeight; });
    await expect(dialog.locator('button[type=submit]')).toBeInViewport();
    if (view === 'access') await page.screenshot({path: screenshotPath(testInfo, 'unified-provisioning-dialog')});
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
  }
});

test('revocation confirmation cancels without changing the key and accepts explicitly', async ({page}) => {
  await settings(page);
  const name = `confirmation-${Date.now()}`;
  const created = await page.request.post('/api/v1/settings/tokens', {data: {name, expires_in_days: 1, services: ['overview'], project_ids: []}});
  expect(created.ok(), await created.text()).toBeTruthy();
  await page.reload();
  const row = page.getByRole('row').filter({hasText: name});
  const trigger = row.getByRole('button', {name: 'Revoke', exact: true});
  page.once('dialog', async dialog => { expect(dialog.type()).toBe('confirm'); await dialog.dismiss(); });
  await trigger.click();
  await expect(trigger).toBeVisible();
  page.once('dialog', dialog => dialog.accept());
  const revoked = page.waitForResponse(response => response.url().includes('/api/v1/settings/tokens/') && response.request().method() === 'DELETE');
  await trigger.click();
  expect((await revoked).status()).toBe(204);
  await expect(row).toContainText('revoked');
});

test('a rejected form request leaves the dialog usable and explains the failure', async ({page}) => {
  await settings(page);
  await page.getByRole('button', {name: 'Create API key', exact: true}).click();
  const dialog = page.getByRole('dialog', {name: 'Create personal API key'});
  await page.route('**/api/v1/settings/tokens', route => route.request().method() === 'POST'
    ? route.fulfill({status: 403, contentType: 'application/json', body: JSON.stringify({error: 'invalid_scope', message: 'Token scope is not assigned to this account.'})})
    : route.continue());
  await dialog.getByLabel('Key name', {exact: true}).fill('rejected-test');
  await dialog.getByRole('checkbox', {name: 'overview', exact: true}).check();
  await dialog.locator('button[type=submit]').click();
  await expect(dialog.getByRole('alert')).toContainText('Token scope is not assigned');
  await expect(dialog).toBeVisible();
  await expect(dialog.locator('button[type=submit]')).toBeEnabled();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('button', {name: 'Create API key', exact: true})).toBeFocused();
});

test('mobile navigation hides its controls from keyboard access and closes with Escape', async ({page}) => {
  await settings(page);
  await page.setViewportSize({width: 390, height: 844});
  await expect(page.locator('#sidebar')).toHaveJSProperty('inert', true);
  await expect(page.locator('#sidebar-toggle')).toHaveAttribute('aria-expanded', 'false');
  await page.locator('#sidebar-toggle').click();
  await expect(page.locator('#sidebar')).toHaveJSProperty('inert', false);
  await expect(page.locator('#sidebar a[aria-current="page"]')).toBeFocused();
  await expect(page.locator('.app-content')).toHaveJSProperty('inert', true);
  await page.keyboard.press('Escape');
  await expect(page.locator('#sidebar-toggle')).toBeFocused();
  await expect(page.locator('#sidebar')).toHaveJSProperty('inert', true);
  await page.setViewportSize({width: 1440, height: 900});
  await expect(page.locator('#sidebar')).toHaveJSProperty('inert', false);
  await expect(page.locator('.app-content')).toHaveJSProperty('inert', false);
  await expect(page.locator('#sidebar')).toBeInViewport();
});

test('console views share layout and accessible control names', async ({page}, testInfo) => {
  test.setTimeout(180_000);
  await settings(page);
  for (const view of ['overview', 'projects', 'pipelines', 'functions', 'models', 'agents', 'agent-designer', 'langflow', 'features', 'storage', 'realtime', 'catalog', 'logs', 'platform', 'access', 'profile', 'settings']) {
    await page.goto(view === "overview" ? "/console" : `/console/${view}`);
    await expect(page.locator('#page-title')).toBeVisible();
      await expect(page.getByText('Loading…', {exact: true})).toHaveCount(0);
    await assertLayout(page, view);
    const scan = await new AxeBuilder({page}).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
    expect(scan.violations.filter(v => ['serious', 'critical'].includes(v.impact)).map(v => ({id: v.id, nodes: v.nodes.map(n => n.target)})), `${view}: accessibility`).toEqual([]);
    if (['projects', 'pipelines', 'access', 'agent-designer'].includes(view)) await page.screenshot({path: screenshotPath(testInfo, `unified-${view}`), fullPage: true});
  }
});

test('public, developer and editorial surfaces use shared tokens without horizontal overflow', async ({page}, testInfo) => {
  await settings(page);
  for (const url of ['/', '/login', '/api-docs', '/blog', '/studio', '/workspace?tool=ide']) {
    await page.goto(url);
    await expect(page.locator('link[rel="stylesheet"]').first()).toBeAttached();
    expect(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--color-paper').trim())).not.toBe('');
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
    await page.screenshot({path: screenshotPath(testInfo, `unified-${url.replace(/[^a-z]/g, '') || 'landing'}`), fullPage: true});
  }
});
