// Acceptance: an administrator creates a branched DAG with a schedule,
// manages the schedule, manually runs it with an allowed override, sees the
// parallel branches overlap, opens a node, and reads its logs. The run must
// be traceable to its definition revision, hash, overrides, policy decision,
// image digests and workload ids. Needs a running stack with the pipeline
// runner and a locally available PIPELINE_TEST_IMAGE.
const {test, expect, ADMIN, signIn, assertLayout, screenshotPath} = require('./fixtures.cjs');

const IMAGE = process.env.PIPELINE_TEST_IMAGE || 'python@sha256:1b668429b3511ab407d8e00648891631b0b1a4d7e15e3ca70f38ab5b91ad4ab4';

function flowYAML(projectID, name) {
  const sleeper = side => `["python", "-c", "import json,time; print('${side} start'); time.sleep(5); print(json.dumps({'side': '${side}'}))"]`;
  return `apiVersion: kionga.dev/v1
kind: Pipeline
metadata:
  name: ${name}
  project: ${projectID}
  version: "1"
  description: Branched acceptance flow
spec:
  executionMode: prefect
  parameters:
    window: daily
  overridable:
    parameters: [window]
    nodes: true
  triggers:
    - type: schedule
      cron: "30 2 * * *"
      timezone: Africa/Nairobi
  nodes:
    - id: extract
      type: container
      image: ${IMAGE}
      command: ["python", "-c", "import json,os; print('window', json.loads(os.environ['KIONGA_PARAMETERS'])['window']); print(json.dumps({'rows': 12}))"]
    - id: left
      type: container
      image: ${IMAGE}
      dependsOn: [extract]
      command: ${sleeper('left')}
    - id: right
      type: container
      image: ${IMAGE}
      dependsOn: [extract]
      command: ${sleeper('right')}
    - id: join
      type: container
      image: ${IMAGE}
      dependsOn: [left, right]
      command: ["python", "-c", "import os; print('inputs', os.environ['KIONGA_DEPENDENCY_OUTPUTS'])"]
`;
}

test.setTimeout(300_000);

