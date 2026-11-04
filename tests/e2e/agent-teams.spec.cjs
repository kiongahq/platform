// Agent teams console. Runs against a gateway with the team routes and the mock LLM backend.
// Without those routes it skips. To exercise the console alone, with canned data:
//   node frontend/tests/fixtures/teams-mock-gateway.mjs 18917
//   KIONGA_GATEWAY_URL=http://127.0.0.1:18917 npm run dev --prefix frontend -- --port 3417
//   KIONGA_TEAMS_MOCK=1 KIONGA_URL=http://localhost:3417 npx playwright test tests/e2e/agent-teams.spec.cjs
const AxeBuilder = require('@axe-core/playwright').default;
const {test, expect, ADMIN, signIn, assertLayout, screenshotPath} = require('./fixtures.cjs');

const MOCK = process.env.KIONGA_TEAMS_MOCK === '1';
const unique = prefix => `${prefix}-${Date.now().toString(36)}-${Math.floor(Math.random() * 1e4)}`;
const VIEWS = ['agent-definitions', 'agent-teams', 'team-runs'];

async function firstProject(page) {
  const {items} = await (await page.request.get('/api/v1/project-options')).json();
  if (items?.length) return items[0].id;
  const created = await page.request.post('/api/v1/projects', {data: {name: unique('teams-e2e')}});
  expect(created.ok(), await created.text()).toBeTruthy();
  return (await created.json()).id;
}
async function createTeam(page, project) {
  const definition = await page.request.post('/api/v1/agent-definitions', {data: {project_id: project, name: unique('worker'), description: 'Does one item.', instructions: 'Do the item.', workspace: 'none', tools: []}});
  expect(definition.ok(), await definition.text()).toBeTruthy();
  const team = await page.request.post('/api/v1/agent-teams', {data: {project_id: project, name: unique('team'), coordinator: {instructions: 'Coordinate the work.'}, members: [(await definition.json()).id]}});
  expect(team.ok(), await team.text()).toBeTruthy();
  return team.json();
}
async function startRun(page, team, task, {sideEffects = false} = {}) {
  await page.goto(`/console/agent-teams?project=${encodeURIComponent(team.project_id)}`);
  await page.getByRole('button', {name: `Start a run of ${team.name}`}).click();
  await page.getByLabel('Task').fill(task);
  if (sideEffects) await page.getByLabel('Allow side effects').check();
  await page.locator('#start-team-run button[type="submit"]').click();
  await expect(page.locator('#team-run')).toBeVisible();
  await expect(page.locator('#team-workers tbody tr').first()).toBeVisible();
}

