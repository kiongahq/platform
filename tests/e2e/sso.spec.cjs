// Sign-in and sign-up through Dex (LDAP test directory and the demo OAuth
// account) next to local break-glass accounts. `make -C deploy local-up`
// starts all of them; skips when the gateway lists no LDAP provider.
// The gateway store outlives a run, so sign-up assertions hold on the first
// sign-in (request created) and every later one (request still pending).
const {test, expect} = require('@playwright/test');
const {ADMIN} = require('./fixtures.cjs');

const PASSWORD = process.env.KIONGA_SSO_TEST_PASSWORD || 'kionga-sso-dev';

test.describe('single sign-on', () => {
  test.beforeEach(async ({request}) => {
    const response = await request.get('/auth/providers');
    const options = response.ok() ? await response.json() : {providers: []};
    test.skip(!options.providers?.some(provider => provider.id === 'ldap'),
      'gateway lists no LDAP sign-in (make -C deploy local-up starts it)');
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

  async function adminRequests(page) {
    const context = page.context().request;
    const login = await context.post('/auth/local/login', {form: {...ADMIN, return_to: '/console.html'}, maxRedirects: 0});
    expect([200, 302]).toContain(login.status());
    return (await (await context.get('/api/v1/admin/access-requests')).json()).items;
  }

  test('login page offers every method: directory, demo OAuth account and local break-glass', async ({page}) => {
    await page.goto('/login.html');
    await expect(page.getByRole('link', {name: 'Sign in with Company directory'})).toBeVisible();
    await expect(page.getByRole('link', {name: 'Sign in with Demo account (local only)'})).toBeVisible();
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

  test('a directory user in no mapped group signs up and waits for approval', async ({page}) => {
    await ssoSignIn(page, 'alan');
    await page.waitForURL(url => url.pathname === '/console.html');
    const profile = await me(page);
    expect(profile.roles).toEqual(['user']);
    expect(profile.provisioned).toBe(false);
    expect(profile.connector).toBe('ldap');
    expect(profile.mode).toBe('oidc');
    expect(profile.access_request?.status).toBe('pending');
    await expect(page.locator('#signup-banner')).toBeVisible();
    await expect(page.locator('#signup-banner-message')).toContainText('waiting for an administrator to approve');
    const status = await page.evaluate(async () => ({
      admin: (await fetch('/api/v1/admin/users')).status,
      projects: (await fetch('/api/v1/projects')).status,
    }));
    expect(status).toEqual({admin: 403, projects: 403});
    const requests = await adminRequests(page);
    const mine = requests.filter(item => item.email === 'alan@kionga.dev');
    expect(mine).toHaveLength(1); // signing in again never files a second request
    expect(mine[0]).toMatchObject({status: 'pending', reason: 'Signed up with ldap'});
  });

  test('the demo OAuth account signs up with one click', async ({page}) => {
    await page.goto('/login.html?return_to=%2Fconsole.html');
    await page.getByRole('link', {name: 'Sign in with Demo account (local only)'}).click();
    await page.waitForURL(url => url.pathname === '/console.html');
    const profile = await me(page);
    expect(profile.connector).toBe('mock');
    expect(profile.email).toBe('kilgore@kilgore.trout');
    expect(profile.roles).toEqual(['user']);
    const requests = (await adminRequests(page)).filter(item => item.email === 'kilgore@kilgore.trout');
    expect(requests).toHaveLength(1);
    expect(requests[0].reason).toBe('Signed up with mock');
  });

  test('the local break-glass admin reports a local sign-in next to single sign-on', async ({page}) => {
    await page.goto('/login.html?return_to=%2Fconsole.html');
    await page.fill('input[name="username"]', ADMIN.username);
    await page.fill('input[name="password"]', ADMIN.password);
    await Promise.all([page.waitForURL(url => url.pathname === '/console.html'), page.click('button[type="submit"]')]);
    const profile = await me(page);
    expect(profile).toMatchObject({mode: 'local', local_accounts: true});
    await expect(page.locator('#signup-banner')).toBeHidden();
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
