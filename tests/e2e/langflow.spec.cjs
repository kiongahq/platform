const {test, expect, ADMIN, signIn} = require('./fixtures.cjs');

test('Langflow is a primary agent canvas entry point', async ({page}) => {
  await signIn(page, ADMIN, '/console/langflow');
  await expect(page.locator('#langflow')).toBeVisible();
  await expect(page.locator('[data-view="langflow"]')).toHaveAttribute('aria-current','page');
  await expect(page.locator('#langflow-list')).toContainText('Local Langflow');
  await page.locator('#langflow [data-view-target="agent-designer"]').click();
  await expect(page.locator('#agent-designer')).toBeVisible();
});
