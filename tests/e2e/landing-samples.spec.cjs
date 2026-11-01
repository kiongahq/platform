const {test, expect} = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;

test('public workspace samples compute edited inputs without creating platform resources', async ({page}, testInfo) => {
  const writes=[], errors=[];
  page.on('request', request => {if(new URL(request.url()).pathname.startsWith('/api/') && request.method()!=='GET')writes.push(request.url());});
  page.on('pageerror', error=>errors.push(error.message));
  await page.goto('/');
  await page.getByRole('link',{name:'Try a workspace',exact:true}).click();
  await page.getByRole('button',{name:'Run cell',exact:true}).click();
  await expect(page.getByText('75.0%',{exact:true})).toBeVisible();
  await page.getByRole('slider').fill('0.9');
  await expect(page.getByText('75.0%',{exact:true})).toHaveCount(0);
  await page.getByRole('button',{name:'Run cell',exact:true}).click();
  await expect(page.getByText('62.5%',{exact:true})).toBeVisible();
  await page.getByRole('tab',{name:'JupyterLab'}).focus();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('tab',{name:'Browser IDE'})).toBeFocused();
  const editor=page.getByRole('textbox',{name:'Sample project configuration',exact:true});
  const config=JSON.parse(await editor.inputValue());config.estimator='random_forest';
  await editor.fill(JSON.stringify(config,null,2));
  await page.getByRole('button',{name:'Check project',exact:true}).click();
  await expect(page.getByRole('status').filter({hasText:'Dependency order'})).toBeVisible();
  await page.getByRole('button',{name:'train.py',exact:true}).click();
  await expect(page.locator('#workspace-sample-ide .sample-code-view .numbered-code')).toContainText('RandomForestClassifier');
  const downloadPromise=page.waitForEvent('download');await page.getByRole('button',{name:'Download file',exact:true}).click();
  expect((await downloadPromise).suggestedFilename()).toBe('train.py');
  await page.getByRole('tab',{name:'Langflow'}).click();
  await page.getByLabel('Incident text').fill('Worker queue is stalled.');
  await page.getByRole('button',{name:'Render prompt',exact:true}).click();
  await expect(page.locator('#workspace-sample-langflow .sample-code-view .numbered-code')).toContainText('Worker queue is stalled.');
  await page.getByRole('button',{name:'Chat model Model request',exact:true}).click();
  await expect(page.getByRole('heading',{name:'Chat model configuration'})).toBeVisible();
  await page.getByRole('tab',{name:'Agent designer',exact:true}).click();
  await page.getByRole('button',{name:'Run routing sample',exact:true}).click();
  await expect(page.locator('#workspace-sample-designer .sample-code-view .numbered-code')).toContainText('"review"');
  await page.getByRole('button',{name:'Try another request',exact:true}).click();
  await page.getByRole('button',{name:'Run routing sample',exact:true}).click();
  await expect(page.locator('#workspace-sample-designer .sample-code-view .numbered-code')).toContainText('"answer"');
  await page.getByRole('tab',{name:'JupyterLab'}).click();
  await expect(page.getByRole('slider')).toHaveValue('0.9');
  for(const name of ['JupyterLab','Browser IDE','Langflow','Agent designer']) {
    await page.getByRole('tab',{name,exact:true}).click();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth-innerWidth),name+' overflow').toBeLessThanOrEqual(1);
    const report=await new AxeBuilder({page}).include('#workspaces').withTags(['wcag2a','wcag2aa','wcag21aa']).analyze();
    expect(report.violations,name+' accessibility').toEqual([]);
  }
  expect(writes).toEqual([]);expect(errors).toEqual([]);
  await page.screenshot({path:testInfo.outputPath('sample-workspace.png'),fullPage:true});
});

test('ASCII field moves without playback controls and respects reduced motion',async({page})=>{
  await page.goto('/');await expect(page.locator('.ascii-flow')).toHaveClass(/ascii-painted/);
  await page.evaluate(()=>document.fonts.ready);
  const frame=()=>page.locator('.ascii-flow canvas').evaluate(canvas=>canvas.toDataURL());
  const first=await frame();await page.waitForTimeout(180);expect(await frame()).not.toBe(first);
  await expect(page.getByRole('button',{name:/^(Pause|Replay|Play flow animation)$/})).toHaveCount(0);
  await page.emulateMedia({reducedMotion:'reduce'});
  await expect(page.locator('.ascii-flow')).toHaveAttribute('data-motion','reduced');
  const still=await frame();await page.waitForTimeout(180);expect(await frame()).toBe(still);
  await page.setViewportSize({width:320,height:800});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
});