test.describe('agent teams', () => {
  test.beforeEach(async ({page}) => {
    await signIn(page, ADMIN, '/console/agent-definitions');
    const probe = await page.request.get('/api/v1/agent-definitions');
    test.skip(probe.status() === 404, 'this gateway does not have the agent team routes yet');
  });

  for (const view of VIEWS) {
    test(`${view} is linked, loads directly and has no serious accessibility violations`, async ({page}) => {
      await page.goto(`/console/${view}`);
      await expect(page.locator('#page-title')).toBeVisible();
      await expect(page.locator(`a[data-view="${view}"]`)).toHaveAttribute('aria-current', 'page');
      await expect(page.getByText('Loading…', {exact: true})).toHaveCount(0);
      await assertLayout(page, view);
      const results = await new AxeBuilder({page}).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
      const serious = results.violations.filter(item => ['serious', 'critical'].includes(item.impact));
      expect(serious.map(item => `${item.id}: ${item.nodes.slice(0, 3).map(node => node.target.join(' ')).join(' | ')}`)).toEqual([]);
    });
  }

  test('the three navigation entries have different icons', async ({page}) => {
    await page.goto('/console/agent-teams');
    const paths = await Promise.all(VIEWS.map(view => page.locator(`a[data-view="${view}"] svg path`).first().getAttribute('d')));
    expect(new Set(paths).size).toBe(3);
  });

  test('an agent can be created with the keyboard, and invalid input is refused with focus on the problems', async ({page}, testInfo) => {
    const project = await firstProject(page);
    await page.goto(`/console/agent-definitions?resource=new&project=${encodeURIComponent(project)}`);
    await page.locator('#definition-name').fill('Not Valid');
    await page.locator('#definition-tool-sandbox').check();
    await page.locator('#definition-save').focus();
    await page.keyboard.press('Enter');
    const summary = page.locator('.team-error-summary');
    await expect(summary).toBeFocused();
    await expect(summary).toContainText('need the sandbox workspace');
    await expect(page.locator('#definition-name')).toHaveAttribute('aria-invalid', 'true');
    await assertLayout(page, 'agent editor with errors');
    await page.screenshot({path: screenshotPath(testInfo, 'teams-agent-errors'), fullPage: true});
    const name = unique('e2e-agent');
    await page.locator('#definition-name').fill(name);
    await page.locator('#definition-description').fill('Handles one item.');
    await page.locator('#definition-instructions').fill('Do the item and report.');
    await page.locator('#definition-workspace').selectOption('sandbox');
    await page.locator('#definition-save').focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('#definition-revision')).toContainText('Revision 1');
    await expect(page).toHaveURL(/resource=adf-/);
    await page.reload();
    await expect(page.locator('#definition-name')).toHaveValue(name);
    await expect(page.locator('#definition-tool-sandbox')).toBeChecked();
  });

  test('a run shows live workers and a person can message it, then cancel it', async ({page}) => {
    const team = await createTeam(page, await firstProject(page));
    await startRun(page, team, MOCK ? 'provider-outage: stays live' : 'Report on the item.');
    if (!MOCK) return; // The mock LLM finishes at once; message and cancel need a run that stays live.
    await expect(page.locator('#team-workers')).toContainText('waiting retry');
    await page.getByLabel('Message', {exact: true}).fill('Please also check the logout path');
    await page.getByRole('button', {name: 'Send message'}).click();
    await expect(page.locator('.team-feed')).toContainText('Please also check the logout path');
    await page.locator('#team-run-cancel').click();
    await page.getByRole('dialog').getByRole('button', {name: 'Cancel run', exact: true}).click();
    await expect(page.locator('#team-run-status')).toContainText('cancelled');
    await expect(page.locator('#team-run-cancel')).toHaveCount(0);
  });

  test('a mock LLM run ends with a final report', async ({page}) => {
    const team = await createTeam(page, await firstProject(page));
    await startRun(page, team, 'Report on the item.');
    await expect(page.locator('#team-run-status')).toContainText('succeeded', {timeout: 60_000});
    await expect(page.locator('#team-run-report')).toBeVisible();
    await assertLayout(page, 'finished run');
  });

  test('approving a side effect lets the run finish', async ({page}, testInfo) => {
    test.skip(!MOCK, 'on a real gateway this needs a mock-script task that uses a side-effect tool');
    const team = await createTeam(page, await firstProject(page));
    await startRun(page, team, 'Push the fix.', {sideEffects: true});
    const approve = page.getByRole('button', {name: /^Approve git_push/});
    await expect(approve).toBeVisible();
    await assertLayout(page, 'run with a pending approval');
    await page.screenshot({path: screenshotPath(testInfo, 'teams-approval'), fullPage: true});
    await approve.click();
    await expect(page.getByRole('heading', {name: 'Pending approvals (0)'})).toBeFocused();
    await expect(page.locator('#team-run-status')).toContainText('succeeded');
    await expect(page.locator('#team-run-report')).toContainText('Not verified');
  });

  test('a sandbox outage shows the banner, and resume clears it', async ({page}) => {
    test.skip(!MOCK, 'on a real gateway this needs a sandbox manager that is down');
    const team = await createTeam(page, await firstProject(page));
    await startRun(page, team, 'sandbox-down');
    await expect(page.locator('#team-sandbox-banner')).toContainText('Sandbox unavailable');
    await page.locator('#team-run-resume').click();
    await page.getByRole('dialog').getByRole('button', {name: 'Resume', exact: true}).click();
    await expect(page.locator('#team-sandbox-banner')).toHaveCount(0);
    await expect(page.locator('#team-run-status')).toContainText('succeeded');
  });

  test('agent output stays text in a real browser', async ({page}) => {
    test.skip(!MOCK, 'the injected output comes from the canned scenario');
    const team = await createTeam(page, await firstProject(page));
    await startRun(page, team, 'attack', {sideEffects: true});
    await expect(page.locator('#team-approvals')).toContainText('<img src=x');
    await expect(page.locator('#team-transcript')).toContainText('<script>');
    expect(await page.locator('#team-run img, #team-run script').count()).toBe(0);
    expect(await page.evaluate(() => window.__owned)).toBeUndefined();
  });
});
