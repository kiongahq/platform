// Visits every console view as an administrator and as a provisioned normal
// user, at desktop and narrow widths, asserting layout invariants and saving
// screenshots as evidence under artifacts/screenshots/<project>/.
const {test, expect, ADMIN, USER, signIn, ensureNormalUser, assertLayout, screenshotPath} = require('./fixtures.cjs');

const adminViews = ['overview', 'projects', 'pipelines', 'functions', 'models', 'agents', 'features', 'storage', 'realtime', 'catalog', 'platform', 'access', 'profile', 'settings', 'blogs'];
const userViews = ['overview', 'projects', 'pipelines', 'models', 'features', 'storage', 'catalog', 'profile', 'settings'];

async function openView(page, view) {
  await page.goto(view === "overview" ? "/console" : `/console/${view}`);
  await expect(page.locator('#page-title')).toBeVisible();
  await expect(page.getByText('Loading…', {exact: true})).toHaveCount(0);
}

test.describe('administrator', () => {
  test.beforeEach(async ({page}) => { await signIn(page, ADMIN); });
  for (const view of adminViews) {
    test(`${view} is aligned and usable`, async ({page}, testInfo) => {
      await openView(page, view);
      await assertLayout(page, `admin ${view}`);
      await page.screenshot({path: screenshotPath(testInfo, `admin-${view}`), fullPage: true});
    });
  }

  test('project detail scrolls, keeps context and hands off with the project', async ({page}, testInfo) => {
    await openView(page, 'projects');
    const row = page.getByRole('table').locator('tbody tr').first();
    const trigger = row.getByRole('button', {name: 'Open project', exact: true});
    test.skip(await trigger.count() === 0, 'No projects exist yet');
    await trigger.click();
    await expect(page.getByRole('heading', {name: 'Related resources'})).toBeVisible();
    const id = new URL(page.url()).searchParams.get('resource');
    await expect(page).toHaveURL(new RegExp(`resource=${id}`));
    await assertLayout(page, 'project detail');
    await page.screenshot({path: screenshotPath(testInfo, 'admin-project-detail'), fullPage: true});
    for (const view of ['pipelines', 'agents']) {
      await page.goto(`/console/projects?resource=${id}`);
      const handoff = page.locator(`main a[href="/console/${view}?project=${id}"]`);
      await handoff.click();
      await expect(page.locator('#page-title')).toBeVisible();
      await expect(page).toHaveURL(url => url.pathname === `/console/${view}` && url.searchParams.get('project') === id);
      await expect(page.locator('#project-context')).toHaveValue(id);
    }
  });

  test('refresh fetches current data and preserves the current view', async ({page}) => {
    await openView(page, 'pipelines');
    const refresh = page.getByRole('button', {name: 'Refresh this page'});
    const refreshed = page.waitForResponse(response => response.url().includes('/api/v1/pipelines/runs') && response.request().method() === 'GET');
    await refresh.click();
    expect((await refreshed).ok()).toBeTruthy();
    await expect(page.getByRole('heading', {name: 'Run history', exact: true})).toBeVisible();
  });
});

test.describe('normal user', () => {
  test.beforeAll(async ({baseURL}) => { await ensureNormalUser(baseURL); });
  test.beforeEach(async ({page}) => { await signIn(page, USER); });
  for (const view of userViews) {
    test(`${view} is aligned and usable`, async ({page}, testInfo) => {
      await openView(page, view);
      await assertLayout(page, `user ${view}`);
      await page.screenshot({path: screenshotPath(testInfo, `user-${view}`), fullPage: true});
    });
  }
  test('administrator pages are denied by the API, not only hidden', async ({page}) => {
    await openView(page, 'overview');
    await expect(page.locator('nav a[href="/console/access"]')).toBeHidden();
    const status = await page.evaluate(async () => (await fetch('/api/v1/admin/users')).status);
    expect(status).toBe(403);
  });
});
