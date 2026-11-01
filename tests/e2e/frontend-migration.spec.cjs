const { test, expect } = require('@playwright/test');

test('public Next pages hydrate without runtime errors', async ({ page }) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  for (const route of ['/', '/login', '/api-docs', '/blog']) {
    await page.goto(route);
    await expect(page.locator(route === '/login' ? '#local-login' : 'h1')).toBeVisible();
  }
  await expect(page.getByRole('searchbox')).toBeVisible();
  await page.getByRole('searchbox').fill('unmatched migration test topic');
  await expect(page.locator('.blog-empty')).toBeVisible();
  expect(errors).toEqual([]);
});

test('canonical protected routes require login and retired HTML routes are unavailable', async ({ page, request }) => {
  for (const route of ['/blog.html', '/console.html', '/studio.html']) {
    const response = await request.get(route, { maxRedirects: 0 });
    expect([302, 401, 404]).toContain(response.status());
    if(response.status()===302) expect(response.headers().location).toMatch(/^\/(?:auth\/)?login\?/);
  }
  for (const route of ['/console', '/console/langflow', '/console/agent-designer', '/console/pipeline-editor', '/workspace', '/studio', '/studio/new']) {
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
  expect(external.filter(url=>url.includes('validator.swagger.io'))).toEqual([]);
  expect(errors).toEqual([]);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
});

test('product landing excludes publication content and reader has independent navigation', async ({page})=>{
  const editorial=[];
  page.on('request',request=>{if(new URL(request.url()).pathname==='/api/v1/blogs')editorial.push(request.url());});
  await page.goto('/');
  await expect(page.locator('.ascii-flow')).toHaveClass(/ascii-painted/);
  await expect(page.locator('#landing-blog-grid')).toHaveCount(0);
  await expect(page.getByRole('link',{name:'Engineering blog',exact:true})).toHaveCount(0);
  expect(editorial).toEqual([]);
  await page.goto('/blog');
  await expect(page.getByRole('navigation',{name:'Publication',exact:true})).toBeVisible();
  await expect(page.getByRole('link',{name:'Open console',exact:true})).toHaveCount(0);
  await expect(page.getByRole('link',{name:'Writing studio',exact:true})).toHaveCount(0);
});
