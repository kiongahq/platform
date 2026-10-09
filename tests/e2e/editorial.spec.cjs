// Editorial workspace acceptance: an author writes an article with blocks
// (no Markdown), uploads an image, autosaves and submits; an editor
// publishes; the public page renders the image with alt text at desktop and
// narrow widths; an ML user without editorial membership is denied.
//   KIONGA_URL=http://localhost:18193 npx playwright test tests/e2e/editorial.spec.cjs
const zlib = require('node:zlib');
const {test, expect, ADMIN, signIn, assertLayout, screenshotPath} = require('./fixtures.cjs');
const {request: requestFactory} = require('@playwright/test');

const AUTHOR = {username: 'e2e-author', password: 'e2e-author-password-123'};
const ML_USER = {username: 'e2e-ml-only', password: 'e2e-ml-only-password-123'};

/* A real PNG (gradient) built in memory so the server's decoder accepts it. */
function png(width, height) {
  const crcChunk = (type, data) => {
    const head = Buffer.alloc(8);
    head.writeUInt32BE(data.length, 0);
    head.write(type, 4, 'ascii');
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(zlib.crc32(Buffer.concat([Buffer.from(type, 'ascii'), data])) >>> 0, 0);
    return Buffer.concat([head, data, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0); ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; ihdr[9] = 2; ihdr[10] = 0; ihdr[11] = 0; ihdr[12] = 0;
  const rows = [];
  for (let y = 0; y < height; y++) {
    const row = Buffer.alloc(1 + width * 3);
    for (let x = 0; x < width; x++) { row[1 + x * 3] = (x * 255 / width) | 0; row[2 + x * 3] = (y * 255 / height) | 0; row[3 + x * 3] = 160; }
    rows.push(row);
  }
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), crcChunk('IHDR', ihdr), crcChunk('IDAT', zlib.deflateSync(Buffer.concat(rows))), crcChunk('IEND', Buffer.alloc(0))]);
}

async function adminContext(baseURL) {
  const context = await requestFactory.newContext({baseURL});
  const login = await context.post('/auth/local/login', {form: {...ADMIN, return_to: '/console.html'}, maxRedirects: 0});
  expect([200, 302]).toContain(login.status());
  return context;
}

async function provision(context, {username, password}) {
  const access = await context.put(`/api/v1/admin/users/${username}`, {data: {
    email: `${username}@example.com`, role: 'user', services: ['overview'], project_ids: [],
    storage: {size_gb: 5, buckets: []},
    compute: {profile: 'starter', vcpus: 1, memory_gb: 2, gpus: 0, max_vms: 0, max_projects: 1, max_concurrent_runs: 1, max_functions: 0},
    disabled: false,
  }});
  expect(access.ok(), await access.text()).toBeTruthy();
  const secret = await context.put(`/api/v1/admin/users/${username}/local-password`, {data: {password}});
  expect(secret.ok(), await secret.text()).toBeTruthy();
}

test.describe.configure({mode: 'serial'});

test.beforeAll(async ({baseURL}) => {
  const admin = await adminContext(baseURL);
  await provision(admin, AUTHOR);
  await provision(admin, ML_USER);
  // The bootstrap administrator claims editorial admin once (409 afterwards).
  const bootstrap = await admin.post('/api/v1/editorial/bootstrap', {data: {display_name: 'Kionga Editors'}});
  expect([201, 409]).toContain(bootstrap.status());
  if (bootstrap.status() === 409) expect((await admin.post('/api/v1/editorial/session')).status()).toBe(201);
  const grant = await admin.put(`/api/v1/editorial/members/${AUTHOR.username}`, {data: {display_name: 'Ada Author', role: 'author'}});
  expect(grant.ok(), await grant.text()).toBeTruthy();
  await admin.dispose();
});

let slug = '';

