const {test, expect} = require('@playwright/test');
const {ADMIN, signIn} = require('./fixtures.cjs');

test('project creation offers a no-template path even without a template catalog', async ({page}) => {
  await signIn(page, ADMIN);
  await page.route('**/api/v1/project-templates', route => route.fulfill({status: 503, json: {message: 'Catalog unavailable'}}));
  await page.goto('/console/projects');
  await page.getByRole('button', {name: 'New project', exact: true}).click();
  await expect(page.getByText('An open project with no generated files or prescribed framework.', {exact: false})).toBeVisible();
  await expect(page.getByLabel(/Framework or language/)).toBeVisible();
  await page.getByLabel(/Framework or language/).fill('Rust');
  await expect(page.getByRole('button', {name: 'Create project', exact: true})).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('agent designer starts empty and pipeline examples can be cleared', async ({page}) => {
  await signIn(page, ADMIN);
  await page.goto('/console/agent-designer');
  await expect(page.locator('#design-new')).toHaveText('Start from scratch');
  await expect(page.locator('.react-flow__node')).toHaveCount(0);
  await page.goto('/console/pipeline-editor');
  await page.getByRole('button', {name: 'Start from scratch', exact: true}).click();
  await expect(page.getByRole('button', {name: 'Add node', exact: true})).toBeVisible();
  await expect(page.locator('.react-flow__node')).toHaveCount(0);
});
