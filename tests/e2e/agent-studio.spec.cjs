const {test, expect, ADMIN, signIn, screenshotPath, assertLayout} = require('./fixtures.cjs');

// Build → validate → test → save a test case → evaluate → publish, in a real
// browser against a gateway with the Agent Designer runner (for example
// `KEEP_RUNNING=1 scripts/agent-designer-smoke.sh`, mock model).
test('native agent studio: template to published version', async ({page}, testInfo) => {
  test.skip(testInfo.project.name === 'narrow', 'the studio workflow is exercised once; layout is checked on desktop');
  await signIn(page, ADMIN, '/console.html?view=agent-designer');
  await expect(page.locator('#agent-designer')).toHaveClass(/active/);
  await expect(page.locator('#design-kpis')).toContainText('Designs');

  await page.locator('#design-template').selectOption('customer-support');
  await expect(page.locator('.design-node')).toHaveCount(7);
  await page.locator('#design-zoom-fit').click();
  await page.locator('#design-name').fill(`studio-e2e-${Date.now()}`);
  await page.locator('#design-save').click();
  await expect(page.locator('#design-message')).toContainText('Saved revision 1.');

  await page.locator('#design-validate').click();
  await expect(page.locator('#design-validation')).toContainText('Runnable');

  await page.locator('[data-design-tab="playground"]').click();
  await page.locator('#design-run-input').fill('I want a refund for order 42');
  await page.locator('#design-run-start').click();
  await expect(page.locator('#design-run-summary')).toContainText('Succeeded in', {timeout: 30_000});
  await expect(page.locator('#design-run-timeline tr')).not.toHaveCount(0);
  await expect(page.locator('#design-run-timeline')).toContainText('skipped');
  await expect(page.locator('[data-design-node="refund"]')).toHaveClass(/run-succeeded/);
  await expect(page.locator('[data-design-node="general"]')).toHaveClass(/run-skipped/);
  await assertLayout(page, 'agent studio playground');
  await page.screenshot({path: screenshotPath(testInfo, 'agent-studio-playground'), fullPage: true});

  await page.locator('#design-run-save-case').click();
  await expect(page.locator('[data-design-panel="tests"]')).toBeVisible();
  await page.locator('#design-case-save').click();
  await expect(page.locator('#design-message')).toContainText('Saved 1 test case.');

  await page.locator('[data-design-tab="evaluations"]').click();
  await page.locator('#design-eval-gate').fill('100');
  await page.locator('#design-eval-form button[type="submit"]').click();
  await expect(page.locator('#design-message')).toContainText('Evaluation completed', {timeout: 60_000});
  await expect(page.locator('#design-evaluations')).toContainText('passed');
  await page.screenshot({path: screenshotPath(testInfo, 'agent-studio-evaluations'), fullPage: true});

  await page.locator('[data-design-tab="versions"]').click();
  await page.locator('#design-publish-evaluation').selectOption({index: 1});
  await page.locator('#design-publish-notes').fill('first release');
  await page.locator('#design-publish-form button[type="submit"]').click();
  await expect(page.locator('#design-message')).toContainText('Published v1.');
  await expect(page.locator('#design-versions')).toContainText('first release');
  await assertLayout(page, 'agent studio versions');
  await page.screenshot({path: screenshotPath(testInfo, 'agent-studio-versions'), fullPage: true});
});
