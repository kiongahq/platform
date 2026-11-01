const {test,expect,ADMIN,signIn}=require('./fixtures.cjs');
const AxeBuilder=require('@axe-core/playwright').default;
const json=(route,body)=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(body)});

test('storage uses bucket icons and navigable S3 breadcrumbs and object links',async({page},info)=>{
 await signIn(page,ADMIN,'/console/storage');
 await page.route('**/api/v1/storage/buckets',route=>json(route,{buckets:[{name:'model-artifacts'}]}));
 await page.route('**/api/v1/storage/objects?*',route=>{const prefix=new URL(route.request().url()).searchParams.get('prefix');return json(route,{prefixes:prefix==='team/'?['team/models/']:prefix?[]:['team/'],objects:prefix==='team/models/'?[{key:'team/models/model.json',size:42}]:[]});});
 await page.route('**/api/v1/storage/object?*',route=>json(route,{key:'team/models/model.json',content_type:'application/json',size:42,content:'{"model":"classifier"}'}));
 await page.reload();
 const bucket=page.getByRole('link',{name:/model-artifacts s3/});
 await expect(bucket.locator('svg').first()).toHaveAttribute('data-storage-icon','save-as');
 await bucket.click();await page.getByRole('link',{name:'team/',exact:true}).click();await page.getByRole('link',{name:'models/',exact:true}).click();
 const trail=page.getByRole('navigation',{name:'S3 path'});await expect(trail.getByRole('link',{name:'team',exact:true})).toHaveAttribute('href',/prefix=team%2F/);
 const object=page.getByRole('link',{name:'model.json',exact:true});await expect(object).toHaveAttribute('href',/key=team%2Fmodels%2Fmodel.json/);await object.click();
 const preview=page.getByRole('dialog',{name:'team/models/model.json',exact:true});await expect(preview).toContainText('classifier');await preview.getByRole('button',{name:'Close team/models/model.json'}).click();
 await trail.getByRole('link',{name:'team',exact:true}).click();await expect(page).toHaveURL(/prefix=team%2F$/);await page.goBack();await expect(trail.getByRole('link',{name:'models',exact:true})).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
 await page.screenshot({path:info.outputPath('storage-browser.png')});
});

test('project details separate commands, collections, errors and produced file trees',async({page},info)=>{
 await signIn(page,ADMIN);
 const record={id:'prj-detail',name:'Project details',template:'blank-python',framework:'python',status:'ready',capabilities:['tests','ci'],scaffold_command:'kionga scaffold project-detail --template blank-python',created_at:'2026-11-01T08:00:00Z'};
 await page.route('**/api/v1/projects/prj-detail',route=>json(route,record));
 await page.route('**/api/v1/projects/prj-detail/scaffold-jobs',route=>json(route,{items:[{id:'job-detail',status:'failed',workspace:'workbench',output_dir:'/workspace/projects/project-detail',error:'kionga scaffold exited with code 1.',output_tail:'Missing repository configuration',files:['src/app/main.py','src/app/utils.py','tests/test_app.py','README.md']}]}));
 await page.goto('/console/projects?resource=prj-detail');
 await expect(page.locator('.fact-values')).toContainText('tests');await expect(page.locator('.fact-values .code-frame')).toHaveCount(0);
 await expect(page.getByRole('alert').filter({hasText:'Scaffold failed'})).toContainText('exited with code 1');
 await expect(page.locator('.code-frame').filter({hasText:'kionga scaffold project-detail'})).toHaveCount(1);
 await page.getByText('Produced files',{exact:true}).click();await expect(page.locator('.file-tree details')).toHaveCount(3);await expect(page.locator('.file-tree')).toContainText('main.py');
 await page.screenshot({path:info.outputPath('project-details.png'),fullPage:true});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
});

test('catalog versions do not produce duplicate keys and dialogs fit the viewport',async({page},info)=>{
 const errors=[];page.on('console',message=>{if(message.type()==='error')errors.push(message.text());});page.on('pageerror',error=>errors.push(error.message));
 await signIn(page,ADMIN);
 await page.route('**/api/v1/catalog*',route=>json(route,{items:[{name:'churn-classifier',kind:'model',version:'v1',metadata:{owner:'team'}},{name:'churn-classifier',kind:'model',version:'v2',metadata:{owner:'team'}}]}));
 await page.goto('/console/catalog');await expect(page.getByRole('row').filter({hasText:'churn-classifier'})).toHaveCount(2);await page.getByRole('button',{name:'Details',exact:true}).first().click();
 const dialog=page.getByRole('dialog',{name:'churn-classifier',exact:true});const bounds=await dialog.boundingBox();const viewport=page.viewportSize();expect(bounds.width).toBeLessThanOrEqual(viewport.width);expect(bounds.x).toBeGreaterThanOrEqual(0);if(viewport.width>1000)expect(bounds.width).toBeGreaterThan(1000);
 expect(await dialog.evaluate(element=>element.scrollWidth-element.clientWidth)).toBeLessThanOrEqual(1);await expect(dialog.locator('.fact-structured')).toHaveCount(1);
 expect(errors).toEqual([]);await page.screenshot({path:info.outputPath('catalog-dialog.png')});
});

