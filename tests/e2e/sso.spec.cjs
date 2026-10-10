// Single sign-on through Dex against an LDAP directory, next to local
// break-glass accounts. Needs the SSO overlay from kiongahq/deploy:
//   make -C deploy sso-up      (Dex + GLAuth test directory, gateway in SSO mode)
// Skips when the gateway lists no single sign-on providers.
const {test, expect} = require('@playwright/test');

const PASSWORD = process.env.KIONGA_SSO_TEST_PASSWORD || 'kionga-sso-dev';

test.describe('single sign-on', () => {
  test.beforeEach(async ({request}) => {
    const response = await request.get('/auth/providers');
    const options = response.ok() ? await response.json() : {providers: []};
    test.skip(!options.providers?.some(provider => provider.id === 'ldap'),
      'gateway is not running with the SSO overlay (make -C deploy sso-up)');
  });

  async function ssoSignIn(page, username, password = PASSWORD) {
    await page.goto('/login.html?return_to=%2Fconsole.html');
    await page.getByRole('link', {name: 'Sign in with Company directory'}).click();
    await page.waitForURL(/\/dex\/auth\/ldap/);
    await page.fill('input[name="login"]', username);
    await page.fill('input[name="password"]', password);
    await page.click('button[type="submit"]');
  }

  async function me(page) {
    return page.evaluate(async () => (await fetch('/api/v1/me')).json());
  }

  test('login page offers the directory and keeps local break-glass sign-in', async ({page}) => {
    await page.goto('/login.html');
    await expect(page.getByRole('link', {name: 'Sign in with Company directory'})).toBeVisible();
    await expect(page.locator('#local-login')).toBeVisible();
    await expect(page.locator('#login-divider')).toBeVisible();
  });

  test('a directory admin signs in through LDAP and gets the admin role from their group', async ({page}) => {
    await ssoSignIn(page, 'ada');
    await page.waitForURL(url => url.pathname === '/console.html');
    const profile = await me(page);
    expect(profile.email).toBe('ada@kionga.dev');
    expect(profile.roles).toEqual(['admin']);
    const cookies = await page.context().cookies();
    const session = cookies.find(cookie => cookie.name === 'kionga_session');
    expect(session?.httpOnly).toBe(true);
    expect(session?.secure).toBe(true);
    expect(cookies.some(cookie => ['kionga_oauth_state', 'kionga_oauth_verifier', 'kionga_oauth_nonce'].includes(cookie.name))).toBe(false);
  });

  test('group mapping gives a data-team member the engineer role', async ({page}) => {
    await ssoSignIn(page, 'grace');
    await page.waitForURL(url => url.pathname === '/console.html');
    expect((await me(page)).roles).toEqual(['engineer']);
  });

  test('a directory user in no mapped group gets no platform role', async ({page}) => {
    await ssoSignIn(page, 'alan');
    await page.waitForURL(url => url.pathname === '/console.html');
    const profile = await me(page);
    expect(profile.roles ?? []).toEqual([]);
    const admin = await page.evaluate(async () => (await fetch('/api/v1/admin/users')).status);
    expect(admin).toBe(403);
  });

  test('a wrong directory password does not create a session', async ({page}) => {
    await ssoSignIn(page, 'ada', 'not-the-password');
    await expect(page.locator('body')).toContainText(/invalid|incorrect|username and password/i);
    const status = await page.evaluate(async () => (await fetch('http://localhost:8080/api/v1/me', {credentials: 'include'}).catch(() => ({status: 0}))).status);
    expect([0, 401]).toContain(status);
  });

  test('sign-out ends the single sign-on session', async ({page}) => {
    await ssoSignIn(page, 'ada');
    await page.waitForURL(url => url.pathname === '/console.html');
    await page.goto('/auth/logout');
    const status = await page.evaluate(async () => (await fetch('/api/v1/me')).status);
    expect(status).toBe(401);
  });
});
