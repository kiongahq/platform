const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM} = require('jsdom');
const tick = () => new Promise(resolve => setTimeout(resolve, 20));

function workspace(t, available = true) {
  const dom = new JSDOM(fs.readFileSync('go/cmd/gateway/web/workspace.html', 'utf8'), {
    url: 'http://localhost/workspace.html?tool=ide&project=p1', runScripts: 'outside-only',
  });
  t.after(() => dom.window.close());
  const requests = [];
  dom.window.fetch = async (path, options) => {
    requests.push({path, options});
    const body = path.endsWith('/project-options') ? {items:[{id:'p1',name:'Fraud detection'}]} :
      path.endsWith('/workspaces') ? {items:['ide','workbench'].map(id=>({id,available,message:'Workspace is offline'}))} :
      {url:path.includes('/ide/') ? '/workspaces/ide/?folder=/workspace/projects/fraud' : '/workspaces/workbench/lab/tree/projects/fraud'};
    return {ok:true,status:200,json:async()=>body};
  };
  dom.window.eval(fs.readFileSync('go/cmd/gateway/web/workspace.js','utf8'));
  return {w:dom.window,requests};
}

test('workspace switching keeps the selected project and gateway origin',async t=>{
  const {w,requests} = workspace(t); await tick();
  assert.equal(w.document.querySelector('#workspace-project').textContent,'Fraud detection');
  assert.match(w.document.querySelector('#workspace-frame').src,/localhost\/workspaces\/ide/);
  const select=w.document.querySelector('#workspace-tool');select.value='workbench';select.dispatchEvent(new w.Event('change'));await tick();
  const launches=requests.filter(r=>r.path.endsWith('/launch'));
  assert.equal(launches.length,2);
  for(const r of launches)assert.deepEqual(JSON.parse(r.options.body),{project_id:'p1'});
  assert.match(w.location.search,/project=p1/);
  assert.match(w.location.search,/tool=workbench/);
  assert.match(w.document.querySelector('#workspace-frame').src,/\/workspaces\/workbench\/lab/);
});

test('offline workspace shows persistent retry without launching',async t=>{
  const {w,requests}=workspace(t,false);await tick();
  assert.equal(w.document.querySelector('#workspace-frame').hidden,true);
  assert.equal(w.document.querySelector('#workspace-retry').hidden,false);
  assert.match(w.document.querySelector('#workspace-message').textContent,/offline/);
  assert.equal(requests.filter(r=>r.path.endsWith('/launch')).length,0);
});