test('agent canvas drags and connects nodes, opens a context menu and saves real positions',async({page},info)=>{
 await signIn(page,ADMIN,'/console/agent-designer');
 const problems=[];page.on('pageerror',error=>problems.push(error.message));
 await expect(page.locator('[data-design-select="input"]')).toBeVisible();
 await page.getByRole('button',{name:'Expand workspace',exact:true}).click();await expect(page.locator('.agent-graph-workspace')).toHaveClass(/designer-expanded/);
 if(!await page.locator('[data-design-add="prompt"]').isVisible())await page.getByText('Components',{exact:true}).click();
 if(info.project.name==='desktop')await page.locator('[data-design-add="prompt"]').dragTo(page.locator('.flow-surface'),{targetPosition:{x:150,y:140}});else await page.locator('[data-design-add="prompt"]').click();await page.getByRole('button',{name:'Fit graph',exact:true}).click();
 const node=page.locator('[data-design-select^="prompt-"]');await expect(node).toBeVisible();
 const nodeId=await node.getAttribute('data-design-select');await node.scrollIntoViewIfNeeded();await expect(node).toBeInViewport();await node.hover();const before=await node.boundingBox();
 // Touch-capable narrow view still permits mouse interactions, with panning/fit controls.
 await page.mouse.move(before.x+before.width/2,before.y+25);await page.mouse.down();await page.mouse.move(before.x+before.width/2+(info.project.name==='narrow'?40:90),before.y+(info.project.name==='narrow'?55:285),{steps:14});await page.mouse.up();
 const moved=await node.boundingBox();expect(Math.abs(moved.x-before.x)+Math.abs(moved.y-before.y)).toBeGreaterThan(35);
 await node.click({button:'right'});const menu=page.getByRole('menu',{name:'Node actions'});await expect(menu).toBeVisible();await menu.getByRole('menuitem',{name:'Configure node'}).click();
 await page.locator('#design-node-name').fill('Context configured');
 await page.getByRole('button',{name:'Fit graph',exact:true}).click();
 const inputHandle=page.locator('.react-flow__node[data-id="input"] .react-flow__handle.source');
 const targetHandle=page.locator(`.react-flow__node[data-id="${nodeId}"] .react-flow__handle.target`);
 await inputHandle.hover();await page.mouse.down();await targetHandle.hover();await page.mouse.up();
 await expect(page.locator('.design-edge')).toHaveCount(2);
 await page.locator(`[data-design-out="${nodeId}"]`).click();await page.locator('[data-design-in="output"]').click();
 await page.getByRole('textbox',{name:'Expanded design name',exact:true}).fill(`refined-canvas-${info.project.name}-${Date.now()}`);
 const saved=page.waitForResponse(response=>response.url().endsWith('/api/v1/agent-designs')&&response.request().method()==='POST');await page.getByRole('button',{name:'Save graph',exact:true}).click();const response=await saved;expect(response.ok()).toBeTruthy();const graph=await response.json();const prompt=graph.nodes.find(item=>item.id===nodeId);expect(prompt.name).toBe('Context configured');expect(prompt.x%20).toBe(0);expect(graph.edges.some(edge=>edge.from==='input'&&edge.to===nodeId)).toBeTruthy();
 await page.getByRole('button',{name:'Exit expanded view',exact:true}).click();await expect(page.locator('.agent-graph-workspace')).not.toHaveClass(/designer-expanded/);
 expect(problems).toEqual([]);await page.screenshot({path:info.outputPath('agent-canvas.png'),fullPage:false});
});

