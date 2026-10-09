// WCAG 2.1 AA scan (axe-core) of every console view. Serious and critical
// violations fail the build; moderate ones are reported in the output.
const {test, expect, ADMIN, signIn} = require('./fixtures.cjs');
const AxeBuilder = require('@axe-core/playwright').default;

const views = ['overview', 'projects', 'pipelines', 'functions', 'models', 'agents', 'features', 'storage', 'realtime', 'catalog', 'logs', 'platform', 'access', 'profile', 'settings', 'blogs'];

test.describe('accessibility', () => {
  test.beforeEach(async ({page}) => { await signIn(page, ADMIN); });
  for (const view of views) {
    test(`${view} has no serious accessibility violations`, async ({page}) => {
      await page.goto(`/console.html?view=${view}`);
      await expect(page.locator(`#${view}`)).toHaveAttribute('aria-busy', 'false');
      const results = await new AxeBuilder({page}).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
      const serious = results.violations.filter(violation => ['serious', 'critical'].includes(violation.impact));
      const summary = serious.map(violation => `${violation.id} (${violation.impact}): ${violation.nodes.slice(0, 3).map(node => node.target.join(' ')).join(' | ')}`);
      expect(summary, `${view}: ${summary.join('\n')}`).toEqual([]);
    });
  }
});
