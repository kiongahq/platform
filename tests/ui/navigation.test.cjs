const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM, VirtualConsole} = require('jsdom');
const html = fs.readFileSync('go/cmd/gateway/web/console.html','utf8');
const script = fs.readFileSync('go/cmd/gateway/web/app.js','utf8');
const tick = () => new Promise(resolve=>setTimeout(resolve,20));

function consoleApp(services, view, overrides={}) {
  const errors=[];
  const virtualConsole=new VirtualConsole();virtualConsole.on('jsdomError', error=>errors.push(error.message));
  const dom=new JSDOM(html,{url:`http://localhost:8080/console.html?view=${view}`,runScripts:'outside-only',pretendToBeVisual:true,virtualConsole});
  const w=dom.window, requests=[];
  w.matchMedia=()=>({matches:false});
  w.CSS={escape:value=>value};
  w.KiongaPipelineGraph={render:()=>'<svg></svg>'};
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
    '/api/v1/features':{items:[]}, '/api/v1/functions':{items:[],configured:false},
    '/api/v1/storage/buckets':{buckets:[]}, '/api/v1/components':[], '/api/v1/connections':{items:[]},
    '/api/v1/realtime':{demos:{}}, '/api/v1/catalog':[], '/api/v1/settings/tokens':{items:[]},
    '/api/v1/access-requests':{items:[]},
    ...overrides,
  };
  const grants={projects:'projects',pipelines:'pipelines',agents:'agents',models:'models',features:'features',functions:'functions',storage:'storage',components:'platform',connections:'platform',realtime:'realtime',catalog:'catalog',prompts:'catalog',tools:'catalog',onboarding:'overview',dashboard:'overview'};
  w.fetch=async path=>{
    requests.push(path);
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

test('project details have scoped handoffs and a bookmarkable resource',async t=>{
 const app=consoleApp(['projects','pipelines','workbench'],'projects');t.after(()=>app.dom.window.close());await tick();
 app.w.document.querySelector('[data-project-detail="p1"]').click();
 assert.equal(app.w.document.querySelector('#metadata-dialog').open,true);
 assert.match(app.w.location.search,/resource=p1/);
 assert.match(app.w.document.querySelector('#metadata-detail a[href*="workspace.html"]').href,/project=p1/);
 app.w.document.querySelector('[data-project-view="pipelines"]').click();await tick();
 assert.equal(app.w.location.search,'?view=pipelines&project=p1');
 assert.equal(app.w.document.querySelector('#metadata-dialog').open,false);
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
