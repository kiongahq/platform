// Against a running gateway: a node dragged past any canvas edge stays visible.
const {test, expect} = require('@playwright/test');
const {signIn, ADMIN} = require('./fixtures.cjs');

for (const [name, dx, dy] of [['right', 900, 0], ['down', 0, 500], ['left', -900, 0], ['up', 0, -500]]) {
  test(`a node dragged ${name} past the canvas stays in view`, async ({page}) => {
    await signIn(page, ADMIN, '/console/agent-designer');
    const flow = page.locator('.react-flow').first();
    await flow.evaluate((element) => element.scrollIntoView({block: 'center'}));
    const node = page.locator('.react-flow__node').first();
    const handle = await node.locator('.graph-node-body').first().boundingBox();
    const x = handle.x + handle.width / 2, y = handle.y + handle.height / 2;
    await page.mouse.move(x, y);
    await page.mouse.down();
    for (let step = 1; step <= 20; step++) {
      await page.mouse.move(x + dx * step / 20, y + dy * step / 20);
      await page.waitForTimeout(30);
    }
    await page.mouse.up();
    await expect(node).toBeVisible();
    const box = await node.boundingBox(), canvas = await flow.boundingBox();
    // Auto-pan can move the view by one more step after the last clamp.
    const slack = 20;
    expect(box.x).toBeGreaterThanOrEqual(canvas.x - slack);
    expect(box.y).toBeGreaterThanOrEqual(canvas.y - slack);
    expect(box.x + box.width).toBeLessThanOrEqual(canvas.x + canvas.width + slack);
    expect(box.y + box.height).toBeLessThanOrEqual(canvas.y + canvas.height + slack);
  });
}
