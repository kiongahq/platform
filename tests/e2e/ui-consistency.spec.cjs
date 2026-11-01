const {test, expect, ADMIN, signIn, assertLayout, screenshotPath} = require('./fixtures.cjs');
const AxeBuilder = require('@axe-core/playwright').default;
const fs = require('node:fs');
const path = require('node:path');

// Optional source preview against an existing gateway: UI assets are current,
// while authentication and all API responses still come from the real backend.
test.beforeEach(async ({page, baseURL}) => {
  if (process.env.KIONGA_TEST_SOURCE_ASSETS !== '1') return;
  const root = path.resolve(__dirname, '../../go/cmd/gateway/web');
  await page.route('**/*', async route => {
    const url = new URL(route.request().url());
    const file = path.resolve(root, '.' + (url.pathname === '/' ? '/index.html' : url.pathname));
    if (route.request().method() === 'GET' && url.origin === new URL(baseURL).origin && file.startsWith(root + path.sep) && fs.existsSync(file) && fs.statSync(file).isFile()) await route.fulfill({path: file});
    else await route.continue();
  });
});

async function settings(page) {
  await signIn(page, ADMIN, '/console.html?view=settings');
  // Redirect follow-up requests bypass Playwright routing; load current source
  // HTML explicitly when previewing assets over a previously compiled gateway.
  if (process.env.KIONGA_TEST_SOURCE_ASSETS === '1') await page.goto('/console.html?view=settings');
  await expect(page.locator('#settings')).toHaveAttribute('aria-busy', 'false');
}

test('explicit themes override the system and compact content clears the header', async ({page}, testInfo) => {
  await page.emulateMedia({colorScheme: 'light'});
  await settings(page);
  const surface = () => page.evaluate(() => getComputedStyle(document.querySelector('.settings-card')).backgroundColor);
  const light = await surface();
  await page.selectOption('#setting-theme', 'dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect.poll(surface).not.toBe(light);
  await page.check('#setting-compact');
  await expect(page.locator('body')).toHaveClass(/compact-ui/);
  await page.evaluate(() => window.scrollTo(0, 0));
  const header = await page.locator('.app-bar').boundingBox();
  const title = await page.locator('.page-header').boundingBox();
  expect(title.y).toBeGreaterThan(header.y + header.height);
  await assertLayout(page, 'compact dark settings');
  const results = await new AxeBuilder({page}).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(results.violations.filter(v => ['serious', 'critical'].includes(v.impact)).map(v => ({id: v.id, nodes: v.nodes.map(n => n.target)}))).toEqual([]);
  await page.evaluate(() => { document.activeElement?.blur(); window.scrollTo(0, 0); });
  await page.screenshot({path: screenshotPath(testInfo, 'unified-settings-dark'), fullPage: true});
  await page.selectOption('#setting-theme', 'light');
  await expect.poll(surface).toBe(light);
  await page.uncheck('#setting-compact');
  await page.evaluate(() => { document.activeElement?.blur(); window.scrollTo(0, 0); });
  await page.screenshot({path: screenshotPath(testInfo, 'unified-settings-light'), fullPage: true});
});

test('dialogs fit the viewport, scroll the body and restore focus after Cancel', async ({page}) => {
  await settings(page);
  const trigger = page.locator('#create-api-token');
  await trigger.click();
  const dialog = page.locator('#api-token-dialog');
  await expect(dialog).toBeVisible();
  await assertLayout(page, 'API key dialog');
  const box = await dialog.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize().width);
  expect(box.y + box.height).toBeLessThanOrEqual(page.viewportSize().height);
  await dialog.getByRole('button', {name: 'Cancel', exact: true}).click();
  await expect(dialog).not.toBeVisible();
  await expect(trigger).toBeFocused();
});

test('long configuration forms keep their actions visible in a short viewport', async ({page}, testInfo) => {
  await settings(page);
  await page.setViewportSize({width: page.viewportSize().width, height: 520});
  for (const [view, triggerID, dialogID] of [
    ['projects', 'new-project', 'project-dialog'],
    ['pipelines', 'new-pipeline-definition', 'pipeline-definition-dialog'],
    ['agents', 'deploy-agent', 'deploy-agent-dialog'],
    ['access', 'add-user-access', 'access-dialog'],
  ]) {
    await page.goto(`/console.html?view=${view}`);
    await expect(page.locator(`#${view}`)).toHaveAttribute('aria-busy', 'false');
    const trigger = page.locator(`#${triggerID}`);
    await trigger.click();
    const dialog = page.locator(`#${dialogID}`);
    await expect(dialog).toBeVisible();
    await assertLayout(page, `${dialogID} short viewport`);
    const cancel = dialog.getByRole('button', {name: 'Cancel', exact: true});
    await expect(cancel).toBeInViewport();
    const scroll = dialog.locator('.modal-scroll');
    expect(await scroll.evaluate(node => node.scrollHeight > node.clientHeight)).toBe(true);
    await scroll.evaluate(node => { node.scrollTop = node.scrollHeight; });
    await expect(cancel).toBeInViewport();
    if (view === 'access') await page.screenshot({path: screenshotPath(testInfo, 'unified-provisioning-dialog')});
    await cancel.click();
    await expect(trigger).toBeFocused();
  }
});

