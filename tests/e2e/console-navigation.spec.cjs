const {test, expect} = require('@playwright/test');
const {ADMIN, signIn} = require('./fixtures.cjs');

test('console navigation preserves the shell, session queries and selected theme', async ({page}) => {
  await page.addInitScript(() => {
    localStorage.setItem('kionga.sidebar.width', '224');
  });
  await signIn(page, ADMIN);
  await expect(page.locator('#page-title')).toHaveText('Overview');
  await expect(page.locator('.sidebar-nav a[data-view="projects"]')).toBeAttached();
  await page.evaluate(() => {
    window.navigationShell = document.querySelector('.app-sidebar');
    window.navigationHeader = document.querySelector('.app-topbar');
    document.documentElement.dataset.theme = 'dark';
  });
  const requests = [];
  page.on('request', request => {
    if (request.isNavigationRequest() || new URL(request.url()).pathname === '/api/v1/me')
      requests.push(request.url());
  });
  for (const [view, title] of [['projects', 'Projects'], ['models', 'Models'], ['overview', 'Overview']]) {
    if (await page.locator('#sidebar-toggle').isVisible())
      await page.locator('#sidebar-toggle').click();
    await page.locator(`.sidebar-nav a[data-view="${view}"]`).click();
    await expect(page.locator('#page-title')).toHaveText(title);
    expect(await page.evaluate(() => window.navigationShell === document.querySelector('.app-sidebar') && window.navigationHeader === document.querySelector('.app-topbar'))).toBe(true);
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  }
  await page.goBack();
  await expect(page.locator('#page-title')).toHaveText('Models');
  expect(requests).toEqual([]);
});

test('with a mouse, the sidebar shows icons until the pointer is on it, without moving the page', async ({page}, info) => {
  test.skip(info.project.name !== 'desktop', 'auto-collapse is for hover-capable desktop screens');
  await signIn(page, ADMIN, '/console');
  const sidebar = page.locator('.app-sidebar');
  const main = page.locator('#main');
  const label = sidebar.locator('nav a span').first();
  await page.mouse.move(900, 400);
  await expect.poll(async () => (await sidebar.boundingBox()).width).toBe(64);
  await expect(label).toBeHidden();
  const left = (await main.boundingBox()).x;

  await page.mouse.move(30, 300);
  await expect(label).toBeVisible();
  expect((await sidebar.boundingBox()).width).toBeGreaterThan(64);
  expect((await main.boundingBox()).x).toBe(left);

  await page.mouse.move(900, 400);
  await expect(label).toBeHidden();
  expect((await main.boundingBox()).x).toBe(left);
});

test('keyboard focus in the sidebar shows its labels', async ({page}, info) => {
  test.skip(info.project.name !== 'desktop', 'auto-collapse is for hover-capable desktop screens');
  await signIn(page, ADMIN, '/console');
  await page.mouse.move(900, 400);
  const label = page.locator('.app-sidebar nav a span').first();
  await expect(label).toBeHidden();
  await page.locator('.app-sidebar nav a').first().focus();
  await expect(label).toBeVisible();
});
