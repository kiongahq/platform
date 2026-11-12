const {test, expect} = require('@playwright/test');
const {ADMIN, signIn} = require('./fixtures.cjs');

test('Studio retains its shell and session and exposes named formatting icons', async ({page}) => {
  await signIn(page, ADMIN);
  let sessions = 0;
  await page.route('**/api/v1/editorial/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith('/session')) {
      sessions++;
      return route.fulfill({json: {member: {subject: 'writer', display_name: 'Test writer', role: 'editorial_admin'}}});
    }
    return route.fulfill({json: {items: []}});
  });
  await page.goto('/studio');
  await expect(page.locator('#ed-member-name')).toHaveText('Test writer');
  const shell = await page.locator('.studio-header').elementHandle();
  const initialSessions = sessions;
  await page.getByRole('link', {name: 'Drafts', exact: true}).click();
  await expect(page.getByRole('heading', {name: 'Drafts', exact: true})).toBeVisible();
  expect(await shell.evaluate(node => node === document.querySelector('.studio-header'))).toBe(true);
  expect(sessions).toBe(initialSessions);
  await page.locator('#ed-new-post').click();
  const bold = page.getByRole('button', {name: 'Bold', exact: true});
  await expect(bold.locator('svg')).toBeVisible();
  await expect(bold).toHaveText('');
  await bold.focus();
  expect(await bold.evaluate(node => getComputedStyle(node, '::after').content)).toBe('"Bold"');
  await bold.click();
  await expect(bold).toHaveAttribute('aria-pressed', 'true');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({path: `/tmp/kionga-studio-${test.info().project.name}.png`, fullPage: true});
});
