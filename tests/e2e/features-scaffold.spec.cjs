// Phase 9/10 acceptance: run a project scaffold in the workspace from the
// console, and see feature stores and data connections with honest states.
//   KIONGA_URL=http://localhost:18194 npx playwright test tests/e2e/features-scaffold.spec.cjs
// Optional: KIONGA_E2E_FEAST_URL points at a reachable Feast server; without
// it the spec checks that an unreachable store is reported as unavailable.
const {test, expect, ADMIN, signIn, assertLayout, screenshotPath} = require('./fixtures.cjs');

const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;

async function createProject(page) {
  const response = await page.request.post('/api/v1/projects', {data: {name: `E2E scaffold ${suffix}`, template: 'blank-python', framework: 'python', accelerator: 'cpu', requested_profile: 'starter'}});
  expect(response.ok(), await response.text()).toBeTruthy();
  return response.json();
}

test.describe('scaffold and feature stores', () => {
  test.beforeEach(async ({page}) => { await signIn(page, ADMIN); });

  test('run in workspace confirms the exact command and shows the result', async ({page}, testInfo) => {
    const project = await createProject(page);
    await page.goto(`/console.html?view=projects&resource=${project.id}`);
    const panel = page.locator('#scaffold-panel');
    await expect(panel).toContainText(`/workspace/projects/${project.namespace}`);
    await expect(panel.getByRole('button', {name: /Copy command/})).toBeVisible();
    const plan = await (await page.request.get(`/api/v1/projects/${project.id}/scaffold-plan`)).json();
    await panel.getByRole('button', {name: 'Run in workspace…'}).click();
    const dialog = page.locator('#scaffold-confirm-dialog');
    await expect(dialog).toBeVisible();
    await expect(page.locator('#scaffold-confirm-command')).toHaveText(project.scaffold_command);
    await expect(dialog.locator('.argv-list li')).toHaveCount(plan.argv.length);
    await page.screenshot({path: screenshotPath(testInfo, 'scaffold-confirm'), fullPage: true});
    if (!plan.available) {
      await expect(page.locator('#scaffold-confirm-run')).toBeDisabled();
      await expect(page.locator('#scaffold-confirm-error')).toHaveText(plan.reason);
      test.skip(true, `No workspace can run scaffold jobs here: ${plan.reason}`);
    }
    await page.locator('#scaffold-confirm-run').click();
    await expect(dialog).toBeHidden();
    const progress = page.locator('#scaffold-run');
    await expect(progress).toContainText(/succeeded|failed/, {timeout: 60_000});
    await expect(progress).toContainText('succeeded');
    await expect(progress).toContainText('README.md');
    await expect(progress).toContainText(/Created \d+ files/);
    await page.evaluate(() => window.scrollTo(0, 0)); // the sticky app bar overlaps scrolled content
    await assertLayout(page, 'project scaffold result');
    await page.screenshot({path: screenshotPath(testInfo, 'scaffold-result'), fullPage: true});

    // Running again must fail honestly: the folder already has files.
    await page.reload();
    await page.locator('#scaffold-panel').getByRole('button', {name: 'Run in workspace…'}).click();
    await page.locator('#scaffold-confirm-run').click();
    await expect(page.locator('#scaffold-run [role=alert]')).toBeVisible({timeout: 60_000});
    await expect(page.locator('#scaffold-run')).toContainText(/not empty/);
  });

  test('features view shows stores and freshness; platform tests connections', async ({page}, testInfo) => {
    const name = `e2e_view_${suffix}`;
    const applied = await page.request.post('/api/v1/features', {data: {name, entity: 'user_id', fields: [{name: 'plan', type: 'string'}], source: 's3://raw/e2e', ttl_seconds: 3600}});
    expect(applied.ok(), await applied.text()).toBeTruthy();
    const reported = await page.request.post(`/api/v1/features/${name}/materializations`, {data: {run_id: `mat-${suffix}`, source_dataset: 's3://raw/e2e', entity_count: 2}});
    expect(reported.ok(), await reported.text()).toBeTruthy();
    const feastURL = process.env.KIONGA_E2E_FEAST_URL || 'http://127.0.0.1:9';
    const created = await page.request.post('/api/v1/admin/feature-stores', {data: {provider: 'feast', name: `e2e-feast-${suffix}`, config: {url: feastURL}}});
    expect(created.ok(), await created.text()).toBeTruthy();
    const store = await created.json();

    await page.goto('/console.html?view=platform');
    const item = page.locator(`#feature-store-connections [data-store-id="${store.id}"]`);
    await expect(item).toContainText('configured');
    await item.getByRole('button', {name: 'Test'}).click();
    await expect(item.locator('.status')).toHaveText(process.env.KIONGA_E2E_FEAST_URL ? 'healthy' : 'unavailable', {timeout: 20_000});
    await expect(page.locator('#feature-store-connections [data-store-id="internal"]')).toBeVisible();
    await page.evaluate(() => window.scrollTo(0, 0));
    await assertLayout(page, 'platform data connections');
    await page.screenshot({path: screenshotPath(testInfo, 'platform-data-connections'), fullPage: true});

    await page.goto('/console.html?view=features');
    await expect(page.locator('#feature-store-list [data-store-id="internal"]')).toContainText('Internal');
    await expect(page.locator(`#feature-store-list [data-store-id="${store.id}"]`)).toContainText('All projects');
    const card = page.locator('.feature-card', {hasText: name});
    await expect(card.locator('.status.fresh')).toBeVisible();
    await expect(card).toContainText('v1');
    await card.click();
    await expect(page.locator('#metadata-detail')).toContainText(`mat-${suffix}`);
    await page.keyboard.press('Escape');
    await page.evaluate(() => window.scrollTo(0, 0));
    await assertLayout(page, 'features with stores');
    await page.screenshot({path: screenshotPath(testInfo, 'features-stores'), fullPage: true});

    const removed = await page.request.delete(`/api/v1/admin/feature-stores/${store.id}`);
    expect(removed.status()).toBe(204);
  });
});
