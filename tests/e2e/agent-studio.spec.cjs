const {test, expect, ADMIN, signIn, screenshotPath, assertLayout} = require('./fixtures.cjs');

// Guide screenshots hide the fixed console chrome that would overlay a panel.
const DOC_CAPTURE = '.app-bar, .skip-link { visibility: hidden !important; }';

// Build → validate → test → save a test case → evaluate → publish, in a real
// browser against a gateway with the Agent Designer runner (for example
// `KEEP_RUNNING=1 scripts/agent-designer-smoke.sh`, mock model).
test('native agent studio: template to published version', async ({page}, testInfo) => {
  test.skip(testInfo.project.name === 'narrow', 'the studio workflow is exercised once; layout is checked on desktop');
  await signIn(page, ADMIN, '/console/agent-designer');
  await expect(page.locator('#agent-designer')).toBeVisible();
  await expect(page.locator('#design-kpis')).toContainText('Designs');

  await page.locator('#design-template').selectOption('customer-support');
  await expect(page.locator('.design-node')).toHaveCount(7);
  await page.getByRole('button', {name: 'Fit graph', exact: true}).click();
  await page.locator('#design-name').fill(`studio-e2e-${Date.now()}`);
  await page.locator('#design-save').click();
  await expect(page.locator('#design-message')).toContainText('Saved revision 1.');

  await page.getByRole('tab', {name: 'Validation', exact: true}).click();
  await page.getByRole('button', {name: 'Validate graph', exact: true}).click();
  await expect(page.getByRole('tabpanel')).toContainText('Runnable');

  await page.getByRole('tab', {name: 'Playground', exact: true}).click();
  await page.getByRole('tabpanel').getByLabel('Message', {exact: true}).fill('I want a refund for order 42');
  await page.getByRole('button', {name: 'Run agent', exact: true}).click();
  await expect(page.locator('.designer-studio').getByRole('status').filter({hasText: 'Succeeded in'})).toContainText('Succeeded in', {timeout: 30_000});
  await expect(page.getByRole('tabpanel').locator('tbody tr')).not.toHaveCount(0);
  await expect(page.getByRole('tabpanel').getByRole('table')).toContainText('skipped');
  await expect(page.locator('[data-design-node="refund"]')).toHaveClass(/run-succeeded/);
  await expect(page.locator('[data-design-node="general"]')).toHaveClass(/run-skipped/);
  await assertLayout(page, 'agent studio playground');
  await page.screenshot({path: screenshotPath(testInfo, 'agent-studio-playground'), fullPage: true});
  // Element captures for the user guide (no fixed console chrome).
  await page.locator('.graph-workbench').screenshot({path: screenshotPath(testInfo, 'agent-studio-canvas-panel'), style: DOC_CAPTURE});
  await page.getByRole('tabpanel').screenshot({path: screenshotPath(testInfo, 'agent-studio-trace-panel'), style: DOC_CAPTURE});

  await page.getByRole('button', {name: 'Save last run as test case', exact: true}).click();
  await expect(page.locator('[data-design-panel="tests"]')).toBeVisible();
  await page.getByRole('button', {name: 'Save test cases', exact: true}).click();
  await expect(page.locator('.designer-studio').getByRole('status').filter({hasText: 'Test cases saved.'})).toHaveText('Test cases saved.');

  await page.getByRole('tab', {name: 'Evaluations', exact: true}).click();
  await page.getByRole('tabpanel').getByLabel('Quality gate (%)', {exact: true}).fill('100');
  await page.getByRole('button', {name: 'Run evaluation', exact: true}).click();
  const evaluationRow = page.getByRole('tabpanel').getByRole('row').filter({hasText: 'completed'});
  await expect(evaluationRow).toContainText('100% passed', {timeout: 60_000});
  await expect(page.getByRole('tabpanel')).toContainText('passed');
  await page.screenshot({path: screenshotPath(testInfo, 'agent-studio-evaluations'), fullPage: true});
  await page.getByRole('tabpanel').screenshot({path: screenshotPath(testInfo, 'agent-studio-evaluations-panel'), style: DOC_CAPTURE});

  await page.getByRole('tab', {name: 'Versions', exact: true}).click();
  await expect(page.getByRole('tab', {name: 'Versions', exact: true})).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('tabpanel', {name: 'Versions', exact: true})).toBeVisible();
  await page.getByRole('tabpanel').getByRole('combobox', {name: /^Passing evaluation/}).selectOption({index: 1});
  await page.getByRole('tabpanel').getByLabel('Version notes', {exact: true}).fill('first release');
  await page.getByRole('button', {name: 'Publish version', exact: true}).click();
  const published = page.getByRole('tabpanel').getByRole('row').filter({hasText: 'first release'});
  await expect(published.getByRole('cell').nth(0)).toHaveText('1');
  await expect(published).toContainText('first release');
  await assertLayout(page, 'agent studio versions');
  await page.screenshot({path: screenshotPath(testInfo, 'agent-studio-versions'), fullPage: true});
  await page.getByRole('tabpanel').screenshot({path: screenshotPath(testInfo, 'agent-studio-versions-panel'), style: DOC_CAPTURE});
});