test('author writes an article with blocks, uploads an image, autosaves and submits', async ({page}, testInfo) => {
  const title = `Tracing a request through the gateway ${testInfo.project.name} ${Date.now()}`;
  await signIn(page, AUTHOR, '/editorial.html');
  await page.goto('/editorial.html#/new');
  await expect(page.locator('#ed-title')).toBeVisible();
  await expect(page.locator('#ed-member-role')).toHaveText('Author');
  await page.fill('#ed-title', title);
  await page.fill('#ed-summary', 'What happens between a client request and a model response, step by step.');
  await page.fill('#ed-tags', 'Gateway, Observability');
  const paragraph = page.locator('#ed-holder .ce-paragraph').first();
  await paragraph.click();
  await page.keyboard.type('Every request enters through the gateway, which authenticates the caller, applies policy, and routes to the right model server. This walkthrough follows one request end to end and shows where latency hides.');
  await page.keyboard.press('Enter');
  // Insert an image block through the Editor.js toolbox.
  await page.locator('.ce-toolbar__plus').click();
  await page.locator('.ce-popover-item[data-item-name="image"]').click();
  const fileInput = page.locator('[data-kimg-file]');
  await fileInput.setInputFiles({name: 'request-flow.png', mimeType: 'image/png', buffer: png(1200, 600)});
  const alt = page.locator('[data-kimg-alt]');
  await expect(alt).toBeVisible();
  await expect(page.locator('.kimg-figure img')).toHaveJSProperty('complete', true);
  await alt.fill('Diagram of a request flowing from the gateway to a model server');
  await page.locator('[data-kimg-caption]').fill('One request, end to end');
  // Autosave: status moves to "Saved hh:mm" and the URL gets the post id.
  await expect(page.locator('#ed-save-status')).toHaveText(/Saved \d\d:\d\d/, {timeout: 20_000});
  await expect(page).toHaveURL(/#\/edit\/post-/);
  slug = await page.locator('#ed-slug').inputValue();
  expect(slug).toMatch(/^tracing-a-request-through-the-gateway/);
  await expect(page.locator('[data-action="publish"]')).toHaveCount(0);
  await assertLayout(page, `editor ${testInfo.project.name}`);
  await page.screenshot({path: screenshotPath(testInfo, 'editorial-author-editor'), fullPage: true});
  await page.locator('[data-action="submit"]').click();
  await expect(page.locator('#ed-post-status')).toContainText('In review');
  // The submitted draft is not public yet.
  expect((await page.request.get(`/api/v1/blogs/${slug}`)).status()).toBe(404);
});

test('editor publishes and the public page renders the image with alt text', async ({page}, testInfo) => {
  expect(slug).not.toBe('');
  await signIn(page, ADMIN, '/editorial.html');
  await page.goto('/editorial.html#/reviews');
  await expect(page.locator('h1')).toHaveText('Reviews');
  await page.locator(`.ed-post-row:has-text("/${slug}") a.ed-post-title`).click();
  await expect(page.locator('[data-action="publish"]')).toBeVisible();
  await page.locator('#ed-preview').click();
  const frame = page.frameLocator('#ed-preview-frame');
  await expect(frame.locator('.article-content img')).toHaveAttribute('alt', /Diagram of a request/);
  await page.locator('[data-preview-mode="mobile"]').click();
  await expect(page.locator('#ed-preview-frame')).toHaveAttribute('data-mode', 'mobile');
  await page.screenshot({path: screenshotPath(testInfo, 'editorial-preview-mobile')});
  await page.locator('#ed-preview-dialog [data-close]').click();
  await page.locator('[data-action="publish"]').click();
  await expect(page.locator('#ed-post-status')).toContainText('Published');
  await page.screenshot({path: screenshotPath(testInfo, 'editorial-editor-published'), fullPage: true});

  await page.goto(`/blog.html?slug=${encodeURIComponent(slug)}`);
  const image = page.locator('#article-content figure img');
  await expect(image).toHaveAttribute('alt', 'Diagram of a request flowing from the gateway to a model server');
  await expect(image).toHaveAttribute('srcset', /480w/);
  await expect.poll(() => image.evaluate(node => node.complete && node.naturalWidth)).toBeGreaterThan(0);
  await expect(page.locator('#article-content figcaption')).toContainText('One request, end to end');
  await expect(page.locator('.article-byline')).toContainText('Ada Author');
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  await page.screenshot({path: screenshotPath(testInfo, 'blog-article-with-image'), fullPage: true});
  // Anonymous readers get the published image.
  const anonymous = await requestFactory.newContext({baseURL: page.url()});
  const src = await image.getAttribute('src');
  expect((await anonymous.get(src)).status()).toBe(200);
  await anonymous.dispose();
});

test('ML user without editorial membership is denied', async ({page}, testInfo) => {
  await signIn(page, ML_USER, '/editorial.html');
  await expect(page.locator('#ed-denied')).toHaveText(/don't have editorial access/);
  await expect(page.locator('#ed-nav')).toBeHidden();
  const statuses = await page.evaluate(async () => ({
    session: (await fetch('/api/v1/editorial/session', {method: 'POST'})).status,
    posts: (await fetch('/api/v1/editorial/posts')).status,
    bootstrap: (await fetch('/api/v1/editorial/bootstrap', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'})).status,
  }));
  expect(statuses).toEqual({session: 403, posts: 403, bootstrap: 403});
  await page.screenshot({path: screenshotPath(testInfo, 'editorial-denied')});
});
