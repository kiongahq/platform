const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {JSDOM}=require('jsdom');
const tick=()=>new Promise(resolve=>setTimeout(resolve,20));

function app(t){
 const dom=new JSDOM(fs.readFileSync('go/cmd/gateway/web/console.html','utf8'),{url:'http://localhost/console.html',runScripts:'outside-only'});
 t.after(()=>dom.window.close());
 const w=dom.window,requests=[];
 w.projectOptions=[{id:'p1',name:'Project one'}];w.selectedProject='p1';
 w.escapeHTML=value=>String(value).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('"','&quot;');
 w.can=()=>true;w.hasService=()=>true;w.toast=()=>{};w.loadModels=async()=>{};w.showView=()=>{};
 let connected=false;
 w.api=async(path,options={})=>{
  requests.push({path,options});
  if(path==='/api/v1/settings/huggingface'){
   if(options.method==='PUT')connected=true;
   if(options.method==='DELETE')connected=false;
   return {connected,configured:true,username:connected?'alice':''};
  }
  if(path.endsWith('/import'))return {id:'m1',project_id:'p1',name:'org/model',version:'a'.repeat(40),artifact_uri:'hf://org/model@'+'a'.repeat(40),stage:'candidate',metrics:{},gate_status:'needs_evaluation',created_at:'2026-01-01T00:00:00Z'};
  return {items:[{id:'org/model',pipeline_tag:'text-generation',downloads:100,tags:['license:apache-2.0']}]};
 };
 w.eval(fs.readFileSync('go/cmd/gateway/web/huggingface.js','utf8'));
 return {w,requests};
}

test('Hub connection clears the token field and never stores it in browser storage',async t=>{
 const {w,requests}=app(t);
 w.document.querySelector('#hf-token').value='hf_secret';
 w.document.querySelector('#hf-account-form').dispatchEvent(new w.Event('submit',{cancelable:true}));await tick();
 assert.equal(w.document.querySelector('#hf-token').value,'');
 assert.match(w.document.querySelector('#hf-account-status').textContent,/Connected as alice/);
 assert.equal(w.localStorage.length,0);assert.equal(w.sessionStorage.length,0);
 assert.deepEqual(JSON.parse(requests[0].options.body),{token:'hf_secret'});
 w.document.querySelector('#hf-disconnect').click();await tick();
 assert.match(w.document.querySelector('#hf-account-status').textContent,/Not connected/);
});

test('Hub search imports into the selected project and produces credential-free download code',async t=>{
 const {w,requests}=app(t);
 w.document.querySelector('#hf-search').value='org/model';
 w.document.querySelector('#hf-search-form').dispatchEvent(new w.Event('submit',{cancelable:true}));await tick();
 assert.match(w.document.querySelector('#hf-results').textContent,/apache-2.0/);
 w.document.querySelector('[data-hf-import]').click();await tick();
 const request=requests.find(r=>r.path.endsWith('/import'));
 assert.deepEqual(JSON.parse(request.options.body),{project_id:'p1',repo_id:'org/model',revision:'main'});
 const code=w.document.querySelector('#hf-code').textContent;
 assert.match(code,/download_model\(model\)/);assert.match(code,/a{40}/);
 assert.doesNotMatch(code,/hf_secret|trust_remote_code=True/);
 assert.equal(w.document.querySelector('#hf-next').hidden,false);
 assert.match(w.document.querySelector('#hf-workspace').href,/project=p1/);
});

test('Hub import requires a project and displays upstream failures',async t=>{
 const {w,requests}=app(t);
 w.document.querySelector('#hf-search-form').dispatchEvent(new w.Event('submit',{cancelable:true}));await tick();
 w.document.querySelector('#hf-project').value='';w.document.querySelector('[data-hf-import]').click();await tick();
 assert.match(w.document.querySelector('#hf-search-status').textContent,/Choose a destination/);
 assert.equal(requests.filter(r=>r.path.endsWith('/import')).length,0);
 w.api=async()=>{throw new Error('Hugging Face rate limit reached');};
 w.document.querySelector('#hf-search-form').dispatchEvent(new w.Event('submit',{cancelable:true}));await tick();
 assert.match(w.document.querySelector('#hf-search-status').textContent,/rate limit/);
 assert.equal(w.document.querySelector('#hf-search-form button').disabled,false);
});
