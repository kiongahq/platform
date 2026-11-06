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