test('landing character art fills the hero and resource examples update the inspector',async({page})=>{
  await page.goto('/');
  await expect(page.locator('.ascii-flow')).toHaveClass(/ascii-painted/);
  const hero=await page.locator('.landing-hero-field').boundingBox();
  const canvas=await page.locator('.ascii-flow canvas').boundingBox();
  expect(Math.abs(canvas.width-hero.width)).toBeLessThanOrEqual(1);
  expect(Math.abs(canvas.height-hero.height)).toBeLessThanOrEqual(1);
  await page.getByRole('button',{name:'train-and-evaluate',exact:true}).click();
  const inspector=page.getByRole('region',{name:'Example resource configuration',exact:true});
  await expect(inspector).toContainText('pipeline.json');
  await expect(inspector).toContainText('depends_on');
  await page.getByRole('button',{name:'churn-classifier',exact:true}).click();
  await expect(inspector).toContainText('model.json');
  await expect(inspector.getByRole('link',{name:'Open models'})).toHaveAttribute('href','/console/models');
  const wordmark=page.getByRole('img',{name:'Kionga, drawn in ASCII characters'});
  await wordmark.scrollIntoViewIfNeeded();
  await expect(wordmark).toHaveClass(/ascii-wordmark-painted/);
  const report=await new AxeBuilder({page}).withTags(['wcag2a','wcag2aa','wcag21aa']).analyze();
  expect(report.violations).toEqual([]);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
});

test('mouse leaves a fading ASCII wake and reduced motion disables it', async ({page}, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop', 'Mouse trail is a desktop interaction');
  // Hold animation frames so ordinary flow movement cannot impersonate a cursor wake.
  await page.addInitScript(() => {
    let next = 0;
    const callbacks = new Map();
    window.requestAnimationFrame = callback => { callbacks.set(++next, callback); return next; };
    window.cancelAnimationFrame = id => callbacks.delete(id);
    window.paintAsciiFrame = time => { const pending = [...callbacks.values()]; callbacks.clear(); pending.forEach(callback => callback(time)); };
  });
  await page.goto('/');
  await expect(page.locator('.ascii-flow')).toHaveClass(/ascii-painted/);
  await page.evaluate(()=>document.fonts.ready);
  const hero = await page.locator('.landing-hero-field').boundingBox();
  const x=hero.width*.8, y=hero.height*.345;
  const ink = () => page.locator('.ascii-flow canvas').evaluate((canvas,{x,y}) => {
    const scale=canvas.width/canvas.getBoundingClientRect().width;
    const data=canvas.getContext('2d').getImageData((x-30)*scale,(y-30)*scale,60*scale,60*scale).data;
    let alpha=0;for(let i=3;i<data.length;i+=4)alpha+=data[i];return alpha;
  },{x,y});
  const baseline=await ink();
  await page.mouse.move(hero.x+x-90,hero.y+y);
  await page.mouse.move(hero.x+x+90,hero.y+y,{steps:12});
  await page.mouse.move(10,10);
  await page.evaluate(()=>window.paintAsciiFrame(1000));
  const wake=await ink();
  expect(wake).toBeGreaterThan(baseline+10000);
  await page.screenshot({path:testInfo.outputPath('ascii-mouse-trail.png')});
  await page.waitForTimeout(1350);
  await page.evaluate(()=>window.paintAsciiFrame(1050));
  expect(await ink()).toBeLessThan(wake*.65);
  await page.emulateMedia({reducedMotion:'reduce'});
  await expect(page.locator('.ascii-flow')).toHaveAttribute('data-motion','reduced');
  const still=await page.locator('.ascii-flow canvas').evaluate(canvas=>canvas.toDataURL());
  await page.mouse.move(hero.x+x,hero.y+y,{steps:10});
  expect(await page.locator('.ascii-flow canvas').evaluate(canvas=>canvas.toDataURL())).toBe(still);
});