test('pipeline node inspection filters logs and resumes from successful outputs with preflight',async({page},info)=>{
 await signIn(page,ADMIN);
 const definition={id:'definition-checkpoint',project_id:'prj-fraud',name:'Checkpoint flow',version:'1',execution_mode:'prefect',overridable:{nodes:true},parameters:{},jobs:[{name:'extract',depends_on:[]},{name:'train',depends_on:['extract']},{name:'evaluate',depends_on:['train']}]};
 const run={id:'run-checkpoint',name:'Failed training',project_id:'prj-fraud',definition_id:definition.id,status:'failed',steps:[{name:'extract',status:'succeeded',depends_on:[],message:'{"rows":12}'},{name:'train',status:'failed',depends_on:['extract'],message:'Training failed'},{name:'evaluate',status:'pending',depends_on:['train']}]};
 let payload;
 await page.route('**/api/v1/pipelines/definitions',route=>json(route,{items:[definition]}));
 await page.route('**/api/v1/pipelines/runs',route=>json(route,{items:[run]}));
 await page.route('**/api/v1/pipelines/runs/run-checkpoint',route=>json(route,run));
 await page.route('**/api/v1/logs?*',route=>json(route,{items:[],entries:[],sources:[]}));
 await page.route('**/api/v1/pipelines/preflight',route=>{payload=route.request().postDataJSON();return json(route,{allowed:true,checks:[],reused_nodes:['extract'],nodes:definition.jobs});});
 await page.route('**/api/v1/pipelines/submit',route=>json(route,{...run,id:'run-resumed'}));
 await page.goto('/console/pipelines?resource=run-checkpoint');const dialog=page.getByRole('dialog',{name:'Failed training',exact:true});
 const logs=page.waitForRequest(request=>request.url().includes('/api/v1/logs?')&&new URL(request.url()).searchParams.get('node')==='train');
 await dialog.getByRole('button',{name:'train, failed',exact:true}).click();await logs;await expect(dialog.getByRole('region',{name:'Selected pipeline node',exact:true})).toContainText('Training failed');
 await dialog.getByRole('button',{name:'Resume from train',exact:true}).click();const config=page.getByRole('dialog',{name:'Run pipeline',exact:true});await expect(config).toContainText('Reuse succeeded nodes');
 await config.getByRole('button',{name:/Preflight|Check run/i}).click();
 expect(payload.definition_id).toBe(definition.id);expect(payload.overrides.rerun_from_run_id).toBe(run.id);expect(payload.overrides.selected_nodes).toEqual(['train','evaluate']);
 await page.screenshot({path:info.outputPath('pipeline-resume.png')});
});

test('account settings expose account links, styled pickers and scoped GitHub project import',async({page},info)=>{
 await signIn(page,ADMIN);
 const fullName=`e2e-org/import-${info.project.name}-${Date.now()}`;
 let installation=0;
 await page.route('**/api/v1/settings/github',route=>{if(route.request().method()==='PUT')installation=route.request().postDataJSON().installation_id;return json(route,{connected:true,configured:true,oauth_configured:true,username:'test-github-user',installation_id:installation,install_url:'https://github.com/apps/kionga-test/installations/new'});});
 await page.route('**/api/v1/settings/github/installations',route=>json(route,{items:[{id:42,account:{login:'e2e-org'},repository_selection:'selected'}]}));
 await page.route('**/api/v1/settings/github/repositories',route=>json(route,{items:[{id:812,full_name:fullName,html_url:`https://github.com/${fullName}`,default_branch:'main',private:true}]}));
 await page.goto('/console/settings');
 await expect(page.getByRole('link',{name:'Manage access tokens',exact:true})).toHaveAttribute('href','https://huggingface.co/settings/tokens');
 await expect(page.getByRole('link',{name:'Install GitHub App',exact:true})).toHaveAttribute('href','https://github.com/apps/kionga-test/installations/new');
 await page.getByRole('combobox',{name:'GitHub App installation',exact:true}).selectOption('42');
 await expect(page.locator('.github-connection')).toContainText('Installation 42');
 const selector=page.getByRole('combobox',{name:'Theme',exact:true});
 expect(await selector.evaluate(element=>getComputedStyle(element,'::picker(select)').appearance)).toBe('base-select');
 await selector.click();await page.screenshot({path:info.outputPath('styled-dropdown.png')});await page.keyboard.press('Escape');
 await page.locator('.github-connection').getByRole('checkbox').check();
 const mutation=page.waitForResponse(response=>response.url().endsWith('/api/v1/projects')&&response.request().method()==='POST');
 await page.getByRole('button',{name:'Sync 1 selected',exact:true}).click();const result=await mutation;expect(result.status(),await result.text()).toBe(201);
 await expect(page.locator('.github-connection')).toContainText('1 project connection synchronized.');
 await page.goto('/console/projects');await expect(page.getByRole('row').filter({hasText:fullName})).toBeVisible();
});

test('access guidance explains actual precedence, group membership and conditions',async({page})=>{
 await signIn(page,ADMIN,'/console/access');
 await page.getByText('How roles, groups, policies and resource grants apply',{exact:true}).click();
 const guide=page.locator('.access-guide');await expect(guide).toContainText('explicit deny wins');await expect(guide).toContainText('not nested groups');await expect(guide).toContainText('default deny');await expect(guide).toContainText('No matching allow');
 const report=await new AxeBuilder({page}).include('.access-guide').withTags(['wcag2a','wcag2aa','wcag21aa']).analyze();expect(report.violations).toEqual([]);
});
