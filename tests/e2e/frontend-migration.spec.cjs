const { test, expect } = require('@playwright/test');

test('public Next pages hydrate without runtime errors', async ({ page }) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  for (const route of ['/login', '/api-docs']) {
    await page.goto(route);
    await expect(page.locator(route === '/login' ? '#local-login' : 'h1')).toBeVisible();
  }
  expect(errors).toEqual([]);
});

test('canonical protected routes require login and retired HTML routes are unavailable', async ({ page, request }) => {
  for (const route of ['/blog.html', '/console.html', '/studio.html']) {
    const response = await request.get(route, { maxRedirects: 0 });
    expect([302, 401, 404]).toContain(response.status());
    if(response.status()===302) expect(response.headers().location).toMatch(/^\/(?:auth\/)?login\?/);
  }
  // The studio exists only with blog authoring on; otherwise it is absent.
  const authoring = (await (await request.get('/api/v1/site')).json()).blog_authoring === true;
  if (!authoring) for (const route of ['/studio', '/studio/new']) expect((await request.get(route, { maxRedirects: 0 })).status()).toBe(404);
  for (const route of ['/console', '/console/langflow', '/console/agent-designer', '/console/pipeline-editor', '/workspace', ...(authoring ? ['/studio', '/studio/new'] : [])]) {
    await page.goto(route);
    await expect(page).toHaveURL(/\/login\?/);
    await expect(page.locator('#local-login')).toBeVisible();
  }
});

test('Swagger renders the gateway contract outside product navigation', async ({page}) => {
  const errors=[];
  page.on('pageerror', error=>errors.push(error.message));
  const external=[];
  page.on('request',request=>{if(new URL(request.url()).origin!==new URL(page.url()).origin)external.push(request.url());});
  await page.goto('/api-docs');
  await expect(page.getByRole('heading',{name:'Kionga API reference',exact:true})).toBeVisible();
  await expect(page.locator('.swagger-ui .opblock').first()).toBeVisible();
  await page.getByPlaceholder('Filter by tag or path').fill('projects');
  await expect(page.locator('.swagger-ui')).toContainText('/api/v1/projects');
  await expect(page.locator('.site-header')).toHaveCount(0);
  expect(external.filter(url=>new URL(url).hostname==='validator.swagger.io')).toEqual([]);
  expect(errors).toEqual([]);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
});

test('the install has no landing or blog pages: the root leads to sign-in', async ({page, request}) => {
  const root = await request.get('/', { maxRedirects: 0 });
  expect(root.status()).toBe(302);
  expect(root.headers().location).toBe('/console');
  for (const route of ['/blog', '/blog/read?slug=any']) expect((await request.get(route, { maxRedirects: 0 })).status()).not.toBe(200);
  await page.goto('/');
  await expect(page).toHaveURL(/\/login\?/);
  await expect(page.locator('.ascii-flow')).toHaveCount(0);
});
