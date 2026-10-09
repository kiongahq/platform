const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM, VirtualConsole} = require('jsdom');
const {html, consoleScripts} = require('./harness.cjs');
const script = consoleScripts();
const tick = () => new Promise(resolve=>setTimeout(resolve,20));

function consoleApp(services, view, overrides={}) {
  const errors=[];
  const virtualConsole=new VirtualConsole();virtualConsole.on('jsdomError', error=>errors.push(error.message));
  const dom=new JSDOM(html,{url:`http://localhost:8080/console.html?view=${view}`,runScripts:'outside-only',pretendToBeVisual:true,virtualConsole});
  const w=dom.window, requests=[];
  w.matchMedia=()=>({matches:false});
  w.CSS={escape:value=>value};
  w.KiongaPipelineGraph={render:()=>'<svg></svg>',enhance:()=>{}};
  w.HTMLDialogElement.prototype.showModal=function(){this.open=true};
  w.HTMLDialogElement.prototype.close=function(){this.open=false;this.dispatchEvent(new w.Event('close'))};
  w.HTMLCanvasElement.prototype.getContext=()=>({scale(){},clearRect(){},fillText(){},beginPath(){},roundRect(){},fill(){}});
  w.EventSource=class {close(){}};
  w.localStorage.setItem('kionga.console.preferences',JSON.stringify({live:false}));
  const identity={subject:'user-1',roles:['user'],services,mode:'local',permissions:{},project_ids:['p1'],provisioned:true,entitlements:null};
  const fixtures={
    '/api/v1/me':identity,
    '/api/v1/project-options':{items:[{id:'p1',name:'Project one',namespace:'project-one'},{id:'p2',name:'Project two',namespace:'project-two'}]},
    '/api/v1/workspaces':{items:[{id:'workbench',name:'JupyterLab',available:false,message:'Workspace is offline'},{id:'ide',name:'Browser IDE',available:false,message:'Workspace is offline'}]},
    '/api/v1/projects':[{id:'p1',name:'Project one',namespace:'project-one',template:'blank',status:'ready'}],
    '/api/v1/pipelines/runs':[], '/api/v1/pipelines/definitions':{items:[]},
    '/api/v1/agents':{items:[],total:0}, '/api/v1/models':{items:[],total:0},
    '/api/v1/features':{items:[]}, '/api/v1/features/views':{items:[]}, '/api/v1/features/stores':{items:[]}, '/api/v1/functions':{items:[],configured:false},
    '/api/v1/storage/buckets':{buckets:[]}, '/api/v1/components':[], '/api/v1/connections':{items:[]},
    '/api/v1/realtime':{demos:{}}, '/api/v1/catalog':[], '/api/v1/settings/tokens':{items:[]},
    '/api/v1/access-requests':{items:[]},
    ...overrides,
  };
  const grants={projects:'projects',pipelines:'pipelines',agents:'agents',models:'models',features:'features',functions:'functions',storage:'storage',components:'platform',connections:'platform',realtime:'realtime',catalog:'catalog',prompts:'catalog',tools:'catalog',onboarding:'overview',dashboard:'overview'};
  w.fetch=async (path,options={})=>{
    requests.push(path);
    if(options.body)(w.__bodies=w.__bodies||[]).push({path,body:options.body});
    const category=path.split('/')[3];
    if (grants[category]&&!services.includes(grants[category])) throw new Error(`Unassigned service requested: ${path}`);
    const fixture=fixtures[path];
    if(fixture instanceof Error)throw fixture;
    if(fixture===undefined)throw new Error(`Unexpected request: ${path}`);
    return {ok:true,status:200,headers:{get:()=> 'application/json'},json:async()=>fixture};
  };
  w.eval(script);
  return {dom,w,requests,errors,fixtures};
}

for(const view of ['agents','platform','functions','storage','pipelines','projects','models','features','realtime','catalog']) {
 test(`${view} loads with only its own service grant`,async t=>{
   const app=consoleApp([view],view);t.after(()=>app.dom.window.close());await tick();
   assert.equal(app.w.document.querySelector('.view.active').id,view);
   assert.equal(app.w.document.querySelector('#view-feedback').hidden,true,app.w.document.querySelector('#view-feedback-message').textContent);
   const forbidden={agents:'/api/v1/prompts',platform:'/api/v1/onboarding/readiness',functions:'/api/v1/projects',storage:'/api/v1/models'}[view];
   if(forbidden)assert.ok(!app.requests.includes(forbidden));
   assert.deepEqual(app.errors,[]);
 });
}