test('confirmation defaults to Cancel and Escape cancels without losing focus', async ({page}) => {
  await settings(page);
  await page.locator('#create-api-token').focus();
  await page.evaluate(() => {
    window.confirmationResult = undefined;
    confirmAction('A tool using this key will lose access.', {title: 'Revoke API key', confirmLabel: 'Revoke key'}).then(value => { window.confirmationResult = value; });
  });
  const dialog = page.getByRole('dialog', {name: 'Revoke API key'});
  await expect(dialog.getByRole('button', {name: 'Cancel', exact: true})).toBeFocused();
  await assertLayout(page, 'confirmation');
  await page.keyboard.press('Escape');
  await expect.poll(() => page.evaluate(() => window.confirmationResult)).toBe(false);
  await expect(page.locator('#create-api-token')).toBeFocused();
  await page.evaluate(() => { confirmAction('Restore the selected revision.', {title: 'Restore revision', confirmLabel: 'Restore', destructive: false}).then(value => { window.confirmationResult = value; }); });
  await page.getByRole('dialog', {name: 'Restore revision'}).getByRole('button', {name: 'Restore', exact: true}).click();
  await expect.poll(() => page.evaluate(() => window.confirmationResult)).toBe(true);
  await page.evaluate(() => { requestNote('Explain this decision.', {title: 'Reject access request', confirmLabel: 'Reject', required: true}).then(value => { window.noteResult = value; }); });
  await page.getByRole('dialog', {name: 'Reject access request'}).getByRole('button', {name: 'Cancel', exact: true}).click();
  await expect.poll(() => page.evaluate(() => window.noteResult)).toBe(null);
});

test('notifications remain visible above an open dialog without moving focus', async ({page}) => {
  await settings(page);
  await page.locator('#create-api-token').click();
  await page.evaluate(() => toast('Your changes have been saved.'));
  const notification = page.locator('#toast');
  await expect(notification).toBeVisible();
  expect(await notification.evaluate(node => node.matches(':popover-open'))).toBe(true);
  expect(await page.evaluate(() => document.activeElement.closest('dialog')?.id)).toBe('api-token-dialog');
  await page.keyboard.press('Escape');
  await expect(page.locator('#api-token-dialog')).not.toBeVisible();
});

test('mobile navigation hides its controls from keyboard access and closes with Escape', async ({page}) => {
  await settings(page);
  await page.setViewportSize({width: 390, height: 844});
  await expect(page.locator('#sidebar')).toHaveJSProperty('inert', true);
  await expect(page.locator('#sidebar-toggle')).toHaveAttribute('aria-expanded', 'false');
  await page.locator('#sidebar-toggle').click();
  await expect(page.locator('#sidebar')).toHaveJSProperty('inert', false);
  await expect(page.locator('#sidebar [data-view="settings"]')).toBeFocused();
  await expect(page.locator('#main')).toHaveJSProperty('inert', true);
  await page.keyboard.press('Escape');
  await expect(page.locator('#sidebar-toggle')).toBeFocused();
  await expect(page.locator('#sidebar')).toHaveJSProperty('inert', true);
  await page.setViewportSize({width: 1440, height: 900});
  await expect(page.locator('#sidebar')).toHaveJSProperty('inert', false);
  await expect(page.locator('#main')).toHaveJSProperty('inert', false);
  await expect(page.locator('.sidebar-footer')).toBeInViewport();
});

test('console views share layout and accessible control names', async ({page}, testInfo) => {
  test.setTimeout(180_000);
  await settings(page);
  for (const view of ['overview', 'projects', 'pipelines', 'functions', 'models', 'agents', 'agent-designer', 'langflow', 'features', 'storage', 'realtime', 'catalog', 'logs', 'platform', 'access', 'profile', 'settings']) {
    await page.goto(`/console.html?view=${view}`);
    await expect(page.locator(`#${view}.active`)).toHaveAttribute('aria-busy', 'false');
    await assertLayout(page, view);
    const scan = await new AxeBuilder({page}).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
    expect(scan.violations.filter(v => ['serious', 'critical'].includes(v.impact)).map(v => ({id: v.id, nodes: v.nodes.map(n => n.target)})), `${view}: accessibility`).toEqual([]);
    if (['projects', 'pipelines', 'access', 'agent-designer'].includes(view)) await page.screenshot({path: screenshotPath(testInfo, `unified-${view}`), fullPage: true});
  }
});

test('public, developer and editorial surfaces use shared tokens without horizontal overflow', async ({page}, testInfo) => {
  await settings(page);
  for (const url of ['/', '/login.html', '/api-docs.html', '/blogs.html', '/editorial.html', '/workspace.html?tool=ide']) {
    await page.goto(url);
    expect(await page.locator('link[href="/tokens.css"]').count()).toBe(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
    await page.screenshot({path: screenshotPath(testInfo, `unified-${url.replace(/[^a-z]/g, '') || 'landing'}`), fullPage: true});
  }
});
