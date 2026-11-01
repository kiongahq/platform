// IAM end to end: an administrator creates a group and a policy in the
// console, attaches the policy, and a normal user gains and then loses
// access; an explicit deny returns the deciding statement; the user cannot
// edit policies. Every IAM tab is layout-audited at both widths.
const {test, expect, ADMIN, USER, signIn, ensureNormalUser, assertLayout, screenshotPath} = require('./fixtures.cjs');

const suffix = info => `${info.project.name}-${Date.now().toString(36)}`;

async function adminAPI(page) {
  // page.request shares the signed-in administrator's session cookie.
  return page.request;
}

async function cleanup(request, prefix) {
  const attachments = (await (await request.get('/api/v1/admin/iam/attachments')).json()).items || [];
  for (const item of attachments.filter(item => item.policy_id.startsWith(prefix))) await request.delete(`/api/v1/admin/iam/attachments/${encodeURIComponent(item.id)}`);
  const policies = (await (await request.get('/api/v1/admin/iam/policies')).json()).items || [];
  for (const item of policies.filter(item => item.id.startsWith(prefix))) await request.delete(`/api/v1/admin/iam/policies/${item.id}`);
  const groups = (await (await request.get('/api/v1/admin/iam/groups')).json()).items || [];
  for (const item of groups.filter(item => item.id.startsWith(prefix))) await request.delete(`/api/v1/admin/iam/groups/${item.id}`);
}

/* Scrolled content passes under the fixed app bar, which is not a layout
 * defect; audit each state from the top of the page. */
async function audit(page, label) {
  await page.evaluate(() => { window.scrollTo(0, 0); document.querySelector('main')?.scrollTo?.(0, 0); });
  await assertLayout(page, label);
}

async function openAccessTab(page, tab) {
  await page.getByRole('tab', {name: tab, exact: false}).click();
  await expect(page.getByRole('tab', {name: tab, exact: false})).toHaveAttribute('aria-selected', 'true');
}