test('startup retry reloads identity before loading a permitted page',async t=>{
 const app=consoleApp(['projects'],'projects',{'/api/v1/me':new Error('Identity temporarily unavailable')});
 t.after(()=>app.dom.window.close());await tick();
 assert.equal(app.w.document.querySelector('#retry-view').hidden,false);
 app.fixtures['/api/v1/me']={subject:'user-1',roles:['user'],services:['projects'],permissions:{},provisioned:true};
 app.w.document.querySelector('#retry-view').click();await tick();
 assert.equal(app.w.document.querySelector('.view.active').id,'projects');
 assert.equal(app.w.document.querySelector('#view-feedback').hidden,true);
});

test('agent presets choose adapter entrypoints and disclose NOOA isolation requirements',async t=>{
 const app=consoleApp(['agents'],'agents');t.after(()=>app.dom.window.close());await tick();
 const select=app.w.document.querySelector('#agent-framework-preset');
 select.value='agno';select.dispatchEvent(new app.w.Event('change'));
 assert.equal(app.w.document.querySelector('#deploy-agent-form [name=graph_module]').value,'agents.framework_examples:build_agno');
 select.value='nooa';select.dispatchEvent(new app.w.Event('change'));
 assert.equal(app.w.document.querySelector('#deploy-agent-form [name=graph_module]').value,'agents.framework_examples:build_nooa');
 assert.match(app.w.document.querySelector('#agent-framework-help').textContent,/not a sandbox/);
});

test('navigation preserves project in URL and back/forward restores the view',async t=>{
 const app=consoleApp(['projects','pipelines'],'projects');t.after(()=>app.dom.window.close());await tick();
 const select=app.w.document.querySelector('#project-context');select.value='p1';select.dispatchEvent(new app.w.Event('change'));await tick();
 app.w.document.querySelector('[data-view="pipelines"]').click();await tick();
 assert.equal(app.w.location.search,'?view=pipelines&project=p1');
 app.w.history.back();await tick();
 assert.equal(app.w.document.querySelector('.view.active').id,'projects');
 assert.equal(select.value,'p1');
});

test('failed page offers retry and other sections remain usable',async t=>{
 const app=consoleApp(['pipelines','projects'],'pipelines',{'/api/v1/pipelines/runs':new Error('Engine unavailable')});t.after(()=>app.dom.window.close());await tick();
 assert.equal(app.w.document.querySelector('#retry-view').hidden,false);
 assert.match(app.w.document.querySelector('#view-feedback-message').textContent,/Engine unavailable/);
 app.w.document.querySelector('[data-view="projects"]').click();await tick();
 assert.equal(app.w.document.querySelector('#view-feedback').hidden,true);
 assert.equal(app.w.document.querySelector('.view.active').id,'projects');
});

test('unprovisioned identity starts at My access',async t=>{
 const app=consoleApp([],'overview');t.after(()=>app.dom.window.close());await tick();
 assert.equal(app.w.document.querySelector('.view.active').id,'profile');
 assert.equal(app.w.document.querySelector('#request-access').disabled,false);
 assert.doesNotMatch(app.w.document.querySelector('#my-access-summary').textContent,/Administrative access/);
});

test('project details open as a scrollable page with scoped handoffs and a bookmarkable resource',async t=>{
 const app=consoleApp(['projects','pipelines','workbench'],'projects',{
   '/api/v1/workspaces':{items:[{id:'workbench',name:'JupyterLab',available:true,state:'ready',message:'Ready'},{id:'ide',name:'Browser IDE',available:false,state:'offline',message:'IDE is offline'}]},
 });t.after(()=>app.dom.window.close());await tick();
 app.w.document.querySelector('[data-project-detail="p1"]').click();await tick();
 const detail=app.w.document.querySelector('#project-detail-panel');
 assert.equal(detail.hidden,false);
 assert.equal(app.w.document.querySelector('#project-list-panel').hidden,true);
 assert.equal(app.w.document.querySelector('#metadata-dialog').open,false);
 assert.match(app.w.location.search,/resource=p1/);
 assert.match(detail.querySelector('a[href*="workspace.html"]').href,/tool=workbench&project=p1/);
 const handoff=detail.querySelector('[data-navigate="pipelines"]');
 assert.ok(handoff,'pipelines handoff present');
 handoff.click();await tick();
 assert.equal(app.w.location.search,'?view=pipelines&project=p1');
 assert.equal(app.w.document.querySelector('.view.active').id,'pipelines');
 assert.equal(app.w.document.querySelector('#project-context').value,'p1');
 app.w.history.back();await tick();
 assert.equal(app.w.document.querySelector('.view.active').id,'projects');
 assert.equal(app.w.document.querySelector('#project-detail-panel').hidden,false);
});

