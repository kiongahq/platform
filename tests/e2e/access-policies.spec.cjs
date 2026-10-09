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
  await page.locator(`#access-tab-${tab}`).click();
  await expect(page.locator(`#access-panel-${tab}`)).toBeVisible();
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

  await page.goto('/console.html?view=access');
  await expect(page.locator('#access.view.active')).toBeVisible();
  await expect(page.locator('#access')).toHaveAttribute('aria-busy', 'false');
  await audit(page, 'access users tab');

  // Group
  await openAccessTab(page, 'groups');
  await page.locator('#add-iam-group').click();
  await page.locator('#iam-group-form [name=name]').fill(groupName);
  await page.locator('#iam-group-form [name=members]').fill(USER.username);
  await page.locator('#iam-group-form button[type=submit]').click();
  await expect(page.locator('#iam-group-dialog')).toBeHidden();
  await expect(page.locator('#iam-group-table')).toContainText(groupName);
  await audit(page, 'access groups tab');

  // Policy, written with the form editor
  await openAccessTab(page, 'policies');
  await page.locator('#add-iam-policy').click();
  await page.locator('#iam-policy-form [name=name]').fill(policyName);
  const statement = page.locator('#policy-statements .policy-statement').first();
  await statement.locator('[name=sid]').fill('ReadTarget');
  await statement.locator('[name=actions]').fill('project:Read, pipeline:Read');
  await statement.locator('[name=resources]').fill(`kionga:project/${project.id}, kionga:project/${project.id}/*`);
  await page.locator('#policy-tab-json').click();
  await expect(page.locator('#policy-json')).toHaveValue(/ReadTarget/);
  await page.locator('#policy-tab-form').click();
  await page.locator('#iam-policy-form button[type=submit]').click();
  await expect(page.locator('#iam-policy-dialog')).toBeHidden();
  await expect(page.locator('#iam-policy-table')).toContainText(policyName);
  await audit(page, 'access policies tab');

  // Attachment
  await openAccessTab(page, 'attachments');
  await page.locator('#iam-attach-policy').selectOption(policyID);
  await page.locator('#iam-attach-form [name=principal_type]').selectOption('group');
  await page.locator('#iam-attach-form [name=principal_id]').fill(groupID);
  await page.locator('#iam-attach-form button[type=submit]').click();
  await expect(page.locator('#iam-attachment-table')).toContainText(`Group · ${groupID}`);
  await audit(page, 'access attachments tab');

  // Simulator agrees with enforcement
  await openAccessTab(page, 'simulator');
  await page.locator('#access-simulator-form [name=principal]').fill(USER.username);
  await page.locator('#simulator-action').selectOption('project:Read');
  await page.locator('#access-simulator-form [name=resource]').fill(`kionga:project/${project.id}`);
  await page.locator('#access-simulator-form button[type=submit]').click();
  await expect(page.locator('#access-simulator-result .decision.allow')).toContainText(`${policyID}@v1#ReadTarget`);
  await expect(page.locator('#access-effective')).toContainText(`group:${groupID}`);
  await audit(page, 'access simulator tab');
  await page.screenshot({path: screenshotPath(testInfo, 'admin-access-simulator'), fullPage: true});

  // The user gains access, visible in the console too.
  expect(await userStatus(`/api/v1/projects/${project.id}`)).toBe(200);
  await userPage.goto('/console.html?view=projects');
  await expect(userPage.locator('#projects')).toHaveAttribute('aria-busy', 'false');
  await expect(userPage.locator('#projects')).toContainText(`IAM target ${tag}`);
  await audit(userPage, 'user projects with granted project');

  // Detach in the console: the user loses access on the next request.
  await openAccessTab(page, 'attachments');
  page.once('dialog', dialog => dialog.accept());
  await page.locator(`[data-iam-detach="${policyID}:group:${groupID}"]`).click();
  await expect(page.locator('#iam-attachment-table')).not.toContainText(`Group · ${groupID}`);
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
  await expect(userPage.locator('[data-view="access"]')).toBeHidden();

  await cleanup(request, 'e2e-');
  await user.close();
});
