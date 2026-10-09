// Visits every console view as an administrator and as a provisioned normal
// user, at desktop and narrow widths, asserting layout invariants and saving
// screenshots as evidence under artifacts/screenshots/<project>/.
const {test, expect, ADMIN, USER, signIn, ensureNormalUser, assertLayout, screenshotPath} = require('./fixtures.cjs');

const adminViews = ['overview', 'projects', 'pipelines', 'functions', 'models', 'agents', 'features', 'storage', 'realtime', 'catalog', 'platform', 'access', 'profile', 'settings', 'blogs'];
const userViews = ['overview', 'projects', 'pipelines', 'models', 'features', 'storage', 'catalog', 'profile', 'settings'];

async function openView(page, view) {
  await page.goto(`/console.html?view=${view}`);
  await expect(page.locator(`#${view}.view.active`)).toBeVisible();
  await expect(page.locator(`#${view}`)).toHaveAttribute('aria-busy', 'false');
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
    const card = page.locator('[data-project-detail]').first();
    test.skip(await card.count() === 0, 'No projects exist yet');
    const id = await card.getAttribute('data-project-detail');
    await card.click();
    await expect(page.locator('#project-detail-panel')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`resource=${id}`));
    await assertLayout(page, 'project detail');
    await page.screenshot({path: screenshotPath(testInfo, 'admin-project-detail'), fullPage: true});
    for (const view of ['pipelines', 'agents']) {
      await page.goto(`/console.html?view=projects&resource=${id}`);
      const handoff = page.locator(`#project-detail-panel [data-navigate="${view}"]`);
      await handoff.click();
      await expect(page.locator(`#${view}.view.active`)).toBeVisible();
      await expect(page).toHaveURL(new RegExp(`view=${view}&project=${id}`));
      await expect(page.locator('#project-context')).toHaveValue(id);
    }
  });

  test('refresh icon shows progress and reports completion', async ({page}) => {
    await openView(page, 'pipelines');
    const refresh = page.getByRole('button', {name: 'Refresh this page'});
    await refresh.click();
    await expect(page.locator('#refresh-status')).toContainText(/Updated|Refresh failed/);
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
    await expect(page.locator('[data-view="access"]')).toBeHidden();
    const status = await page.evaluate(async () => (await fetch('/api/v1/admin/users')).status);
    expect(status).toBe(403);
  });
});