test('project detail deep link renders without opening a dialog',async t=>{
 const app=consoleApp(['projects'],'projects&resource=p1');t.after(()=>app.dom.window.close());await tick();await tick();
 assert.equal(app.w.document.querySelector('#project-detail-panel').hidden,false);
 assert.match(app.w.document.querySelector('#project-detail-panel h2').textContent,/Project one/);
 assert.equal(app.w.document.querySelector('#view-feedback').hidden,true);
});

test('project selector is shown only on views it filters',async t=>{
 const app=consoleApp(['pipelines','features'],'pipelines');t.after(()=>app.dom.window.close());await tick();
 assert.equal(app.w.document.querySelector('#project-context-wrap').hidden,false);
 app.w.document.querySelector('[data-view="features"]').click();await tick();
 assert.equal(app.w.document.querySelector('#project-context-wrap').hidden,true);
});

test('refresh is an icon button with an accessible name and reports completion',async t=>{
 const app=consoleApp(['pipelines'],'pipelines');t.after(()=>app.dom.window.close());await tick();
 const button=app.w.document.querySelector('#refresh-view');
 assert.equal(button.getAttribute('aria-label'),'Refresh this page');
 assert.ok(button.querySelector('svg'));
 button.click();
 assert.equal(button.getAttribute('aria-busy'),'true');
 await tick();
 assert.equal(button.hasAttribute('aria-busy'),false);
 assert.match(app.w.document.querySelector('#refresh-status').textContent,/Updated/);
 app.fixtures['/api/v1/pipelines/runs']=new Error('Engine down');
 button.click();await tick();
 assert.match(app.w.document.querySelector('#refresh-status').textContent,/Refresh failed: Engine down/);
});

test('pipeline run table renders aligned rows with trigger and accessible progress',async t=>{
 const app=consoleApp(['pipelines'],'pipelines',{'/api/v1/pipelines/runs':[{id:'run-1',name:'train',project_id:'p1',status:'running',progress:40,created_at:new Date().toISOString(),steps:[]}]});
 t.after(()=>app.dom.window.close());await tick();
 const row=app.w.document.querySelector('#run-table tr');
 assert.equal(row.children.length,app.w.document.querySelectorAll('.runs-table thead th').length);
 assert.equal(row.querySelector('[role=progressbar]').getAttribute('aria-valuenow'),'40');
 assert.match(row.textContent,/Manual/);
 assert.match(row.textContent,/Project one/);
});

test('empty run table spans every column',async t=>{
 const app=consoleApp(['pipelines'],'pipelines');t.after(()=>app.dom.window.close());await tick();
 const cell=app.w.document.querySelector('#run-table td');
 assert.equal(cell.getAttribute('colspan'),String(app.w.document.querySelectorAll('.runs-table thead th').length));
 assert.equal(app.w.document.querySelector('#new-pipeline-definition').className,app.w.document.querySelector('#new-project').className);
});

test('page retry recovers when its API becomes available',async t=>{
 const app=consoleApp(['pipelines'],'pipelines',{'/api/v1/pipelines/runs':new Error('Temporary outage')});t.after(()=>app.dom.window.close());await tick();
 app.fixtures['/api/v1/pipelines/runs']=[];
 app.w.document.querySelector('#retry-view').click();await tick();
 assert.equal(app.w.document.querySelector('#view-feedback').hidden,true);
 assert.equal(app.w.document.querySelector('#pipelines').getAttribute('aria-busy'),'false');
});

test('offline workspace link explains why it cannot open',async t=>{
 const app=consoleApp(['workbench'],'profile');t.after(()=>app.dom.window.close());await tick();
 const link=app.w.document.querySelector('#workbench-link');
 assert.equal(link.getAttribute('aria-disabled'),'true');
 assert.match(link.title,/offline/);
});