test('branched DAG: schedule, manual run with override, parallel nodes, node logs, provenance', async ({page}, testInfo) => {
  await signIn(page, ADMIN);
  const call = (path, options = {}) => page.evaluate(async ([path, options]) => {
    const response = await fetch(path, {...options, headers: {'Content-Type': 'application/json'}});
    return {status: response.status, body: await response.json().catch(() => ({}))};
  }, [path, options]);

  const projects = (await call('/api/v1/project-options')).body.items;
  test.skip(!projects.length, 'needs a project');
  const project = projects[0];
  const name = `accept-${testInfo.project.name}-${Date.now()}`;
  const created = await call('/api/v1/pipelines/definitions/yaml', {method: 'POST', body: JSON.stringify({yaml: flowYAML(project.id, name), message: 'acceptance flow'})});
  expect(created.status, JSON.stringify(created.body)).toBe(201);
  const definition = created.body;
  expect(definition.triggers[0].next_run_at).toBeTruthy();

  // Schedule: visible, pausable, resumable in the UI.
  await page.goto(`/console.html?view=pipelines&project=${project.id}`);
  const card = page.locator(`[data-definition-detail="${definition.id}"]`);
  await expect(card).toContainText('30 2 * * *');
  await expect(card).toContainText('Africa/Nairobi');
  await card.click();
  const detail = page.locator('#definition-detail-dialog');
  await expect(detail.locator('.dag-node-group')).toHaveCount(4);
  await expect(detail.locator('[data-edge]')).toHaveCount(4);
  await detail.getByRole('button', {name: 'Pause'}).click();
  await expect(detail.locator('.schedule-line .status')).toHaveText(/paused/i);
  await detail.getByRole('button', {name: 'Resume'}).click();
  await expect(detail.locator('.schedule-line .status')).toHaveText(/active/i);
  await detail.getByRole('tab', {name: /Revisions/}).click();
  await expect(detail.locator('.revision-row')).toHaveCount(3);
  await page.screenshot({path: screenshotPath(testInfo, 'acceptance-flow-detail'), fullPage: true});

  // Manual run with an allowed override, after a passing preflight.
  await detail.getByRole('button', {name: '▶ Run'}).click();
  const dialog = page.locator('#submit-dialog');
  await expect(dialog.locator('[data-param="window"]')).toBeVisible();
  await dialog.locator('[data-param="window"]').fill('weekly');
  await dialog.getByRole('button', {name: 'Check run'}).click();
  await expect(dialog.locator('#submit-checks')).toContainText('parameters.window');
  await expect(dialog.getByRole('button', {name: 'Start run'})).toBeEnabled();
  await assertLayout(page, 'run configuration');
  await page.screenshot({path: screenshotPath(testInfo, 'acceptance-run-config'), fullPage: true});
  await dialog.getByRole('button', {name: 'Start run'}).click();
  await expect(dialog).toBeHidden();

  // Wait for the run to finish.
  let run;
  await expect.poll(async () => {
    const runs = (await call('/api/v1/pipelines/runs')).body;
    run = runs.find(item => item.definition_id === definition.id);
    return run?.status;
  }, {timeout: 240_000, intervals: [2000]}).toMatch(/succeeded|failed/);
  run = (await call(`/api/v1/pipelines/runs/${run.id}`)).body;
  expect(run.status, JSON.stringify(run.steps.map(step => [step.name, step.status, step.message]))).toBe('succeeded');

  // Provenance: revision, hash, overrides, policy decision, digests, workloads.
  expect(run.trigger).toBe('manual');
  expect(run.provenance.definition_revision).toBe(3);
  expect(run.provenance.definition_sha256).toMatch(/^[a-f0-9]{64}$/);
  expect(run.provenance.overrides.parameters.window).toBe('weekly');
  expect(run.provenance.policy_decision).toContain('role-baseline');
  expect(Object.keys(run.provenance.image_digests).sort()).toEqual(['extract', 'join', 'left', 'right']);
  for (const step of run.steps) {
    expect(step.workload_kind).toBe('docker-container');
    expect(step.workload_id).toMatch(/^[a-f0-9]{12}$/);
  }

  // Parallel branches overlap; the join started after both ended.
  const at = name => run.steps.find(step => step.name === name);
  const span = name => [Date.parse(at(name).started_at), Date.parse(at(name).ended_at)];
  const [leftStart, leftEnd] = span('left');
  const [rightStart, rightEnd] = span('right');
  expect(leftStart).toBeLessThan(rightEnd);
  expect(rightStart).toBeLessThan(leftEnd);
  expect(span('join')[0]).toBeGreaterThanOrEqual(Math.max(leftEnd, rightEnd));

  // Logs: runner output and platform events, filtered by node.
  const logs = (await call(`/api/v1/logs?run_id=${run.id}&node=left&limit=100`)).body.items;
  expect(logs.some(entry => entry.source === 'runner' && entry.message === 'left start')).toBeTruthy();
  expect(logs.some(entry => entry.source === 'platform' && entry.message === 'left → succeeded')).toBeTruthy();
  const extract = (await call(`/api/v1/logs?run_id=${run.id}&node=extract&q=window`)).body.items;
  expect(extract.map(entry => entry.message)).toContain('window weekly');

  // Node detail in the UI.
  await page.goto(`/console.html?view=pipelines&project=${project.id}&resource=${run.id}`);
  const runDialog = page.locator('#run-dialog');
  await expect(runDialog).toBeVisible();
  await runDialog.locator('[data-graph-node="left"]').click();
  const panel = runDialog.locator('#run-node-detail');
  await expect(panel).toContainText('Docker container');
  await expect(panel.locator('.logs')).toContainText('left start');
  await panel.scrollIntoViewIfNeeded();
  await panel.screenshot({path: screenshotPath(testInfo, 'acceptance-node-logs')});
});