test('administrator grants and revokes project access through a group policy', async ({page, browser, baseURL}, testInfo) => {
  await ensureNormalUser(baseURL, {services: ['overview', 'projects', 'pipelines'], projectIDs: []});
  await signIn(page, ADMIN);
  const request = await adminAPI(page);
  await cleanup(request, 'e2e-');
  const tag = suffix(testInfo);
  const created = await request.post('/api/v1/projects', {data: {name: `IAM target ${tag}`, template: 'blank'}});
  expect(created.ok(), await created.text()).toBeTruthy();
  const project = await created.json();
  const groupName = `E2E analysts ${tag}`;
  const policyName = `E2E read ${tag}`;
  const groupID = groupName.toLowerCase().replace(/[^a-z0-9]+/g, '-');
  const policyID = policyName.toLowerCase().replace(/[^a-z0-9]+/g, '-');

  const user = await browser.newContext({viewport: page.viewportSize()});
  const userPage = await user.newPage();
  await signIn(userPage, USER);
  const userStatus = path => userPage.evaluate(async target => (await fetch(target)).status, path);
  expect(await userStatus(`/api/v1/projects/${project.id}`)).toBe(404);

  await page.goto('/console/access');
  await expect(page.locator('#page-title')).toHaveText('Access');
  await expect(page.getByRole('heading', {name: 'Users and resource grants'})).toBeVisible();
  await audit(page, 'access users tab');

  // Group
  await openAccessTab(page, 'groups');
  await page.getByRole('button', {name: 'New group', exact: true}).click();
  await page.getByRole('dialog', {name: 'New group'}).getByLabel('Name', {exact: true}).fill(groupName);
  await page.getByRole('dialog', {name: 'New group'}).getByLabel(/^Members/).fill(USER.username);
  await page.getByRole('dialog', {name: 'New group'}).locator('button[type=submit]').click();
  await expect(page.getByRole('dialog', {name: 'New group'})).toBeHidden();
  await expect(page.getByRole('table')).toContainText(groupName);
  await audit(page, 'access groups tab');

  // Policy, written with the form editor
  await openAccessTab(page, 'policies');
  await page.getByRole('button', {name: 'New policy', exact: true}).click();
  await page.getByRole('dialog', {name: 'New policy'}).getByLabel('Name', {exact: true}).fill(policyName);
  await page.getByRole('button', {name: 'Add statement', exact: true}).click();
  const statement = page.getByRole('group', {name: 'Statement 1', exact: true});
  await statement.getByLabel('Statement ID', {exact: true}).fill('ReadTarget');
  await statement.getByLabel(/^Actions/).fill('project:Read, pipeline:Read');
  await statement.getByLabel(/^Resources/).fill(`kionga:project/${project.id}, kionga:project/${project.id}/*`);
  await page.getByRole('button', {name: 'JSON', exact: true}).click();
  await expect(page.getByRole('textbox', {name: 'Policy JSON', exact: true})).toHaveValue(/ReadTarget/);
  await page.getByRole('button', {name: 'Form', exact: true}).click();
  await page.getByRole('dialog', {name: 'New policy'}).locator('button[type=submit]').click();
  await expect(page.getByRole('dialog', {name: 'New policy'})).toBeHidden();
  await expect(page.getByRole('table')).toContainText(policyName);
  await audit(page, 'access policies tab');

  // Attachment
  await openAccessTab(page, 'attachments');
  await page.getByRole('combobox', {name: 'Policy', exact: true}).selectOption(policyID);
  await page.getByRole('combobox', {name: 'Principal type', exact: true}).selectOption('group');
  await page.getByLabel('Subject or group ID', {exact: true}).fill(groupID);
  await page.getByRole('button', {name: 'Attach policy', exact: true}).click();
  await expect(page.getByRole('table')).toContainText(groupID);
  await audit(page, 'access attachments tab');

  // Simulator agrees with enforcement
  await openAccessTab(page, 'simulator');
  await page.getByLabel(/^Account subject/).fill(USER.username);
  await page.getByRole('combobox', {name: 'Action', exact: true}).selectOption('project:Read');
  await page.getByLabel('Resource', {exact: true}).fill(`kionga:project/${project.id}`);
  await page.getByRole('button', {name: 'Explain access', exact: true}).click();
  await expect(page.locator('main')).toContainText(`${policyID}@v1#ReadTarget`);
  await expect(page.locator('main .numbered-code').last()).toContainText(`group:${groupID}`);
  await audit(page, 'access simulator tab');
  await page.screenshot({path: screenshotPath(testInfo, 'admin-access-simulator'), fullPage: true});

  // The user gains access, visible in the console too.
  expect(await userStatus(`/api/v1/projects/${project.id}`)).toBe(200);
  await userPage.goto('/console/projects');
  await expect(userPage.getByRole('table')).toBeVisible();
  await expect(userPage.locator('main')).toContainText(`IAM target ${tag}`);
  await audit(userPage, 'user projects with granted project');

  // Detach in the console: the user loses access on the next request.
  await openAccessTab(page, 'attachments');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('row').filter({hasText: policyID}).filter({hasText: groupID}).getByRole('button', {name: 'Detach'}).click();
  await expect(page.getByRole('table')).not.toContainText(groupID);
  expect(await userStatus(`/api/v1/projects/${project.id}`)).toBe(404);

  // An explicit deny names the statement that decided.
  const deny = await request.post('/api/v1/admin/iam/policies', {data: {id: `e2e-deny-${tag}`.toLowerCase().replace(/[^a-z0-9-]+/g, '-'), name: `E2E deny create ${tag}`, statements: [{sid: 'NoNewProjects', effect: 'deny', actions: ['project:Create'], resources: ['*']}]}});
  expect(deny.ok(), await deny.text()).toBeTruthy();
  const denyPolicy = await deny.json();
  expect((await request.post('/api/v1/admin/iam/attachments', {data: {policy_id: denyPolicy.id, principal_type: 'user', principal_id: USER.username}})).status()).toBe(201);
  const denied = await userPage.evaluate(async () => {
    const response = await fetch('/api/v1/projects', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({name: 'Should be denied', template: 'blank'})});
    return {status: response.status, body: await response.json()};
  });
  expect(denied.status).toBe(403);
  expect(denied.body.decision.decided_by).toBe(`${denyPolicy.id}@v1#NoNewProjects`);
  const explained = await userPage.evaluate(async () => (await (await fetch('/api/v1/iam/explain?action=project:Create&resource=kionga:project/*')).json()).decision);
  expect(explained.allowed).toBe(false);
  expect(explained.decided_by).toBe(`${denyPolicy.id}@v1#NoNewProjects`);

  // A normal user cannot edit policies, even through the API.
  const edit = await userPage.evaluate(async () => (await fetch('/api/v1/admin/iam/policies', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({name: 'Mine', statements: [{effect: 'allow', actions: ['*'], resources: ['*']}]})})).status);
  expect(edit).toBe(403);
  await expect(userPage.locator('nav a[href="/console/access"]')).toBeHidden();

  await cleanup(request, 'e2e-');
  await user.close();
});