test('definition editor validates live, shows node issues, and saves canonical YAML from the form',async t=>{
 const valid={valid:true,issues:[],yaml:'apiVersion: kionga.dev/v1\nkind: Pipeline\n',sha256:'abc',layers:[['extract'],['train']],request:{project_id:'p1',name:'event-pipeline',version:'1',execution_mode:'prefect',jobs:[{name:'extract',kind:'container',image:'x',depends_on:[]},{name:'train',kind:'container',image:'x',depends_on:['extract']}]}};
 const app=consoleApp(['pipelines','functions'],'pipelines',{'/api/v1/me':{subject:'user-1',roles:['user'],services:['pipelines','functions'],mode:'local',permissions:{pipelines_write:true},project_ids:['p1'],provisioned:true,entitlements:null},'/api/v1/pipelines/validate':valid,'/api/v1/pipelines/definitions/yaml':{id:'pipe-1',revision:1}});
 t.after(()=>app.dom.window.close());await tick();
 app.w.document.querySelector('#new-pipeline-definition').click();await tick();await new Promise(r=>setTimeout(r,300));
 assert.equal(app.w.document.querySelector('#pipeline-definition-dialog').open,true);
 assert.equal(app.w.document.querySelectorAll('#definition-nodes .node-editor').length,2);
 assert.match(app.w.document.querySelector('#pipeline-preview-status').textContent,/2 nodes · 2 stages · valid/);
 const validation=app.w.__bodies.filter(b=>b.path==='/api/v1/pipelines/validate').pop();
 assert.equal(JSON.parse(validation.body).request.jobs[1].depends_on[0],'extract');
 app.fixtures['/api/v1/pipelines/validate']={...valid,valid:false,issues:[{path:'spec.nodes[1].dependsOn',node:'train',message:'node "train" depends on "nowhere", which does not exist'}]};
 app.w.document.querySelector('#definition-add-node').click();await new Promise(r=>setTimeout(r,300));
 assert.match(app.w.document.querySelector('#definition-issues').textContent,/node train/);
 assert.equal(app.w.document.querySelector('#definition-nodes .node-editor.has-issue [data-field="name"]').value,'train');
 app.fixtures['/api/v1/pipelines/validate']=valid;
 app.w.document.querySelector('#pipeline-definition-form').dispatchEvent(new app.w.Event('submit',{cancelable:true}));await tick();await tick();
 const saved=app.w.__bodies.find(b=>b.path==='/api/v1/pipelines/definitions/yaml');
 assert.ok(saved,'definition saved');
 assert.match(JSON.parse(saved.body).yaml,/kionga.dev\/v1/);
});

test('switching to YAML shows the canonical text from the validator',async t=>{
 const valid={valid:true,issues:[],yaml:'apiVersion: kionga.dev/v1\nkind: Pipeline\nmetadata:\n  name: x\n',sha256:'abc',layers:[['a']],request:{project_id:'p1',name:'x',version:'1',jobs:[{name:'a',kind:'container',image:'x'}]}};
 const app=consoleApp(['pipelines'],'pipelines',{'/api/v1/me':{subject:'user-1',roles:['user'],services:['pipelines'],mode:'local',permissions:{pipelines_write:true},project_ids:['p1'],provisioned:true,entitlements:null},'/api/v1/pipelines/validate':valid});
 t.after(()=>app.dom.window.close());await tick();
 app.w.document.querySelector('#new-pipeline-definition').click();await tick();
 app.w.document.querySelector('#definition-tab-yaml').click();await tick();await tick();
 assert.equal(app.w.document.querySelector('#definition-yaml-panel').hidden,false);
 assert.match(app.w.document.querySelector('#definition-yaml').value,/name: x/);
 assert.equal(app.w.document.querySelector('#definition-tab-yaml').getAttribute('aria-selected'),'true');
});

test('line diff marks additions and deletions',async t=>{
 const app=consoleApp(['pipelines'],'pipelines');t.after(()=>app.dom.window.close());await tick();
 const parts=app.w.lineDiff?app.w.lineDiff('a\nb\nc','a\nc\nd'):app.w.eval('lineDiff')('a\nb\nc','a\nc\nd');
 assert.equal(JSON.stringify(parts.map(p=>p.type+':'+p.text)),JSON.stringify(['same:a','del:b','same:c','add:d']));
});
